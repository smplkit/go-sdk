package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/smplkit/go-sdk/v3/internal/debug"
	genconfig "github.com/smplkit/go-sdk/v3/internal/generated/config"
)

// ConfigChangeEvent describes a single value change detected on refresh.
type ConfigChangeEvent struct {
	// ConfigID is the config ID that changed (e.g. "user_service").
	ConfigID string
	// ItemKey is the item key within the config that changed.
	ItemKey string
	// OldValue is the value before the change (nil if the key was new).
	OldValue interface{}
	// NewValue is the value after the change (nil if the key was removed).
	NewValue interface{}
	// Source is "websocket" for server-pushed changes or "manual" for Refresh calls.
	Source string
}

type configChangeListener struct {
	configID string // "" matches all configs
	itemKey  string // "" matches all items
	cb       func(*ConfigChangeEvent)
}

// ConfigClient provides operations for config resources and
// resolved value access.
// Obtain one via Client.Config().
type ConfigClient struct {
	client      *Client
	generated   genconfig.ClientInterface
	configCache map[string]map[string]interface{}

	initOnce sync.Once
	initErr  error

	listenersMu sync.Mutex
	listeners   []configChangeListener

	// proxyCache returns the same *LiveConfig instance on repeat
	// Get / GetOrCreate calls so callers can hold one as a parent
	// reference. Mirrors Python's _proxies cache.
	proxyCacheMu sync.Mutex
	proxyCache   map[string]*LiveConfig

	wsManager *sharedWebSocket

	management *ConfigManagement
}

// Management returns the sub-object for config CRUD operations.
//
// Returns the same *ConfigManagement instance that
// client.Manage().Config() returns — runtime and management surfaces
// share one management object.
func (c *ConfigClient) Management() *ConfigManagement {
	if c.management == nil {
		c.management = newConfigManagement(c.generated)
		c.management.attachRuntime(c)
	}
	return c.management
}

