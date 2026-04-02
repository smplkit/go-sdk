package smplkit

import (
	"context"
	"fmt"
	"time"
)

// Flag represents a flag resource from the smplkit platform.
type Flag struct {
	// ID is the unique identifier (UUID) of the flag.
	ID string
	// Key is the human-readable flag key.
	Key string
	// Name is the display name for the flag.
	Name string
	// Type is the value type (BOOLEAN, STRING, NUMERIC, JSON).
	Type string
	// Default is the default value for the flag.
	Default interface{}
	// Values is the closed set of possible values.
	Values []FlagValue
	// Description is an optional description of the flag.
	Description *string
	// Environments maps environment names to their configuration.
	Environments map[string]interface{}
	// CreatedAt is the creation timestamp.
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	// client is the back-reference to FlagsClient, set by factory methods.
	client *FlagsClient
}

// FlagValue represents a named value in a flag's value set.
type FlagValue struct {
	Name  string
	Value interface{}
}

// Update replaces this flag's attributes on the server. Only non-nil fields
// in params are changed.
func (f *Flag) Update(ctx context.Context, params UpdateFlagParams) error {
	updated, err := f.client.updateFlag(ctx, f, params)
	if err != nil {
		return err
	}
	f.apply(updated)
	return nil
}

// AddRule adds a rule to a specific environment. The built rule must include
// an "environment" key (use Rule.Environment("env_key") before Build).
func (f *Flag) AddRule(ctx context.Context, builtRule map[string]interface{}) error {
	envKey, ok := builtRule["environment"].(string)
	if !ok || envKey == "" {
		return fmt.Errorf("smplkit: built rule must include 'environment' key; use NewRule(...).Environment(\"env_key\").When(...).Serve(...).Build()")
	}

	// Re-fetch current state to avoid stale data.
	current, err := f.client.Get(ctx, f.ID)
	if err != nil {
		return err
	}
	f.apply(current)

	envs := copyEnvMap(f.Environments)
	envData, ok := envs[envKey].(map[string]interface{})
	if !ok {
		envData = map[string]interface{}{"enabled": true, "rules": []interface{}{}}
	} else {
		envData = copyMap(envData)
	}

	rules, _ := envData["rules"].([]interface{})
	ruleCopy := make(map[string]interface{})
	for k, v := range builtRule {
		if k != "environment" {
			ruleCopy[k] = v
		}
	}
	rules = append(rules, ruleCopy)
	envData["rules"] = rules
	envs[envKey] = envData

	updated, err := f.client.updateFlag(ctx, f, UpdateFlagParams{
		Environments: envs,
	})
	if err != nil {
		return err
	}
	f.apply(updated)
	return nil
}

func (f *Flag) apply(other *Flag) {
	f.ID = other.ID
	f.Key = other.Key
	f.Name = other.Name
	f.Type = other.Type
	f.Default = other.Default
	f.Values = other.Values
	f.Description = other.Description
	f.Environments = other.Environments
	f.CreatedAt = other.CreatedAt
	f.UpdatedAt = other.UpdatedAt
}

// UpdateFlagParams holds the optional fields for updating a flag.
type UpdateFlagParams struct {
	// Name overrides the flag's display name.
	Name *string
	// Description overrides the flag's description.
	Description *string
	// Default overrides the flag's default value.
	Default interface{}
	// Values overrides the flag's value set.
	Values []FlagValue
	// Environments replaces the flag's environments map.
	Environments map[string]interface{}
}

// CreateFlagParams holds the parameters for creating a new flag.
type CreateFlagParams struct {
	// Key is the human-readable flag key (required).
	Key string
	// Name is the display name (required).
	Name string
	// Type is the value type (required).
	Type FlagType
	// Default is the default value (required).
	Default interface{}
	// Description is an optional description.
	Description *string
	// Values is the closed set of possible values. Auto-generated for boolean flags.
	Values []FlagValue
}

// ContextType represents a context type resource from the management API.
type ContextType struct {
	// ID is the unique identifier (UUID) of the context type.
	ID string
	// Key is the human-readable key.
	Key string
	// Name is the display name.
	Name string
	// Attributes holds the context type's attribute definitions.
	Attributes map[string]interface{}
}

func copyEnvMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return make(map[string]interface{})
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
