package smplkit

import (
	"context"
	"log"
	"time"
)

// Config represents a configuration resource from the smplkit platform.
type ConfigEntry struct {
	// ID is the config identifier (e.g. "user_service").
	ID string
	// Name is the display name for the config.
	Name string
	// Description is an optional description of the config.
	Description *string
	// Parent is the parent config ID, or nil for root configs.
	Parent *string
	// Items holds the base configuration values.
	Items map[string]interface{}
	// Environments maps environment names to their value overrides.
	Environments map[string]map[string]interface{}
	// CreatedAt is the creation timestamp.
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	// client is the back-reference to the management surface that owns
	// the create/update/delete logic for this active-record model.
	client *ConfigManagement
}

// ConfigOption configures an unsaved Config returned by ConfigClient.New.
type ConfigOption func(*ConfigEntry)

// WithConfigName sets the display name for a config.
func WithConfigName(name string) ConfigOption {
	return func(c *ConfigEntry) { c.Name = name }
}

// WithConfigDescription sets the description for a config.
func WithConfigDescription(desc string) ConfigOption {
	return func(c *ConfigEntry) { c.Description = &desc }
}

// WithConfigParent sets the parent config UUID for inheritance.
func WithConfigParent(parentID string) ConfigOption {
	return func(c *ConfigEntry) { c.Parent = &parentID }
}

// WithConfigItems sets the base configuration values for a config.
func WithConfigItems(items map[string]interface{}) ConfigOption {
	return func(c *ConfigEntry) { c.Items = items }
}

// WithConfigEnvironments sets the environment-specific overrides for a config.
func WithConfigEnvironments(envs map[string]map[string]interface{}) ConfigOption {
	return func(c *ConfigEntry) { c.Environments = envs }
}

// Save persists the config to the server.
// The Config instance is updated with the server response.
func (c *ConfigEntry) Save(ctx context.Context) error {
	if c.client == nil {
		return &Error{Message: "config was constructed without a client; cannot save"}
	}
	if c.CreatedAt == nil {
		return c.client.createConfig(ctx, c)
	}
	return c.client.updateConfig(ctx, c)
}

// Delete removes the config from the server. Equivalent to
// mgmt.Config().Delete(ctx, c.ID).
func (c *ConfigEntry) Delete(ctx context.Context) error {
	if c.client == nil || c.ID == "" {
		return &Error{Message: "config was constructed without a client or id; cannot delete"}
	}
	return c.client.Delete(ctx, c.ID)
}

func (c *ConfigEntry) apply(other *ConfigEntry) {
	c.ID = other.ID
	c.Name = other.Name
	c.Description = other.Description
	c.Parent = other.Parent
	c.Items = other.Items
	c.Environments = other.Environments
	c.CreatedAt = other.CreatedAt
	c.UpdatedAt = other.UpdatedAt
}

// LiveConfig is a live, dict-like, read-only proxy for a config's resolved
// values. Returned by ConfigClient.Get and Subscribe. Every read goes
// through the client's resolved-config cache, so WebSocket updates are
// picked up automatically — no Subscribe step is required (rule 10 of
// the cross-SDK overhaul).
//
// Customer mutation paths are absent: there is no Set / Put / Delete
// method on LiveConfig. To mutate configs use the management surface:
//
//	client.Manage().Config().Get(ctx, id) // active-record model with Save / Delete
type LiveConfig struct {
	client *ConfigClient
	id     string
}

// ID returns the config ID this proxy reads from.
func (lc *LiveConfig) ID() string { return lc.id }

// Value returns a defensive copy of the latest resolved values.
func (lc *LiveConfig) Value() map[string]interface{} {
	if lc.client.configCache == nil {
		return nil
	}
	resolved, ok := lc.client.configCache[lc.id]
	if !ok {
		return nil
	}
	cp := make(map[string]interface{}, len(resolved))
	for k, v := range resolved {
		cp[k] = v
	}
	return cp
}

// ValueInto unmarshals the latest resolved values into the target struct.
// The target must be a pointer to a struct. Dot-notation keys (e.g. "database.host")
// are expanded into nested structures before unmarshaling.
func (lc *LiveConfig) ValueInto(target interface{}) error {
	resolved := lc.Value()
	return unmarshalResolved(resolved, target)
}

// Get returns a single resolved value by key. The second return is false
// if the key is absent. Mirrors Python's `proxy[key]` / `proxy.get(key)`
// dict-like access.
func (lc *LiveConfig) Get(key string) (interface{}, bool) {
	if lc.client.configCache == nil {
		return nil, false
	}
	resolved, ok := lc.client.configCache[lc.id]
	if !ok {
		return nil, false
	}
	v, present := resolved[key]
	return v, present
}

// Has reports whether a resolved value exists for the given key.
func (lc *LiveConfig) Has(key string) bool {
	_, ok := lc.Get(key)
	return ok
}

// Keys returns a snapshot of the resolved key set in unspecified order.
func (lc *LiveConfig) Keys() []string {
	if lc.client.configCache == nil {
		return nil
	}
	resolved, ok := lc.client.configCache[lc.id]
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(resolved))
	for k := range resolved {
		keys = append(keys, k)
	}
	return keys
}

