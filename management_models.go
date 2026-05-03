package smplkit

import (
	"context"
	"time"
)

// ── Environment ──────────────────────────────────────────────────────────────

// EnvironmentOption configures an unsaved Environment returned by EnvironmentsManagement.New.
type EnvironmentOption func(*Environment)

// WithEnvironmentColor sets the display color (hex string, e.g. "#ef4444").
func WithEnvironmentColor(color string) EnvironmentOption {
	return func(e *Environment) { e.Color = &color }
}

// WithEnvironmentClassification sets the classification (STANDARD or AD_HOC).
func WithEnvironmentClassification(c EnvironmentClassification) EnvironmentOption {
	return func(e *Environment) { e.Classification = c }
}

// Environment represents a smplkit environment resource.
// Mutate fields and call Save(ctx) to persist.
type Environment struct {
	// ID is the environment slug (e.g. "production").
	ID string
	// Name is the display name.
	Name string
	// Color is an optional hex color string for the Console UI.
	Color *string
	// Classification controls whether this environment participates in ordering.
	Classification EnvironmentClassification
	// CreatedAt is the server creation timestamp (nil for unsaved instances).
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	client *EnvironmentsManagement
}

// TypedColor returns the environment's display color as a typed Color
// value, or a zero Color if no color is set on this environment.
//
// Mirrors Python's Environment.color property which returns a Color
// instance (rule 9). The wire transports a hex string; the SDK wraps
// it at the boundary. A malformed hex string on the wire returns the
// zero Color rather than surfacing a validation error on read.
func (e *Environment) TypedColor() Color {
	if e.Color == nil || *e.Color == "" {
		return Color{}
	}
	c, err := NewColor(*e.Color)
	if err != nil {
		return Color{}
	}
	return c
}

// SetTypedColor sets the environment's display color from a typed Color.
// A zero Color clears the color. Call Save(ctx) to persist.
func (e *Environment) SetTypedColor(c Color) {
	if c.IsZero() {
		e.Color = nil
		return
	}
	hex := c.Hex()
	e.Color = &hex
}

// Save creates or updates the environment on the server.
// The instance is updated with server-returned fields on success.
// PUT semantics: creates if CreatedAt is nil, otherwise updates.
func (e *Environment) Save(ctx context.Context) error {
	if e.client == nil {
		return &Error{Message: "environment was constructed without a client; cannot save"}
	}
	if e.CreatedAt == nil {
		return e.client.create(ctx, e)
	}
	return e.client.update(ctx, e)
}

func (e *Environment) apply(other *Environment) {
	e.ID = other.ID
	e.Name = other.Name
	e.Color = other.Color
	e.Classification = other.Classification
	e.CreatedAt = other.CreatedAt
	e.UpdatedAt = other.UpdatedAt
}

// ── ContextType ───────────────────────────────────────────────────────────────

// ContextTypeOption configures an unsaved ContextType returned by ContextTypesManagement.New.
type ContextTypeOption func(*ContextType)

// WithContextTypeName sets the display name for a context type.
func WithContextTypeName(name string) ContextTypeOption {
	return func(ct *ContextType) { ct.Name = name }
}

// ContextType represents a context-type resource on the smplkit platform.
// Mutate Attributes via AddAttribute / RemoveAttribute / UpdateAttribute,
// then call Save(ctx) to persist.
type ContextType struct {
	// ID is the context type identifier (e.g. "user", "account").
	ID string
	// Name is the display name.
	Name string
	// Attributes maps attribute names to their metadata dicts.
	Attributes map[string]map[string]interface{}
	// CreatedAt is the server creation timestamp (nil for unsaved instances).
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	client *ContextTypesManagement
}

// AddAttribute adds (or replaces) an attribute slot with the given metadata.
// Call Save(ctx) to persist.
func (ct *ContextType) AddAttribute(name string, meta ...map[string]interface{}) {
	if ct.Attributes == nil {
		ct.Attributes = make(map[string]map[string]interface{})
	}
	if len(meta) > 0 && meta[0] != nil {
		m := make(map[string]interface{}, len(meta[0]))
		for k, v := range meta[0] {
			m[k] = v
		}
		ct.Attributes[name] = m
	} else {
		ct.Attributes[name] = map[string]interface{}{}
	}
}

