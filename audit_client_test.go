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
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"x.created","resource_type":"x","resource_id":"1"}}}`))
	})
	defer cleanup()

	start := time.Now()
	for i := 0; i < 20; i++ {
		if err := events.Record(CreateEventInput{
			EventType:    "user.created",
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

func TestAuditEvents_Record_InlineFlush(t *testing.T) {
	var posts atomic.Int32
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"x.created","resource_type":"x","resource_id":"1"}}}`))
	})
	defer cleanup()

	// Flush:true blocks until the event is durable — no separate Flush call.
	if err := events.Record(CreateEventInput{
		EventType:    "user.created",
		ResourceType: "user",
		ResourceID:   "u-1",
		Flush:        true,
		FlushTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if posts.Load() < 1 {
		t.Fatal("inline flush should have delivered the event before Record returned")
	}
}

func TestAuditEvents_Record_InlineFlush_DefaultTimeout(t *testing.T) {
	var posts atomic.Int32
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"x.created","resource_type":"x","resource_id":"1"}}}`))
	})
	defer cleanup()

	// FlushTimeout left zero → the 5s default applies (exercises that branch).
	if err := events.Record(CreateEventInput{
		EventType:    "user.created",
		ResourceType: "user",
		ResourceID:   "u-2",
		Flush:        true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if posts.Load() < 1 {
		t.Fatal("inline flush with default timeout should have delivered the event")
	}
}

func TestAuditEvents_Create_RequiresFields(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	defer cleanup()

	err := events.Record(CreateEventInput{ResourceType: "user", ResourceID: "u-1"})
	if err == nil || !strings.Contains(err.Error(), "EventType") {
		t.Fatalf("expected EventType-required error, got %v", err)
	}
}

func TestAuditEvents_Create_PassesIdempotencyKeyHeader(t *testing.T) {
	gotKey := make(chan string, 1)
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey <- r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"x.created","resource_type":"x","resource_id":"1"}}}`))
	})
	defer cleanup()

	if err := events.Record(CreateEventInput{
		EventType:      "user.created",
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
					"event_type":      "user.created",
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
	if ev.EventType != "user.created" {
		t.Fatalf("EventType=%q", ev.EventType)
	}
}

func TestAuditEvents_Get_404(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	})
	defer cleanup()

	_, err := events.Get(context.Background(), uuid.New())
	var nfe *NotFoundError
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.As(err, &nfe) {
		t.Fatalf("expected NotFoundError, got %T: %v", err, err)
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
		    "attributes":{"event_type":"a.b","resource_type":"a","resource_id":"1","occurred_at":"2026-05-06T12:00:00Z","created_at":"2026-05-06T12:00:01Z","actor_type":"API_KEY","actor_id":null,"actor_label":"","snapshot":null,"data":{},"idempotency_key":"k"}
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

// The runtime audit client stamps X-Smplkit-Environment from the SDK's
// configured environment on every call (ADR-055), while the account-wide
// forwarder-CRUD client does not.
func TestClient_RuntimeAudit_InjectsEnvironmentHeader(t *testing.T) {
	gotEnv := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotEnv <- r.Header.Get("X-Smplkit-Environment"):
		default:
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_size":50}}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{
		APIKey:      "sk_api_test",
		Environment: "production",
		Service:     "test",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	if _, err := c.Audit().Events().List(context.Background(), ListEventsInput{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	select {
	case env := <-gotEnv:
		if env != "production" {
			t.Fatalf("expected X-Smplkit-Environment=production on runtime call, got %q", env)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the runtime audit request")
	}
}

// The forwarder-CRUD surface must NOT carry the environment header —
// enablement is per-forwarder via the environments map, and forwarder CRUD
// operates account-wide.
func TestClient_ForwarderAudit_OmitsEnvironmentHeader(t *testing.T) {
	gotEnv := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotEnv <- r.Header.Get("X-Smplkit-Environment"):
		default:
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":1,"size":1000}}}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{
		APIKey:      "sk_api_test",
		Environment: "production",
		Service:     "test",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	if _, err := c.Audit().Forwarders().List(context.Background(), ListForwardersInput{}); err != nil {
		t.Fatalf("Forwarders.List: %v", err)
	}
	select {
	case env := <-gotEnv:
		if env != "" {
			t.Fatalf("expected no X-Smplkit-Environment on forwarder call, got %q", env)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the forwarder audit request")
	}
}

// A caller-supplied X-Smplkit-Environment extra header wins over the
// SDK-configured environment on runtime audit calls (explicit override).
func TestClient_RuntimeAudit_ExtraHeaderEnvironmentWins(t *testing.T) {
	gotEnv := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotEnv <- r.Header.Get("X-Smplkit-Environment"):
		default:
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_size":50}}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{
		APIKey:       "sk_api_test",
		Environment:  "production",
		Service:      "test",
		ExtraHeaders: map[string]string{"X-Smplkit-Environment": "staging"},
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	if _, err := c.Audit().Events().List(context.Background(), ListEventsInput{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	select {
	case env := <-gotEnv:
		if env != "staging" {
			t.Fatalf("expected explicit extra-header env=staging to win, got %q", env)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the runtime audit request")
	}
}

// ---------------------------------------------------------------------------
// NewAuditClient — standalone construction
// ---------------------------------------------------------------------------

// NewAuditClient builds its own gen clients; the runtime surface stamps the
// configured environment header while the forwarder surface does not. This
// exercises every editor closure (env, SDK headers, extra headers) on both
// the runtime and forwarder transports.
func TestNewAuditClient_StandaloneSurface(t *testing.T) {
	type capture struct {
		env       string
		accept    string
		userAgent string
		extra     string
	}
	captured := make(chan capture, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- capture{
			env:       r.Header.Get("X-Smplkit-Environment"),
			accept:    r.Header.Get("Accept"),
			userAgent: r.Header.Get("User-Agent"),
			extra:     r.Header.Get("X-Custom"),
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":1,"size":1000}}}`))
	}))
	defer srv.Close()

	ac, err := NewAuditClient(Config{
		APIKey:           "sk_api_test",
		Environment:      "production",
		Service:          "test",
		DisableTelemetry: true,
		ExtraHeaders:     map[string]string{"X-Custom": "hi"},
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewAuditClient: %v", err)
	}
	defer ac.Close()

	// Accessors are all non-nil.
	if ac.Events() == nil || ac.ResourceTypes() == nil || ac.EventTypes() == nil ||
		ac.Categories() == nil || ac.Forwarders() == nil {
		t.Fatal("expected all AuditClient accessors to be non-nil")
	}

	// Runtime call carries the environment header + SDK + extra headers.
	if _, err := ac.ResourceTypes().List(context.Background(), ListResourceTypesInput{}); err != nil {
		t.Fatalf("ResourceTypes.List: %v", err)
	}
	select {
	case c := <-captured:
		if c.env != "production" {
			t.Errorf("expected runtime env header=production, got %q", c.env)
		}
		if !strings.Contains(c.accept, "application/vnd.api+json") {
			t.Errorf("expected Accept header, got %q", c.accept)
		}
		if c.userAgent != userAgent {
			t.Errorf("expected User-Agent=%q, got %q", userAgent, c.userAgent)
		}
		if c.extra != "hi" {
			t.Errorf("expected X-Custom=hi extra header, got %q", c.extra)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the runtime audit request")
	}

	// Forwarder call carries SDK + extra headers but NOT the environment header.
	if _, err := ac.Forwarders().List(context.Background(), ListForwardersInput{}); err != nil {
		t.Fatalf("Forwarders.List: %v", err)
	}
	select {
	case c := <-captured:
		if c.env != "" {
			t.Errorf("expected no env header on forwarder call, got %q", c.env)
		}
		if c.extra != "hi" {
			t.Errorf("expected X-Custom=hi extra header on forwarder call, got %q", c.extra)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the forwarder audit request")
	}
}

// NewAuditClient surfaces config-resolution errors (here: a missing API key
// with a non-default profile so the local ~/.smplkit default profile can't
// satisfy it).
func TestNewAuditClient_ConfigError(t *testing.T) {
	_, err := NewAuditClient(Config{
		Profile:     "definitely-not-a-real-profile",
		Environment: "production",
		Service:     "test",
	})
	if err == nil {
		t.Fatal("expected config-resolution error from missing API key")
	}
}

// NewAuditClient honors a caller-supplied *http.Client (its transport is
// wrapped with the auth transport rather than replaced), so requests still
// reach the server and authenticate.
func TestNewAuditClient_CustomHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":1,"size":1000}}}`))
	}))
	defer srv.Close()

	ac, err := NewAuditClient(Config{
		APIKey:      "sk_api_test",
		Environment: "production",
		Service:     "test",
	}, withBaseURLOverride(srv.URL), WithHTTPClient(&http.Client{}))
	if err != nil {
		t.Fatalf("NewAuditClient: %v", err)
	}
	defer ac.Close()
	if _, err := ac.Categories().List(context.Background(), ListCategoriesInput{}); err != nil {
		t.Fatalf("Categories.List: %v", err)
	}
}

// Close on a standalone AuditClient drains the buffer and is safe to call.
func TestNewAuditClient_Close(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	ac, err := NewAuditClient(Config{
		APIKey:      "sk_api_test",
		Environment: "production",
		Service:     "test",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewAuditClient: %v", err)
	}
	if err := ac.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Close is a no-op (returns nil) when the events sub-client is absent — the
// nil-guard branch in AuditClient.Close.
func TestAuditClient_Close_NilEvents(t *testing.T) {
	ac := &AuditClient{}
	if err := ac.Close(); err != nil {
		t.Fatalf("Close on empty AuditClient: %v", err)
	}
}

// Event.Environment is read-only and surfaced from the read path.
func TestEventFromResource_PopulatesEnvironment(t *testing.T) {
	env := "production"
	idStr := "11111111-2222-3333-4444-555555555555"
	res := genaudit.EventResource{
		Id: &idStr,
		Attributes: genaudit.Event{
			EventType:    "user.created",
			ResourceType: "user",
			ResourceId:   "u-1",
			Environment:  &env,
		},
	}
	got := eventFromResource(res)
	if got.Environment != "production" {
		t.Fatalf("expected Environment=production, got %q", got.Environment)
	}
}

// eventFromResource surfaces Category from the read path.
func TestEventFromResource_PopulatesCategory(t *testing.T) {
	cat := "billing"
	idStr := "11111111-2222-3333-4444-555555555555"
	res := genaudit.EventResource{
		Id: &idStr,
		Attributes: genaudit.Event{
			EventType:    "invoice.paid",
			ResourceType: "invoice",
			ResourceId:   "inv-1",
			Category:     &cat,
		},
	}
	got := eventFromResource(res)
	if got.Category != "billing" {
		t.Fatalf("expected Category=billing, got %q", got.Category)
	}
}

// eventFromResource leaves the id zero-valued when the wire omits it (nil Id
// branch).
func TestEventFromResource_NilID(t *testing.T) {
	res := genaudit.EventResource{
		Attributes: genaudit.Event{
			EventType:    "user.created",
			ResourceType: "user",
			ResourceId:   "u-1",
		},
	}
	got := eventFromResource(res)
	if got.ID != uuid.Nil {
		t.Fatalf("expected zero ID when wire omits it, got %s", got.ID)
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
		{EventType: "x", ResourceID: "u-1"},
		{EventType: "x", ResourceType: "user"},
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
		EventType:      "invoice.created",
		ResourceType:   "invoice",
		ResourceID:     "inv-1",
		OccurredAt:     &occurred,
		Category:       "billing",
		Data:           map[string]interface{}{"snapshot": map[string]interface{}{"total_cents": 4900}, "req_id": "abc"},
		IdempotencyKey: "k-1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	events.Flush(2 * time.Second)
}

// Record threads the optional Category onto the wire when non-empty.
func TestAuditEvents_Record_PassesCategory(t *testing.T) {
	captured := make(chan string, 1)
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured <- string(b)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"x.created","resource_type":"x","resource_id":"1","category":"billing"}}}`))
	})
	defer cleanup()
	if err := events.Record(CreateEventInput{
		EventType:    "invoice.paid",
		ResourceType: "invoice",
		ResourceID:   "inv-1",
		Category:     "billing",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	events.Flush(2 * time.Second)
	body := <-captured
	if !strings.Contains(body, `"category":"billing"`) {
		t.Errorf("expected category in body, got: %s", body)
	}
}

// Record passes customer-supplied actor attribution straight onto the
// wire — the wrapper does not validate or backfill the fields.
func TestAuditEvents_Record_ForwardsActorFields(t *testing.T) {
	type captured struct {
		ActorType, ActorID, ActorLabel string
	}
	gotBody := make(chan captured, 1)
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Data struct {
				Attributes struct {
					ActorType  *string `json:"actor_type"`
					ActorId    *string `json:"actor_id"`
					ActorLabel *string `json:"actor_label"`
				} `json:"attributes"`
			} `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c := captured{}
		if body.Data.Attributes.ActorType != nil {
			c.ActorType = *body.Data.Attributes.ActorType
		}
		if body.Data.Attributes.ActorId != nil {
			c.ActorID = *body.Data.Attributes.ActorId
		}
		if body.Data.Attributes.ActorLabel != nil {
			c.ActorLabel = *body.Data.Attributes.ActorLabel
		}
		gotBody <- c
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"x.created","resource_type":"x","resource_id":"1"}}}`))
	})
	defer cleanup()
	if err := events.Record(CreateEventInput{
		EventType:    "user.created",
		ResourceType: "user",
		ResourceID:   "u-1",
		ActorType:    "EXTERNAL_SERVICE",
		ActorID:      "not-a-uuid:billing-bot",
		ActorLabel:   "Billing Bot",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	events.Flush(2 * time.Second)
	select {
	case c := <-gotBody:
		if c.ActorType != "EXTERNAL_SERVICE" || c.ActorID != "not-a-uuid:billing-bot" || c.ActorLabel != "Billing Bot" {
			t.Fatalf("unexpected actor fields on wire: %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received a POST")
	}
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

// ActorID is a free-form string on the wire — non-UUID filter values
// pass straight through to the server.
func TestAuditEvents_List_FreeFormActorID(t *testing.T) {
	var capturedQuery string
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_size":50}}`))
	})
	defer cleanup()
	if _, err := events.List(context.Background(), ListEventsInput{ActorID: "not-a-uuid:bot"}); err != nil {
		t.Fatalf("expected non-UUID actor id to pass through, got %v", err)
	}
	if !strings.Contains(capturedQuery, "not-a-uuid%3Abot") {
		t.Fatalf("expected raw query to forward actor id verbatim, got %q", capturedQuery)
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
		EventType:       "user.created",
		ResourceType:    "user",
		ResourceID:      "u-1",
		ActorType:       "USER",
		ActorID:         "not-a-uuid:billing-bot",
		OccurredAtRange: "[2026-04-01T00:00:00Z,*)",
		Search:          "inv-",
		PageSize:        1,
		PageAfter:       "abc",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
}

