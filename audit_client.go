package smplkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// AuditClient is the Smpl Audit client.
//
// Audit installs no in-process machinery, so a single client exposes the
// full surface — event recording and reads, distinct-value discovery, and
// SIEM forwarder CRUD — reachable as client.Audit() or constructed directly
// via NewAuditClient.
//
// Namespaces: Events (Record/Flush/List/Get), ResourceTypes, EventTypes,
// Categories (discovery), and Forwarders (CRUD).
type AuditClient struct {
	client        *SmplClient
	gen           *genaudit.ClientWithResponses
	events        *AuditEvents
	resourceTypes *AuditResourceTypes
	eventTypes    *AuditEventTypes
	categories    *AuditCategories
	forwarders    *AuditForwarders
}

// AuditEvents handles event recording, listing, and retrieval. Writes are
// fire-and-forget by default and return as soon as the event is enqueued
// onto the in-process buffer. With Config.DisableEventBuffering set, writes
// are synchronous instead — one POST per Record, no buffer.
//
// environment is the SDK's configured runtime environment (empty when
// unset). It is stamped onto the event request body when recording and
// supplied as the default filter[environment] on List (ADR-055).
type AuditEvents struct {
	gen *genaudit.ClientWithResponses
	// buffer is the in-memory queue + worker behind fire-and-forget writes.
	// nil in stateless mode (Config.DisableEventBuffering), where Record
	// POSTs synchronously instead.
	buffer      *auditEventBuffer
	environment string
}

// AuditResourceTypes lists the distinct resource-type slugs seen in the account.
//
// environment is the SDK's configured runtime environment (empty when unset);
// it scopes the listing as the default filter[environment] (ADR-055).
type AuditResourceTypes struct {
	gen         *genaudit.ClientWithResponses
	environment string
}

// AuditEventTypes lists the distinct event type slugs seen in the account.
//
// environment is the SDK's configured runtime environment (empty when unset);
// it scopes the listing as the default filter[environment] (ADR-055).
type AuditEventTypes struct {
	gen         *genaudit.ClientWithResponses
	environment string
}

// AuditCategories lists the distinct category values seen in the account.
//
// environment is the SDK's configured runtime environment (empty when unset);
// it scopes the listing as the default filter[environment] (ADR-055).
type AuditCategories struct {
	gen         *genaudit.ClientWithResponses
	environment string
}

// Events returns the events sub-client.
func (a *AuditClient) Events() *AuditEvents {
	return a.events
}

// ResourceTypes returns the resource-types index sub-client.
func (a *AuditClient) ResourceTypes() *AuditResourceTypes {
	return a.resourceTypes
}

// EventTypes returns the event-types index sub-client.
func (a *AuditClient) EventTypes() *AuditEventTypes {
	return a.eventTypes
}

// Categories returns the categories index sub-client.
func (a *AuditClient) Categories() *AuditCategories {
	return a.categories
}

// Forwarders returns the SIEM forwarder CRUD sub-client.
func (a *AuditClient) Forwarders() *AuditForwarders {
	return a.forwarders
}

// newAuditClient assembles an AuditClient from a single generated audit client.
//
// Environment scoping is body-driven (ADR-055): the configured environment is
// stamped onto the event request body when recording and supplied as the
// default filter[environment] on the read / discovery surfaces (Events.List,
// ResourceTypes, EventTypes, Categories). Forwarder CRUD is account-wide and
// environment-agnostic, so it carries no environment. One transport backs the
// whole surface. The top-level SmplClient wires this in and sets the optional
// client back-reference itself.
//
// disableBuffering selects the stateless write path
// (Config.DisableEventBuffering): no buffer is constructed — and therefore
// no worker goroutine ever starts — and Record POSTs synchronously.
func newAuditClient(gen *genaudit.ClientWithResponses, environment string, disableBuffering bool) *AuditClient {
	events := &AuditEvents{gen: gen, environment: environment}
	if !disableBuffering {
		events.buffer = newAuditEventBuffer(gen)
	}
	return &AuditClient{
		gen:           gen,
		events:        events,
		resourceTypes: &AuditResourceTypes{gen: gen, environment: environment},
		eventTypes:    &AuditEventTypes{gen: gen, environment: environment},
		categories:    &AuditCategories{gen: gen, environment: environment},
		forwarders:    &AuditForwarders{gen: gen},
	}
}

