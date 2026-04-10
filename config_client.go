package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/google/uuid"

	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
)

// ConfigChangeEvent describes a single value change detected on refresh.
type ConfigChangeEvent struct {
	// ConfigKey is the config key that changed (e.g. "user_service").
	ConfigKey string
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
	configKey string // "" matches all configs
	itemKey   string // "" matches all items
	cb        func(*ConfigChangeEvent)
}

// ConfigClient provides CRUD operations for config resources and
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
}

// --- Factory method (Active Record pattern) ---

// New creates an unsaved Config with the given key. Call Save(ctx) to persist.
// If name is not provided via WithConfigName, it is auto-generated from the key.
func (c *ConfigClient) New(key string, opts ...ConfigOption) *Config {
	cfg := &Config{
		Key:          key,
		Name:         keyToDisplayName(key),
		Items:        map[string]interface{}{},
		Environments: map[string]map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// --- Management CRUD ---

// Get retrieves a config by its key.
// Returns SmplNotFoundError if no match.
func (c *ConfigClient) Get(ctx context.Context, key string) (*Config, error) {
	params := &genconfig.ListConfigsParams{FilterKey: &key}
	resp, err := c.generated.ListConfigs(ctx, params)
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

	var result genconfig.ConfigListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, &SmplNotFoundError{
			SmplError: SmplError{
				Message:    fmt.Sprintf("config with key %q not found", key),
				StatusCode: 404,
			},
		}
	}
	return resourceToConfig(result.Data[0], c), nil
}

// getByID retrieves a config by its UUID (internal use for chain walking).
func (c *ConfigClient) getByID(ctx context.Context, id string) (*Config, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("smplkit: invalid config ID %q: %w", id, err)
	}

	resp, err := c.generated.GetConfig(ctx, uid)
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

// List returns all configs for the account.
func (c *ConfigClient) List(ctx context.Context) ([]*Config, error) {
	resp, err := c.generated.ListConfigs(ctx, nil)
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

	var result genconfig.ConfigListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	configs := make([]*Config, len(result.Data))
	for i := range result.Data {
		configs[i] = resourceToConfig(result.Data[i], c)
	}
	return configs, nil
}

// Delete removes a config by its key.
// Returns SmplNotFoundError if not found.
func (c *ConfigClient) Delete(ctx context.Context, key string) error {
	cfg, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	return c.deleteByID(ctx, cfg.ID)
}

// deleteByID removes a config by its UUID.
func (c *ConfigClient) deleteByID(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("smplkit: invalid config ID %q: %w", id, err)
	}

	resp, err := c.generated.DeleteConfig(ctx, uid)
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
	return checkStatus(resp.StatusCode, body)
}