// captureEventsListQuery runs events.List with the given input against a
// stub server and returns the decoded filter[environment] query value and
// whether the parameter was present at all.
func captureEventsListQuery(t *testing.T, input ListEventsInput) (value string, present bool) {
	t.Helper()
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, r *http.Request) {
		value = r.URL.Query().Get("filter[environment]")
		_, present = r.URL.Query()["filter[environment]"]
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_size":50}}`))
	})
	defer cleanup()
	if _, err := events.List(context.Background(), input); err != nil {
		t.Fatalf("List: %v", err)
	}
	return value, present
}

func TestAuditEvents_List_EnvironmentsOmittedByDefault(t *testing.T) {
	// nil Environments
	if _, present := captureEventsListQuery(t, ListEventsInput{}); present {
		t.Error("expected filter[environment] absent when Environments is nil")
	}
	// empty (non-nil) slice
	if _, present := captureEventsListQuery(t, ListEventsInput{Environments: []string{}}); present {
		t.Error("expected filter[environment] absent when Environments is empty")
	}
	// slice of only blank entries collapses to nothing → omitted
	if _, present := captureEventsListQuery(t, ListEventsInput{Environments: []string{"", "   "}}); present {
		t.Error("expected filter[environment] absent when Environments has only blanks")
	}
}