// NewAuditClient builds a standalone Smpl Audit client.
//
// Audit installs no in-process machinery, so a single client exposes the
// full surface — event recording and reads, distinct-value discovery, and
// SIEM forwarder CRUD.
//
// Environment scoping is body-driven (ADR-055): cfg.Environment is stamped
// onto the event request body when recording and supplied as the default
// filter[environment] on the read / discovery surfaces (Events.List,
// ResourceTypes, EventTypes, Categories). It no longer rides on a request
// header, so a single transport backs the whole surface, forwarder CRUD
// included. Config is resolved from cfg merged with SMPLKIT_* environment
// variables and the ~/.smplkit profile.
//
// Call Close when done to release the underlying HTTP resources and drain
// the in-memory event buffer. With cfg.DisableEventBuffering set, no buffer
// (and no background goroutine) exists — Record POSTs synchronously and
// Close has nothing to drain.
func NewAuditClient(cfg Config, opts ...ClientOption) (*AuditClient, error) {
	rc, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}

	optCfg := defaultConfig()
	for _, opt := range opts {
		opt(&optCfg)
	}

	httpClient := optCfg.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: optCfg.timeout}
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: rc.apiKey, base: base}

	auditURL := serviceURL(optCfg, "audit", rc)

	// Capture extra headers once; the editor closures below close over it.
	extraHeaders := rc.extraHeaders

	auditHeaderEditor := genaudit.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	auditExtraEditor := genaudit.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})

	auditRaw, _ := genaudit.NewClient(auditURL,
		genaudit.WithHTTPClient(httpClient),
		auditHeaderEditor,
		auditExtraEditor,
	)
	auditGen := &genaudit.ClientWithResponses{ClientInterface: auditRaw}

	return newAuditClient(auditGen, rc.environment, rc.disableEventBuffering), nil
}

// Close drains the in-memory event buffer, blocking until it empties or the
// drain times out. Call it when done with a standalone AuditClient (one built
// via NewAuditClient) so buffered fire-and-forget events are delivered before
// the process exits. When the audit surface was wired in by a top-level
// Client, SmplClient.Close drives this. A no-op in stateless mode
// (Config.DisableEventBuffering), where there is no buffer to drain.
func (a *AuditClient) Close() error {
	if a.events != nil {
		a.events.close()
	}
	return nil
}

// Record enqueues an audit event for delivery.
//
// By default it returns nil immediately and the buffer worker handles the
// actual POST, retrying on transient failures. Set input.Flush to block until
// the event has drained (or input.FlushTimeout elapses) before returning —
// useful when the caller needs the event durable before continuing.
// ResourceType beginning with "smpl." is rejected by the server with 403 —
// that namespace is reserved for smplkit-emitted events.
//
// In stateless mode (Config.DisableEventBuffering) every call performs one
// synchronous POST and returns the SDK's typed errors on failure;
// input.Flush and input.FlushTimeout are meaningless there and ignored.
func (e *AuditEvents) Record(input CreateEventInput) error {
	if input.EventType == "" || input.ResourceType == "" || input.ResourceID == "" {
		return errors.New("audit Record requires EventType, ResourceType, and ResourceID")
	}

	attrs := genaudit.Event{
		EventType:    input.EventType,
		ResourceType: input.ResourceType,
		ResourceId:   input.ResourceID,
	}
	if input.OccurredAt != nil {
		attrs.OccurredAt = input.OccurredAt
	}
	if input.ActorType != "" {
		at := input.ActorType
		attrs.ActorType = &at
	}
	if input.ActorID != "" {
		aid := input.ActorID
		attrs.ActorId = &aid
	}
	if input.ActorLabel != "" {
		al := input.ActorLabel
		attrs.ActorLabel = &al
	}
	if input.Category != "" {
		cat := input.Category
		attrs.Category = &cat
	}
	if input.Severity != "" {
		sev := genaudit.Severity(input.Severity)
		attrs.Severity = &sev
	}
	if input.Data != nil {
		d := input.Data
		attrs.Data = &d
	}
	if input.DoNotForward {
		dnf := true
		attrs.DoNotForward = &dnf
	}
	// Stamp the SDK's configured environment onto the event body — the
	// body-driven replacement for the old X-Smplkit-Environment header
	// (ADR-055). Omitted when unset so a single-environment credential
	// resolves it server-side.
	if e.environment != "" {
		env := e.environment
		attrs.Environment = &env
	}

	body := genaudit.EventRequest{
		Data: genaudit.EventResource{
			Attributes: attrs,
		},
	}
	if e.buffer == nil {
		return e.recordSync(body, input.IdempotencyKey)
	}
	e.buffer.enqueue(body, input.IdempotencyKey)
	if input.Flush {
		timeout := input.FlushTimeout
		if timeout == 0 {
			timeout = auditDefaultFlushTimeout
		}
		e.buffer.flush(timeout)
	}
	return nil
}

