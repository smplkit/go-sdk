package smplkit

import (
	"context"
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

	// client is the back-reference to ConfigClient, set by factory methods.
	client *ConfigClient
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
	if c.CreatedAt == nil {
		return c.client.createConfig(ctx, c)
	}
	return c.client.updateConfig(ctx, c)
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

// LiveConfig is a handle returned by Subscribe that always reflects the latest
// resolved values for a config ID.
type LiveConfig struct {
	client *ConfigClient
	id     string
}

// Value returns the latest resolved values for this config.
func (lc *LiveConfig) Value() map[string]interface{} {
	if lc.client.configCache == nil {
		return nil
	}
	resolved, ok := lc.client.configCache[lc.id]
	if !ok {
		return nil
	}
	// Return a copy.
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