// RemoveAttribute removes an attribute slot. Call Save(ctx) to persist.
func (ct *ContextType) RemoveAttribute(name string) {
	delete(ct.Attributes, name)
}

// UpdateAttribute replaces an attribute slot's metadata. Call Save(ctx) to persist.
func (ct *ContextType) UpdateAttribute(name string, meta map[string]interface{}) {
	if ct.Attributes == nil {
		ct.Attributes = make(map[string]map[string]interface{})
	}
	m := make(map[string]interface{}, len(meta))
	for k, v := range meta {
		m[k] = v
	}
	ct.Attributes[name] = m
}

// Save creates or updates the context type on the server.
// Creates if CreatedAt is nil, otherwise updates.
func (ct *ContextType) Save(ctx context.Context) error {
	if ct.client == nil {
		return &Error{Message: "context type was constructed without a client; cannot save"}
	}
	if ct.CreatedAt == nil {
		return ct.client.create(ctx, ct)
	}
	return ct.client.update(ctx, ct)
}

func (ct *ContextType) apply(other *ContextType) {
	ct.ID = other.ID
	ct.Name = other.Name
	ct.Attributes = other.Attributes
	ct.CreatedAt = other.CreatedAt
	ct.UpdatedAt = other.UpdatedAt
}

// ── ContextEntity ─────────────────────────────────────────────────────────────

// ContextEntity is the active-record model returned by
// ContextsManagement.List / Get. Mutate Attributes (or Name) and call
// Save(ctx) to persist; call Delete(ctx) to remove.
//
// Mirrors Python's smplkit.Context active-record model (rule 3). The
// builder-style smplkit.Context (in flags_types.go) remains the
// canonical input type for flag evaluation; this struct is the
// management-side read/write model with a back-reference to its parent
// client.
type ContextEntity struct {
	// ContextType is the context type key (e.g. "user").
	ContextType string
	// Key is the entity's unique identifier within its type.
	Key string
	// Name is an optional display name.
	Name *string
	// Attributes holds arbitrary attribute data.
	Attributes map[string]interface{}
	// CreatedAt is the server creation timestamp.
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	client *ContextsManagement
}

// CompositeID returns the composite "type:key" identifier.
func (ce *ContextEntity) CompositeID() string {
	return ce.ContextType + ":" + ce.Key
}

// Save persists the context entity to the server. The instance is
// updated with server-returned fields on success.
func (ce *ContextEntity) Save(ctx context.Context) error {
	if ce.client == nil {
		return &Error{Message: "context entity was constructed without a client; cannot save"}
	}
	return ce.client.saveEntity(ctx, ce)
}

// Delete removes this context entity from the server. Equivalent to
// mgmt.Contexts().Delete(ctx, ce.ContextType, ce.Key).
func (ce *ContextEntity) Delete(ctx context.Context) error {
	if ce.client == nil {
		return &Error{Message: "context entity was constructed without a client; cannot delete"}
	}
	return ce.client.Delete(ctx, ce.ContextType, ce.Key)
}

// ── AccountSettings ───────────────────────────────────────────────────────────

// AccountSettings holds per-account configuration.
// The wire format is an opaque JSON object; documented keys are exposed as
// typed accessors, and all keys (known and unknown) are preserved in Raw.
// Mutate via the setters and call Save(ctx) to persist.
type AccountSettings struct {
	// Raw is the full settings map. Mutations here are persisted on Save.
	Raw map[string]interface{}

	client *AccountSettingsManagement
}

// EnvironmentOrder returns the canonical ordering of STANDARD environments.
// Returns nil if not set.
func (s *AccountSettings) EnvironmentOrder() []string {
	raw, ok := s.Raw["environment_order"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// SetEnvironmentOrder sets the canonical ordering of STANDARD environments.
func (s *AccountSettings) SetEnvironmentOrder(order []string) {
	if s.Raw == nil {
		s.Raw = make(map[string]interface{})
	}
	cp := make([]interface{}, len(order))
	for i, v := range order {
		cp[i] = v
	}
	s.Raw["environment_order"] = cp
}

// Save writes the full settings object back to the server.
func (s *AccountSettings) Save(ctx context.Context) error {
	if s.client == nil {
		return &Error{Message: "account settings was constructed without a client; cannot save"}
	}
	return s.client.save(ctx, s)
}

func (s *AccountSettings) apply(other *AccountSettings) {
	s.Raw = other.Raw
}
