package smplkit

import (
	"time"

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// ForwarderType is a SIEM streaming destination type. The audit
// service rejects any value outside the constants below with a 400.
// See ADR-047 §2.12.
type ForwarderType = genaudit.ForwarderType

// Supported ForwarderType values, alphabetical by wire constant.
const (
	ForwarderTypeDatadog   = genaudit.DATADOG
	ForwarderTypeElastic   = genaudit.ELASTIC
	ForwarderTypeHoneycomb = genaudit.HONEYCOMB
	ForwarderTypeHTTP      = genaudit.HTTP
	ForwarderTypeNewRelic  = genaudit.NEWRELIC
	ForwarderTypeSplunkHEC = genaudit.SPLUNKHEC
	ForwarderTypeSumoLogic = genaudit.SUMOLOGIC
)

// ForwarderTypes enumerates every supported ForwarderType value. Useful
// for `<select>`-style menus or membership checks on free-form input.
var ForwarderTypes = []ForwarderType{
	ForwarderTypeDatadog,
	ForwarderTypeElastic,
	ForwarderTypeHoneycomb,
	ForwarderTypeHTTP,
	ForwarderTypeNewRelic,
	ForwarderTypeSplunkHEC,
	ForwarderTypeSumoLogic,
}

// HttpMethod is the HTTP verb a forwarder uses when delivering an
// event. The audit service rejects any value outside the constants
// below with a 400.
type HttpMethod = genaudit.HttpConfigurationMethod

// Supported HttpMethod values, alphabetical.
const (
	HttpMethodDelete = genaudit.HttpConfigurationMethodDELETE
	HttpMethodGet    = genaudit.HttpConfigurationMethodGET
	HttpMethodPatch  = genaudit.HttpConfigurationMethodPATCH
	HttpMethodPost   = genaudit.HttpConfigurationMethodPOST
	HttpMethodPut    = genaudit.HttpConfigurationMethodPUT
)

// HttpMethods enumerates every supported HttpMethod value.
var HttpMethods = []HttpMethod{
	HttpMethodDelete,
	HttpMethodGet,
	HttpMethodPatch,
	HttpMethodPost,
	HttpMethodPut,
}

// ForwarderTransformType identifies the engine used to evaluate a
// forwarder's transform expression. Currently only JSONATA is
// supported; additional engines may join later.
type ForwarderTransformType = genaudit.ForwarderTransformType

// Supported ForwarderTransformType values.
const (
	ForwarderTransformTypeJSONata = genaudit.JSONATA
)

// AuditEvent is the public-facing representation of an audit event.
//
// ADR-047 §2.3.1. The SDK exposes flat-named fields rather than the
// nested JSON:API attribute object — the wrapper takes care of the
// envelope on both create and read.
type AuditEvent struct {
	// ID is the server-assigned UUID for this event.
	ID uuid.UUID
	// Action is the action slug — e.g. "user.created", "invoice.paid".
	Action string
	// ResourceType is the type of resource the action operated on — e.g. "invoice".
	ResourceType string
	// ResourceID is the customer-facing id of the resource the action operated on.
	ResourceID string
	// OccurredAt is when the action actually happened, as reported by the source.
	OccurredAt time.Time
	// CreatedAt is when the audit service first ingested this event.
	CreatedAt time.Time
	// ActorType identifies the kind of actor that performed the action
	// ("user", "api_key", "system", …). Empty when unknown.
	ActorType string
	// ActorID is the UUID of the actor when the actor is a tracked
	// entity (user, api_key). Nil for system actors or anonymous events.
	ActorID *uuid.UUID
	// ActorLabel is a display label for the actor — typically a name or
	// email. Empty when unknown.
	ActorLabel string
	// Data is the free-form per-event payload defined by the customer.
	Data map[string]interface{}
	// IdempotencyKey is the customer-supplied dedupe key. Empty when
	// the customer didn't supply one.
	IdempotencyKey string
	// DoNotForward, when true, skips this event from SIEM forwarder
	// delivery regardless of any matching forwarder filter.
	DoNotForward bool
}

// CreateEventInput is the input for AuditEvents.Record.
//
// Customers must NOT use a ResourceType prefixed with "smpl." — the
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
	// Action is the action slug — required.
	Action string
	// ResourceType is the type of resource the action operated on. Must
	// not start with "smpl." (reserved for SDK-emitted events).
	ResourceType string
	// ResourceID is the customer-facing id of the resource.
	ResourceID string
	// OccurredAt is when the action actually happened. Defaults to the
	// server's receive time when nil.
	OccurredAt *time.Time
	// Data is free-form contextual JSON. To record a resource snapshot,
	// nest it inside Data — the smplkit internal convention is
	// Data["snapshot"], but the shape is unconstrained.
	Data map[string]interface{}
	// IdempotencyKey is an optional customer-supplied dedupe key.
	IdempotencyKey string
	// DoNotForward suppresses SIEM forwarder execution for this event.
	// The event itself is still recorded; the forwarder loop records a
	// "skipped_do_not_forward" delivery row for each enabled forwarder.
	DoNotForward bool
}

