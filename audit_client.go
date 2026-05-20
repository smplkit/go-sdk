package smplkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// AuditClient is the runtime audit surface — accessed via Client.Audit().
//
// Sub-clients: Events for event recording / listing / retrieval,
// ResourceTypes for the distinct resource-type index,
// EventTypes for the distinct event-type index.
//
// SIEM forwarder CRUD lives on the management plane:
// Client.Manage().Audit().Forwarders().
type AuditClient struct {
	client        *Client
	gen           *genaudit.ClientWithResponses
	events        *AuditEvents
	resourceTypes *AuditResourceTypes
	eventTypes    *AuditEventTypes
}

// AuditEvents handles event recording, listing, and retrieval. Writes are
// fire-and-forget by default and return as soon as the event is enqueued
// onto the in-process buffer (ADR-047 §2.6).
type AuditEvents struct {
	gen    *genaudit.ClientWithResponses
	buffer *auditEventBuffer
}

// AuditResourceTypes lists the distinct resource-type slugs seen in the account.
type AuditResourceTypes struct {
	gen *genaudit.ClientWithResponses
}

// AuditEventTypes lists the distinct event type slugs seen in the account.
type AuditEventTypes struct {
	gen *genaudit.ClientWithResponses
}

// Audit returns the audit-product sub-client. Same instance every call.
func (c *Client) Audit() *AuditClient {
	return c.audit
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

// Record enqueues an audit event for asynchronous delivery.
//
// Returns nil immediately. The buffer worker handles the actual POST
// and retries on transient failures. ResourceType beginning with
// "smpl." is rejected by the server with 403 — that namespace is
// reserved for smplkit-emitted events.
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
	if input.Data != nil {
		d := input.Data
		attrs.Data = &d
	}
	if input.DoNotForward {
		dnf := true
		attrs.DoNotForward = &dnf
	}

	body := genaudit.EventRequest{
		Data: genaudit.EventResource{
			Attributes: attrs,
		},
	}
	e.buffer.enqueue(body, input.IdempotencyKey)
	return nil
}

// List returns one page of audit events scoped to the caller's account.
//
// Filters are exact-match except OccurredAtRange which uses the
// platform's range syntax (e.g. "[2026-01-01T00:00:00Z,*)"). Pass
// the previous page's NextCursor as PageAfter to walk subsequent pages.
func (e *AuditEvents) List(ctx context.Context, input ListEventsInput) (*ListEventsPage, error) {
	params := &genaudit.ListEventsParams{}
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
// processes that don't always reach Client.Close.
func (e *AuditEvents) Flush(timeout time.Duration) {
	e.buffer.flush(timeout)
}

// close drains and stops the background worker. Called from Client.Close.
func (e *AuditEvents) close() {
	if e.buffer != nil {
		e.buffer.close(5 * time.Second)
	}
}

// List returns one page of distinct resource-type slugs seen in the account.
//
// Backed by a maintain-by-write side table (ADR-047 §2.5), so the
// response time is independent of event volume. Sorted alphabetically;
// offset pagination via PageNumber / PageSize (ADR-014).
func (rt *AuditResourceTypes) List(ctx context.Context, input ListResourceTypesInput) (*ResourceTypeListPage, error) {
	params := &genaudit.ListResourceTypesParams{}
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
	if attrs.Data != nil {
		out.Data = *attrs.Data
	}
	if attrs.IdempotencyKey != nil {
		out.IdempotencyKey = *attrs.IdempotencyKey
	}
	if attrs.DoNotForward != nil {
		out.DoNotForward = *attrs.DoNotForward
	}
	return out
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
