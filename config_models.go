package smplkit

import (
	"context"
	"time"
)

// Config represents a configuration resource from the smplkit platform.
type Config struct {
	// ID is the unique identifier (UUID) of the config. Empty for unsaved configs.
	ID string
	// Key is the human-readable config key (e.g. "user_service").
	Key string
	// Name is the display name for the config.
	Name string
	// Description is an optional description of the config.
	Description *string
	// Parent is the parent config UUID, or nil for root configs.
	Parent *string
	// Items holds the base configuration values (extracted raw values from typed items).
	Items map[string]interface{}
	// Environments maps environment names to their value overrides.
	// Each environment entry is a map that contains a "values" key
	// with extracted raw values from wrapped overrides.
	Environments map[string]map[string]interface{}
	// CreatedAt is the creation timestamp.
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	// client is the back-reference to ConfigClient, set by factory methods.
	client *ConfigClient
}

// ConfigOption configures an unsaved Config returned by ConfigClient.New.
type ConfigOption func(*Config)

// WithConfigName sets the display name for a config.
func WithConfigName(name string) ConfigOption {
	return func(c *Config) { c.Name = name }
}

// WithConfigDescription sets the description for a config.
func WithConfigDescription(desc string) ConfigOption {
	return func(c *Config) { c.Description = &desc }
}

// WithConfigParent sets the parent config UUID for inheritance.
func WithConfigParent(parentID string) ConfigOption {
	return func(c *Config) { c.Parent = &parentID }
}

// WithConfigItems sets the base configuration values for a config.
func WithConfigItems(items map[string]interface{}) ConfigOption {
	return func(c *Config) { c.Items = items }
}

// WithConfigEnvironments sets the environment-specific overrides for a config.
func WithConfigEnvironments(envs map[string]map[string]interface{}) ConfigOption {
	return func(c *Config) { c.Environments = envs }
}

// Save creates (POST) the config if ID is empty, or updates (PUT) if ID is set.
// Applies the server response back to the Config instance.
func (c *Config) Save(ctx context.Context) error {
	if c.ID == "" {
		return c.client.createConfig(ctx, c)
	}
	return c.client.updateConfig(ctx, c)
}

func (c *Config) apply(other *Config) {
	c.ID = other.ID
	c.Key = other.Key
	c.Name = other.Name
	c.Description = other.Description
	c.Parent = other.Parent
	c.Items = other.Items
	c.Environments = other.Environments
	c.CreatedAt = other.CreatedAt
	c.UpdatedAt = other.UpdatedAt
}

// LiveConfig is a handle returned by Subscribe that always reflects the latest
// cached resolved values for a config key.
type LiveConfig struct {
	client *ConfigClient
	key    string
}

// Value returns the latest resolved values for this config.
func (lc *LiveConfig) Value() map[string]interface{} {
	if lc.client.configCache == nil {
		return nil
	}
	resolved, ok := lc.client.configCache[lc.key]
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
// The target must be a pointer to a struct. Dot-notation keys are unflattened
// into nested maps before unmarshaling.
func (lc *LiveConfig) ValueInto(target interface{}) error {
	resolved := lc.Value()
	return unmarshalResolved(resolved, target)
}
