package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/smplkit/go-sdk/internal/debug"
	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
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

	wsManager *sharedWebSocket

	management *ConfigManagement
}

// Management returns the sub-object for config CRUD operations.
func (c *ConfigClient) Management() *ConfigManagement {
	if c.management == nil {
		c.management = &ConfigManagement{client: c}
	}
	return c.management
}

// getByID retrieves a config by its ID (internal use for chain walking).
func (c *ConfigClient) getByID(ctx context.Context, id string) (*Config, error) {
	resp, err := c.generated.GetConfig(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	return resourceToConfig(result.Data, c), nil
}

// createConfig creates the config on the server and updates the local instance.
func (c *ConfigClient) createConfig(ctx context.Context, cfg *Config) error {
	reqBody := buildConfigRequest(cfg.ID, cfg.Name, cfg.Description, cfg.Parent, cfg.Items, cfg.Environments)

	resp, err := c.generated.CreateConfigWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	cfg.apply(resourceToConfig(result.Data, c))
	return nil
}

// updateConfig updates the config on the server and updates the local instance.
func (c *ConfigClient) updateConfig(ctx context.Context, cfg *Config) error {
	reqBody := buildConfigRequest(cfg.ID, cfg.Name, cfg.Description, cfg.Parent, cfg.Items, cfg.Environments)

	resp, err := c.generated.UpdateConfigWithApplicationVndAPIPlusJSONBody(ctx, cfg.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	cfg.apply(resourceToConfig(result.Data, c))
	return nil
}

// Get returns the resolved config values for the given ID.
func (c *ConfigClient) Get(ctx context.Context, id string) (map[string]interface{}, error) {
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
	// Return a copy.
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
	resolved, err := c.Get(ctx, id)
	if err != nil {
		return err
	}
	return unmarshalResolved(resolved, target)
}

// Subscribe returns a LiveConfig whose Value() always reflects the latest
// resolved values for the given config ID.
func (c *ConfigClient) Subscribe(ctx context.Context, id string) (*LiveConfig, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	return &LiveConfig{client: c, id: id}, nil
}

// ensureInit performs initialization on first runtime access.
func (c *ConfigClient) ensureInit(ctx context.Context) error {
	c.initOnce.Do(func() {
		environment := c.client.environment
		debug.Debug("api", "fetching config definitions")
		configs, err := c.Management().List(ctx)
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
		ws := c.client.ensureWS()
		c.wsManager = ws
		ws.on("config_changed", c.handleConfigChanged)
		ws.on("config_deleted", c.handleConfigChanged)
	})
	return c.initErr
}

func (c *ConfigClient) handleConfigChanged(data map[string]interface{}) {
	configID, _ := data["id"].(string)
	debug.Debug("websocket", "config event received, id=%q", configID)
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
		return &SmplError{Message: "No environment set."}
	}

	configs, err := c.Management().List(ctx)
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

// resourceToConfig converts a generated ConfigResource to the SDK Config type.
func resourceToConfig(r genconfig.ConfigResource, c *ConfigClient) *Config {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	return &Config{
		ID:           id,
		Name:         attrs.Name,
		Description:  attrs.Description,
		Parent:       attrs.Parent,
		Items:        extractItemValues(derefMap(attrs.Items)),
		Environments: extractEnvOverrides(derefEnvs(attrs.Environments)),
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       c,
	}
}

// buildConfigRequest constructs a ConfigResponse for create or update.
func buildConfigRequest(id, name string, desc, parent *string, items map[string]interface{}, envs map[string]map[string]interface{}) genconfig.ConfigResponse {
	return genconfig.ConfigResponse{
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
