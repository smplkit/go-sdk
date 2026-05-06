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
		if err := events.Create(CreateEventInput{
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

	err := events.Create(CreateEventInput{ResourceType: "user", ResourceID: "u-1"})
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

	if err := events.Create(CreateEventInput{
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

// uuidPtr is exercised here so the symbol isn't dead code.
var _ = uuidPtr

// Silence imports used in test plumbing.
var _ io.Writer = io.Discard
var _ = errors.New
