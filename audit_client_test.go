package smplkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// newTestAuditEvents wires the wrapper namespace against an httptest server.
func newTestAuditEvents(t *testing.T, handler http.HandlerFunc) (*AuditEvents, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	events := &AuditEvents{
		gen:    wrapped,
		buffer: newAuditEventBuffer(wrapped),
	}
	cleanup := func() {
		events.close()
		srv.Close()
	}
	return events, cleanup
}

func TestAuditEvents_Create_FireAndForget(t *testing.T) {
	var posts atomic.Int32
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"action":"x.created","resource_type":"x","resource_id":"1"}}}`))
	})
	defer cleanup()

	start := time.Now()
	for i := 0; i < 20; i++ {
		if err := events.Record(CreateEventInput{
			Action:       "user.created",
			ResourceType: "user",
			ResourceID:   "u-1",
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Create should return immediately; took %s", elapsed)
	}

	events.Flush(2 * time.Second)
	if posts.Load() == 0 {
		t.Fatal("expected the buffer worker to issue at least one POST")
	}
}

func TestAuditEvents_Create_RequiresFields(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	defer cleanup()

	err := events.Record(CreateEventInput{ResourceType: "user", ResourceID: "u-1"})
	if err == nil || !strings.Contains(err.Error(), "Action") {
		t.Fatalf("expected Action-required error, got %v", err)
	}
}

func TestAuditEvents_Create_PassesIdempotencyKeyHeader(t *testing.T) {
	gotKey := make(chan string, 1)
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey <- r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"action":"x.created","resource_type":"x","resource_id":"1"}}}`))
	})
	defer cleanup()

	if err := events.Record(CreateEventInput{
		Action:         "user.created",
		ResourceType:   "user",
		ResourceID:     "u-1",
		IdempotencyKey: "key-abc",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	events.Flush(2 * time.Second)

	select {
	case key := <-gotKey:
		if key != "key-abc" {
			t.Fatalf("expected Idempotency-Key=key-abc, got %q", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received a POST")
	}
}

func TestAuditEvents_Get_RoundTrip(t *testing.T) {
	eventID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, eventID.String()) {
			http.Error(w, "wrong path", 400)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
		body := map[string]any{
			"data": map[string]any{
				"id":   eventID.String(),
				"type": "event",
				"attributes": map[string]any{
					"action":          "user.created",
					"resource_type":   "user",
					"resource_id":     "u-1",
					"occurred_at":     "2026-05-06T12:00:00Z",
					"created_at":      "2026-05-06T12:00:01Z",
					"actor_type":      "API_KEY",
					"actor_id":        nil,
					"actor_label":     "",
					"snapshot":        nil,
					"data":            map[string]any{},
					"idempotency_key": "auto-abc",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	defer cleanup()

	ev, err := events.Get(context.Background(), eventID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.ID != eventID {
		t.Fatalf("ID mismatch: %s", ev.ID)
	}
	if ev.Action != "user.created" {
		t.Fatalf("Action=%q", ev.Action)
	}
}

func TestAuditEvents_Get_404(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", 404)
	})
	defer cleanup()

	_, err := events.Get(context.Background(), uuid.New())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

func TestAuditEvents_List_ParsesNextCursor(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
		  "data":[{
		    "id":"11111111-2222-3333-4444-555555555555",
		    "type":"event",
		    "attributes":{"action":"a.b","resource_type":"a","resource_id":"1","occurred_at":"2026-05-06T12:00:00Z","created_at":"2026-05-06T12:00:01Z","actor_type":"API_KEY","actor_id":null,"actor_label":"","snapshot":null,"data":{},"idempotency_key":"k"}
		  }],
		  "meta":{"page_size":1},
		  "links":{"next":"/api/v1/events?page[size]=1&page[after]=tok-xyz"}
		}`))
	})
	defer cleanup()

	page, err := events.List(context.Background(), ListEventsInput{PageSize: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.NextCursor != "tok-xyz" {
		t.Fatalf("expected NextCursor=tok-xyz, got %q", page.NextCursor)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(page.Events))
	}
}

// Cover the Client.Audit() and AuditClient.Events() accessors plus the
// branches that don't fit the success-path tests.
func TestClient_AuditAccessors(t *testing.T) {
	gotAccept := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept <- r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_size":50}}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{
		APIKey:      "sk_api_test",
		Environment: "dev",
		Service:     "test",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if c.Audit() == nil {
		t.Fatal("Audit() returned nil")
	}
	if c.Audit().Events() == nil {
		t.Fatal("Events() returned nil")
	}

	// Issue a request through the audit client so the request-editor
	// closure (which sets Accept + User-Agent) runs at least once.
	if _, err := c.Audit().Events().List(context.Background(), ListEventsInput{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	select {
	case accept := <-gotAccept:
		if !strings.Contains(accept, "application/vnd.api+json") {
			t.Fatalf("Accept header not propagated: %q", accept)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the audit request")
	}
}

func TestAuditEvents_List_TransportError(t *testing.T) {
	// A closed server triggers a transport-level error from the gen client,
	// covering the wrapper's "audit List: %w" branch.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()
	gen, _ := genaudit.NewClient(url)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	events := &AuditEvents{
		gen:    wrapped,
		buffer: newAuditEventBuffer(wrapped),
	}
	defer events.close()
	if _, err := events.List(context.Background(), ListEventsInput{}); err == nil {
		t.Fatal("expected transport error from closed server")
	}
}

func TestAuditEvents_Create_RequiredFields(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
	})
	defer cleanup()
	for _, in := range []CreateEventInput{
		{ResourceType: "user", ResourceID: "u-1"},
		{Action: "x", ResourceID: "u-1"},
		{Action: "x", ResourceType: "user"},
	} {
		if err := events.Record(in); err == nil {
			t.Fatalf("expected error for missing fields: %+v", in)
		}
	}
}

func TestAuditEvents_Create_AllOptionalFields(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
	})
	defer cleanup()
	occurred := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	if err := events.Record(CreateEventInput{
		Action:         "invoice.created",
		ResourceType:   "invoice",
		ResourceID:     "inv-1",
		OccurredAt:     &occurred,
		Data:           map[string]interface{}{"snapshot": map[string]interface{}{"total_cents": 4900}, "req_id": "abc"},
		IdempotencyKey: "k-1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	events.Flush(2 * time.Second)
}

func TestAuditEvents_List_Error(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", 500)
	})
	defer cleanup()
	if _, err := events.List(context.Background(), ListEventsInput{}); err == nil {
		t.Fatal("expected error from 500 response")
	}
}

func TestAuditEvents_List_BadActorID(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	defer cleanup()
	if _, err := events.List(context.Background(), ListEventsInput{ActorID: "not-a-uuid"}); err == nil {
		t.Fatal("expected error from invalid UUID")
	}
}

func TestAuditEvents_List_AllFilters(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_size":1}}`))
	})
	defer cleanup()
	_, err := events.List(context.Background(), ListEventsInput{
		Action:          "user.created",
		ResourceType:    "user",
		ResourceID:      "u-1",
		ActorType:       "USER",
		ActorID:         "11111111-2222-3333-4444-555555555555",
		OccurredAtRange: "[2026-04-01T00:00:00Z,*)",
		Search:          "inv-",
		PageSize:        1,
		PageAfter:       "abc",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
}

func TestAuditEvents_Get_500(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", 500)
	})
	defer cleanup()
	if _, err := events.Get(context.Background(), uuid.New()); err == nil {
		t.Fatal("expected error from 500 response")
	}
}

// Exercise eventFromResource's nullable-attr branches.
func TestEventFromResource_PopulatedActor(t *testing.T) {
	actorID := uuid.New()
	at := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	actorType := "USER"
	actorLabel := "mike@example.com"
	idemKey := "auto-1"
	res := genaudit.EventResource{
		Id: "11111111-2222-3333-4444-555555555555",
		Attributes: genaudit.Event{
			Action:         "user.created",
			ResourceType:   "user",
			ResourceId:     "u-1",
			OccurredAt:     &at,
			CreatedAt:      &at,
			ActorType:      &actorType,
			ActorId:        &actorID,
			ActorLabel:     &actorLabel,
			Data:           &map[string]interface{}{"snapshot": map[string]interface{}{"email": "m@example.com"}, "req_id": "abc"},
			IdempotencyKey: &idemKey,
		},
	}
	got := eventFromResource(res)
	if got.ActorID == nil || *got.ActorID != actorID {
		t.Fatalf("ActorID not populated: %v", got.ActorID)
	}
	if got.ActorType != "USER" || got.ActorLabel != actorLabel {
		t.Fatal("actor fields not propagated")
	}
	if got.Data == nil {
		t.Fatal("data not propagated")
	}
	if _, ok := got.Data["snapshot"]; !ok {
		t.Fatal("data.snapshot not propagated")
	}
}

func TestEventFromResource_PopulatesDoNotForward(t *testing.T) {
	dnf := true
	res := genaudit.EventResource{
		Id: "11111111-2222-3333-4444-555555555555",
		Attributes: genaudit.Event{
			Action:       "user.created",
			ResourceType: "user",
			ResourceId:   "u-1",
			DoNotForward: &dnf,
		},
	}
	got := eventFromResource(res)
	if !got.DoNotForward {
		t.Fatal("expected DoNotForward=true to propagate")
	}
}

func TestAuditEventBuffer_EnqueueAfterCloseIsNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
	}))
	defer srv.Close()
	gen, _ := genaudit.NewClient(srv.URL)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	buf := newAuditEventBuffer(wrapped)
	buf.close(2 * time.Second)
	// Subsequent enqueue is silently ignored; should not panic.
	body := genaudit.EventResponse{
		Data: genaudit.EventResource{Attributes: genaudit.Event{Action: "x", ResourceType: "x", ResourceId: "1"}},
	}
	buf.enqueue(body, "")
}