// createConfig sends a POST to create the config, then applies the response.
func (c *ConfigClient) createConfig(ctx context.Context, cfg *Config) error {
	reqBody := buildConfigRequest("", cfg.Name, &cfg.Key, cfg.Description, cfg.Parent, cfg.Items, cfg.Environments)

	resp, err := c.generated.CreateConfig(ctx, reqBody)
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

// updateConfig sends a PUT to update the config, then applies the response.
func (c *ConfigClient) updateConfig(ctx context.Context, cfg *Config) error {
	uid, err := uuid.Parse(cfg.ID)
	if err != nil {
		return fmt.Errorf("smplkit: invalid config ID %q: %w", cfg.ID, err)
	}

	reqBody := buildConfigRequest(cfg.ID, cfg.Name, &cfg.Key, cfg.Description, cfg.Parent, cfg.Items, cfg.Environments)

	resp, err := c.generated.UpdateConfig(ctx, uid, reqBody)
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

// --- Runtime: Resolve / Subscribe ---

// Resolve returns the resolved config values for the given key.
func (c *ConfigClient) Resolve(ctx context.Context, key string) (map[string]interface{}, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	resolved, ok := c.configCache[key]
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

// ResolveInto resolves the config and unmarshals it into the target struct.
// The target must be a pointer to a struct. Dot-notation keys are unflattened
// into nested maps before unmarshaling via JSON round-trip.
func (c *ConfigClient) ResolveInto(ctx context.Context, key string, target interface{}) error {
	resolved, err := c.Resolve(ctx, key)
	if err != nil {
		return err
	}
	return unmarshalResolved(resolved, target)
}

// Subscribe returns a LiveConfig whose Value() always reflects the latest
// resolved values for the given key.
func (c *ConfigClient) Subscribe(ctx context.Context, key string) (*LiveConfig, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	return &LiveConfig{client: c, key: key}, nil
}

// --- Runtime: Lazy Init ---

// ensureInit performs lazy initialization on first prescriptive access.
// Fetches all configs, resolves values for the environment, and caches them.
func (c *ConfigClient) ensureInit(ctx context.Context) error {
	c.initOnce.Do(func() {
		environment := c.client.environment
		configs, err := c.List(ctx)
		if err != nil {
			c.initErr = err
			return
		}

		cache := make(map[string]map[string]interface{})
		for _, cfg := range configs {
			chain, fetchErr := c.fetchChain(ctx, cfg.ID)
			if fetchErr != nil {
				c.initErr = fetchErr
				return
			}
			cache[cfg.Key] = resolveChain(chain, environment)
		}
		c.configCache = cache
	})
	return c.initErr
}

// --- Runtime: Refresh & OnChange ---

// Refresh re-fetches all configs and re-resolves values.
// OnChange listeners fire for any values that changed.
func (c *ConfigClient) Refresh(ctx context.Context) error {
	if err := c.ensureInit(ctx); err != nil {
		return err
	}
	environment := c.client.environment
	if environment == "" {
		return &SmplError{Message: "No environment set."}
	}

	configs, err := c.List(ctx)
	if err != nil {
		return err
	}

	newCache := make(map[string]map[string]interface{})
	for _, cfg := range configs {
		chain, fetchErr := c.fetchChain(ctx, cfg.ID)
		if fetchErr != nil {
			return fetchErr
		}
		newCache[cfg.Key] = resolveChain(chain, environment)
	}
	oldCache := c.configCache
	c.configCache = newCache
	c.diffAndFire(oldCache, newCache, "manual")
	return nil
}

// OnChange registers a listener that fires when a config value changes (on Refresh).
// Use WithConfigKey and/or WithItemKey to scope the listener.
func (c *ConfigClient) OnChange(cb func(*ConfigChangeEvent), opts ...ChangeListenerOption) {
	var cfg changeListenerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	c.listenersMu.Lock()
	c.listeners = append(c.listeners, configChangeListener{
		configKey: cfg.configKey,
		itemKey:   cfg.itemKey,
		cb:        cb,
	})
	c.listenersMu.Unlock()
}

// ChangeListenerOption configures an OnChange listener.
type ChangeListenerOption func(*changeListenerConfig)

type changeListenerConfig struct {
	configKey string
	itemKey   string
}

// WithConfigKey restricts the listener to changes in the given config.
func WithConfigKey(key string) ChangeListenerOption {
	return func(c *changeListenerConfig) {
		c.configKey = key
	}
}

// WithItemKey restricts the listener to changes of the given item key.
func WithItemKey(key string) ChangeListenerOption {
	return func(c *changeListenerConfig) {
		c.itemKey = key
	}
}

// --- Prescriptive access (legacy, delegates to Resolve) ---

// GetValue reads a resolved config value.
func (c *ConfigClient) GetValue(ctx context.Context, configKey string, itemKey ...string) (interface{}, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	resolved, ok := c.configCache[configKey]
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

// GetString returns the resolved string value for (configKey, itemKey).
func (c *ConfigClient) GetString(ctx context.Context, configKey, itemKey string, defaultVal ...string) (string, error) {
	val, err := c.GetValue(ctx, configKey, itemKey)
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

// GetInt returns the resolved int value for (configKey, itemKey).
func (c *ConfigClient) GetInt(ctx context.Context, configKey, itemKey string, defaultVal ...int) (int, error) {
	val, err := c.GetValue(ctx, configKey, itemKey)
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

// GetBool returns the resolved bool value for (configKey, itemKey).
func (c *ConfigClient) GetBool(ctx context.Context, configKey, itemKey string, defaultVal ...bool) (bool, error) {
	val, err := c.GetValue(ctx, configKey, itemKey)
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

// --- Internal helpers ---

// diffAndFire compares old and new caches and fires change listeners.
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
				evt := &ConfigChangeEvent{
					ConfigKey: cfgKey,
					ItemKey:   iKey,
					OldValue:  oldVal,
					NewValue:  newVal,
					Source:    source,
				}
				for _, l := range listeners {
					if l.configKey != "" && l.configKey != cfgKey {
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
	key := ""
	if attrs.Key != nil {
		key = *attrs.Key
	}
	return &Config{
		ID:           id,
		Key:          key,
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

// buildConfigRequest constructs a ResponseConfig for create or update.
func buildConfigRequest(id, name string, key, desc, parent *string, items map[string]interface{}, envs map[string]map[string]interface{}) genconfig.ResponseConfig {
	var idPtr *string
	if id != "" {
		idPtr = &id
	}
	configType := "config"
	return genconfig.ResponseConfig{
		Data: genconfig.ResourceConfig{
			Id:   idPtr,
			Type: &configType,
			Attributes: genconfig.Config{
				Name:         name,
				Key:          key,
				Description:  desc,
				Parent:       parent,
				Items:        refMap(wrapItemValues(items)),
				Environments: refEnvs(wrapEnvOverrides(envs)),
			},
		},
	}
}

// unmarshalResolved unflattens dot-notation keys and unmarshals into target.
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

// --- Value wrapping/unwrapping helpers ---

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
