package smplkit

import "fmt"

// This file adds the per-environment unified verbs (rule 7 of the
// cross-SDK overhaul). Each verb takes an `environment` string where:
//
//   - empty string ("") targets the base/flag-level value
//   - non-empty value scopes the operation to that environment
//
// Mirrors the Python pattern `set_default(value, *, environment=None)`
// where None scopes to the base. Go has no kwargs, so we use the empty
// string convention — it's clean, mirrors the intent, and avoids the
// need for an extra Option type just to disambiguate one parameter.
//
// The pre-existing verb set (SetEnvironmentEnabled, SetEnvironmentDefault,
// ClearRules) is retained for backward compatibility but is now layered
// on top of these unified verbs.

// ── Flag per-environment unified verbs ─────────────────────────────────────

// SetDefault sets the default served value. environment="" updates the
// flag-level base default; environment="production" sets the per-env
// default. Mirrors Python's flag.set_default(value, *, environment=None).
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
// environment. environment must be non-empty (mirrors Python's
// `clear_default(*, environment)` which makes the kwarg required).
//
// Call Flag.Save(ctx) to persist.
func (f *Flag) ClearDefault(environment string) {
	if environment == "" {
		return // no-op; matches Python's "must be set" semantics
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
// single environment. Mirrors Python's enable_rules(*, environment=None).
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
// Mirrors Python's clear_rules(*, environment=None) with environment=None.
// For a single-environment clear, use the existing ClearRules(envKey).
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
// f for chaining. Mirrors Python's flag.add_value.
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
// non-empty scopes to that environment. Mirrors Python's
// logger.set_level(level, *, environment=None).
func (l *Logger) SetLevel(level LogLevel, environment string) {
	if environment == "" {
		l.Level = &level
		return
	}
	l.SetEnvironmentLevel(environment, level)
}

// ClearLevel clears the level. environment="" clears the logger's base
// level (causing it to inherit from its parent); non-empty clears the
// per-env override. Mirrors Python's clear_level(*, environment=None).
func (l *Logger) ClearLevel(environment string) {
	if environment == "" {
		l.Level = nil
		return
	}
	l.ClearEnvironmentLevel(environment)
}

// ── ConfigEntry per-environment unified verbs ──────────────────────────────

// SetString sets a string item. environment="" updates the base item;
// non-empty scopes to that environment.
func (c *ConfigEntry) SetString(name, value, environment string) {
	c.setItem(name, value, environment)
}

// SetNumber sets a numeric item.
func (c *ConfigEntry) SetNumber(name string, value float64, environment string) {
	c.setItem(name, value, environment)
}

// SetBoolean sets a boolean item.
func (c *ConfigEntry) SetBoolean(name string, value bool, environment string) {
	c.setItem(name, value, environment)
}

// SetJSON sets a JSON item.
func (c *ConfigEntry) SetJSON(name string, value interface{}, environment string) {
	c.setItem(name, value, environment)
}

// Remove removes an item. environment="" removes from base; non-empty
// removes the per-env override only.
func (c *ConfigEntry) Remove(name, environment string) {
	if environment == "" {
		if c.Items != nil {
			delete(c.Items, name)
		}
		return
	}
	if c.Environments == nil {
		return
	}
	if env, ok := c.Environments[environment]; ok {
		delete(env, name)
	}
}

func (c *ConfigEntry) setItem(name string, value interface{}, environment string) {
	if environment == "" {
		if c.Items == nil {
			c.Items = make(map[string]interface{})
		}
		c.Items[name] = value
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
