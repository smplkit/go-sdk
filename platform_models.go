package smplkit

// Active-record models for client.Platform().* resources.

import (
	"context"
	"time"
)

// ── Environment ──────────────────────────────────────────────────────────────

// EnvironmentOption configures an unsaved Environment returned by EnvironmentsClient.New.
type EnvironmentOption func(*Environment)

// WithEnvironmentColor sets the display color (hex string, e.g. "#ef4444").
func WithEnvironmentColor(color string) EnvironmentOption {
	return func(e *Environment) { e.Color = &color }
}

// WithEnvironmentClassification sets the classification (STANDARD or AD_HOC).
func WithEnvironmentClassification(c EnvironmentClassification) EnvironmentOption {
	return func(e *Environment) { e.Classification = c }
}

// Environment resource. Mutate fields, then call Save(ctx).
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

	client *EnvironmentsClient
}

// TypedColor returns the environment's display color as a typed Color
// value, or a zero Color if no color is set on this environment.
//
// Mirrors Python's Environment.color property which returns a Color
// instance. The wire transports a hex string; the SDK wraps it at the
// boundary. A malformed hex string on the wire returns the zero Color
// rather than surfacing a validation error on read.
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
		return &Error{Message: "Environment was constructed without a client; cannot save"}
	}
	if e.CreatedAt == nil {
		return e.client.create(ctx, e)
	}
	return e.client.update(ctx, e)
}

// Delete removes this environment from the server.
func (e *Environment) Delete(ctx context.Context) error {
	if e.client == nil || e.ID == "" {
		return &Error{Message: "Environment was constructed without a client or id; cannot delete"}
	}
	return e.client.Delete(ctx, e.ID)
}

func (e *Environment) apply(other *Environment) {
	e.ID = other.ID
	e.Name = other.Name
	e.Color = other.Color
	e.Classification = other.Classification
	e.CreatedAt = other.CreatedAt
	e.UpdatedAt = other.UpdatedAt
}

// ── Service ──────────────────────────────────────────────────────────────────

// Service resource — a backend application or microservice in the
// customer's stack that contexts can be evaluated against.
//
// Mutate fields, then call Save(ctx).
type Service struct {
	// ID is the service slug (e.g. "user_service").
	ID string
	// Name is the display name.
	Name string
	// CreatedAt is the server creation timestamp (nil for unsaved instances).
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	client *ServicesClient
}

// Save creates or updates the service on the server.
// The instance is updated with server-returned fields on success.
// PUT semantics: creates if CreatedAt is nil, otherwise updates.
func (s *Service) Save(ctx context.Context) error {
	if s.client == nil {
		return &Error{Message: "Service was constructed without a client; cannot save"}
	}
	if s.CreatedAt == nil {
		return s.client.create(ctx, s)
	}
	return s.client.update(ctx, s)
}

// Delete removes this service from the server.
func (s *Service) Delete(ctx context.Context) error {
	if s.client == nil || s.ID == "" {
		return &Error{Message: "Service was constructed without a client or id; cannot delete"}
	}
	return s.client.Delete(ctx, s.ID)
}

func (s *Service) apply(other *Service) {
	s.ID = other.ID
	s.Name = other.Name
	s.CreatedAt = other.CreatedAt
	s.UpdatedAt = other.UpdatedAt
}

// ── ContextType ───────────────────────────────────────────────────────────────

// ContextTypeOption configures an unsaved ContextType returned by ContextTypesClient.New.
type ContextTypeOption func(*ContextType)

// WithContextTypeName sets the display name for a context type.
func WithContextTypeName(name string) ContextTypeOption {
	return func(ct *ContextType) { ct.Name = name }
}

// ContextType resource on the smplkit platform.
// Mutate Attributes via AddAttribute / RemoveAttribute / UpdateAttribute,
// then call Save(ctx).
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

	client *ContextTypesClient
}

// AddAttribute adds a known-attribute slot with the given metadata.
// Local; call Save(ctx) to persist.
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

// RemoveAttribute removes a known-attribute slot. Local; call Save(ctx) to persist.
func (ct *ContextType) RemoveAttribute(name string) {
	delete(ct.Attributes, name)
}

// UpdateAttribute replaces a known-attribute slot's metadata. Local; call Save(ctx).
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
		return &Error{Message: "ContextType was constructed without a client; cannot save"}
	}
	if ct.CreatedAt == nil {
		return ct.client.create(ctx, ct)
	}
	return ct.client.update(ctx, ct)
}

// Delete removes this context type from the server.
func (ct *ContextType) Delete(ctx context.Context) error {
	if ct.client == nil || ct.ID == "" {
		return &Error{Message: "ContextType was constructed without a client or id; cannot delete"}
	}
	return ct.client.Delete(ctx, ct.ID)
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
// ContextsClient.List / Get. Mutate Attributes (or Name) and call
// Save(ctx) to persist; call Delete(ctx) to remove.
//
// The builder-style Context (in flags_types.go) remains the canonical
// input type for flag evaluation; this struct is the management-side
// read/write model with a back-reference to its parent client.
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

	client *ContextsClient
}

// CompositeID returns the composite "type:key" identifier.
func (ce *ContextEntity) CompositeID() string {
	return ce.ContextType + ":" + ce.Key
}

// Save persists the context entity to the server. The instance is
// updated with server-returned fields on success.
func (ce *ContextEntity) Save(ctx context.Context) error {
	if ce.client == nil {
		return &Error{Message: "ContextEntity was constructed without a client; cannot save"}
	}
	return ce.client.saveEntity(ctx, ce)
}

// Delete removes this context entity from the server. Equivalent to
// client.Platform().Contexts().Delete(ctx, ce.ContextType, ce.Key).
func (ce *ContextEntity) Delete(ctx context.Context) error {
	if ce.client == nil {
		return &Error{Message: "ContextEntity was constructed without a client; cannot delete"}
	}
	return ce.client.Delete(ctx, ce.ContextType, ce.Key)
}
