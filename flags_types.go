package smplkit

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"sort"
)

// FlagType represents the value type of a flag.
type FlagType string

// Supported FlagType values, alphabetical by wire constant.
const (
	// FlagTypeBoolean represents a boolean flag.
	FlagTypeBoolean FlagType = "BOOLEAN"
	// FlagTypeJSON represents a JSON flag.
	FlagTypeJSON FlagType = "JSON"
	// FlagTypeNumeric represents a numeric flag.
	FlagTypeNumeric FlagType = "NUMERIC"
	// FlagTypeString represents a string flag.
	FlagTypeString FlagType = "STRING"
)

// Context represents a typed evaluation context entity.
//
// Each Context identifies an entity (user, account, device, etc.) by type
// and key, with optional attributes that JSON Logic rules can target.
//
//	ctx := smplkit.NewContext("user", "user-123", map[string]any{
//	    "plan": "enterprise",
//	    "firstName": "Alice",
//	})
//
// Identity locking note: a persisted Context has a stable (type, key)
// identity. Go's idiom of exported struct fields makes that lock
// unenforceable at compile time — ctx.Type = "X" is always allowed by the
// language. Customer code that wants identity-locking semantics should treat
// persisted Contexts (those returned by client.Platform().Contexts().Get /
// List) as read-only and construct fresh ones via NewContext for new
// entities. NewContext enforces the runtime "must not be empty" check,
// panicking on empty inputs.
type Context struct {
	// Type is the context type (e.g. "user", "account").
	Type string
	// Key is the unique identifier for this entity.
	Key string
	// Name is an optional display name.
	Name string
	// Attributes holds arbitrary key-value data for rule evaluation.
	Attributes map[string]interface{}
}

// NewContext creates a new evaluation context. The optional attrs map provides
// attributes for JSON Logic rule evaluation. Use WithName to set a display name,
// or WithAttr to add individual attributes.
//
//	// Using a map:
//	ctx := smplkit.NewContext("user", "user-123", map[string]any{"plan": "enterprise"})
//
//	// Using functional options:
//	ctx := smplkit.NewContext("user", "user-123", nil,
//	    smplkit.WithName("Alice"),
//	    smplkit.WithAttr("plan", "enterprise"),
//	)
//
// Fail-fast validation: empty type or key panics with a clear message. Go's
// type system already enforces string arguments, so only non-emptiness needs
// checking. Numeric IDs must be stringified at the SDK boundary — silent
// normalization weakens the contract.
func NewContext(contextType, key string, attrs map[string]interface{}, opts ...ContextOption) Context {
	if contextType == "" {
		panic("smplkit: Context type must be a non-empty string")
	}
	if key == "" {
		panic("smplkit: Context key must be a non-empty string. " +
			"If your identifier is numeric, stringify it at the SDK boundary.")
	}
	c := Context{
		Type:       contextType,
		Key:        key,
		Attributes: make(map[string]interface{}),
	}
	for k, v := range attrs {
		c.Attributes[k] = v
	}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// CompositeID returns the composite "type:key" identifier. Useful when
// calling sub-clients that accept either a composite id or separate
// (type, key) arguments.
func (c Context) CompositeID() string {
	return c.Type + ":" + c.Key
}

// ContextOption configures a Context. Use WithName and WithAttr.
type ContextOption func(*Context)

// WithName sets the display name on a Context.
func WithName(name string) ContextOption {
	return func(c *Context) {
		c.Name = name
	}
}

// WithAttr adds a single attribute to a Context.
func WithAttr(key string, value interface{}) ContextOption {
	return func(c *Context) {
		c.Attributes[key] = value
	}
}

// Rule is a fluent builder for JSON Logic rule dicts.
//
//	rule := smplkit.NewRule("Enable for enterprise users").
//	    When("user.plan", "==", "enterprise").
//	    Serve(true).
//	    Build()
type Rule struct {
	description string
	conditions  []map[string]interface{}
	value       interface{}
	environment string
}

// NewRule creates a new rule builder with the given description.
func NewRule(description string) *Rule {
	return &Rule{description: description}
}

// Environment tags this rule with an environment key (required for AddRule).
func (r *Rule) Environment(envKey string) *Rule {
	r.environment = envKey
	return r
}

// When adds a condition. Multiple calls are AND'd.
// Supported operators: ==, !=, >, <, >=, <=, in, contains.
func (r *Rule) When(variable, op string, value interface{}) *Rule {
	var condition map[string]interface{}
	if op == "contains" {
		// JSON Logic "in" with reversed operands: value in var
		condition = map[string]interface{}{
			"in": []interface{}{value, map[string]interface{}{"var": variable}},
		}
	} else {
		condition = map[string]interface{}{
			op: []interface{}{map[string]interface{}{"var": variable}, value},
		}
	}
	r.conditions = append(r.conditions, condition)
	return r
}

// Serve sets the value returned when this rule matches.
func (r *Rule) Serve(value interface{}) *Rule {
	r.value = value
	return r
}

// Build finalizes and returns the rule as a plain map.
//
// Every rule requires an environment. The builder permits a chained
// Environment(...) call and validates at the AddRule boundary — Flag.AddRule
// rejects any rule whose Build() output has no "environment" key.
func (r *Rule) Build() map[string]interface{} {
	var logic interface{}
	switch len(r.conditions) {
	case 0:
		logic = map[string]interface{}{}
	case 1:
		logic = r.conditions[0]
	default:
		conds := make([]interface{}, len(r.conditions))
		for i, c := range r.conditions {
			conds[i] = c
		}
		logic = map[string]interface{}{"and": conds}
	}

	result := map[string]interface{}{
		"description": r.description,
		"logic":       logic,
		"value":       r.value,
	}

	if r.environment != "" {
		result["environment"] = r.environment
	}

	return result
}

// contextsToEvalDict converts a list of Context objects to the evaluation format.
func contextsToEvalDict(contexts []Context) map[string]interface{} {
	result := make(map[string]interface{})
	for _, ctx := range contexts {
		entry := make(map[string]interface{}, len(ctx.Attributes)+1)
		entry["key"] = ctx.Key
		for k, v := range ctx.Attributes {
			entry[k] = v
		}
		result[ctx.Type] = entry
	}
	return result
}

// hashContext computes a stable hash for a context evaluation map.
func hashContext(evalDict map[string]interface{}) string {
	// Sort keys for deterministic serialization.
	b, _ := marshalSorted(evalDict)
	return fmt.Sprintf("%x", md5.Sum(b))
}

// marshalSorted produces a deterministic JSON representation of a value
// by sorting map keys at every level.
func marshalSorted(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		entries := make([]string, 0, len(keys))
		for _, k := range keys {
			kb, _ := json.Marshal(k)
			vb, _ := marshalSorted(val[k])
			entries = append(entries, string(kb)+":"+string(vb))
		}
		return []byte("{" + joinStrings(entries) + "}"), nil
	default:
		return json.Marshal(v)
	}
}

func joinStrings(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += "," + p
	}
	return result
}