// Len returns the number of resolved keys.
func (lc *LiveConfig) Len() int {
	if lc.client.configCache == nil {
		return 0
	}
	resolved, ok := lc.client.configCache[lc.id]
	if !ok {
		return 0
	}
	return len(resolved)
}

// OnChange registers a listener that fires when any item in this config
// changes. Mirrors Python's `proxy.on_change(fn)` listener-form sugar
// (rule 11 — the proxy-scoped form of OnChange).
func (lc *LiveConfig) OnChange(cb func(*ConfigChangeEvent)) {
	lc.client.OnChange(cb, WithConfigID(lc.id))
}

// OnChangeKey registers a listener that fires only when the named item
// in this config changes. Mirrors `proxy.on_change(item_key=...)`.
func (lc *LiveConfig) OnChangeKey(key string, cb func(*ConfigChangeEvent)) {
	lc.client.OnChange(cb, WithConfigID(lc.id), WithItemKey(key))
}

// ItemOption configures an item declaration made through a typed getter.
type ItemOption func(*itemOptions)

type itemOptions struct {
	description string
}

// WithItemDescription attaches a human-readable description to a
// declared item. Mirrors Python's `description="..."` kwarg.
func WithItemDescription(description string) ItemOption {
	return func(o *itemOptions) { o.description = description }
}

func applyItemOptions(opts []ItemOption) itemOptions {
	var o itemOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// ID exposed for callers that need the proxy's config id (parent
// resolution, logging). Already declared above on LiveConfig.

// GetBool reads a BOOLEAN item, registering the declaration on first
// call. Returns the resolved value, or the default when the item is
// absent or the resolved value cannot be coerced to bool.
func (lc *LiveConfig) GetBool(key string, defaultValue bool, opts ...ItemOption) bool {
	o := applyItemOptions(opts)
	lc.client.observeItemDeclaration(lc.id, key, "BOOLEAN", defaultValue, o.description)
	resolved, ok := lc.client.cachedValues(lc.id)
	if !ok {
		return defaultValue
	}
	v, present := resolved[key]
	if !present {
		return defaultValue
	}
	if b, ok := v.(bool); ok {
		return b
	}
	log.Printf("smplkit: item type mismatch: %s.%s expected BOOLEAN, returning default", lc.id, key)
	return defaultValue
}

// GetInt reads a NUMBER item as int, registering the declaration on
// first call. Returns the default on absence or type mismatch.
func (lc *LiveConfig) GetInt(key string, defaultValue int, opts ...ItemOption) int {
	o := applyItemOptions(opts)
	lc.client.observeItemDeclaration(lc.id, key, "NUMBER", defaultValue, o.description)
	resolved, ok := lc.client.cachedValues(lc.id)
	if !ok {
		return defaultValue
	}
	v, present := resolved[key]
	if !present {
		return defaultValue
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		if n == float64(int(n)) {
			return int(n)
		}
	}
	log.Printf("smplkit: item type mismatch: %s.%s expected NUMBER (int), returning default", lc.id, key)
	return defaultValue
}

// GetFloat reads a NUMBER item as float64, registering the declaration
// on first call. Returns the default on absence or type mismatch.
func (lc *LiveConfig) GetFloat(key string, defaultValue float64, opts ...ItemOption) float64 {
	o := applyItemOptions(opts)
	lc.client.observeItemDeclaration(lc.id, key, "NUMBER", defaultValue, o.description)
	resolved, ok := lc.client.cachedValues(lc.id)
	if !ok {
		return defaultValue
	}
	v, present := resolved[key]
	if !present {
		return defaultValue
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	log.Printf("smplkit: item type mismatch: %s.%s expected NUMBER (float), returning default", lc.id, key)
	return defaultValue
}

// GetString reads a STRING item, registering the declaration on first
// call. Returns the default on absence or type mismatch.
func (lc *LiveConfig) GetString(key string, defaultValue string, opts ...ItemOption) string {
	o := applyItemOptions(opts)
	lc.client.observeItemDeclaration(lc.id, key, "STRING", defaultValue, o.description)
	resolved, ok := lc.client.cachedValues(lc.id)
	if !ok {
		return defaultValue
	}
	v, present := resolved[key]
	if !present {
		return defaultValue
	}
	if s, ok := v.(string); ok {
		return s
	}
	log.Printf("smplkit: item type mismatch: %s.%s expected STRING, returning default", lc.id, key)
	return defaultValue
}

// GetJSON reads a JSON item, registering the declaration on first call.
// Unlike the other typed getters this accepts any value type since JSON
// is the escape hatch for structured payloads. Returns the default when
// the item is absent.
func (lc *LiveConfig) GetJSON(key string, defaultValue interface{}, opts ...ItemOption) interface{} {
	o := applyItemOptions(opts)
	lc.client.observeItemDeclaration(lc.id, key, "JSON", defaultValue, o.description)
	resolved, ok := lc.client.cachedValues(lc.id)
	if !ok {
		return defaultValue
	}
	v, present := resolved[key]
	if !present {
		return defaultValue
	}
	return v
}
