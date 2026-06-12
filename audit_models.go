package smplkit

import (
	"time"

	"github.com/google/uuid"
)

// ForwarderType is a SIEM streaming destination type. The audit
// service rejects any value outside the constants below with a 400.
type ForwarderType string

// Supported ForwarderType values, alphabetical by wire constant.
const (
	ForwarderTypeDatadog   ForwarderType = "datadog"
	ForwarderTypeElastic   ForwarderType = "elastic"
	ForwarderTypeHoneycomb ForwarderType = "honeycomb"
	ForwarderTypeHTTP      ForwarderType = "http"
	ForwarderTypeNewRelic  ForwarderType = "new_relic"
	ForwarderTypeSplunkHEC ForwarderType = "splunk_hec"
	ForwarderTypeSumoLogic ForwarderType = "sumo_logic"
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
type HttpMethod string

// Supported HttpMethod values, alphabetical.
const (
	HttpMethodDelete HttpMethod = "DELETE"
	HttpMethodGet    HttpMethod = "GET"
	HttpMethodPatch  HttpMethod = "PATCH"
	HttpMethodPost   HttpMethod = "POST"
	HttpMethodPut    HttpMethod = "PUT"
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
type ForwarderTransformType string

// Supported ForwarderTransformType values.
const (
	ForwarderTransformTypeJSONata ForwarderTransformType = "JSONATA"
)

// AuditEvent represents an audit event returned by the audit service.
//
// The SDK exposes flat-named fields rather than the nested JSON:API
// attribute object — the wrapper takes care of the envelope on both
// create and read.
type AuditEvent struct {
	// ID is the server-assigned UUID for this event.
	ID uuid.UUID
	// EventType is the event type slug — e.g. "user.created", "invoice.paid".
	EventType string
	// ResourceType is the type of resource the event operated on — e.g. "invoice".
	ResourceType string
	// ResourceID is the customer-facing id of the resource the event operated on.
	ResourceID string
	// OccurredAt is when the event actually happened, as reported by the source.
	OccurredAt time.Time
	// CreatedAt is when the audit service first ingested this event.
	CreatedAt time.Time
	// ActorType is a free-form label for the kind of actor that caused
	// the event (e.g. "USER", "API_KEY", "SYSTEM", or any custom value).
	// Empty when the caller did not supply one; the audit service never
	// backfills from the request credential.
	ActorType string
	// ActorID is the actor identifier. Free-form — any string scheme
	// is accepted; non-UUID values round-trip verbatim. Empty when not
	// supplied.
	ActorID string
	// ActorLabel is a human-readable label for the actor (e.g. an email
	// address or API key name). Empty when not supplied.
	ActorLabel string
	// Category is a free-form bucket label for the event (e.g. "auth",
	// "billing", "config-change"). Stored exactly as supplied; drives the
	// audit log's category filter and the categories discovery listing
	// (client.Audit().Categories()). Empty when not supplied.
	Category string
	// Data is the free-form per-event payload defined by the customer.
	Data map[string]interface{}
	// IdempotencyKey is the customer-supplied dedupe key. Empty when
	// the customer didn't supply one.
	IdempotencyKey string
	// DoNotForward, when true, skips this event from SIEM forwarder
	// delivery regardless of any matching forwarder filter.
	DoNotForward bool
	// Environment is the environment the event was recorded in. Read-only
	// and always present on reads — the audit service resolves it when the
	// event is recorded (from a single-environment credential, or from the
	// runtime SDK's configured environment, which the SDK sends on every
	// recording call). Never set on the recording request body.
	Environment string
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
	// EventType is the event type slug — required.
	EventType string
	// ResourceType is the type of resource the event operated on. Must
	// not start with "smpl." (reserved for SDK-emitted events).
	ResourceType string
	// ResourceID is the customer-facing id of the resource.
	ResourceID string
	// OccurredAt is when the event actually happened. Defaults to the
	// server's receive time when nil.
	OccurredAt *time.Time
	// ActorType is a free-form label for the kind of actor that caused
	// the event (e.g. "USER", "API_KEY", "SYSTEM", or any custom value).
	// The audit service never backfills this from the request
	// credential — set it explicitly when you want the event attributed.
	ActorType string
	// ActorID is the actor identifier. Free-form — any string scheme is
	// accepted.
	ActorID string
	// ActorLabel is a human-readable label for the actor (e.g. an email
	// address or API key name).
	ActorLabel string
	// Category is an optional free-form bucket label for the event (e.g.
	// "auth", "billing", "config-change"). Stored exactly as supplied;
	// powers the audit log's category filter and the categories discovery
	// listing (client.Audit().Categories()). Omit it to leave the event
	// uncategorized.
	Category string
	// Data is free-form contextual JSON. To record a resource snapshot,
	// nest it inside Data — the smplkit internal convention is
	// Data["snapshot"], but the shape is unconstrained.
	Data map[string]interface{}
	// IdempotencyKey is an optional customer-supplied dedupe key. When
	// omitted, the server derives one from the event content (account_id
	// + event_type + resource_type + resource_id + occurred_at + actor_*
	// + data).
	IdempotencyKey string
	// DoNotForward suppresses SIEM forwarder execution for this event.
	// The event itself is still recorded; the forwarder loop records a
	// "skipped_do_not_forward" delivery row for each enabled forwarder.
	DoNotForward bool
}

// ListEventsInput passes filters and pagination to AuditEvents.List.
type ListEventsInput struct {
	// Environments scopes results to the given environment keys (e.g.
	// "production", "staging"). When omitted or empty, results are scoped
	// to your single accessible environment. The reserved value "smplkit"
	// selects platform change events that smplkit records about your own
	// resources (flags, configuration, and so on); these are not tied to a
	// deployment environment and are readable regardless of which
	// environments you manage. Multiple values are sent as a single
	// comma-separated filter[environment].
	Environments []string
	// EventType filters by exact event type slug.
	EventType string
	// ResourceType filters by exact resource type.
	ResourceType string
	// ResourceID filters by exact resource id.
	ResourceID string
	// ActorType filters by exact actor type. Matches the literal string
	// stored on the event.
	ActorType string
	// ActorID filters by exact actor id. Matches the literal string
	// stored on the event — any identifier scheme works.
	ActorID string
	// OccurredAtRange filters by occurred_at using the platform's range
	// syntax (e.g. "[2026-01-01T00:00:00Z,*)").
	OccurredAtRange string
	// Search performs a case-insensitive substring match against resource_id
	// or description. A search filter must be scoped — combine it with
	// occurred_at_range, or with both resource_type and resource_id — or the
	// request is rejected.
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
// Resource types and event types (read-only index surfaces)
// ---------------------------------------------------------------------------

// Pagination is the offset-pagination meta block returned on every standard
// list response. Page and Size always reflect the parameters that served the
// response. Total and TotalPages are populated only when the request set
// MetaTotal=true.
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
	// CreatedAt is the earliest sighting of this resource_type for the
	// account.
	CreatedAt time.Time
}

// ListResourceTypesInput is the pagination input for AuditResourceTypes.List.
type ListResourceTypesInput struct {
	// Environments scopes results to the given environment keys (e.g.
	// "production", "staging"). When omitted or empty, results are scoped
	// to your single accessible environment. The reserved value "smplkit"
	// selects platform change events that smplkit records about your own
	// resources (flags, configuration, and so on); these are not tied to a
	// deployment environment and are readable regardless of which
	// environments you manage. Multiple values are sent as a single
	// comma-separated filter[environment].
	Environments []string
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

// AuditEventType is one row from the event-types index.
type AuditEventType struct {
	// ID is the event type slug (e.g. "invoice.created").
	ID string
	// EventType is the same slug as ID; both fields are populated for clarity.
	EventType string
	// CreatedAt is the earliest sighting of this event_type for the account.
	// When the parent List call filtered by ResourceType, this is the first
	// sighting of that specific (event_type, resource_type) pair rather than
	// the event_type overall.
	CreatedAt time.Time
}

// ListEventTypesInput is the filter + pagination input for AuditEventTypes.List.
type ListEventTypesInput struct {
	// Environments scopes results to the given environment keys (e.g.
	// "production", "staging"). When omitted or empty, results are scoped
	// to your single accessible environment. The reserved value "smplkit"
	// selects platform change events that smplkit records about your own
	// resources (flags, configuration, and so on); these are not tied to a
	// deployment environment and are readable regardless of which
	// environments you manage. Multiple values are sent as a single
	// comma-separated filter[environment].
	Environments []string
	// FilterResourceType, when set, returns only event types seen with that
	// specific resource type.
	FilterResourceType string
	// PageNumber is the 1-based page index. Zero defers to the server default.
	PageNumber int
	// PageSize is items per page. Zero defers to the server default.
	PageSize int
	// MetaTotal asks the server to populate total / total_pages.
	MetaTotal bool
}

// EventTypeListPage is one page of event type slugs.
type EventTypeListPage struct {
	// EventTypes is the slice of event types on this page.
	EventTypes []AuditEventType
	// Pagination describes the page boundaries and totals (if requested).
	Pagination Pagination
}

// AuditCategory is a distinct category value seen for the account.
//
// Same shape as AuditResourceType and AuditEventType — ID and Category are
// the same value, surfaced as the JSON:API resource id. The duplication keeps
// SDK consumers from having to dig into the ID field when populating filter UI
// controls; pick whichever name reads better in context.
type AuditCategory struct {
	// ID is the category value, surfaced as the JSON:API resource id.
	ID string
	// Category is the same value as ID; provided for readability.
	Category string
	// CreatedAt is the earliest sighting of this category for the account.
	CreatedAt time.Time
}

// ListCategoriesInput is the filter + pagination input for AuditCategories.List.
type ListCategoriesInput struct {
	// Environments scopes results to the given environment keys (e.g.
	// "production", "staging"). When omitted or empty, results are scoped
	// to your single accessible environment. The reserved value "smplkit"
	// selects platform change events that smplkit records about your own
	// resources (flags, configuration, and so on); these are not tied to a
	// deployment environment and are readable regardless of which
	// environments you manage. Multiple values are sent as a single
	// comma-separated filter[environment].
	Environments []string
	// PageNumber is the 1-based page index. Zero defers to the server default.
	PageNumber int
	// PageSize is items per page. Zero defers to the server default.
	PageSize int
	// MetaTotal asks the server to populate total / total_pages.
	MetaTotal bool
}

// CategoryListPage is one page of category values.
type CategoryListPage struct {
	// Categories is the slice of categories on this page.
	Categories []AuditCategory
	// Pagination describes the page boundaries and totals (if requested).
	Pagination Pagination
}

// ---------------------------------------------------------------------------
// Forwarders (SIEM streaming)
// ---------------------------------------------------------------------------

// HttpHeader is a single name/value HTTP header on a forwarder
// destination request.
type HttpHeader struct {
	// Name is the header name (e.g. "Authorization", "DD-API-KEY").
	Name string
	// Value is the header value. Supply it plaintext on writes; reads
	// return it plaintext too, so a Get → mutate → Save round-trip
	// preserves header values without re-entering secrets.
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
	// credentials; supply them plaintext on writes. Reads return them
	// plaintext too, so a Get → mutate → Save round-trip preserves them.
	Headers []HttpHeader
	// SuccessStatus is the response status the destination must return
	// for delivery to count as success — an exact code ("200", "204")
	// or a class ("2xx", "4xx"). Defaults to "2xx" server-side when
	// zero-valued.
	SuccessStatus string
	// TlsVerify controls whether the destination's TLS certificate
	// chain is verified. Pointer-valued so the zero-value HttpConfiguration
	// is unambiguous: nil means "leave at the server default of true",
	// &false means "skip verification" (the trial-Splunk escape hatch),
	// &true means "verify explicitly". Prefer pinning the issuing CA via
	// CaCert for long-lived self-signed setups.
	TlsVerify *bool
	// CaCert is an optional PEM-encoded certificate (or bundle) trusted
	// in addition to the system CA store. Ignored when TlsVerify points
	// to false. Empty/nil string (the default) means "use system CAs only".
	CaCert *string
}

// ForwarderEnvironment is a per-environment override for a forwarder's
// enablement and optional configuration. A forwarder delivers events in
// a given environment only when that environment has an entry in
// Forwarder.Environments with Enabled=true; an environment with no entry
// (or Enabled=false) receives no deliveries.
type ForwarderEnvironment struct {
	// Enabled controls whether the forwarder delivers events in this
	// environment. Defaults to false.
	Enabled bool
	// Configuration is an optional per-environment destination
	// configuration that fully replaces the forwarder's base
	// Configuration for this environment. Nil (the default) inherits the
	// base configuration. As with the base configuration, header values
	// are plaintext on both writes and reads, so a Get → mutate → Save
	// round-trip preserves them.
	Configuration *HttpConfiguration
}

// Forwarder is a SIEM streaming destination configured on the
// customer's account. Active-record style: mutate fields directly and
// call Save(ctx) to persist, or Delete(ctx) to remove. Header values in
// Configuration.Headers are returned plaintext on reads, so a Get →
// mutate → Save round-trip preserves them.
type Forwarder struct {
	// ID is the caller-supplied key for this forwarder. Required at
	// create time (the audit service does not auto-generate it); unique
	// within the account and immutable for the lifetime of the forwarder.
	// The audit service returns 409 if another live forwarder already uses
	// this id. Empty string until Save has run for an unsaved instance
	// constructed without an id.
	ID string
	// Name is the display name. Free-form.
	Name string
	// Description is an optional free-text description.
	Description *string
	// ForwarderType is the destination type — see ForwarderType.
	ForwarderType ForwarderType
	// Enabled is read-only and always false. The base enablement is
	// pinned off; whether a forwarder actually delivers is decided per
	// environment via Environments. Mutating this field has
	// no effect on the server — the wrapper does not send it. It is kept
	// so reads round-trip the server value.
	Enabled bool
	// Environments holds per-environment overrides keyed by environment
	// key (e.g. "production", "staging"). A forwarder delivers in an
	// environment only when Environments[env].Enabled is true. Each entry
	// may carry an optional HttpConfiguration override; leave it nil to
	// inherit the base Configuration. Every referenced environment must
	// exist and be managed for the account. Nil/empty means the forwarder
	// delivers nowhere until enabled per environment.
	Environments map[string]ForwarderEnvironment
	// Filter is an optional JSON Logic expression evaluated per event.
	// When set, events that don't match are recorded as filtered_out
	// deliveries instead of being POSTed to the destination.
	Filter map[string]interface{}
	// Transform is an optional template applied to each event before
	// delivery. Shape depends on TransformType; for JSONATA, a string
	// containing a JSONata expression. Future engines may carry richer
	// shapes (objects, arrays, …), so the field is untyped. Nil
	// delivers the event JSON as-is; when set, TransformType must also
	// be set.
	Transform interface{}
	// TransformType identifies the engine used to evaluate Transform.
	// Currently only ForwarderTransformTypeJSONata is supported. Must
	// be set whenever Transform is set.
	TransformType *ForwarderTransformType
	// ForwardSmplkitEvents, when true, also delivers smplkit's own
	// platform change events (flag, configuration, and similar changes
	// smplkit records about your resources) through this forwarder. Each
	// such event is delivered through every environment this forwarder is
	// enabled in, using that environment's resolved configuration. Nil or
	// false (the default) means platform change events are not forwarded.
	// Independent of the per-environment Environments enablement, since
	// platform change events are not tied to a deployment environment.
	ForwardSmplkitEvents *bool
	// Configuration is the destination request configuration.
	Configuration HttpConfiguration
	// CreatedAt is when the audit service first persisted this
	// forwarder. Nil for an unsaved instance.
	CreatedAt *time.Time
	// UpdatedAt is when this forwarder was last mutated.
	UpdatedAt *time.Time
	// DeletedAt is when the forwarder was deleted. Nil for live forwarders.
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
	// to its forwarders client so Save / Delete work directly.
	Forwarders []Forwarder
	// Pagination describes the page boundaries and totals (if
	// requested via MetaTotal).
	Pagination Pagination
}