func TestAuditEventBuffer_OverflowEvictsOldest(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(201)
	}))
	defer srv.Close()
	gen, _ := genaudit.NewClient(srv.URL)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	buf := newAuditEventBuffer(wrapped)
	defer buf.close(2 * time.Second)

	buf.maxSize = 3
	buf.watermark = 999 // suppress auto-drain
	buf.flushEvery = 60 * time.Second
	for i := 0; i < 10; i++ {
		body := genaudit.EventResponse{
			Data: genaudit.EventResource{Attributes: genaudit.Event{Action: "x", ResourceType: "x", ResourceId: "1"}},
		}
		buf.enqueue(body, "")
	}
	buf.flush(2 * time.Second)
	// Coverage goal: enqueue's overflow-eviction branch fired.
	if posts.Load() == 0 {
		t.Fatal("expected at least one POST after overflow")
	}
}

func TestAuditEventBuffer_FlushTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	gen, _ := genaudit.NewClient(srv.URL)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	buf := newAuditEventBuffer(wrapped)
	defer buf.close(2 * time.Second)

	// Watermark > 1 keeps the worker idle while we add a single item.
	buf.watermark = 999
	buf.flushEvery = 60 * time.Second

	body := genaudit.EventResponse{
		Data: genaudit.EventResource{Attributes: genaudit.Event{Action: "x", ResourceType: "x", ResourceId: "1"}},
	}
	buf.enqueue(body, "")
	start := time.Now()
	buf.flush(50 * time.Millisecond)
	if time.Since(start) < 40*time.Millisecond {
		t.Fatalf("flush returned faster than its timeout: %s", time.Since(start))
	}
}

