package smplkit

import (
	"context"
	"fmt"
	"time"
)

// Flag represents a flag resource from the smplkit platform.
type Flag struct {
	// ID is the unique identifier (UUID) of the flag. Empty for unsaved flags.
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

// FlagOption configures an unsaved Flag returned by factory methods.
type FlagOption func(*Flag)

// WithFlagName sets the display name for a flag.
func WithFlagName(name string) FlagOption {
	return func(f *Flag) { f.Name = name }
}

// WithFlagDescription sets the description for a flag.
func WithFlagDescription(desc string) FlagOption {
	return func(f *Flag) { f.Description = &desc }
}

// WithFlagValues sets the closed value set for a flag.
func WithFlagValues(values []FlagValue) FlagOption {
	return func(f *Flag) { f.Values = values }
}

// Save creates (POST) the flag if ID is empty, or updates (PUT) if ID is set.
// Applies the server response back to the Flag instance.
func (f *Flag) Save(ctx context.Context) error {
	if f.ID == "" {
		return f.client.createFlag(ctx, f)
	}
	return f.client.updateFlag(ctx, f)
}

// AddRule appends a rule to the specified environment. The builtRule must
// include an "environment" key (use NewRule(...).Environment("env").Build()).
// This is a local mutation — call Save(ctx) to persist.
func (f *Flag) AddRule(builtRule map[string]interface{}) error {
	envKey, ok := builtRule["environment"].(string)
	if !ok || envKey == "" {
		return fmt.Errorf("smplkit: built rule must include 'environment' key; use NewRule(...).Environment(\"env_key\").When(...).Serve(...).Build()")
	}

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
	f.Environments = envs
	return nil
}

// SetEnvironmentEnabled sets the enabled flag for an environment.
// This is a local mutation — call Save(ctx) to persist.
func (f *Flag) SetEnvironmentEnabled(envKey string, enabled bool) {
	envs := copyEnvMap(f.Environments)
	envData, ok := envs[envKey].(map[string]interface{})
	if !ok {
		envData = map[string]interface{}{"rules": []interface{}{}}
	} else {
		envData = copyMap(envData)
	}
	envData["enabled"] = enabled
	envs[envKey] = envData
	f.Environments = envs
}

// SetEnvironmentDefault sets the environment-specific default value.
// This is a local mutation — call Save(ctx) to persist.
func (f *Flag) SetEnvironmentDefault(envKey string, defaultVal interface{}) {
	envs := copyEnvMap(f.Environments)
	envData, ok := envs[envKey].(map[string]interface{})
	if !ok {
		envData = map[string]interface{}{"rules": []interface{}{}}
	} else {
		envData = copyMap(envData)
	}
	envData["default"] = defaultVal
	envs[envKey] = envData
	f.Environments = envs
}

// ClearRules removes all rules for the specified environment.
// This is a local mutation — call Save(ctx) to persist.
func (f *Flag) ClearRules(envKey string) {
	envs := copyEnvMap(f.Environments)
	if envData, ok := envs[envKey].(map[string]interface{}); ok {
		envData = copyMap(envData)
		envData["rules"] = []interface{}{}
		envs[envKey] = envData
		f.Environments = envs
	}
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