// getByID retrieves a config by its ID (internal use for chain walking).
func (c *ConfigClient) getByID(ctx context.Context, id string) (*ConfigEntry, error) {
	resp, err := c.generated.GetConfig(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{
			Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	return resourceToConfig(result.Data, c.Management()), nil
}

// (createConfig and updateConfig moved to config_management.go so
// the active-record save path doesn't depend on the runtime client —
// rule 1 of the cross-SDK overhaul.)

// Get returns the resolved config values for the given ID.
// Get returns a LiveConfig — a live, dict-like, read-only proxy whose
// reads always reflect the latest resolved values for the given config
// ID. WebSocket updates are picked up automatically; there is no
// separate Subscribe step (rule 10 of the cross-SDK overhaul).
//
// Mirrors Python's client.config.get(id) which returns a
// LiveConfigProxy. Customer code that wants a one-shot snapshot map
// should call .Value() (or use Snapshot for the verbatim snapshot
// shape).
func (c *ConfigClient) Get(ctx context.Context, id string) (*LiveConfig, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	if metrics := c.client.metrics; metrics != nil {
		metrics.Record("config.resolutions", 1, "resolutions", map[string]string{"config": id})
	}
	return c.cachedProxy(id), nil
}

// GetOrCreate declares a configuration from code; returns a live, dict-like view.
//
// Idempotent. Repeated calls with the same id return the same
// *LiveConfig instance. The first call queues a discovery payload (the
// config plus any items declared via typed getters on the returned
// handle) for upload to POST /api/v1/configs/bulk on next flush. If
// the config already exists server-side, managed=true configs are left
// untouched; managed=false configs receive the SDK's items via
// source-row upsert per ADR-024 §2.9.
//
// Unlike Get, this method does NOT error when the id is absent from
// the cache — discovery handles that case.
//
// Mirrors Python's client.config.get_or_create(id, ...).
func (c *ConfigClient) GetOrCreate(ctx context.Context, id string, opts ...ConfigOption) (*LiveConfig, error) {
	cfg := &ConfigEntry{ID: id}
	for _, opt := range opts {
		opt(cfg)
	}
	parent := ""
	if cfg.Parent != nil {
		parent = *cfg.Parent
	}
	name := ""
	if cfg.Name != "" {
		name = cfg.Name
	}
	description := ""
	if cfg.Description != nil {
		description = *cfg.Description
	}
	c.observeConfigDeclaration(id, parent, name, description)
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	return c.cachedProxy(id), nil
}

// cachedProxy returns the canonical *LiveConfig for an id, caching so
// repeat calls return the same handle (parent-by-reference support).
func (c *ConfigClient) cachedProxy(id string) *LiveConfig {
	c.proxyCacheMu.Lock()
	defer c.proxyCacheMu.Unlock()
	if c.proxyCache == nil {
		c.proxyCache = make(map[string]*LiveConfig)
	}
	if proxy, ok := c.proxyCache[id]; ok {
		return proxy
	}
	proxy := &LiveConfig{client: c, id: id}
	c.proxyCache[id] = proxy
	return proxy
}

// cachedValues exposes the resolved values map for typed getters on
// LiveConfig. The second return is false if the id is not in the cache.
func (c *ConfigClient) cachedValues(id string) (map[string]interface{}, bool) {
	if c.configCache == nil {
		return nil, false
	}
	resolved, ok := c.configCache[id]
	return resolved, ok
}

// observeConfigDeclaration queues a config declaration with the
// management buffer. Called by GetOrCreate.
func (c *ConfigClient) observeConfigDeclaration(configID, parent, name, description string) {
	mgmt := c.Management()
	if mgmt == nil {
		return
	}
	service := ""
	environment := ""
	if c.client != nil {
		service = c.client.service
		environment = c.client.environment
	}
	mgmt.RegisterConfig(configID, service, environment, parent, name, description)
}

// observeItemDeclaration queues a config item declaration with the
// management buffer. Called by LiveConfig's typed getters.
func (c *ConfigClient) observeItemDeclaration(configID, itemKey, itemType string, defaultVal interface{}, description string) {
	mgmt := c.Management()
	if mgmt == nil {
		return
	}
	mgmt.RegisterConfigItem(configID, itemKey, itemType, defaultVal, description)
}

// Snapshot returns a one-shot copy of the resolved values for the given
// config ID. Use Get to receive a live proxy that always reflects the
// latest values without re-fetching.
func (c *ConfigClient) Snapshot(ctx context.Context, id string) (map[string]interface{}, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	if metrics := c.client.metrics; metrics != nil {
		metrics.Record("config.resolutions", 1, "resolutions", map[string]string{"config": id})
	}
	resolved, ok := c.configCache[id]
	if !ok {
		return nil, nil
	}
	cp := make(map[string]interface{}, len(resolved))
	for k, v := range resolved {
		cp[k] = v
	}
	return cp, nil
}

// GetInto resolves the config and unmarshals it into the target struct.
// The target must be a pointer to a struct. Dot-notation keys (e.g. "database.host")
// are expanded into nested structures before unmarshaling.
func (c *ConfigClient) GetInto(ctx context.Context, id string, target interface{}) error {
	resolved, err := c.Snapshot(ctx, id)
	if err != nil {
		return err
	}
	return unmarshalResolved(resolved, target)
}

// Subscribe is a deprecated alias for Get.
//
// Deprecated: Use Get — it now returns the same LiveConfig proxy that
// Subscribe used to provide. The Python SDK collapsed the two methods
// into one for the same reason (rule 10).
func (c *ConfigClient) Subscribe(ctx context.Context, id string) (*LiveConfig, error) {
	return c.Get(ctx, id)
}

// ensureInit performs initialization on first runtime access.
func (c *ConfigClient) ensureInit(ctx context.Context) error {
	c.initOnce.Do(func() {
		environment := c.client.environment
		debug.Debug("api", "fetching config definitions")
		configs, err := c.fetchAllConfigs(ctx)
		if err != nil {
			c.initErr = err
			return
		}
		debug.Debug("api", "fetched %d configs", len(configs))

		cache := make(map[string]map[string]interface{})
		for _, cfg := range configs {
			chain, fetchErr := c.fetchChain(ctx, cfg.ID)
			if fetchErr != nil {
				c.initErr = fetchErr
				return
			}
			cache[cfg.ID] = resolveChain(chain, environment)
		}
		c.configCache = cache

		// Register WebSocket listeners for real-time config updates.
		// The WS connect happens in the background — callers that need
		// to be sure the subscription is registered server-side before
		// firing writes should call Client.WaitUntilReady (matches
		// Python's wait_until_ready and TypeScript's waitUntilReady).
		ws := c.client.ensureWS()
		c.wsManager = ws
		ws.on("config_changed", c.handleConfigChanged)
		ws.on("config_deleted", c.handleConfigDeleted)
		ws.on("configs_changed", c.handleConfigsChanged)
	})
	return c.initErr
}

func (c *ConfigClient) handleConfigChanged(data map[string]interface{}) {
	configKey, _ := data["id"].(string)
	debug.Debug("websocket", "config_changed event received, key=%q", configKey)

	ctx := context.Background()
	environment := c.client.environment

	if c.configCache == nil {
		c.configCache = make(map[string]map[string]interface{})
	}

	// Snapshot pre-state for this config.
	oldResolved := c.configCache[configKey]

	// Scoped fetch: fetch the chain for this single config key and resolve.
	chain, err := c.fetchChain(ctx, configKey)
	if err != nil {
		return
	}
	newResolved := resolveChain(chain, environment)

	// Only update and fire if content changed.
	if reflect.DeepEqual(oldResolved, newResolved) {
		return
	}

	oldCache := map[string]map[string]interface{}{configKey: oldResolved}
	newCache := map[string]map[string]interface{}{configKey: newResolved}

	c.configCache[configKey] = newResolved
	c.diffAndFire(oldCache, newCache, "websocket")
}

func (c *ConfigClient) handleConfigDeleted(data map[string]interface{}) {
	configKey, _ := data["id"].(string)
	debug.Debug("websocket", "config_deleted event received, key=%q", configKey)

	if c.configCache == nil {
		return
	}

	oldResolved, existed := c.configCache[configKey]
	if !existed {
		return
	}

	delete(c.configCache, configKey)

	// Fire listeners with old value → nil to signal removal.
	oldCache := map[string]map[string]interface{}{configKey: oldResolved}
	newCache := map[string]map[string]interface{}{configKey: {}}
	c.diffAndFire(oldCache, newCache, "websocket")
}

func (c *ConfigClient) handleConfigsChanged(_ map[string]interface{}) {
	debug.Debug("websocket", "configs_changed event received")
	_ = c.Refresh(context.Background())
}

// Refresh re-fetches all configs and resolves current values.
// OnChange listeners fire for any values that changed.
func (c *ConfigClient) Refresh(ctx context.Context) error {
	if err := c.ensureInit(ctx); err != nil {
		return err
	}
	environment := c.client.environment
	if environment == "" {
		return &Error{Message: "No environment set."}
	}

	configs, err := c.fetchAllConfigs(ctx)
	if err != nil {
		return err
	}

	newCache := make(map[string]map[string]interface{})
	for _, cfg := range configs {
		chain, fetchErr := c.fetchChain(ctx, cfg.ID)
		if fetchErr != nil {
			return fetchErr
		}
		newCache[cfg.ID] = resolveChain(chain, environment)
	}
	oldCache := c.configCache
	c.configCache = newCache
	c.diffAndFire(oldCache, newCache, "manual")
	return nil
}

// OnChange registers a listener that fires when a config value changes (on Refresh).
// Use WithConfigID and/or WithItemKey to scope the listener.
func (c *ConfigClient) OnChange(cb func(*ConfigChangeEvent), opts ...ChangeListenerOption) {
	var cfg changeListenerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	c.listenersMu.Lock()
	c.listeners = append(c.listeners, configChangeListener{
		configID: cfg.configID,
		itemKey:  cfg.itemKey,
		cb:       cb,
	})
	c.listenersMu.Unlock()
}

// ChangeListenerOption configures an OnChange listener.
type ChangeListenerOption func(*changeListenerConfig)

type changeListenerConfig struct {
	configID string
	itemKey  string
}

// WithConfigID restricts the listener to changes in the given config.
func WithConfigID(id string) ChangeListenerOption {
	return func(c *changeListenerConfig) {
		c.configID = id
	}
}

// WithItemKey restricts the listener to changes of the given item key.
func WithItemKey(key string) ChangeListenerOption {
	return func(c *changeListenerConfig) {
		c.itemKey = key
	}
}

// GetValue reads a resolved config value.
func (c *ConfigClient) GetValue(ctx context.Context, configID string, itemKey ...string) (interface{}, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	resolved, ok := c.configCache[configID]
	if !ok {
		return nil, nil
	}
	if len(itemKey) == 0 {
		cp := make(map[string]interface{}, len(resolved))
		for k, v := range resolved {
			cp[k] = v
		}
		return cp, nil
	}
	val, ok := resolved[itemKey[0]]
	if !ok {
		return nil, nil
	}
	return val, nil
}

// GetString returns the resolved string value for (configID, itemKey).
func (c *ConfigClient) GetString(ctx context.Context, configID, itemKey string, defaultVal ...string) (string, error) {
	val, err := c.GetValue(ctx, configID, itemKey)
	if err != nil {
		return "", err
	}
	if s, ok := val.(string); ok {
		return s, nil
	}
	if len(defaultVal) > 0 {
		return defaultVal[0], nil
	}
	return "", nil
}

// GetInt returns the resolved int value for (configID, itemKey).
func (c *ConfigClient) GetInt(ctx context.Context, configID, itemKey string, defaultVal ...int) (int, error) {
	val, err := c.GetValue(ctx, configID, itemKey)
	if err != nil {
		return 0, err
	}
	switch n := val.(type) {
	case int:
		return n, nil
	case float64:
		return int(n), nil
	case int64:
		return int(n), nil
	}
	if len(defaultVal) > 0 {
		return defaultVal[0], nil
	}
	return 0, nil
}

// GetBool returns the resolved bool value for (configID, itemKey).
func (c *ConfigClient) GetBool(ctx context.Context, configID, itemKey string, defaultVal ...bool) (bool, error) {
	val, err := c.GetValue(ctx, configID, itemKey)
	if err != nil {
		return false, err
	}
	if b, ok := val.(bool); ok {
		return b, nil
	}
	if len(defaultVal) > 0 {
		return defaultVal[0], nil
	}
	return false, nil
}

// diffAndFire compares old and new values and fires change listeners.
func (c *ConfigClient) diffAndFire(oldCache, newCache map[string]map[string]interface{}, source string) { //nolint:unparam // "websocket" source will be used when real-time config push is wired up
	c.listenersMu.Lock()
	listeners := make([]configChangeListener, len(c.listeners))
	copy(listeners, c.listeners)
	c.listenersMu.Unlock()

	if len(listeners) == 0 {
		return
	}

	allConfigKeys := make(map[string]struct{})
	for k := range oldCache {
		allConfigKeys[k] = struct{}{}
	}
	for k := range newCache {
		allConfigKeys[k] = struct{}{}
	}

	for cfgKey := range allConfigKeys {
		oldItems := oldCache[cfgKey]
		newItems := newCache[cfgKey]
		if oldItems == nil {
			oldItems = map[string]interface{}{}
		}
		if newItems == nil {
			newItems = map[string]interface{}{}
		}

		allItemKeys := make(map[string]struct{})
		for k := range oldItems {
			allItemKeys[k] = struct{}{}
		}
		for k := range newItems {
			allItemKeys[k] = struct{}{}
		}

		for iKey := range allItemKeys {
			oldVal := oldItems[iKey]
			newVal := newItems[iKey]
			if !reflect.DeepEqual(oldVal, newVal) {
				if c.client != nil {
					if metrics := c.client.metrics; metrics != nil {
						metrics.Record("config.changes", 1, "changes", map[string]string{"config": cfgKey})
					}
				}
				evt := &ConfigChangeEvent{
					ConfigID: cfgKey,
					ItemKey:  iKey,
					OldValue: oldVal,
					NewValue: newVal,
					Source:   source,
				}
				for _, l := range listeners {
					if l.configID != "" && l.configID != cfgKey {
						continue
					}
					if l.itemKey != "" && l.itemKey != iKey {
						continue
					}
					func() {
						defer func() { recover() }() //nolint:errcheck
						l.cb(evt)
					}()
				}
			}
		}
	}
}

// fetchAllConfigs walks every page of /configs and accumulates the full
// list. The server caps page size at fetchAllPageSize; we stop when a
// short page (fewer than fetchAllPageSize items) comes back.
func (c *ConfigClient) fetchAllConfigs(ctx context.Context) ([]*ConfigEntry, error) {
	mgmt := c.Management()
	var all []*ConfigEntry
	for page := 1; ; page++ {
		batch, err := mgmt.List(ctx, WithPageNumber(page), WithPageSize(fetchAllPageSize))
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < fetchAllPageSize {
			return all, nil
		}
	}
}

// fetchChain fetches the full ancestor chain starting from rootID.
func (c *ConfigClient) fetchChain(ctx context.Context, rootID string) ([]chainEntry, error) {
	var chain []chainEntry
	currentID := rootID
	for currentID != "" {
		node, err := c.getByID(ctx, currentID)
		if err != nil {
			return nil, err
		}
		chain = append(chain, chainEntry{
			ID:           node.ID,
			Values:       node.Items,
			Environments: node.Environments,
		})
		if node.Parent == nil {
			break
		}
		currentID = *node.Parent
	}
	return chain, nil
}

// resourceToConfig converts a generated ConfigResource to the SDK
// ConfigEntry type. The management back-reference allows the active
// record to Save / Delete itself.
func resourceToConfig(r genconfig.ConfigResource, m *ConfigManagement) *ConfigEntry {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	return &ConfigEntry{
		ID:           id,
		Name:         attrs.Name,
		Description:  attrs.Description,
		Parent:       attrs.Parent,
		Items:        extractItemValues(derefMap(attrs.Items)),
		Environments: extractEnvOverrides(derefEnvs(attrs.Environments)),
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       m,
	}
}

// buildConfigRequest constructs a ConfigRequest for create or update.
func buildConfigRequest(id, name string, desc, parent *string, items map[string]interface{}, envs map[string]map[string]interface{}) genconfig.ConfigRequest {
	return genconfig.ConfigRequest{
		Data: genconfig.ConfigResource{
			Id:   &id,
			Type: genconfig.ConfigResourceTypeConfig,
			Attributes: genconfig.Config{
				Name:         name,
				Description:  desc,
				Parent:       parent,
				Items:        refMap(wrapItemValues(items)),
				Environments: refEnvs(wrapEnvOverrides(envs)),
			},
		},
	}
}

// unmarshalResolved expands dot-notation keys and unmarshals into target.
func unmarshalResolved(resolved map[string]interface{}, target interface{}) error {
	if resolved == nil {
		return nil
	}
	nested := unflattenDotNotation(resolved)
	data, err := json.Marshal(nested)
	if err != nil {
		return fmt.Errorf("smplkit: failed to marshal resolved config: %w", err)
	}
	return json.Unmarshal(data, target)
}

// unflattenDotNotation converts flat dot-notation keys into nested maps.
// e.g. {"database.host": "localhost", "database.port": 5432} →
// {"database": {"host": "localhost", "port": 5432}}
func unflattenDotNotation(flat map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for key, value := range flat {
		parts := strings.Split(key, ".")
		current := result
		for i, part := range parts {
			if i == len(parts)-1 {
				current[part] = value
			} else {
				if next, ok := current[part]; ok {
					if nextMap, ok := next.(map[string]interface{}); ok {
						current = nextMap
					} else {
						// Conflict: overwrite non-map with map.
						newMap := make(map[string]interface{})
						current[part] = newMap
						current = newMap
					}
				} else {
					newMap := make(map[string]interface{})
					current[part] = newMap
					current = newMap
				}
			}
		}
	}
	return result
}

func derefMap(m *map[string]genconfig.ConfigItemDefinition) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{}, len(*m))
	for k, v := range *m {
		result[k] = map[string]interface{}{"value": v.Value}
	}
	return result
}

