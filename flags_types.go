package smplkit

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"sort"
)

// FlagType represents the value type of a flag.
type FlagType string

const (
	// FlagTypeBoolean represents a boolean flag.
	FlagTypeBoolean FlagType = "BOOLEAN"
	// FlagTypeString represents a string flag.
	FlagTypeString FlagType = "STRING"
	// FlagTypeNumeric represents a numeric flag.
	FlagTypeNumeric FlagType = "NUMERIC"
	// FlagTypeJSON represents a JSON flag.
	FlagTypeJSON FlagType = "JSON"
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
func NewContext(contextType, key string, attrs map[string]interface{}, opts ...ContextOption) Context {
	c := Context{
		Type:       contextType,
		Key:        key,
		Attributes: make(map[string]interface{}),
	}
	if attrs != nil {
		for k, v := range attrs {
			c.Attributes[k] = v
		}
	}
	for _, opt := range opts {
		opt(&c)
	}
	return c
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

// contextsToEvalDict converts a list of Context objects to the nested evaluation
// dict for JSON Logic. Each Context's type becomes a top-level key.
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

// hashContext computes a stable MD5 hash for a context evaluation dict.
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
		return []byte("{" + joinStrings(entries, ",") + "}"), nil
	default:
		return json.Marshal(v)
	}
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}