func TestAuditEventBuffer_GivesUpAfterMaxAttempts(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	gen, _ := genaudit.NewClient(srv.URL)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	buf := newAuditEventBuffer(wrapped)
	defer buf.close(2 * time.Second)

	buf.maxAttempts = 3
	buf.initialBack = 50 * time.Millisecond
	buf.flushEvery = 25 * time.Millisecond

	body := genaudit.EventResponse{
		Data: genaudit.EventResource{Attributes: genaudit.Event{Action: "x", ResourceType: "x", ResourceId: "1"}},
	}
	buf.enqueue(body, "")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= int32(buf.maxAttempts) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if attempts.Load() < int32(buf.maxAttempts) {
		t.Fatalf("expected gave-up branch to fire; attempts=%d", attempts.Load())
	}
}

func TestAuditEvents_Get_404Handler(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	})
	defer cleanup()
	if _, err := events.Get(context.Background(), uuid.New()); err == nil {
		t.Fatal("expected error from 404 response")
	}
}

func TestAuditEventBuffer_WatermarkTriggersDrain(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(201)
	}))
	defer srv.Close()
	gen, _ := genaudit.NewClient(srv.URL)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	buf := newAuditEventBuffer(wrapped)
	defer buf.close(2 * time.Second)
	// Set watermark very low so each enqueue triggers signalWake.
	buf.watermark = 1
	buf.flushEvery = 60 * time.Second
	body := genaudit.EventResponse{
		Data: genaudit.EventResource{Attributes: genaudit.Event{Action: "x", ResourceType: "x", ResourceId: "1"}},
	}
	for i := 0; i < 5; i++ {
		buf.enqueue(body, "")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if posts.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if posts.Load() < 1 {
		t.Fatal("expected watermark-triggered drain to fire at least one POST")
	}
}