// ListEventsInput passes filters and pagination to AuditEvents.List.
type ListEventsInput struct {
	// Action filters by exact action slug.
	Action string
	// ResourceType filters by exact resource type.
	ResourceType string
	// ResourceID filters by exact resource id.
	ResourceID string
	// ActorType filters by exact actor type.
	ActorType string
	// ActorID filters by exact actor UUID (string form).
	ActorID string
	// OccurredAtRange filters by occurred_at using the platform's range
	// syntax (e.g. "[2026-01-01T00:00:00Z,*)" — ADR-014).
	OccurredAtRange string
	// Search performs a case-insensitive substring match against
	// resource_id.
	Search string
	// PageSize is items per page. Zero defers to the server default.
	PageSize int
	// PageAfter is the opaque cursor returned as NextCursor by the
	// previous call. Empty fetches the first page.
	PageAfter string
}

// ListEventsPage is one page of audit events. NextCursor is the opaque
// token to pass back as ListEventsInput.PageAfter on the next call;
// empty string means this is the last page.
type ListEventsPage struct {
	// Events is the slice of audit events on this page.
	Events []AuditEvent
	// NextCursor is the opaque token to pass as PageAfter on the next
	// call. Empty when this is the last page.
	NextCursor string
}

// ---------------------------------------------------------------------------
// Resource types and actions (read-only index surfaces)
// ---------------------------------------------------------------------------

// Pagination is the offset-pagination meta block returned on every standard
// list response (ADR-014). Page and Size always reflect the parameters that
// served the response. Total and TotalPages are populated only when the
// request set MetaTotal=true.
type Pagination struct {
	// Page is the 1-based page number that served the response.
	Page int
	// Size is the page size that served the response.
	Size int
	// Total is the total number of matching items across all pages.
	// Populated only when the request set MetaTotal=true.
	Total *int
	// TotalPages is the total number of pages at the requested page size.
	// Populated only when the request set MetaTotal=true.
	TotalPages *int
}

// AuditResourceType is one row from the resource-type index.
type AuditResourceType struct {
	// ID is the resource_type slug (e.g. "invoice").
	ID string
	// ResourceType is the same slug as ID; both fields are populated
	// for clarity.
	ResourceType string
}

// ListResourceTypesInput is the pagination input for AuditResourceTypes.List.
type ListResourceTypesInput struct {
	// PageNumber is the 1-based page index. Zero defers to the server default.
	PageNumber int
	// PageSize is items per page. Zero defers to the server default.
	PageSize int
	// MetaTotal asks the server to populate total / total_pages.
	MetaTotal bool
}

// ResourceTypeListPage is one page of resource-type slugs.
type ResourceTypeListPage struct {
	// ResourceTypes is the slice of resource types on this page.
	ResourceTypes []AuditResourceType
	// Pagination describes the page boundaries and totals (if requested).
	Pagination Pagination
}

// AuditAction is one row from the actions index.
type AuditAction struct {
	// ID is the action slug (e.g. "invoice.created").
	ID string
	// Action is the same slug as ID; both fields are populated for clarity.
	Action string
}

// ListActionsInput is the filter + pagination input for AuditActions.List.
type ListActionsInput struct {
	// FilterResourceType, when set, returns only actions seen with that
	// specific resource type.
	FilterResourceType string
	// PageNumber is the 1-based page index. Zero defers to the server default.
	PageNumber int
	// PageSize is items per page. Zero defers to the server default.
	PageSize int
	// MetaTotal asks the server to populate total / total_pages.
	MetaTotal bool
}