func refMap(m map[string]interface{}) *map[string]genconfig.ConfigItemDefinition {
	if m == nil {
		return nil
	}
	result := make(map[string]genconfig.ConfigItemDefinition, len(m))
	for k, v := range m {
		inner := v.(map[string]interface{})
		t := genconfig.ConfigItemDefinitionType(inner["type"].(string))
		result[k] = genconfig.ConfigItemDefinition{Value: inner["value"], Type: &t}
	}
	return &result
}

func derefEnvs(envs *map[string]genconfig.EnvironmentOverride) map[string]map[string]interface{} {
	if envs == nil {
		return nil
	}
	result := make(map[string]map[string]interface{}, len(*envs))
	for k, v := range *envs {
		entry := make(map[string]interface{})
		if v.Values != nil {
			vals := make(map[string]interface{}, len(*v.Values))
			for vk, vv := range *v.Values {
				vals[vk] = map[string]interface{}{"value": vv.Value}
			}
			entry["values"] = vals
		}
		result[k] = entry
	}
	return result
}

func refEnvs(envs map[string]map[string]interface{}) *map[string]genconfig.EnvironmentOverride {
	if envs == nil {
		return nil
	}
	result := make(map[string]genconfig.EnvironmentOverride, len(envs))
	for envName, envEntry := range envs {
		var override genconfig.EnvironmentOverride
		if vals, ok := envEntry["values"]; ok {
			if valsMap, ok := vals.(map[string]interface{}); ok {
				wrapped := make(map[string]genconfig.ConfigItemOverride, len(valsMap))
				for vk, vv := range valsMap {
					wrapped[vk] = genconfig.ConfigItemOverride{Value: vv.(map[string]interface{})["value"]}
				}
				override.Values = &wrapped
			}
		}
		result[envName] = override
	}
	return &result
}

