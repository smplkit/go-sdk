package smplkit

import (
	"context"
	"time"
)

// ItemType is the declared value type of a config item. It constrains the
// JSON shape of the item's value and of every per-environment override of
// the same key. Mirrors the flag-side FlagType enum.
type ItemType string

// Supported ItemType values, alphabetical by wire constant.
const (
	// ItemTypeBoolean represents a boolean config item.
	ItemTypeBoolean ItemType = "BOOLEAN"
	// ItemTypeJSON represents a JSON config item.
	ItemTypeJSON ItemType = "JSON"
	// ItemTypeNumber represents a numeric config item.
	ItemTypeNumber ItemType = "NUMBER"
	// ItemTypeString represents a string config item.
	ItemTypeString ItemType = "STRING"
)

// ConfigItem is a single typed item within a ConfigEntry. Pass one to
// ConfigEntry.Set to write (or replace) an item with full control over its
// declared type and description; the SetString / SetNumber / SetBoolean /
// SetJSON convenience setters build a ConfigItem for you.
type ConfigItem struct {
	// Name is the item key within its config.
	Name string
	// Value is the item's value.
	Value interface{}
	// Type is the declared value type (STRING, NUMBER, BOOLEAN, or JSON).
	Type ItemType
	// Description is an optional human-readable description.
	Description string
}

// ConfigEntry represents a configuration resource from the smplkit platform.
type ConfigEntry struct {
	// ID is the config identifier (e.g. "user_service").
	ID string
	// Name is the display name for the config.
	Name string
	// Description is an optional description of the config.
	Description *string
	// Parent is the parent config ID, or nil for root configs.
	Parent *string
	// Items holds the base configuration values as a flat {key: value} map.
	Items map[string]interface{}
	// Environments maps environment names to their value overrides.
	Environments map[string]map[string]interface{}
	// CreatedAt is the creation timestamp.
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	// itemsRaw retains the full typed shape of each base item
	// ({key: {value, type, description}}) parsed from the wire or written by
	// a setter, so the declared type and description survive a get-mutate-put
	// round-trip rather than being re-inferred. Surfaced via ItemsRaw().
	itemsRaw map[string]map[string]interface{}

	// client is the back-reference to the fused ConfigClient that owns
	// the create/update/delete logic for this active-record model.
	client *ConfigClient
}

// ItemsRaw returns the full typed view of the base items as a read-only deep
// copy: {key: {"value": ..., "type": "STRING"|"NUMBER"|"BOOLEAN"|"JSON",
// "description": ...}}. The "description" key is present only when the item
// carries one. Unlike Items (which exposes resolved values only), this view
// preserves each item's declared type and description.
func (c *ConfigEntry) ItemsRaw() map[string]map[string]interface{} {
	if c.itemsRaw == nil {
		return nil
	}
	out := make(map[string]map[string]interface{}, len(c.itemsRaw))
	for k, raw := range c.itemsRaw {
		cp := make(map[string]interface{}, len(raw))
		for ik, iv := range raw {
			cp[ik] = iv
		}
		out[k] = cp
	}
	return out
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
// client.Config().Delete(ctx, c.ID).
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
	c.itemsRaw = other.itemsRaw
	c.CreatedAt = other.CreatedAt
	c.UpdatedAt = other.UpdatedAt
}

// LiveConfig is a live, dict-like, read-only proxy for a config's resolved
// values. Returned by ConfigClient.Subscribe. Every read goes through the
// client's resolved-config cache, so pushed updates are picked up
// automatically.
//
// Customer mutation paths are absent: there is no Set / Put / Delete
// method on LiveConfig. To mutate configs use the editable record:
//
//	client.Config().Get(ctx, id) // active-record model with Save / Delete
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

// Get returns a single resolved value by key. The second return is false
// if the key is absent.
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

// OnChange registers a change listener scoped to this config: it fires when
// any item in this config changes.
func (lc *LiveConfig) OnChange(cb func(*ConfigChangeEvent)) {
	lc.client.OnChange(cb, WithConfigID(lc.id))
}

// OnChangeKey registers a listener that fires only when the named item
// in this config changes. Mirrors `proxy.on_change(item_key=...)`.
func (lc *LiveConfig) OnChangeKey(key string, cb func(*ConfigChangeEvent)) {
	lc.client.OnChange(cb, WithConfigID(lc.id), WithItemKey(key))
}
