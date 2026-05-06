package smplkit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// AuditClient is the audit-product surface — accessed via Client.Audit().
//
// Today the audit namespace is exclusively events; future iterations may
// add SIEM exports or custom resource types as additional sub-clients.
type AuditClient struct {
	client *Client
	gen    *genaudit.ClientWithResponses
	events *AuditEvents
}

// AuditEvents handles event creation, listing, and retrieval. Reads are
// synchronous; writes are fire-and-forget by default and return as soon
// as the event is enqueued onto the in-process buffer (ADR-047 §2.6).
type AuditEvents struct {
	gen    *genaudit.ClientWithResponses
	buffer *auditEventBuffer
}

// Audit returns the audit-product sub-client. Same instance every call.
func (c *Client) Audit() *AuditClient {
	return c.audit
}

// Events returns the events sub-client.
func (a *AuditClient) Events() *AuditEvents {
	return a.events
}

// Create enqueues an audit event for asynchronous delivery.
//
// Returns nil immediately. The buffer worker handles the actual POST
// and retries on transient failures. ResourceType beginning with
// ``smpl.`` is rejected by the server with 403 — that namespace is
// reserved for smplkit-emitted events.
func (e *AuditEvents) Create(input CreateEventInput) error {
	if input.Action == "" || input.ResourceType == "" || input.ResourceID == "" {
		return errors.New("audit Create requires Action, ResourceType, and ResourceID")
	}

	attrs := genaudit.Event{
		Action:       input.Action,
		ResourceType: input.ResourceType,
		ResourceId:   input.ResourceID,
	}
	if input.OccurredAt != nil {
		attrs.OccurredAt = input.OccurredAt
	}
	if input.Snapshot != nil {
		s := input.Snapshot
		attrs.Snapshot = &s
	}
	if input.Data != nil {
		d := input.Data
		attrs.Data = &d
	}

	resourceType := "event"
	body := genaudit.EventResponse{
		Data: genaudit.EventResource{
			Id:         "", // server assigns
			Type:       &resourceType,
			Attributes: attrs,
		},
	}
	e.buffer.enqueue(body, input.IdempotencyKey)
	return nil
}

// List returns one page of audit events scoped to the caller's account.
//
// Filters are exact-match except OccurredAtRange which uses the
// platform's range syntax (e.g. ``[2026-01-01T00:00:00Z,*)``). Pass
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
		return nil, fmt.Errorf("audit List failed: status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}

	body := resp.ApplicationvndApiJSON200
	page := &ListEventsPage{
		Events: make([]AuditEvent, 0, len(body.Data)),
	}
	for _, r := range body.Data {
		page.Events = append(page.Events, eventFromResource(r))
	}
	if body.Links != nil && body.Links.Next != nil {
		// Extract the cursor token from the link; the server emits a
		// relative URL like ``/api/v1/events?page[size]=50&page[after]=<token>``.
		if i := strings.Index(*body.Links.Next, "page[after]="); i >= 0 {
			page.NextCursor = (*body.Links.Next)[i+len("page[after]="):]
			if amp := strings.Index(page.NextCursor, "&"); amp >= 0 {
				page.NextCursor = page.NextCursor[:amp]
			}
		}
	}
	return page, nil
}

// Get retrieves a single audit event by id.
//
// Returns an error wrapping HTTP status 404 if no event with that id
// exists in the caller's account.
func (e *AuditEvents) Get(ctx context.Context, eventID uuid.UUID) (*AuditEvent, error) {
	resp, err := e.gen.GetEventWithResponse(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("audit Get: %w", err)
	}
	if resp.StatusCode() == 404 {
		return nil, fmt.Errorf("audit Get: event not found (status=404)")
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("audit Get failed: status=%d body=%s", resp.StatusCode(), string(resp.Body))
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

// eventFromResource converts the JSON:API resource shape into the
// flat AuditEvent struct.
func eventFromResource(r genaudit.EventResource) AuditEvent {
	id, _ := uuid.Parse(r.Id)
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
	if attrs.Snapshot != nil {
		out.Snapshot = *attrs.Snapshot
	}
	if attrs.Data != nil {
		out.Data = *attrs.Data
	}
	if attrs.IdempotencyKey != nil {
		out.IdempotencyKey = *attrs.IdempotencyKey
	}
	return out
}

// Silence unused-symbol lint on package-level helper used by tests.
var _ = strconv.Itoa