func extractItemValues(items map[string]interface{}) map[string]interface{} {
	if items == nil {
		return nil
	}
	result := make(map[string]interface{}, len(items))
	for k, v := range items {
		if m, ok := v.(map[string]interface{}); ok {
			if val, exists := m["value"]; exists {
				result[k] = val
				continue
			}
		}
		result[k] = v
	}
	return result
}

func extractEnvOverrides(envs map[string]map[string]interface{}) map[string]map[string]interface{} {
	if envs == nil {
		return nil
	}
	result := make(map[string]map[string]interface{}, len(envs))
	for envName, envEntry := range envs {
		extracted := make(map[string]interface{}, len(envEntry))
		for k, v := range envEntry {
			if k == "values" {
				if valsMap, ok := v.(map[string]interface{}); ok {
					unwrapped := make(map[string]interface{}, len(valsMap))
					for vk, vv := range valsMap {
						if m, ok := vv.(map[string]interface{}); ok {
							if val, exists := m["value"]; exists {
								unwrapped[vk] = val
								continue
							}
						}
						unwrapped[vk] = vv
					}
					extracted[k] = unwrapped
					continue
				}
			}
			extracted[k] = v
		}
		result[envName] = extracted
	}
	return result
}

func wrapItemValues(items map[string]interface{}) map[string]interface{} {
	if items == nil {
		return nil
	}
	result := make(map[string]interface{}, len(items))
	for k, v := range items {
		result[k] = map[string]interface{}{
			"value": v,
			"type":  "JSON",
		}
	}
	return result
}

func wrapEnvOverrides(envs map[string]map[string]interface{}) map[string]map[string]interface{} {
	if envs == nil {
		return nil
	}
	result := make(map[string]map[string]interface{}, len(envs))
	for envName, envEntry := range envs {
		wrapped := make(map[string]interface{}, len(envEntry))
		for k, v := range envEntry {
			if k == "values" {
				if valsMap, ok := v.(map[string]interface{}); ok {
					wrappedVals := make(map[string]interface{}, len(valsMap))
					for vk, vv := range valsMap {
						wrappedVals[vk] = map[string]interface{}{
							"value": vv,
						}
					}
					wrapped[k] = wrappedVals
					continue
				}
			}
			wrapped[k] = v
		}
		result[envName] = wrapped
	}
	return result
}
