package smplkit

import "fmt"

// This file adds the per-environment unified verbs. Each verb takes an
// `environment` string where:
//
//   - empty string ("") targets the base/flag-level value
//   - non-empty value scopes the operation to that environment
//
// The empty-string convention keeps each verb to a single positional
// argument, avoiding an extra Option type just to disambiguate one
// parameter.
//
// The pre-existing verb set (SetEnvironmentEnabled, SetEnvironmentDefault,
// ClearRules) is retained for backward compatibility but is now layered
// on top of these unified verbs.

// ── Flag per-environment unified verbs ─────────────────────────────────────

// SetDefault sets the default served value. environment="" updates the
// flag-level base default; environment="production" sets the per-env
// default.
//
// Call Flag.Save(ctx) to persist.
func (f *Flag) SetDefault(value interface{}, environment string) {
	if environment == "" {
		f.Default = value
		return
	}
	f.SetEnvironmentDefault(environment, value)
}

// ClearDefault clears the per-environment default override on the named
// environment. environment must be non-empty; an empty environment is a
// no-op.
//
// Call Flag.Save(ctx) to persist.
func (f *Flag) ClearDefault(environment string) {
	if environment == "" {
		return // no-op; an empty environment is treated as "not set"
	}
	envs := copyEnvMap(f.Environments)
	if envData, ok := envs[environment].(map[string]interface{}); ok {
		envData = copyMap(envData)
		delete(envData, "default")
		envs[environment] = envData
		f.Environments = envs
	}
}

// EnableRules enables rule evaluation. environment="" enables rules in
// every environment configured on this flag; non-empty scopes to that
// single environment.
func (f *Flag) EnableRules(environment string) {
	if environment == "" {
		envs := copyEnvMap(f.Environments)
		for k := range envs {
			if envData, ok := envs[k].(map[string]interface{}); ok {
				envData = copyMap(envData)
				envData["enabled"] = true
				envs[k] = envData
			}
		}
		f.Environments = envs
		return
	}
	f.SetEnvironmentEnabled(environment, true)
}

// DisableRules disables rule evaluation (kill switch). environment=""
// disables rules in every environment configured on this flag.
func (f *Flag) DisableRules(environment string) {
	if environment == "" {
		envs := copyEnvMap(f.Environments)
		for k := range envs {
			if envData, ok := envs[k].(map[string]interface{}); ok {
				envData = copyMap(envData)
				envData["enabled"] = false
				envs[k] = envData
			}
		}
		f.Environments = envs
		return
	}
	f.SetEnvironmentEnabled(environment, false)
}

// ClearRulesAll clears rules in every environment configured on this flag.
// For a single-environment clear, use ClearRules(envKey).
func (f *Flag) ClearRulesAll() {
	envs := copyEnvMap(f.Environments)
	for k := range envs {
		if envData, ok := envs[k].(map[string]interface{}); ok {
			envData = copyMap(envData)
			envData["rules"] = []interface{}{}
			envs[k] = envData
		}
	}
	f.Environments = envs
}

// AddValue appends a constrained value to the flag's value set. Returns
// f for chaining.
func (f *Flag) AddValue(name string, value interface{}) *Flag {
	if f.Values == nil {
		empty := []FlagValue{}
		f.Values = &empty
	}
	*f.Values = append(*f.Values, FlagValue{Name: name, Value: value})
	return f
}

// RemoveValue removes the first values entry whose Value field matches.
// Returns f for chaining.
func (f *Flag) RemoveValue(value interface{}) *Flag {
	if f.Values == nil {
		return f
	}
	out := make([]FlagValue, 0, len(*f.Values))
	removed := false
	for _, v := range *f.Values {
		if !removed && fmtEqual(v.Value, value) {
			removed = true
			continue
		}
		out = append(out, v)
	}
	*f.Values = out
	return f
}

// ClearValues sets values to nil (unconstrained). Call Save(ctx) to persist.
func (f *Flag) ClearValues() {
	f.Values = nil
}

