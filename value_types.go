package smplkit

// This file defines the read-only / frozen value types that the SDK
// surfaces from runtime caches and CRUD read-back paths. The
// types are immutable in spirit and (where the language allows) in
// practice: their fields are unexported and only getter methods are
// provided, so customer code that grabs one of these types cannot
// silently mutate SDK-internal state.
//
// The read-only contract is enforced, not aspirational.

// FlagRule is a single targeting rule on a Flag. Frozen — author rules
// via the smplkit.Rule fluent builder and pass through Flag.AddRule.
type FlagRule struct {
	logic       map[string]interface{}
	value       interface{}
	description string
}

// NewFlagRule constructs a FlagRule. Customer code typically uses the
// Rule fluent builder; this constructor is used by the SDK when parsing
// server responses and exposed for completeness.
func NewFlagRule(logic map[string]interface{}, value interface{}, description string) FlagRule {
	cp := make(map[string]interface{}, len(logic))
	for k, v := range logic {
		cp[k] = v
	}
	return FlagRule{logic: cp, value: value, description: description}
}

// Logic returns a defensive copy of the JSON Logic predicate. Empty map
// means "always match".
func (r FlagRule) Logic() map[string]interface{} {
	cp := make(map[string]interface{}, len(r.logic))
	for k, v := range r.logic {
		cp[k] = v
	}
	return cp
}

// Value returns the value served when Logic evaluates truthy.
func (r FlagRule) Value() interface{} { return r.value }

// Description returns the human-readable label, if any.
func (r FlagRule) Description() string { return r.description }

// FlagEnvironment is the per-environment configuration on a Flag. Frozen.
// Mutate via Flag.AddRule / Flag.EnableRules / Flag.DisableRules /
// Flag.SetDefault / Flag.ClearRules with environment scoping.
type FlagEnvironment struct {
	enabled  bool
	default_ interface{}
	rules    []FlagRule
}

// NewFlagEnvironment constructs a FlagEnvironment. Used by the SDK when
// parsing server responses; exposed for completeness.
func NewFlagEnvironment(enabled bool, defaultValue interface{}, rules []FlagRule) FlagEnvironment {
	cp := make([]FlagRule, len(rules))
	copy(cp, rules)
	return FlagEnvironment{enabled: enabled, default_: defaultValue, rules: cp}
}

// Enabled reports whether the flag is active in this environment.
func (e FlagEnvironment) Enabled() bool { return e.enabled }

// Default returns the environment-specific default override, or nil if
// no override is set (in which case the flag's base default applies).
func (e FlagEnvironment) Default() interface{} { return e.default_ }

// Rules returns a defensive copy of the targeting rules.
func (e FlagEnvironment) Rules() []FlagRule {
	cp := make([]FlagRule, len(e.rules))
	copy(cp, e.rules)
	return cp
}

// LoggerEnvironment is the per-environment configuration on a Logger or
// LogGroup — currently just an optional level override. Frozen.
type LoggerEnvironment struct {
	level *LogLevel
}

// NewLoggerEnvironment constructs a LoggerEnvironment. Used by the SDK
// when parsing server responses.
func NewLoggerEnvironment(level *LogLevel) LoggerEnvironment {
	if level == nil {
		return LoggerEnvironment{}
	}
	cp := *level
	return LoggerEnvironment{level: &cp}
}

// Level returns the per-env level override, or nil if no override is set
// (in which case inheritance applies).
func (e LoggerEnvironment) Level() *LogLevel {
	if e.level == nil {
		return nil
	}
	cp := *e.level
	return &cp
}

// ConfigEnvironment is the per-environment value overrides on a ConfigEntry.
// Frozen — mutate via ConfigEntry.SetString / SetNumber / SetBoolean /
// SetJSON / Remove with an environment argument.
//
// An override stores only the raw value; the declared type and description
// come from the base item.
type ConfigEnvironment struct {
	values map[string]interface{}
}

// NewConfigEnvironment constructs a ConfigEnvironment. Used by the SDK
// when parsing server responses.
func NewConfigEnvironment(values map[string]interface{}) ConfigEnvironment {
	cp := make(map[string]interface{}, len(values))
	for k, v := range values {
		cp[k] = v
	}
	return ConfigEnvironment{values: cp}
}

// Values returns a defensive copy of the per-env value overrides.
func (e ConfigEnvironment) Values() map[string]interface{} {
	cp := make(map[string]interface{}, len(e.values))
	for k, v := range e.values {
		cp[k] = v
	}
	return cp
}

// Note: ConfigChangeEvent, FlagChangeEvent, and LoggerChangeEvent are
// defined alongside their respective runtime clients (config_client.go,
// flags_runtime.go, logging_models.go). Those types pre-date this file
// and use exported fields for backward compatibility; treat them as
// read-only at the API boundary — the runtime never re-uses an event
// instance, so listener-time mutation cannot bleed across subscribers.
