package smplkit

import (
	"time"

	"github.com/google/uuid"
)

// AuditEvent is the public-facing representation of an audit event.
//
// ADR-047 §2.3.1. The SDK exposes flat-named fields rather than the
// nested JSON:API attribute object — the wrapper takes care of the
// envelope on both create and read.
type AuditEvent struct {
	ID             uuid.UUID
	Action         string
	ResourceType   string
	ResourceID     string
	OccurredAt     time.Time
	CreatedAt      time.Time
	ActorType      string
	ActorID        *uuid.UUID
	ActorLabel     string
	Snapshot       map[string]interface{}
	Data           map[string]interface{}
	IdempotencyKey string
}

// CreateEventInput is the input for AuditEvents.Create.
//
// Customers must NOT use a ResourceType prefixed with “smpl.“ — the
// server returns 403 for those because that namespace is reserved for
// smplkit-emitted events.
type CreateEventInput struct {
	Action         string
	ResourceType   string
	ResourceID     string
	OccurredAt     *time.Time
	Snapshot       map[string]interface{}
	Data           map[string]interface{}
	IdempotencyKey string
}

// ListEventsInput passes filters and pagination to AuditEvents.List.
type ListEventsInput struct {
	Action          string
	ResourceType    string
	ResourceID      string
	ActorType       string
	ActorID         string
	OccurredAtRange string // e.g. "[2026-01-01T00:00:00Z,*)" (ADR-014)
	PageSize        int
	PageAfter       string
}

// ListEventsPage is one page of audit events.
//
// NextCursor is the opaque token to pass back as ListEventsInput.PageAfter
// on the next call. Empty string means this is the last page.
type ListEventsPage struct {
	Events     []AuditEvent
	NextCursor string
}
