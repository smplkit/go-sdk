package smplkit

import (
	"time"

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// ForwarderType is a SIEM streaming destination type. Aliases the
// generated typed string so customers refer to it as
// “smplkit.ForwarderType“ without reaching into the internal
// generated package, while still satisfying the generated client's
// type constraints.
//
// ADR-047 §2.12. Use the published constants below — the audit
// service rejects any other value with a 400.
type ForwarderType = genaudit.ForwarderType

// Constants for every supported ForwarderType value. The names match
// the codegen but are re-exported here so customer code stays inside
// the public “smplkit“ package.
const (
	ForwarderTypeHTTP      = genaudit.Http
	ForwarderTypeDatadog   = genaudit.Datadog
	ForwarderTypeSplunkHEC = genaudit.SplunkHec
	ForwarderTypeSumoLogic = genaudit.SumoLogic
	ForwarderTypeNewRelic  = genaudit.NewRelic
	ForwarderTypeHoneycomb = genaudit.Honeycomb
	ForwarderTypeElastic   = genaudit.Elastic
)

// ForwarderTypes lists every supported ForwarderType value, in spec
// order. Useful for `<select>`-style enumerations or membership
// checks on free-form input.
var ForwarderTypes = []ForwarderType{
	ForwarderTypeHTTP,
	ForwarderTypeDatadog,
	ForwarderTypeSplunkHEC,
	ForwarderTypeSumoLogic,
	ForwarderTypeNewRelic,
	ForwarderTypeHoneycomb,
	ForwarderTypeElastic,
}

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
	Data           map[string]interface{}
	IdempotencyKey string
	DoNotForward   bool
}

// CreateEventInput is the input for AuditEvents.Create.
//
// Customers must NOT use a ResourceType prefixed with “smpl.“ — the
// server returns 403 for those because that namespace is reserved for
// smplkit-emitted events.
//
// DoNotForward suppresses SIEM forwarder execution for this event. The
// event itself is still recorded; the forwarder loop records a
// "skipped_do_not_forward" delivery row for each enabled forwarder so
// the skip is visible in the delivery log.
// Data is free-form contextual JSON. To record a resource snapshot,
// nest it inside Data — smplkit's internal convention is
// Data["snapshot"], but the shape is unconstrained.
type CreateEventInput struct {
	Action         string
	ResourceType   string
	ResourceID     string
	OccurredAt     *time.Time
	Data           map[string]interface{}
	IdempotencyKey string
	DoNotForward   bool
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

// ---------------------------------------------------------------------------
// Forwarders (SIEM streaming, Pro tier)
// ---------------------------------------------------------------------------

// HttpHeader is one header on a forwarder destination request.
type HttpHeader struct {
	Name  string
	Value string
}

// ForwarderHttp is the destination HTTP configuration for a forwarder.
//
// SuccessStatus is a 3-character string: an exact code (e.g. "200")
// or a class (e.g. "2xx").
type ForwarderHttp struct {
	Method        string
	URL           string
	Headers       []HttpHeader
	Body          *string
	SuccessStatus string
}

// Forwarder is a SIEM streaming destination configured on the customer's
// account. Header values returned on reads are always redacted —
// re-supply real values when calling Update.
type Forwarder struct {
	ID            uuid.UUID
	Name          string
	Slug          string
	ForwarderType ForwarderType
	Enabled       bool
	Filter        map[string]interface{}
	Transform     *string
	HTTP          ForwarderHttp
	Data          map[string]interface{}
	CreatedAt     *time.Time
	UpdatedAt     *time.Time
	DeletedAt     *time.Time
	Version       *int
}

// CreateForwarderInput is the input for AuditForwarders.Create.
type CreateForwarderInput struct {
	Name          string
	ForwarderType ForwarderType
	HTTP          ForwarderHttp
	Enabled       bool
	Filter        map[string]interface{}
	Transform     string
	Data          map[string]interface{}
}

// UpdateForwarderInput is the full-replace input for AuditForwarders.Update.
//
// Header values must be re-supplied as plaintext; reads return them
// redacted, so a body containing "<redacted>" would persist that
// literal. Track real header values client-side and round-trip them.
type UpdateForwarderInput = CreateForwarderInput

// ListForwardersInput is the filter + pagination input for List.
type ListForwardersInput struct {
	ForwarderType ForwarderType
	Enabled       *bool
	PageSize      int
	PageAfter     string
}

// ListForwardersPage is one page of forwarders.
type ListForwardersPage struct {
	Forwarders []Forwarder
	NextCursor string
}

// ForwarderDeliveryStatus is one of the delivery outcome statuses.
type ForwarderDeliveryStatus string

const (
	ForwarderDeliverySucceeded           ForwarderDeliveryStatus = "succeeded"
	ForwarderDeliveryFailed              ForwarderDeliveryStatus = "failed"
	ForwarderDeliveryFilteredOut         ForwarderDeliveryStatus = "filtered_out"
	ForwarderDeliverySkippedDoNotForward ForwarderDeliveryStatus = "skipped_do_not_forward"
)

// ForwarderDelivery is one row in the append-only delivery log.
type ForwarderDelivery struct {
	ID             uuid.UUID
	ForwarderID    uuid.UUID
	EventID        uuid.UUID
	AttemptNumber  int
	Status         ForwarderDeliveryStatus
	Request        map[string]interface{}
	ResponseStatus *int
	ResponseBody   *string
	LatencyMs      *int
	Error          *string
	CreatedAt      *time.Time
}

// ListDeliveriesInput is the filter + pagination input for the delivery log.
type ListDeliveriesInput struct {
	Status         ForwarderDeliveryStatus
	CreatedAtRange string // ADR-014 range syntax
	PageSize       int
	PageAfter      string
}

// ListDeliveriesPage is one page of forwarder delivery rows.
type ListDeliveriesPage struct {
	Deliveries []ForwarderDelivery
	NextCursor string
}

// RetryFailedDeliveriesSummary is the response shape from the bulk
// retry action.
type RetryFailedDeliveriesSummary struct {
	Attempted int
	Succeeded int
	Failed    int
}

// TestForwarderInput is the body for the test_forwarder/execute proxy.
type TestForwarderInput struct {
	URL           string
	Method        string
	Headers       []HttpHeader
	Body          *string
	SuccessStatus string
	TimeoutMs     *int
}

// TestForwarderResult is the proxied response from the destination.
type TestForwarderResult struct {
	Succeeded       bool
	ResponseStatus  *int
	ResponseHeaders map[string]string
	ResponseBody    string
	LatencyMs       *int
	Error           *string
}