func TestAuditEvents_List_LinksWithExtraQuery(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
		  "data":[],
		  "meta":{"page_size":1},
		  "links":{"next":"/api/v1/events?page[size]=1&page[after]=tok-xyz&extra=junk"}
		}`))
	})
	defer cleanup()
	page, err := events.List(context.Background(), ListEventsInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.NextCursor != "tok-xyz" {
		t.Fatalf("expected NextCursor=tok-xyz (trimmed), got %q", page.NextCursor)
	}
}

func TestAuditEvents_Get_EmptyBody(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
	})
	defer cleanup()
	if _, err := events.Get(context.Background(), uuid.New()); err == nil {
		t.Fatal("expected error from empty 200 body")
	}
}

func TestAuditEventBuffer_DropsPermanent4xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(400)
	}))
	defer srv.Close()
	gen, _ := genaudit.NewClient(srv.URL)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	buf := newAuditEventBuffer(wrapped)
	defer buf.close(2 * time.Second)
	buf.watermark = 1

	body := genaudit.EventResponse{
		Data: genaudit.EventResource{Attributes: genaudit.Event{Action: "x", ResourceType: "x", ResourceId: "1"}},
	}
	buf.enqueue(body, "")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	// Give the buffer a moment to NOT retry (permanent failure → dropped).
	time.Sleep(300 * time.Millisecond)
	if attempts.Load() != 1 {
		t.Fatalf("expected exactly 1 attempt for 4xx; got %d", attempts.Load())
	}
}

func TestAuditEventBuffer_BackoffCappedAtMax(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	gen, _ := genaudit.NewClient(srv.URL)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	buf := newAuditEventBuffer(wrapped)
	defer buf.close(2 * time.Second)
	// Push initial backoff well past maxBack so the cap branch fires.
	buf.initialBack = 1 * time.Second
	buf.maxBack = 100 * time.Millisecond
	buf.maxAttempts = 3
	buf.watermark = 1
	buf.flushEvery = 50 * time.Millisecond
	body := genaudit.EventResponse{
		Data: genaudit.EventResource{Attributes: genaudit.Event{Action: "x", ResourceType: "x", ResourceId: "1"}},
	}
	buf.enqueue(body, "")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() < 3 {
		time.Sleep(50 * time.Millisecond)
	}
	if attempts.Load() < 3 {
		t.Fatalf("expected 3 attempts (max), got %d", attempts.Load())
	}
}

func TestAuditEventBuffer_RetriesTransient(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		if attempts.Load() < 2 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()

	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}

	buf := newAuditEventBuffer(wrapped)
	defer buf.close(2 * time.Second)

	body := genaudit.EventResponse{
		Data: genaudit.EventResource{
			Id:         "",
			Attributes: genaudit.Event{Action: "x", ResourceType: "x", ResourceId: "1"},
		},
	}
	buf.enqueue(body, "")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatalf("expected retry to succeed; got attempts=%d", attempts.Load())
	}
}

// Silence imports used in test plumbing.
var _ io.Writer = io.Discard
var _ = errors.New