// fmtEqual is a deep-equal-by-fmt for interface{} values. Adequate for
// removing flag values where the wire types are JSON primitives.
func fmtEqual(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// ── Logger per-environment unified verbs ───────────────────────────────────

// SetLevel sets the level. environment="" updates the logger's base level;
// non-empty scopes to that environment.
func (l *Logger) SetLevel(level LogLevel, environment string) {
	if environment == "" {
		l.Level = &level
		return
	}
	l.SetEnvironmentLevel(environment, level)
}

// ClearLevel clears the level. environment="" clears the logger's base
// level (causing it to inherit from its parent); non-empty clears the
// per-env override.
func (l *Logger) ClearLevel(environment string) {
	if environment == "" {
		l.Level = nil
		return
	}
	l.ClearEnvironmentLevel(environment)
}

// ── LogGroup per-environment unified verbs ─────────────────────────────────

// SetLevel sets the level. environment="" updates the log group's base level;
// non-empty scopes to that environment.
//
// Call LogGroup.Save(ctx) to persist.
func (g *LogGroup) SetLevel(level LogLevel, environment string) {
	if environment == "" {
		g.Level = &level
		return
	}
	g.SetEnvironmentLevel(environment, level)
}

// ClearLevel clears the level. environment="" clears the log group's base
// level (causing it to inherit from its parent); non-empty clears the
// per-env override.
//
// Call LogGroup.Save(ctx) to persist.
func (g *LogGroup) ClearLevel(environment string) {
	if environment == "" {
		g.Level = nil
		return
	}
	g.ClearEnvironmentLevel(environment)
}

// ── ConfigEntry per-environment unified verbs ──────────────────────────────

// ItemOption configures a config item setter (SetString / SetNumber /
// SetBoolean / SetJSON). Use WithItemDescription.
type ItemOption func(*itemOpts)

type itemOpts struct {
	description string
}

// WithItemDescription attaches a human-readable description to a config item.
// Ignored when setting a per-environment override (overrides store the raw
// value only; the declared type and description come from the base item).
func WithItemDescription(description string) ItemOption {
	return func(o *itemOpts) { o.description = description }
}

func resolveItemOpts(opts []ItemOption) itemOpts {
	var o itemOpts
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Set writes (or replaces) an item from a ConfigItem. environment="" updates
// the base item, carrying the item's declared type and description; a
// non-empty environment stores only the raw value as an override on that
// environment (the type and description come from the base item, so the
// ConfigItem's Type and Description are ignored). Call Save(ctx) to persist.
func (c *ConfigEntry) Set(item ConfigItem, environment string) {
	c.setItemTyped(item.Name, item.Value, item.Type, item.Description, environment)
}

// SetString sets a string item. environment="" updates the base item;
// non-empty scopes the value to that environment as an override. Pass
// WithItemDescription to attach a description (base item only). Call
// Save(ctx) to persist.
func (c *ConfigEntry) SetString(name, value, environment string, opts ...ItemOption) {
	c.setItemTyped(name, value, ItemTypeString, resolveItemOpts(opts).description, environment)
}

// SetNumber sets a numeric item. environment="" updates the base item;
// non-empty scopes the value to that environment as an override. Pass
// WithItemDescription to attach a description (base item only). Call
// Save(ctx) to persist.
func (c *ConfigEntry) SetNumber(name string, value float64, environment string, opts ...ItemOption) {
	c.setItemTyped(name, value, ItemTypeNumber, resolveItemOpts(opts).description, environment)
}

// SetBoolean sets a boolean item. environment="" updates the base item;
// non-empty scopes the value to that environment as an override. Pass
// WithItemDescription to attach a description (base item only). Call
// Save(ctx) to persist.
func (c *ConfigEntry) SetBoolean(name string, value bool, environment string, opts ...ItemOption) {
	c.setItemTyped(name, value, ItemTypeBoolean, resolveItemOpts(opts).description, environment)
}

// SetJSON sets a JSON item. environment="" updates the base item; non-empty
// scopes the value to that environment as an override. Pass WithItemDescription
// to attach a description (base item only). Call Save(ctx) to persist.
func (c *ConfigEntry) SetJSON(name string, value interface{}, environment string, opts ...ItemOption) {
	c.setItemTyped(name, value, ItemTypeJSON, resolveItemOpts(opts).description, environment)
}

// Remove removes an item. environment="" removes from base; non-empty
// removes the per-env override only.
func (c *ConfigEntry) Remove(name, environment string) {
	if environment == "" {
		if c.Items != nil {
			delete(c.Items, name)
		}
		if c.itemsRaw != nil {
			delete(c.itemsRaw, name)
		}
		return
	}
	if c.Environments == nil {
		return
	}
	env, ok := c.Environments[environment]
	if !ok {
		return
	}
	delete(env, name)
}

// setItemTyped writes an item into the flat value map and, for the base
// config, retains the full typed shape ({value, type, description}) in
// itemsRaw so the declared type and description survive a get-mutate-put.
// A per-environment override (environment != "") stores only the raw value:
// the wire shape and the in-memory shape are both flat ({env: {key: rawValue}})
// per ADR-024 §2.4, and the type/description come from the base item.
func (c *ConfigEntry) setItemTyped(name string, value interface{}, itemType ItemType, description, environment string) {
	if environment == "" {
		if c.Items == nil {
			c.Items = make(map[string]interface{})
		}
		c.Items[name] = value
		if c.itemsRaw == nil {
			c.itemsRaw = make(map[string]map[string]interface{})
		}
		raw := map[string]interface{}{"value": value, "type": string(itemType)}
		if description != "" {
			raw["description"] = description
		}
		c.itemsRaw[name] = raw
		return
	}
	if c.Environments == nil {
		c.Environments = make(map[string]map[string]interface{})
	}
	env, ok := c.Environments[environment]
	if !ok {
		env = make(map[string]interface{})
		c.Environments[environment] = env
	}
	env[name] = value
}