func TestAuditEvents_List_EnvironmentsSingleValue(t *testing.T) {
	value, present := captureEventsListQuery(t, ListEventsInput{Environments: []string{"production"}})
	if !present {
		t.Fatal("expected filter[environment] to be present")
	}
	if value != "production" {
		t.Errorf("expected filter[environment]=production, got %q", value)
	}
}

func TestAuditEvents_List_EnvironmentsMultipleCommaJoin(t *testing.T) {
	value, present := captureEventsListQuery(t, ListEventsInput{Environments: []string{"production", "staging"}})
	if !present {
		t.Fatal("expected filter[environment] to be present")
	}
	if value != "production,staging" {
		t.Errorf("expected filter[environment]=production,staging, got %q", value)
	}
}

func TestAuditEvents_List_EnvironmentsSmplkitBucketAccepted(t *testing.T) {
	// The reserved "smplkit" control-plane bucket is just another value on
	// the wire — possibly combined with real environment keys.
	value, present := captureEventsListQuery(t, ListEventsInput{Environments: []string{"smplkit", "production"}})
	if !present {
		t.Fatal("expected filter[environment] to be present")
	}
	if value != "smplkit,production" {
		t.Errorf("expected filter[environment]=smplkit,production, got %q", value)
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
	actorID := "not-a-uuid:billing-bot"
	at := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	actorType := "EXTERNAL_SERVICE"
	actorLabel := "Billing Bot"
	idemKey := "auto-1"
	idStr := "11111111-2222-3333-4444-555555555555"
	res := genaudit.EventResource{
		Id: &idStr,
		Attributes: genaudit.Event{
			EventType:      "user.created",
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
	if got.ActorID != actorID {
		t.Fatalf("ActorID not populated: %q", got.ActorID)
	}
	if got.ActorType != "EXTERNAL_SERVICE" || got.ActorLabel != actorLabel {
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
	idStr2 := "11111111-2222-3333-4444-555555555555"
	res := genaudit.EventResource{
		Id: &idStr2,
		Attributes: genaudit.Event{
			EventType:    "user.created",
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
	body := genaudit.EventRequest{
		Data: genaudit.EventResource{Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"}},
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
	buf := newAuditEventBufferStopped(wrapped)
	buf.maxSize = 3
	buf.watermark = 999 // suppress auto-drain
	buf.flushEvery = 60 * time.Second
	go buf.run()
	defer buf.close(2 * time.Second)
	for i := 0; i < 10; i++ {
		body := genaudit.EventRequest{
			Data: genaudit.EventResource{Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"}},
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
	buf := newAuditEventBufferStopped(wrapped)
	// Watermark > 1 keeps the worker idle while we add a single item.
	buf.watermark = 999
	buf.flushEvery = 60 * time.Second
	go buf.run()
	defer buf.close(2 * time.Second)

	body := genaudit.EventRequest{
		Data: genaudit.EventResource{Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"}},
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
	buf := newAuditEventBufferStopped(wrapped)
	buf.maxAttempts = 3
	buf.initialBack = 50 * time.Millisecond
	buf.flushEvery = 25 * time.Millisecond
	go buf.run()
	defer buf.close(2 * time.Second)

	body := genaudit.EventRequest{
		Data: genaudit.EventResource{Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"}},
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
	buf := newAuditEventBufferStopped(wrapped)
	// Set watermark very low so each enqueue triggers signalWake.
	buf.watermark = 1
	buf.flushEvery = 60 * time.Second
	go buf.run()
	defer buf.close(2 * time.Second)
	body := genaudit.EventRequest{
		Data: genaudit.EventResource{Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"}},
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

// List with nil links leaves NextCursor empty (the body.Links == nil branch).
func TestAuditEvents_List_NilLinks(t *testing.T) {
	events, cleanup := newTestAuditEvents(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_size":50}}`))
	})
	defer cleanup()
	page, err := events.List(context.Background(), ListEventsInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.NextCursor != "" {
		t.Fatalf("expected empty NextCursor with no links, got %q", page.NextCursor)
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
	buf := newAuditEventBufferStopped(wrapped)
	buf.watermark = 1
	go buf.run()
	defer buf.close(2 * time.Second)

	body := genaudit.EventRequest{
		Data: genaudit.EventResource{Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"}},
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
	buf := newAuditEventBufferStopped(wrapped)
	// Push initial backoff well past maxBack so the cap branch fires.
	buf.initialBack = 1 * time.Second
	buf.maxBack = 100 * time.Millisecond
	buf.maxAttempts = 3
	buf.watermark = 1
	buf.flushEvery = 50 * time.Millisecond
	go buf.run()
	defer buf.close(2 * time.Second)
	body := genaudit.EventRequest{
		Data: genaudit.EventResource{Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"}},
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

	// Start the worker via the stopped-then-start path with a tiny retry
	// backoff so the second attempt fires promptly and deterministically even
	// under heavy -race load (avoiding a missed-deadline flake and a leaked
	// worker that would otherwise retry a closed server after teardown).
	buf := newAuditEventBufferStopped(wrapped)
	buf.initialBack = 5 * time.Millisecond
	go buf.run()
	defer buf.close(2 * time.Second)

	body := genaudit.EventRequest{
		Data: genaudit.EventResource{
			Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"},
		},
	}
	buf.enqueue(body, "")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatalf("expected retry to succeed; got attempts=%d", attempts.Load())
	}
}

// handleOutcome treats a nil *resp (transport error → status 0) as a
// retryable outcome: drainOnce passes status=0 alongside the error, and the
// item is requeued rather than dropped. Covers the "err != nil, status 0"
// path through handleOutcome and the requeue branch in drainOnce.
func TestAuditEventBuffer_TransportErrorRequeues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // every POST now fails at the transport layer.
	gen, _ := genaudit.NewClient(url)
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	buf := newAuditEventBufferStopped(wrapped)
	buf.maxAttempts = 2
	buf.initialBack = 20 * time.Millisecond
	buf.watermark = 1
	buf.flushEvery = 20 * time.Millisecond
	go buf.run()
	defer buf.close(2 * time.Second)

	body := genaudit.EventRequest{
		Data: genaudit.EventResource{Attributes: genaudit.Event{EventType: "x", ResourceType: "x", ResourceId: "1"}},
	}
	buf.enqueue(body, "")
	// Give the worker time to exhaust both attempts and drop the item.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		buf.mu.Lock()
		empty := len(buf.queue) == 0 && buf.inFlight == 0
		buf.mu.Unlock()
		if empty {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	buf.mu.Lock()
	remaining := len(buf.queue)
	buf.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected the item to be dropped after max attempts, %d remain", remaining)
	}
}

// Silence imports used in test plumbing.
var _ io.Writer = io.Discard
var _ = errors.New
