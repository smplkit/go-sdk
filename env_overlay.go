package smplkit

import "strings"

// Flat per-environment override overlays (ADR-056).
//
// A resource's per-environment override is carried on the wire as a flat,
// sparse leaf-path object: `enabled` plus only the leaves that environment
// overrides, with each header addressed individually as `headers.<name>`.
// The generated clients surface this object untyped, as a
// map[string]interface{}, so the helpers below coerce JSON-decoded scalars
// back into the wrapper's field types. They are shared by the jobs and audit
// forwarder wrappers, which build and parse the same overlay shape.
//
// Reads are PURE OVERRIDE: a leaf the environment does not override is absent
// from the overlay and reads back as the wrapper's zero value / nil — never
// the base value. The server resolves base ⊕ overrides when the resource
// fires; the SDK resolves nothing.

// overlayString returns the string leaf at key, or "" when it is absent (or
// not a string). Empty therefore means "this environment does not override
// this leaf".
func overlayString(raw map[string]interface{}, key string) string {
	if v, ok := raw[key].(string); ok {
		return v
	}
	return ""
}

// overlayStringPtr returns a *string for the leaf at key, or nil when it is
// absent — the unambiguous "not overridden" signal for a leaf whose empty
// value is meaningful (e.g. a request body or CA certificate).
func overlayStringPtr(raw map[string]interface{}, key string) *string {
	if v, ok := raw[key].(string); ok {
		return &v
	}
	return nil
}

// overlayBool returns the bool leaf at key, or false when it is absent.
func overlayBool(raw map[string]interface{}, key string) bool {
	v, _ := raw[key].(bool)
	return v
}

// overlayBoolPtr returns a *bool for the leaf at key, or nil when it is absent
// — the unambiguous "not overridden" signal for a leaf whose false value is
// meaningful (e.g. tls_verify).
func overlayBoolPtr(raw map[string]interface{}, key string) *bool {
	if v, ok := raw[key].(bool); ok {
		return &v
	}
	return nil
}

// overlayInt returns the int leaf at key, or 0 when it is absent. JSON numbers
// decode to float64 through an interface{}, so float64 is accepted (and int
// defensively, for callers that build an overlay in memory).
func overlayInt(raw map[string]interface{}, key string) int {
	switch v := raw[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// overlayHeaders extracts the headers.<name> leaves from a flat overlay,
// splitting each key on the FIRST dot so a dotted header name (e.g.
// headers.X-Foo.Bar) keeps its full name. Non-string values and keys that are
// not header leaves are ignored. Returns nil when the overlay overrides no
// header.
func overlayHeaders(raw map[string]interface{}) map[string]string {
	var headers map[string]string
	for key, value := range raw {
		group, name, found := strings.Cut(key, ".")
		if !found || group != "headers" || name == "" {
			continue
		}
		s, ok := value.(string)
		if !ok {
			continue
		}
		if headers == nil {
			headers = map[string]string{}
		}
		headers[name] = s
	}
	return headers
}