// recordSync is the stateless write path (Config.DisableEventBuffering):
// one synchronous POST per Record. It issues the same wire call the buffer
// worker's delivery path uses and maps failures to the SDK's typed errors.
func (e *AuditEvents) recordSync(body genaudit.EventRequest, idempKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	params := &genaudit.RecordEventParams{}
	if idempKey != "" {
		ik := idempKey
		params.IdempotencyKey = &ik
	}
	resp, err := e.gen.RecordEventWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, params, body)
	if err != nil {
		return fmt.Errorf("audit Record: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return checkStatus(resp.StatusCode(), resp.Body)
	}
	return nil
}

// List returns one page of audit events scoped to the caller's account.
//
// Filters are exact-match except OccurredAtRange which uses the
// platform's range syntax (e.g. "[2026-01-01T00:00:00Z,*)"). Pass
// the previous page's NextCursor as PageAfter to walk subsequent pages.
func (e *AuditEvents) List(ctx context.Context, input ListEventsInput) (*ListEventsPage, error) {
	params := &genaudit.ListEventsParams{}
	if env := resolveEnvironmentFilter(input.Environments, e.environment); env != "" {
		params.FilterEnvironment = &env
	}
	if input.EventType != "" {
		params.FilterEventType = &input.EventType
	}
	if input.ResourceType != "" {
		params.FilterResourceType = &input.ResourceType
	}
	if input.ResourceID != "" {
		params.FilterResourceId = &input.ResourceID
	}
	if input.ActorType != "" {
		params.FilterActorType = &input.ActorType
	}
	if input.ActorID != "" {
		params.FilterActorId = &input.ActorID
	}
	if input.Category != "" {
		params.FilterCategory = &input.Category
	}
	if input.OccurredAtRange != "" {
		params.FilterOccurredAt = &input.OccurredAtRange
	}
	if input.Search != "" {
		params.FilterSearch = &input.Search
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.PageAfter != "" {
		params.PageAfter = &input.PageAfter
	}

	resp, err := e.gen.ListEventsWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("audit List: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}

	body := resp.ApplicationvndApiJSON200
	page := &ListEventsPage{
		Events: make([]AuditEvent, 0, len(body.Data)),
	}
	for _, r := range body.Data {
		page.Events = append(page.Events, eventFromResource(r))
	}
	if body.Links != nil && body.Links.Next != nil {
		page.NextCursor = extractNextCursor(body.Links.Next)
	}
	return page, nil
}

// Get retrieves a single audit event by id.
//
// Returns a *NotFoundError if no event with that id exists in the caller's account.
func (e *AuditEvents) Get(ctx context.Context, eventID uuid.UUID) (*AuditEvent, error) {
	resp, err := e.gen.GetEventWithResponse(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("audit Get: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	ev := eventFromResource(resp.ApplicationvndApiJSON200.Data)
	return &ev, nil
}

// Flush blocks until the in-memory event buffer is drained or the
// timeout elapses. Useful for shutdown semantics in short-lived
// processes that don't always reach SmplClient.Close. A no-op in
// stateless mode (Config.DisableEventBuffering), where every Record is
// already durable on return.
func (e *AuditEvents) Flush(timeout time.Duration) {
	if e.buffer != nil {
		e.buffer.flush(timeout)
	}
}

// close drains and stops the background worker. Called from SmplClient.Close.
func (e *AuditEvents) close() {
	if e.buffer != nil {
		e.buffer.close(5 * time.Second)
	}
}

// List returns one page of distinct resource-type slugs seen in the account.
//
// Response time is independent of how many years of events the account has
// accumulated. Sorted alphabetically; offset pagination via PageNumber /
// PageSize.
func (rt *AuditResourceTypes) List(ctx context.Context, input ListResourceTypesInput) (*ResourceTypeListPage, error) {
	params := &genaudit.ListResourceTypesParams{}
	if env := resolveEnvironmentFilter(input.Environments, rt.environment); env != "" {
		params.FilterEnvironment = &env
	}
	if input.PageNumber > 0 {
		params.PageNumber = &input.PageNumber
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.MetaTotal {
		mt := true
		params.MetaTotal = &mt
	}
	resp, err := rt.gen.ListResourceTypesWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("audit ResourceTypes.List: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	body := resp.ApplicationvndApiJSON200
	page := &ResourceTypeListPage{
		ResourceTypes: make([]AuditResourceType, 0, len(body.Data)),
		Pagination:    paginationFromMeta(body.Meta.Pagination),
	}
	for _, r := range body.Data {
		page.ResourceTypes = append(page.ResourceTypes, AuditResourceType{
			ID:           r.Id,
			ResourceType: r.Attributes.ResourceType,
			CreatedAt:    r.Attributes.CreatedAt,
		})
	}
	return page, nil
}

// List returns one page of distinct event type slugs seen in the account.
//
// Without FilterResourceType, returns one row per distinct event type. With
// the filter, returns only the event types seen with that specific resource
// type. Sorted alphabetically; offset pagination via PageNumber / PageSize.
func (et *AuditEventTypes) List(ctx context.Context, input ListEventTypesInput) (*EventTypeListPage, error) {
	params := &genaudit.ListEventTypesParams{}
	if env := resolveEnvironmentFilter(input.Environments, et.environment); env != "" {
		params.FilterEnvironment = &env
	}
	if input.FilterResourceType != "" {
		params.FilterResourceType = &input.FilterResourceType
	}
	if input.PageNumber > 0 {
		params.PageNumber = &input.PageNumber
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.MetaTotal {
		mt := true
		params.MetaTotal = &mt
	}
	resp, err := et.gen.ListEventTypesWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("audit EventTypes.List: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	body := resp.ApplicationvndApiJSON200
	page := &EventTypeListPage{
		EventTypes: make([]AuditEventType, 0, len(body.Data)),
		Pagination: paginationFromMeta(body.Meta.Pagination),
	}
	for _, r := range body.Data {
		page.EventTypes = append(page.EventTypes, AuditEventType{
			ID:        r.Id,
			EventType: r.Attributes.EventType,
			CreatedAt: r.Attributes.CreatedAt,
		})
	}
	return page, nil
}

// List returns one page of distinct category values seen in the account.
//
// Response time is independent of how many years of events the account has
// accumulated. Sorted alphabetically; offset pagination via PageNumber /
// PageSize.
func (cat *AuditCategories) List(ctx context.Context, input ListCategoriesInput) (*CategoryListPage, error) {
	params := &genaudit.ListCategoriesParams{}
	if env := resolveEnvironmentFilter(input.Environments, cat.environment); env != "" {
		params.FilterEnvironment = &env
	}
	if input.PageNumber > 0 {
		params.PageNumber = &input.PageNumber
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.MetaTotal {
		mt := true
		params.MetaTotal = &mt
	}
	resp, err := cat.gen.ListCategoriesWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("audit Categories.List: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	body := resp.ApplicationvndApiJSON200
	page := &CategoryListPage{
		Categories: make([]AuditCategory, 0, len(body.Data)),
		Pagination: paginationFromMeta(body.Meta.Pagination),
	}
	for _, r := range body.Data {
		page.Categories = append(page.Categories, AuditCategory{
			ID:        r.Id,
			Category:  r.Attributes.Category,
			CreatedAt: r.Attributes.CreatedAt,
		})
	}
	return page, nil
}

// paginationFromMeta converts the generated PaginationMeta into the
// wrapper-public Pagination shape, sharing the optional Total /
// TotalPages pointers as-is.
func paginationFromMeta(p genaudit.PaginationMeta) Pagination {
	return Pagination{
		Page:       p.Page,
		Size:       p.Size,
		Total:      p.Total,
		TotalPages: p.TotalPages,
	}
}

// eventFromResource converts the JSON:API resource shape into the
// flat AuditEvent struct.
func eventFromResource(r genaudit.EventResource) AuditEvent {
	var id uuid.UUID
	if r.Id != nil {
		id, _ = uuid.Parse(*r.Id)
	}
	attrs := r.Attributes
	out := AuditEvent{
		ID:           id,
		EventType:    attrs.EventType,
		ResourceType: attrs.ResourceType,
		ResourceID:   attrs.ResourceId,
	}
	if attrs.OccurredAt != nil {
		out.OccurredAt = *attrs.OccurredAt
	}
	if attrs.CreatedAt != nil {
		out.CreatedAt = *attrs.CreatedAt
	}
	if attrs.ActorType != nil {
		out.ActorType = *attrs.ActorType
	}
	if attrs.ActorId != nil {
		out.ActorID = *attrs.ActorId
	}
	if attrs.ActorLabel != nil {
		out.ActorLabel = *attrs.ActorLabel
	}
	if attrs.Category != nil {
		out.Category = *attrs.Category
	}
	if attrs.Data != nil {
		out.Data = *attrs.Data
	}
	if attrs.IdempotencyKey != nil {
		out.IdempotencyKey = *attrs.IdempotencyKey
	}
	if attrs.DoNotForward != nil {
		out.DoNotForward = *attrs.DoNotForward
	}
	if attrs.Environment != nil {
		out.Environment = *attrs.Environment
	}
	return out
}

// resolveEnvironmentFilter resolves the filter[environment] value for an audit
// read / discovery surface. An explicit environments list always wins (it is
// comma-joined); otherwise the client's configured environment scopes the read
// — the body-driven replacement for the old X-Smplkit-Environment header, which
// previously scoped every read to the configured environment (ADR-055). With no
// explicit list and no configured environment it returns "" so the parameter is
// omitted and the credential's own scoping applies server-side.
func resolveEnvironmentFilter(environments []string, configured string) string {
	if joined := joinEnvironments(environments); joined != "" {
		return joined
	}
	return configured
}

// joinEnvironments comma-joins the requested environment keys into the
// single filter[environment] value the audit read endpoints accept.
// Empty/whitespace-only entries are dropped; a nil or all-empty slice
// yields "" so the caller falls back to the configured-environment default.
func joinEnvironments(envs []string) string {
	if len(envs) == 0 {
		return ""
	}
	kept := make([]string, 0, len(envs))
	for _, e := range envs {
		if trimmed := strings.TrimSpace(e); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, ",")
}

// extractNextCursor extracts the page[after] cursor value from a
// JSON:API next-link string (e.g. "/api/v1/events?page[after]=tok&page[size]=50").
func extractNextCursor(next *string) string {
	if next == nil {
		return ""
	}
	if i := strings.Index(*next, "page[after]="); i >= 0 {
		token := (*next)[i+len("page[after]="):]
		if amp := strings.Index(token, "&"); amp >= 0 {
			token = token[:amp]
		}
		return token
	}
	return ""
}
