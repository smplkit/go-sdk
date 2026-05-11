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
// Actions for the distinct action index.
//
// SIEM forwarder CRUD lives on the management plane:
// Client.Manage().Audit().Forwarders().
type AuditClient struct {
	client        *Client
	gen           *genaudit.ClientWithResponses
	events        *AuditEvents
	resourceTypes *AuditResourceTypes
	actions       *AuditActions
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

// AuditActions lists the distinct action slugs seen in the account.
type AuditActions struct {
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

// Actions returns the actions index sub-client.
func (a *AuditClient) Actions() *AuditActions {
	return a.actions
}

// Record enqueues an audit event for asynchronous delivery.
//
// Returns nil immediately. The buffer worker handles the actual POST
// and retries on transient failures. ResourceType beginning with
// "smpl." is rejected by the server with 403 — that namespace is
// reserved for smplkit-emitted events.
func (e *AuditEvents) Record(input CreateEventInput) error {
	if input.Action == "" || input.ResourceType == "" || input.ResourceID == "" {
		return errors.New("audit Record requires Action, ResourceType, and ResourceID")
	}

	attrs := genaudit.Event{
		Action:       input.Action,
		ResourceType: input.ResourceType,
		ResourceId:   input.ResourceID,
	}
	if input.OccurredAt != nil {
		attrs.OccurredAt = input.OccurredAt
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
	if input.Action != "" {
		params.FilterAction = &input.Action
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
		actorUUID, err := uuid.Parse(input.ActorID)
		if err != nil {
			return nil, fmt.Errorf("audit List: invalid ActorID: %w", err)
		}
		params.FilterActorId = &actorUUID
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
// cursor pagination via PageAfter.
func (rt *AuditResourceTypes) List(ctx context.Context, input ListResourceTypesInput) (*ResourceTypeListPage, error) {
	params := &genaudit.ListResourceTypesParams{}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.PageAfter != "" {
		params.PageAfter = &input.PageAfter
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
	}
	for _, r := range body.Data {
		page.ResourceTypes = append(page.ResourceTypes, AuditResourceType{
			ID:           r.Id,
			ResourceType: r.Attributes.ResourceType,
		})
	}
	if body.Links != nil && body.Links.Next != nil {
		page.NextCursor = extractNextCursor(body.Links.Next)
	}
	return page, nil
}

// List returns one page of distinct action slugs seen in the account.
//
// Without FilterResourceType, returns one row per distinct action. With
// the filter, returns only the actions seen with that specific resource
// type. Sorted alphabetically; cursor pagination via PageAfter.
func (ac *AuditActions) List(ctx context.Context, input ListActionsInput) (*ActionListPage, error) {
	params := &genaudit.ListActionsParams{}
	if input.FilterResourceType != "" {
		params.FilterResourceType = &input.FilterResourceType
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.PageAfter != "" {
		params.PageAfter = &input.PageAfter
	}
	resp, err := ac.gen.ListActionsWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("audit Actions.List: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	body := resp.ApplicationvndApiJSON200
	page := &ActionListPage{
		Actions: make([]AuditAction, 0, len(body.Data)),
	}
	for _, r := range body.Data {
		page.Actions = append(page.Actions, AuditAction{
			ID:     r.Id,
			Action: r.Attributes.Action,
		})
	}
	if body.Links != nil && body.Links.Next != nil {
		page.NextCursor = extractNextCursor(body.Links.Next)
	}
	return page, nil
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
		Action:       attrs.Action,
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
		actorID := *attrs.ActorId
		out.ActorID = &actorID
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
