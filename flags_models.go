package smplkit

import (
	"context"
	"fmt"
	"time"
)

// Flag represents a flag resource from the smplkit platform.
type Flag struct {
	// ID is the flag identifier (e.g. "dark-mode").
	ID string
	// Name is the display name for the flag.
	Name string
	// Type is the value type (BOOLEAN, STRING, NUMERIC, JSON).
	Type string
	// Default is the default value for the flag.
	Default interface{}
	// Values is the closed set of possible values (constrained), or nil (unconstrained).
	Values *[]FlagValue
	// Description is an optional description of the flag.
	Description *string
	// Environments maps environment names to their configuration.
	Environments map[string]interface{}
	// CreatedAt is the creation timestamp.
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	// client is the back-reference to the fused FlagsClient that owns
	// the create/update/delete logic for this active-record model.
	client *FlagsClient
}

// FlagValue represents a named value in a flag's value set.
type FlagValue struct {
	// Name is the human-readable label for this value option.
	Name string
	// Value is the raw value returned when this option is selected.
	// Shape depends on the flag's FlagType.
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

// WithFlagValues sets the closed value set for a flag (constrained).
func WithFlagValues(values []FlagValue) FlagOption {
	return func(f *Flag) { f.Values = &values }
}

// Save persists the flag to the server.
// The Flag instance is updated with the server response.
func (f *Flag) Save(ctx context.Context) error {
	if f.client == nil {
		return &Error{Message: "flag was constructed without a client; cannot save"}
	}
	if f.CreatedAt == nil {
		return f.client.createFlag(ctx, f)
	}
	return f.client.updateFlag(ctx, f)
}

// Delete removes this flag from the server.
func (f *Flag) Delete(ctx context.Context) error {
	if f.client == nil || f.ID == "" {
		return &Error{Message: "flag was constructed without a client or id; cannot delete"}
	}
	return f.client.Delete(ctx, f.ID)
}

// AddRule appends a rule to the specified environment. Call Save(ctx) to
// persist.
//
// The builtRule is the map produced by
// Rule(..., environment).When(...).Serve(...) (in Go, the fluent
// NewRule(...).Environment("env").When(...).Serve(...).Build() chain); it
// must include an "environment" key naming the target environment.
// Returns an error when the built rule has no "environment" key.
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

// SetEnvironmentEnabled sets whether rule evaluation is enabled for the
// environment named by envKey to the given enabled value. Call Save(ctx)
// to persist.
//
// This is a retained, single-environment verb layered on the unified
// per-env verbs EnableRules / DisableRules; pass an empty environment to
// those to apply the change across every configured environment.
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

// SetEnvironmentDefault sets the default value served in the environment
// named by envKey to defaultVal, used when no rule matches there. Call
// Save(ctx) to persist.
//
// This is a retained, single-environment verb layered on the unified
// SetDefault verb; SetDefault with an empty environment sets the
// flag-level base default instead.
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

// ClearRules removes all rules for the environment named by envKey. Call
// Save(ctx) to persist.
//
// This is a retained, single-environment verb; the unified ClearRulesAll
// verb clears rules across every configured environment instead.
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
	f.Name = other.Name
	f.Type = other.Type
	f.Default = other.Default
	f.Values = other.Values
	f.Description = other.Description
	f.Environments = other.Environments
	f.CreatedAt = other.CreatedAt
	f.UpdatedAt = other.UpdatedAt
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
