package smplkit

// This file wires the frozen wrapper types (FlagEnvironment,
// LoggerEnvironment, ConfigEnvironment) to the active-record models
// (Flag, Logger, LogGroup, ConfigEntry) so customers reading per-env
// data get typed access — not raw map[string]interface{} with wire-shape
// strings.
//
// Mirrors rule 8 of the cross-SDK overhaul (PR #127): per-environment
// data on a model is exposed as map[string]<typed>Environment, with the
// wrapper class enforcing read-only access.
//
// The pre-existing `.Environments` map[string]interface{} fields stay
// in place for the runtime cache / wire decoding path; the typed
// accessor below converts and returns a typed snapshot.

// TypedEnvironments returns a typed, read-only view of per-environment
// configuration on a Flag.
//
// The returned map is a defensive copy; mutating it has no effect on
// the underlying flag. Mutate the flag via SetDefault / EnableRules /
// DisableRules / AddRule (with environment scoping) instead.
func (f *Flag) TypedEnvironments() map[string]FlagEnvironment {
	out := make(map[string]FlagEnvironment, len(f.Environments))
	for k, v := range f.Environments {
		envMap, ok := v.(map[string]interface{})
		if !ok {
			out[k] = FlagEnvironment{}
			continue
		}
		enabled, _ := envMap["enabled"].(bool)
		def := envMap["default"]
		var rules []FlagRule
		if raw, ok := envMap["rules"].([]interface{}); ok {
			rules = make([]FlagRule, 0, len(raw))
			for _, r := range raw {
				rmap, ok := r.(map[string]interface{})
				if !ok {
					continue
				}
				logic, _ := rmap["logic"].(map[string]interface{})
				desc, _ := rmap["description"].(string)
				rules = append(rules, NewFlagRule(logic, rmap["value"], desc))
			}
		}
		out[k] = NewFlagEnvironment(enabled, def, rules)
	}
	return out
}

// TypedEnvironments returns a typed, read-only view of per-environment
// configuration on a Logger. Each value's Level() is a *LogLevel — nil
// when no override is set for that environment.
func (l *Logger) TypedEnvironments() map[string]LoggerEnvironment {
	return loggerTypedEnvironments(l.Environments)
}

// TypedEnvironments returns a typed, read-only view of per-environment
// configuration on a LogGroup.
func (g *LogGroup) TypedEnvironments() map[string]LoggerEnvironment {
	return loggerTypedEnvironments(g.Environments)
}

// loggerTypedEnvironments is the shared implementation for Logger and
// LogGroup — both share the same wire shape and exposed wrapper type.
func loggerTypedEnvironments(envs map[string]interface{}) map[string]LoggerEnvironment {
	out := make(map[string]LoggerEnvironment, len(envs))
	for k, v := range envs {
		envMap, ok := v.(map[string]interface{})
		if !ok {
			out[k] = NewLoggerEnvironment(nil)
			continue
		}
		var lvl *LogLevel
		if levelStr, ok := envMap["level"].(string); ok && levelStr != "" {
			ll := LogLevel(levelStr)
			lvl = &ll
		}
		out[k] = NewLoggerEnvironment(lvl)
	}
	return out
}

// TypedEnvironments returns a typed, read-only view of per-environment
// item overrides on a ConfigEntry.
func (c *ConfigEntry) TypedEnvironments() map[string]ConfigEnvironment {
	out := make(map[string]ConfigEnvironment, len(c.Environments))
	for k, v := range c.Environments {
		out[k] = NewConfigEnvironment(v)
	}
	return out
}