// ActionListPage is one page of action slugs.
type ActionListPage struct {
	// Actions is the slice of actions on this page.
	Actions []AuditAction
	// Pagination describes the page boundaries and totals (if requested).
	Pagination Pagination
}

// ---------------------------------------------------------------------------
// Forwarders (SIEM streaming — management plane)
// ---------------------------------------------------------------------------

// HttpHeader is a single name/value HTTP header on a forwarder
// destination request.
type HttpHeader struct {
	// Name is the header name (e.g. "Authorization", "DD-API-KEY").
	Name string
	// Value is the header value, plaintext on writes. The audit service
	// encrypts values at rest; reads return them as "<redacted>".
	Value string
}

// HttpConfiguration is the destination HTTP request configuration used
// by a forwarder of the HTTP family (HTTP, DATADOG, SPLUNK_HEC,
// SUMO_LOGIC, NEW_RELIC, HONEYCOMB, ELASTIC). Other transports will
// join this as members of a discriminated union under the
// Configuration field of Forwarder when they ship.
type HttpConfiguration struct {
	// Method is the HTTP verb used for delivery. Defaults to
	// HttpMethodPost when zero-valued.
	Method HttpMethod
	// URL is the destination the audit service POSTs each event to.
	URL string
	// Headers are attached to every outbound request. Values carry
	// credentials and are encrypted at rest server-side; reads return
	// them redacted.
	Headers []HttpHeader
	// SuccessStatus is the response status the destination must return
	// for delivery to count as success — an exact code ("200", "204")
	// or a class ("2xx", "4xx"). Defaults to "2xx" server-side when
	// zero-valued.
	SuccessStatus string
}

// Forwarder is a SIEM streaming destination configured on the
// customer's account. Active-record style: mutate fields directly and
// call Save(ctx) to persist, or Delete(ctx) to remove. Headers in
// Configuration.Headers are always returned redacted on reads — re-
// supply the real values before calling Save (the SDK does not cache
// them client-side).
type Forwarder struct {
	// ID is the server-assigned UUID for this forwarder. Zero-valued
	// until Save has run for the first time.
	ID uuid.UUID
	// Name is the display name. Free-form.
	Name string
	// Description is an optional free-text description.
	Description *string
	// ForwarderType is the destination type — see ForwarderType.
	ForwarderType ForwarderType
	// Enabled controls delivery. When false the audit service skips
	// this forwarder but still records filtered_out deliveries.
	Enabled bool
	// Filter is an optional JSON Logic expression evaluated per event.
	// When set, events that don't match are recorded as filtered_out
	// deliveries instead of being POSTed to the destination.
	Filter map[string]interface{}
	// Transform is an optional template applied to each event before
	// delivery. Shape depends on TransformType; for JSONATA, a
	// JSONata expression. Nil delivers the event JSON as-is.
	Transform *string
	// TransformType identifies the engine used to evaluate Transform.
	// Currently only ForwarderTransformTypeJSONata is supported.
	TransformType *ForwarderTransformType
	// Configuration is the destination request configuration.
	Configuration HttpConfiguration
	// CreatedAt is when the audit service first persisted this
	// forwarder. Nil for an unsaved instance.
	CreatedAt *time.Time
	// UpdatedAt is when this forwarder was last mutated.
	UpdatedAt *time.Time
	// DeletedAt is the soft-delete timestamp. Nil for live forwarders.
	DeletedAt *time.Time
	// Version is a monotonic counter bumped on every server-side write.
	Version *int

	client *AuditForwarders
}

// ListForwardersInput is the filter + pagination input for List.
type ListForwardersInput struct {
	// ForwarderType filters to a single destination type. Zero-valued
	// returns every type.
	ForwarderType ForwarderType
	// Enabled filters by enabled/disabled state. Nil returns both.
	Enabled *bool
	// PageNumber is the 1-based page index. Zero defers to the server
	// default (page 1).
	PageNumber int
	// PageSize is items per page. Zero defers to the server default
	// (1000, max 1000).
	PageSize int
	// MetaTotal asks the server to populate total / total_pages in the
	// returned Pagination. Costs an extra COUNT query.
	MetaTotal bool
}

// ListForwardersPage is one page of forwarders.
type ListForwardersPage struct {
	// Forwarders is the slice of forwarders on this page, each bound
	// to the management client so Save / Delete work directly.
	Forwarders []Forwarder
	// Pagination describes the page boundaries and totals (if
	// requested via MetaTotal).
	Pagination Pagination
}
