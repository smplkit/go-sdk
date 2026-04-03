package smplkit

import (
	"context"
	"time"
)

// Config represents a configuration resource from the smplkit platform.
type Config struct {
	// ID is the unique identifier (UUID) of the config.
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

// Update replaces this config's attributes on the server. Any nil field in
// params falls back to the config's current value. Updates the config's fields
// in place on success.
//
// Returns SmplNotFoundError if the config no longer exists.
func (c *Config) Update(ctx context.Context, params UpdateConfigParams) error {
	return c.update(ctx, params)
}

// SetValues replaces the base or environment-specific values for this config.
// Pass an empty string for environment to replace base values.
// Pass an environment name (e.g. "production") to replace that environment's values.
//
// Returns SmplNotFoundError if the config no longer exists.
func (c *Config) SetValues(ctx context.Context, values map[string]interface{}, environment string) error {
	var newItems map[string]interface{}
	var newEnvs map[string]map[string]interface{}

	if environment == "" {
		newItems = values
		newEnvs = c.Environments
	} else {
		newItems = c.Items
		envEntry := make(map[string]interface{})
		if existing, ok := c.Environments[environment]; ok {
			for k, v := range existing {
				envEntry[k] = v
			}
		}
		envEntry["values"] = values
		newEnvs = make(map[string]map[string]interface{})
		for k, v := range c.Environments {
			newEnvs[k] = v
		}
		newEnvs[environment] = envEntry
	}

	return c.update(ctx, UpdateConfigParams{
		Items:        newItems,
		Environments: newEnvs,
	})
}

// SetValue sets a single key within base or environment-specific values.
// Pass an empty string for environment to set a base value.
// This merges the key into existing values rather than replacing all values.
//
// Returns SmplNotFoundError if the config no longer exists.
func (c *Config) SetValue(ctx context.Context, key string, value interface{}, environment string) error {
	if environment == "" {
		merged := make(map[string]interface{})
		for k, v := range c.Items {
			merged[k] = v
		}
		merged[key] = value
		return c.SetValues(ctx, merged, "")
	}

	existing := make(map[string]interface{})
	if envEntry, ok := c.Environments[environment]; ok {
		if vals, ok := envEntry["values"]; ok {
			if valsMap, ok := vals.(map[string]interface{}); ok {
				for k, v := range valsMap {
					existing[k] = v
				}
			}
		}
	}
	existing[key] = value
	return c.SetValues(ctx, existing, environment)
}

// Connect creates a live, reactive ConfigRuntime for this config in the given
// environment. The runtime maintains a WebSocket connection for real-time updates
// and supports OnChange listeners. Call Close on the returned runtime when done.
//
// For simple prescriptive access, use ConfigClient.GetValue after calling
// Client.Connect instead.
func (c *Config) Connect(ctx context.Context, environment string) (*ConfigRuntime, error) {
	return c.client.connect(ctx, c, environment)
}

// update is the internal implementation shared by Update, SetValues, and SetValue.
func (c *Config) update(ctx context.Context, params UpdateConfigParams) error {
	name := c.Name
	if params.Name != nil {
		name = *params.Name
	}
	desc := c.Description
	if params.Description != nil {
		desc = params.Description
	}
	items := c.Items
	if params.Items != nil {
		items = params.Items
	}
	envs := c.Environments
	if params.Environments != nil {
		envs = params.Environments
	}

	updated, err := c.client.updateByID(ctx, c.ID, name, c.Key, desc, c.Parent, items, envs)
	if err != nil {
		return err
	}
	c.Name = updated.Name
	c.Description = updated.Description
	c.Items = updated.Items
	c.Environments = updated.Environments
	c.UpdatedAt = updated.UpdatedAt
	return nil
}

// UpdateConfigParams holds the optional fields for updating a config.
// Any nil field falls back to the config's current value.
type UpdateConfigParams struct {
	// Name overrides the config's display name.
	Name *string
	// Description overrides the config's description.
	Description *string
	// Items replaces the config's base values entirely (raw values, not wrapped).
	Items map[string]interface{}
	// Environments replaces the config's environments map entirely.
	Environments map[string]map[string]interface{}
}

// CreateConfigParams holds the parameters for creating a new config.
type CreateConfigParams struct {
	// Name is the display name (required).
	Name string
	// Key is the human-readable key. Auto-generated by the server if nil.
	Key *string
	// Description is an optional description.
	Description *string
	// Parent is the parent config UUID.
	Parent *string
	// Items holds the initial base values (raw values, not wrapped).
	Items map[string]interface{}
	// Environments holds the initial environment-specific overrides.
	Environments map[string]map[string]interface{}
}

// GetOption configures a Get request. Use WithKey or WithID to specify
// the lookup strategy.
type GetOption func(*getConfig)

type getConfig struct {
	key *string
	id  *string
}

// WithKey returns a GetOption that looks up a config by its human-readable key.
func WithKey(key string) GetOption {
	return func(g *getConfig) {
		g.key = &key
	}
}

// WithID returns a GetOption that looks up a config by its UUID.
func WithID(id string) GetOption {
	return func(g *getConfig) {
		g.id = &id
	}
}
