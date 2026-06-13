package smplkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

const fwdIDStr = "showcase-forwarder"

// newTestAuditForwarders wires an AuditForwarders wrapper against an httptest server.
func newTestAuditForwarders(t *testing.T, handler http.HandlerFunc) (*AuditForwarders, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	fwds := &AuditForwarders{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	cleanup := func() { srv.Close() }
	return fwds, cleanup
}

// newClosedAuditForwarders returns an AuditForwarders whose backing server has
// been closed, exercising transport-error branches.
func newClosedAuditForwarders(t *testing.T) *AuditForwarders {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close()
	gen, _ := genaudit.NewClient(url)
	return &AuditForwarders{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
}

// writeForwarderResource serves a single-forwarder JSON:API response.
// The second positional argument used to be the (now-removed) slug —
// kept to minimize call-site churn but unused beyond the name field.
func writeForwarderResource(w http.ResponseWriter, status int, name, _ string) {
	w.Header().Set("Content-Type", "application/vnd.api+json")
	w.WriteHeader(status)
	body := map[string]any{
		"data": map[string]any{
			"id":   fwdIDStr,
			"type": "forwarder",
			"attributes": map[string]any{
				"name":           name,
				"forwarder_type": "datadog",
				"enabled":        true,
				"configuration": map[string]any{
					"method": "POST",
					"url":    "https://siem.example.com/in",
					"headers": []map[string]string{
						{"name": "DD-API-KEY", "value": "<redacted>"},
					},
					"success_status": "2xx",
				},
				"created_at": "2026-05-07T12:00:00+00:00",
				"updated_at": "2026-05-07T12:00:00+00:00",
				"version":    1,
			},
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// ---------------------------------------------------------------------------
// client.Audit().Forwarders() accessor (one unified audit surface)
// ---------------------------------------------------------------------------

func TestClient_AuditForwardersAccessor(t *testing.T) {
	c, err := NewClient(Config{APIKey: "sk_api_test", Environment: "dev", Service: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if c.Audit().Forwarders() == nil {
		t.Fatal("Audit().Forwarders() returned nil")
	}
}

// ---------------------------------------------------------------------------
// AuditClient.ResourceTypes(), EventTypes(), and Categories() accessors
// ---------------------------------------------------------------------------

func TestAuditClient_DiscoveryAccessors(t *testing.T) {
	c, err := NewClient(Config{APIKey: "sk_api_test", Environment: "dev", Service: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if c.Audit().ResourceTypes() == nil {
		t.Fatal("ResourceTypes() returned nil")
	}
	if c.Audit().EventTypes() == nil {
		t.Fatal("EventTypes() returned nil")
	}
	if c.Audit().Categories() == nil {
		t.Fatal("Categories() returned nil")
	}
}

// ---------------------------------------------------------------------------
// Forwarder CRUD
// ---------------------------------------------------------------------------

func TestAuditForwarders_List_PaginatesWithOffset(t *testing.T) {
	calls := 0
	var capturedQueries []string
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		capturedQueries = append(capturedQueries, r.URL.RawQuery)
		calls++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":[{"id":"` + fwdIDStr + `","type":"forwarder","attributes":{"name":"A","forwarder_type":"HTTP","enabled":true,"configuration":{"url":"https://x"}}}],"meta":{"pagination":{"page":1,"size":1,"total":2,"total_pages":2}}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":[{"id":"` + fwdIDStr + `","type":"forwarder","attributes":{"name":"B","forwarder_type":"HTTP","enabled":true,"configuration":{"url":"https://y"}}}],"meta":{"pagination":{"page":2,"size":1}}}`))
		}
	})
	defer cleanup()

	first, err := fwds.List(context.Background(), ListForwardersInput{
		ForwarderType: ForwarderTypeDatadog,
		PageNumber:    1, PageSize: 1, MetaTotal: true,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if first.Pagination.Page != 1 || first.Pagination.Size != 1 {
		t.Errorf("expected page=1 size=1, got %+v", first.Pagination)
	}
	if first.Pagination.Total == nil || *first.Pagination.Total != 2 {
		t.Errorf("expected total=2, got %+v", first.Pagination.Total)
	}
	if first.Pagination.TotalPages == nil || *first.Pagination.TotalPages != 2 {
		t.Errorf("expected total_pages=2, got %+v", first.Pagination.TotalPages)
	}
	if !strings.Contains(capturedQueries[0], "page%5Bnumber%5D=1") ||
		!strings.Contains(capturedQueries[0], "page%5Bsize%5D=1") ||
		!strings.Contains(capturedQueries[0], "meta%5Btotal%5D=true") {
		t.Errorf("expected page[number]/page[size]/meta[total] in first query, got %q", capturedQueries[0])
	}

	second, err := fwds.List(context.Background(), ListForwardersInput{PageNumber: 2, PageSize: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if second.Pagination.Page != 2 || second.Pagination.Size != 1 {
		t.Errorf("expected page=2 size=1, got %+v", second.Pagination)
	}
	if second.Pagination.Total != nil || second.Pagination.TotalPages != nil {
		t.Errorf("expected nil total/total_pages without MetaTotal, got %+v / %+v",
			second.Pagination.Total, second.Pagination.TotalPages)
	}
}

func TestAuditForwarders_Get_404Handled(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()
	_, err := fwds.Get(context.Background(), fwdIDStr)
	var nfe *NotFoundError
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.As(err, &nfe) {
		t.Fatalf("expected NotFoundError, got %T: %v", err, err)
	}
}

func TestAuditForwarders_Get_Success(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		writeForwarderResource(w, http.StatusOK, "x", "x")
	})
	defer cleanup()
	fwd, err := fwds.Get(context.Background(), fwdIDStr)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fwd.Name != "x" {
		t.Errorf("expected name=x, got %q", fwd.Name)
	}
	if len(fwd.Configuration.Headers) != 1 || fwd.Configuration.Headers[0].Value != "<redacted>" {
		t.Errorf("expected redacted header, got %+v", fwd.Configuration.Headers)
	}
}

func TestAuditForwarders_Delete(t *testing.T) {
	var method string
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()
	if err := fwds.Delete(context.Background(), fwdIDStr); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if method != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", method)
	}
}

func TestAuditForwarders_Delete_NonSuccess(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()
	if err := fwds.Delete(context.Background(), fwdIDStr); err == nil {
		t.Fatal("expected error on 404 delete")
	}
}

func TestAuditForwarders_List_NonSuccess(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := fwds.List(context.Background(), ListForwardersInput{}); err == nil {
		t.Fatal("expected error on 500")
	}
}

// ---------------------------------------------------------------------------
// Transport-error paths
// ---------------------------------------------------------------------------

func TestAuditForwarders_List_TransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	if _, err := fwds.List(context.Background(), ListForwardersInput{}); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.List") {
		t.Fatalf("expected wrapped List transport error, got %v", err)
	}
}

func TestAuditForwarders_Get_TransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	if _, err := fwds.Get(context.Background(), fwdIDStr); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.Get") {
		t.Fatalf("expected wrapped Get transport error, got %v", err)
	}
}

func TestAuditForwarders_Delete_TransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	if err := fwds.Delete(context.Background(), fwdIDStr); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.Delete") {
		t.Fatalf("expected wrapped Delete transport error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Other error / branch paths
// ---------------------------------------------------------------------------

func TestAuditForwarders_Get_NonSuccessNon404(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := fwds.Get(context.Background(), fwdIDStr); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Fatalf("expected wrapped 500 error, got %v", err)
	}
}

func TestForwarderFromResource_PopulatesOptionalFields(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"` + fwdIDStr + `","type":"forwarder","attributes":{` +
			`"name":"X","description":"a forwarder","forwarder_type":"HTTP","enabled":true,` +
			`"configuration":{"url":"https://x","method":"POST","success_status":"2xx",` +
			`"headers":[{"name":"H","value":"v"}]},` +
			`"filter":{"==":["a","a"]},"transform":"$","transform_type":"JSONATA"` +
			`}}}`))
	})
	defer cleanup()
	fwd, err := fwds.Get(context.Background(), fwdIDStr)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fwd.Filter == nil {
		t.Errorf("expected Filter populated, got nil")
	}
	if s, ok := fwd.Transform.(string); !ok || s != "$" {
		t.Errorf("expected Transform=\"$\", got %v", fwd.Transform)
	}
	if fwd.Description == nil || *fwd.Description != "a forwarder" {
		t.Errorf("expected Description=\"a forwarder\", got %v", fwd.Description)
	}
	if len(fwd.Configuration.Headers) != 1 || fwd.Configuration.Headers[0].Name != "H" {
		t.Errorf("expected HTTP.Headers round-tripped, got %v", fwd.Configuration.Headers)
	}
}

// extractNextCursor branch coverage (& trim, no page[after]) is exercised
// via the events tests in audit_client_test.go — events stays cursor-paged.

// ---------------------------------------------------------------------------
// do_not_forward (exercises AuditEvents on the runtime client)
// ---------------------------------------------------------------------------

// newTestAuditClient wires a full AuditClient (all sub-clients share one
// backing test server), mirroring the real assembly in newAuditClient.
func newTestAuditClient(t *testing.T, handler http.HandlerFunc) (*AuditClient, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	c := newAuditClient(wrapped, "")
	cleanup := func() {
		_ = c.Close()
		srv.Close()
	}
	return c, cleanup
}

func TestAuditEvents_Record_PassesDoNotForward(t *testing.T) {
	captured := make(chan string, 1)
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured <- string(b)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"x.created","resource_type":"x","resource_id":"1","do_not_forward":true}}}`))
	})
	defer cleanup()

	if err := c.Events().Record(CreateEventInput{
		EventType:    "user.created",
		ResourceType: "user",
		ResourceID:   "u-1",
		DoNotForward: true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	c.Events().Flush(2 * time.Second)

	body := <-captured
	if !strings.Contains(body, `"do_not_forward":true`) {
		t.Errorf("expected do_not_forward in body, got: %s", body)
	}
}

// newAuditClient wires every sub-client off a single gen client and threads the
// configured environment onto the body-driven surfaces (Events + discovery) but
// not onto account-wide forwarder CRUD (ADR-055); assert that wiring.
func TestNewAuditClient_WiresSubClients(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	g, _ := genaudit.NewClient(srv.URL)
	gen := &genaudit.ClientWithResponses{ClientInterface: g}
	c := newAuditClient(gen, "production")
	defer c.Close()

	if c.Events() == nil || c.ResourceTypes() == nil || c.EventTypes() == nil ||
		c.Categories() == nil || c.Forwarders() == nil {
		t.Fatal("expected all sub-clients wired")
	}
	// One transport backs the whole surface — every sub-client shares it.
	if c.gen != gen {
		t.Error("expected AuditClient.gen to be the shared gen client")
	}
	if c.Forwarders().gen != gen || c.Events().gen != gen ||
		c.ResourceTypes().gen != gen || c.EventTypes().gen != gen ||
		c.Categories().gen != gen {
		t.Error("expected every sub-client to use the shared gen client")
	}
	// The configured environment is threaded onto the body-driven surfaces
	// (Events + discovery), the default for filter[environment] and the
	// recording body. Forwarder CRUD is account-wide and carries no environment.
	if c.Events().environment != "production" || c.ResourceTypes().environment != "production" ||
		c.EventTypes().environment != "production" || c.Categories().environment != "production" {
		t.Error("expected configured environment threaded onto Events + discovery sub-clients")
	}
}

// ---------------------------------------------------------------------------
// ResourceTypes, EventTypes, and Categories
// ---------------------------------------------------------------------------

func newTestAuditResourceTypes(t *testing.T, handler http.HandlerFunc) (*AuditResourceTypes, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	rt := &AuditResourceTypes{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	return rt, func() { srv.Close() }
}

func newTestAuditEventTypes(t *testing.T, handler http.HandlerFunc) (*AuditEventTypes, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	ac := &AuditEventTypes{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	return ac, func() { srv.Close() }
}

// newTestAuditCategories wires an AuditCategories wrapper against an httptest
// server, mirroring the resource-type / event-type helpers.
func newTestAuditCategories(t *testing.T, handler http.HandlerFunc) (*AuditCategories, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	cat := &AuditCategories{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	return cat, func() { srv.Close() }
}

func TestAuditResourceTypes_List_ReturnsSlug(t *testing.T) {
	rt, cleanup := newTestAuditResourceTypes(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"invoice","type":"resource_type","attributes":{"resource_type":"invoice","created_at":"2026-05-01T00:00:00Z"}},{"id":"user","type":"resource_type","attributes":{"resource_type":"user","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	page, err := rt.List(context.Background(), ListResourceTypesInput{})
	if err != nil {
		t.Fatalf("ResourceTypes.List: %v", err)
	}
	if len(page.ResourceTypes) != 2 {
		t.Fatalf("expected 2 resource types, got %d", len(page.ResourceTypes))
	}
	ids := make(map[string]bool)
	for _, r := range page.ResourceTypes {
		ids[r.ID] = true
	}
	if !ids["invoice"] {
		t.Error("expected invoice in resource types")
	}
	if page.Pagination.Page != 1 || page.Pagination.Size != 1000 {
		t.Errorf("expected pagination page=1 size=1000, got %+v", page.Pagination)
	}
}

func TestAuditResourceTypes_List_ParsesPaginationWithTotals(t *testing.T) {
	rt, cleanup := newTestAuditResourceTypes(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"invoice","type":"resource_type","attributes":{"resource_type":"invoice","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":2,"size":1,"total":3,"total_pages":3}}}`))
	})
	defer cleanup()

	page, err := rt.List(context.Background(), ListResourceTypesInput{PageNumber: 2, PageSize: 1, MetaTotal: true})
	if err != nil {
		t.Fatalf("ResourceTypes.List: %v", err)
	}
	if page.Pagination.Page != 2 || page.Pagination.Size != 1 {
		t.Errorf("expected page=2 size=1, got %+v", page.Pagination)
	}
	if page.Pagination.Total == nil || *page.Pagination.Total != 3 {
		t.Errorf("expected total=3, got %+v", page.Pagination.Total)
	}
	if page.Pagination.TotalPages == nil || *page.Pagination.TotalPages != 3 {
		t.Errorf("expected total_pages=3, got %+v", page.Pagination.TotalPages)
	}
}

func TestAuditResourceTypes_List_Error(t *testing.T) {
	rt, cleanup := newTestAuditResourceTypes(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := rt.List(context.Background(), ListResourceTypesInput{}); err == nil {
		t.Fatal("expected error from 500")
	}
}

func TestAuditResourceTypes_List_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()
	gen, _ := genaudit.NewClient(url)
	rt := &AuditResourceTypes{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	if _, err := rt.List(context.Background(), ListResourceTypesInput{}); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestAuditResourceTypes_List_Environments(t *testing.T) {
	var captured string
	var present bool
	rt, cleanup := newTestAuditResourceTypes(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query().Get("filter[environment]")
		_, present = r.URL.Query()["filter[environment]"]
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	// Omitted by default.
	if _, err := rt.List(context.Background(), ListResourceTypesInput{}); err != nil {
		t.Fatalf("ResourceTypes.List: %v", err)
	}
	if present {
		t.Error("expected filter[environment] absent when Environments is nil")
	}

	// Single value.
	if _, err := rt.List(context.Background(), ListResourceTypesInput{Environments: []string{"production"}}); err != nil {
		t.Fatalf("ResourceTypes.List: %v", err)
	}
	if !present || captured != "production" {
		t.Errorf("expected filter[environment]=production, got %q (present=%v)", captured, present)
	}

	// Multiple values comma-join, including the reserved smplkit bucket.
	if _, err := rt.List(context.Background(), ListResourceTypesInput{Environments: []string{"smplkit", "staging"}}); err != nil {
		t.Fatalf("ResourceTypes.List: %v", err)
	}
	if !present || captured != "smplkit,staging" {
		t.Errorf("expected filter[environment]=smplkit,staging, got %q (present=%v)", captured, present)
	}
}

func TestAuditEventTypes_List_ReturnsSlugs(t *testing.T) {
	ac, cleanup := newTestAuditEventTypes(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"invoice.created","type":"event_type","attributes":{"event_type":"invoice.created","created_at":"2026-05-01T00:00:00Z"}},{"id":"user.updated","type":"event_type","attributes":{"event_type":"user.updated","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	page, err := ac.List(context.Background(), ListEventTypesInput{})
	if err != nil {
		t.Fatalf("EventTypes.List: %v", err)
	}
	if len(page.EventTypes) != 2 {
		t.Fatalf("expected 2 event types, got %d", len(page.EventTypes))
	}
	ids := make(map[string]bool)
	for _, a := range page.EventTypes {
		ids[a.ID] = true
	}
	if !ids["invoice.created"] {
		t.Error("expected invoice.created in event types")
	}
	if page.Pagination.Page != 1 || page.Pagination.Size != 1000 {
		t.Errorf("expected pagination page=1 size=1000, got %+v", page.Pagination)
	}
}

func TestAuditEventTypes_List_FilterResourceType(t *testing.T) {
	var capturedQuery string
	ac, cleanup := newTestAuditEventTypes(t, func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"invoice.created","type":"event_type","attributes":{"event_type":"invoice.created","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	if _, err := ac.List(context.Background(), ListEventTypesInput{FilterResourceType: "invoice"}); err != nil {
		t.Fatalf("EventTypes.List: %v", err)
	}
	if !strings.Contains(capturedQuery, "invoice") {
		t.Errorf("expected filter[resource_type] in query, got %q", capturedQuery)
	}
}

func TestAuditEventTypes_List_Environments(t *testing.T) {
	var captured string
	var present bool
	ac, cleanup := newTestAuditEventTypes(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query().Get("filter[environment]")
		_, present = r.URL.Query()["filter[environment]"]
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	// Omitted by default.
	if _, err := ac.List(context.Background(), ListEventTypesInput{}); err != nil {
		t.Fatalf("EventTypes.List: %v", err)
	}
	if present {
		t.Error("expected filter[environment] absent when Environments is nil")
	}

	// Single value.
	if _, err := ac.List(context.Background(), ListEventTypesInput{Environments: []string{"production"}}); err != nil {
		t.Fatalf("EventTypes.List: %v", err)
	}
	if !present || captured != "production" {
		t.Errorf("expected filter[environment]=production, got %q (present=%v)", captured, present)
	}

	// Multiple values comma-join.
	if _, err := ac.List(context.Background(), ListEventTypesInput{Environments: []string{"production", "staging"}}); err != nil {
		t.Fatalf("EventTypes.List: %v", err)
	}
	if !present || captured != "production,staging" {
		t.Errorf("expected filter[environment]=production,staging, got %q (present=%v)", captured, present)
	}

	// Reserved smplkit bucket accepted as a standalone value.
	if _, err := ac.List(context.Background(), ListEventTypesInput{Environments: []string{"smplkit"}}); err != nil {
		t.Fatalf("EventTypes.List: %v", err)
	}
	if !present || captured != "smplkit" {
		t.Errorf("expected filter[environment]=smplkit, got %q (present=%v)", captured, present)
	}
}

func TestAuditEventTypes_List_ParsesPaginationWithTotals(t *testing.T) {
	ac, cleanup := newTestAuditEventTypes(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"invoice.created","type":"event_type","attributes":{"event_type":"invoice.created","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":2,"size":1,"total":3,"total_pages":3}}}`))
	})
	defer cleanup()

	page, err := ac.List(context.Background(), ListEventTypesInput{PageNumber: 2, PageSize: 1, MetaTotal: true})
	if err != nil {
		t.Fatalf("EventTypes.List: %v", err)
	}
	if page.Pagination.Page != 2 || page.Pagination.Size != 1 {
		t.Errorf("expected page=2 size=1, got %+v", page.Pagination)
	}
	if page.Pagination.Total == nil || *page.Pagination.Total != 3 {
		t.Errorf("expected total=3, got %+v", page.Pagination.Total)
	}
	if page.Pagination.TotalPages == nil || *page.Pagination.TotalPages != 3 {
		t.Errorf("expected total_pages=3, got %+v", page.Pagination.TotalPages)
	}
}

func TestAuditEventTypes_List_Error(t *testing.T) {
	ac, cleanup := newTestAuditEventTypes(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := ac.List(context.Background(), ListEventTypesInput{}); err == nil {
		t.Fatal("expected error from 500")
	}
}

func TestAuditEventTypes_List_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()
	gen, _ := genaudit.NewClient(url)
	ac := &AuditEventTypes{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	if _, err := ac.List(context.Background(), ListEventTypesInput{}); err == nil {
		t.Fatal("expected transport error")
	}
}

// ---------------------------------------------------------------------------
// Categories.List — mirrors ResourceTypes.List
// ---------------------------------------------------------------------------

func TestAuditCategories_List_ReturnsValues(t *testing.T) {
	cat, cleanup := newTestAuditCategories(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"auth","type":"category","attributes":{"category":"auth","created_at":"2026-05-01T00:00:00Z"}},{"id":"billing","type":"category","attributes":{"category":"billing","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	page, err := cat.List(context.Background(), ListCategoriesInput{})
	if err != nil {
		t.Fatalf("Categories.List: %v", err)
	}
	if len(page.Categories) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(page.Categories))
	}
	vals := make(map[string]string)
	for _, c := range page.Categories {
		vals[c.ID] = c.Category
	}
	if vals["billing"] != "billing" {
		t.Errorf("expected billing category with ID==Category, got %+v", page.Categories)
	}
	if page.Pagination.Page != 1 || page.Pagination.Size != 1000 {
		t.Errorf("expected pagination page=1 size=1000, got %+v", page.Pagination)
	}
}

func TestAuditCategories_List_ParsesPaginationWithTotals(t *testing.T) {
	cat, cleanup := newTestAuditCategories(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"auth","type":"category","attributes":{"category":"auth","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":2,"size":1,"total":3,"total_pages":3}}}`))
	})
	defer cleanup()

	page, err := cat.List(context.Background(), ListCategoriesInput{PageNumber: 2, PageSize: 1, MetaTotal: true})
	if err != nil {
		t.Fatalf("Categories.List: %v", err)
	}
	if page.Pagination.Page != 2 || page.Pagination.Size != 1 {
		t.Errorf("expected page=2 size=1, got %+v", page.Pagination)
	}
	if page.Pagination.Total == nil || *page.Pagination.Total != 3 {
		t.Errorf("expected total=3, got %+v", page.Pagination.Total)
	}
	if page.Pagination.TotalPages == nil || *page.Pagination.TotalPages != 3 {
		t.Errorf("expected total_pages=3, got %+v", page.Pagination.TotalPages)
	}
}

func TestAuditCategories_List_WithPageNumber(t *testing.T) {
	var capturedQuery string
	cat, cleanup := newTestAuditCategories(t, func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":3,"size":1}}}`))
	})
	defer cleanup()

	if _, err := cat.List(context.Background(), ListCategoriesInput{PageNumber: 3, PageSize: 1}); err != nil {
		t.Fatalf("Categories.List: %v", err)
	}
	if !strings.Contains(capturedQuery, "page%5Bnumber%5D=3") ||
		!strings.Contains(capturedQuery, "page%5Bsize%5D=1") {
		t.Errorf("expected page[number]=3 and page[size]=1 in query, got %q", capturedQuery)
	}
}

func TestAuditCategories_List_Environments(t *testing.T) {
	var captured string
	var present bool
	cat, cleanup := newTestAuditCategories(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query().Get("filter[environment]")
		_, present = r.URL.Query()["filter[environment]"]
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	// Omitted by default.
	if _, err := cat.List(context.Background(), ListCategoriesInput{}); err != nil {
		t.Fatalf("Categories.List: %v", err)
	}
	if present {
		t.Error("expected filter[environment] absent when Environments is nil")
	}

	// Single value.
	if _, err := cat.List(context.Background(), ListCategoriesInput{Environments: []string{"production"}}); err != nil {
		t.Fatalf("Categories.List: %v", err)
	}
	if !present || captured != "production" {
		t.Errorf("expected filter[environment]=production, got %q (present=%v)", captured, present)
	}

	// Multiple values comma-join, including the reserved smplkit bucket.
	if _, err := cat.List(context.Background(), ListCategoriesInput{Environments: []string{"smplkit", "staging"}}); err != nil {
		t.Fatalf("Categories.List: %v", err)
	}
	if !present || captured != "smplkit,staging" {
		t.Errorf("expected filter[environment]=smplkit,staging, got %q (present=%v)", captured, present)
	}
}

func TestAuditCategories_List_Error(t *testing.T) {
	cat, cleanup := newTestAuditCategories(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := cat.List(context.Background(), ListCategoriesInput{}); err == nil {
		t.Fatal("expected error from 500")
	}
}

func TestAuditCategories_List_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()
	gen, _ := genaudit.NewClient(url)
	cat := &AuditCategories{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	if _, err := cat.List(context.Background(), ListCategoriesInput{}); err == nil {
		t.Fatal("expected transport error")
	}
}

// ---------------------------------------------------------------------------
// ResourceTypes.List and EventTypes.List — PageNumber/PageSize query coverage
// ---------------------------------------------------------------------------

func TestAuditResourceTypes_List_WithPageNumber(t *testing.T) {
	var capturedQuery string
	rt, cleanup := newTestAuditResourceTypes(t, func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":3,"size":1}}}`))
	})
	defer cleanup()

	if _, err := rt.List(context.Background(), ListResourceTypesInput{PageNumber: 3, PageSize: 1}); err != nil {
		t.Fatalf("ResourceTypes.List: %v", err)
	}
	if !strings.Contains(capturedQuery, "page%5Bnumber%5D=3") ||
		!strings.Contains(capturedQuery, "page%5Bsize%5D=1") {
		t.Errorf("expected page[number]=3 and page[size]=1 in query, got %q", capturedQuery)
	}
}

func TestAuditEventTypes_List_WithPageNumber(t *testing.T) {
	var capturedQuery string
	ac, cleanup := newTestAuditEventTypes(t, func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":3,"size":1}}}`))
	})
	defer cleanup()

	if _, err := ac.List(context.Background(), ListEventTypesInput{PageNumber: 3, PageSize: 1}); err != nil {
		t.Fatalf("EventTypes.List: %v", err)
	}
	if !strings.Contains(capturedQuery, "page%5Bnumber%5D=3") ||
		!strings.Contains(capturedQuery, "page%5Bsize%5D=1") {
		t.Errorf("expected page[number]=3 and page[size]=1 in query, got %q", capturedQuery)
	}
}

func TestExtractNextCursor(t *testing.T) {
	cases := []struct {
		name string
		next *string
		want string
	}{
		{"nil", nil, ""},
		{"tokenAtEnd", ptr("/api/v1/events?page[size]=1&page[after]=tok"), "tok"},
		{"tokenMidWithAmp", ptr("/api/v1/events?page[after]=tok-mid&page[size]=1"), "tok-mid"},
		{"noPageAfter", ptr("/api/v1/events?page[size]=1"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractNextCursor(tc.next); got != tc.want {
				t.Fatalf("extractNextCursor: want %q, got %q", tc.want, got)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------
// Active-record surface: New, Save, Delete, options
// ---------------------------------------------------------------------------

func TestAuditForwarders_New_DefaultsAndOptions(t *testing.T) {
	fwds := &AuditForwarders{}
	prodCfg := HttpConfiguration{URL: "https://prod.example.com/in"}
	fwd := fwds.New(
		"my-forwarder",
		"My Forwarder",
		ForwarderTypeDatadog,
		HttpConfiguration{URL: "https://x"},
		WithForwarderDescription("a description"),
		WithForwarderEnvironments(map[string]ForwarderEnvironment{
			"production": {Enabled: true, Configuration: &prodCfg},
			"staging":    {Enabled: false},
		}),
		WithForwarderFilter(map[string]interface{}{"==": []any{"x", "x"}}),
		WithForwarderTransform(ForwarderTransformTypeJSONata, "$"),
	)
	if fwd.ID != "my-forwarder" {
		t.Errorf("expected ID=my-forwarder, got %q", fwd.ID)
	}
	if fwd.Name != "My Forwarder" {
		t.Errorf("expected Name=My Forwarder, got %q", fwd.Name)
	}
	if fwd.ForwarderType != ForwarderTypeDatadog {
		t.Errorf("expected ForwarderType=Datadog, got %v", fwd.ForwarderType)
	}
	if fwd.Configuration.URL != "https://x" {
		t.Errorf("expected URL=https://x, got %q", fwd.Configuration.URL)
	}
	if fwd.Description == nil || *fwd.Description != "a description" {
		t.Errorf("expected Description set, got %v", fwd.Description)
	}
	prod, ok := fwd.Environments["production"]
	if !ok || !prod.Enabled {
		t.Errorf("expected production enabled=true, got %+v", fwd.Environments["production"])
	}
	if prod.Configuration == nil || prod.Configuration.URL != "https://prod.example.com/in" {
		t.Errorf("expected production config override, got %+v", prod.Configuration)
	}
	if stg, ok := fwd.Environments["staging"]; !ok || stg.Enabled {
		t.Errorf("expected staging enabled=false, got %+v", fwd.Environments["staging"])
	}
	if fwd.Filter == nil {
		t.Errorf("expected Filter set, got nil")
	}
	if s, ok := fwd.Transform.(string); !ok || s != "$" {
		t.Errorf("expected Transform=$, got %v", fwd.Transform)
	}
	if fwd.TransformType == nil || *fwd.TransformType != ForwarderTransformTypeJSONata {
		t.Errorf("expected TransformType=JSONATA, got %v", fwd.TransformType)
	}
	if fwd.client == nil {
		t.Errorf("expected client back-reference set")
	}
}

func TestAuditForwarders_New_EnvironmentsDefaultEmpty(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	// Base enablement is server-pinned false (ADR-055) and the wrapper
	// does not synthesize one; a forwarder created without environments
	// delivers nowhere until enabled per environment.
	if fwd.Enabled {
		t.Errorf("expected Enabled=false by default (read-only, server-pinned)")
	}
	if len(fwd.Environments) != 0 {
		t.Errorf("expected empty Environments by default, got %+v", fwd.Environments)
	}
	if fwd.Description != nil {
		t.Errorf("expected Description=nil by default")
	}
}

func TestForwarder_Save_NoClient(t *testing.T) {
	fwd := &Forwarder{Name: "x"}
	err := fwd.Save(context.Background())
	if err == nil || !strings.Contains(err.Error(), "without a client") {
		t.Fatalf("expected no-client error, got %v", err)
	}
}

func TestForwarder_Save_CreatePath(t *testing.T) {
	var captured string
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		if r.Method != http.MethodPost {
			t.Errorf("expected POST for create path, got %s", r.Method)
		}
		writeForwarderResource(w, http.StatusCreated, "my-forwarder", "")
	})
	defer cleanup()

	fwd := fwds.New("my-forwarder", "my-forwarder", ForwarderTypeDatadog,
		HttpConfiguration{Method: HttpMethodPost, URL: "https://x"},
		WithForwarderDescription("hi"),
		WithForwarderFilter(map[string]interface{}{"==": []any{1, 1}}),
		WithForwarderTransform(ForwarderTransformTypeJSONata, "$"),
	)
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if fwd.ID == "" {
		t.Errorf("expected ID populated after save, got empty")
	}
	if fwd.CreatedAt == nil {
		t.Errorf("expected CreatedAt populated after save")
	}
	if !strings.Contains(captured, `"description":"hi"`) {
		t.Errorf("expected description in request body, got %s", captured)
	}
}

func TestForwarder_Save_UpdatePath(t *testing.T) {
	calls := 0
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeForwarderResource(w, http.StatusCreated, "my-forwarder", "")
			return
		}
		if r.Method != http.MethodPut {
			t.Errorf("second call expected PUT, got %s", r.Method)
		}
		writeForwarderResource(w, http.StatusOK, "my-forwarder-renamed", "")
	})
	defer cleanup()

	fwd := fwds.New("my-forwarder", "my-forwarder", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	fwd.Name = "my-forwarder-renamed"
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	if fwd.Name != "my-forwarder-renamed" {
		t.Errorf("expected name refreshed from server, got %q", fwd.Name)
	}
}

func TestForwarder_Save_CreateError(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err == nil {
		t.Fatal("expected error from Save (create), got nil")
	}
}

func TestForwarder_Save_UpdateError(t *testing.T) {
	calls := 0
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			writeForwarderResource(w, http.StatusCreated, "x", "")
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	if err := fwd.Save(context.Background()); err == nil {
		t.Fatal("expected error from Save (update), got nil")
	}
}

func TestForwarder_Save_CreateEmptyBody(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
	})
	defer cleanup()
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err == nil || !strings.Contains(err.Error(), "empty 201 body") {
		t.Fatalf("expected empty-201 error, got %v", err)
	}
}

func TestForwarder_Save_CreateTransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err == nil {
		t.Fatal("expected transport error from Save (create)")
	}
}

func TestForwarder_Save_UpdateTransportError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			writeForwarderResource(w, http.StatusCreated, "x", "")
			return
		}
	}))
	gen, _ := genaudit.NewClient(srv.URL)
	fwds := &AuditForwarders{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	srv.Close()
	if err := fwd.Save(context.Background()); err == nil {
		t.Fatal("expected transport error from Save (update)")
	}
}

func TestForwarder_Delete_NoClient(t *testing.T) {
	fwd := &Forwarder{Name: "x", ID: fwdIDStr}
	err := fwd.Delete(context.Background())
	if err == nil || !strings.Contains(err.Error(), "without a client or id") {
		t.Fatalf("expected no-client-or-id error, got %v", err)
	}
}

func TestForwarder_Delete_NoID(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	fwd.ID = "" // simulate an unsaved instance — Delete must refuse.
	err := fwd.Delete(context.Background())
	if err == nil || !strings.Contains(err.Error(), "without a client or id") {
		t.Fatalf("expected no-client-or-id error, got %v", err)
	}
}

func TestForwarder_Delete_Success(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeForwarderResource(w, http.StatusCreated, "x", "")
	})
	defer cleanup()
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := fwd.Delete(context.Background()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// forwarderResourceFromForwarder branches: nil Transform, set Transform
// with explicit TransformType, no Description, no Filter.
func TestForwarderResourceFromForwarder_AllBranches(t *testing.T) {
	tt := ForwarderTransformTypeJSONata
	desc := "d"
	cases := []struct {
		name string
		fwd  *Forwarder
	}{
		{
			name: "minimal",
			fwd: &Forwarder{
				Name:          "x",
				ForwarderType: ForwarderTypeHTTP,
				Configuration: HttpConfiguration{URL: "https://x"},
			},
		},
		{
			name: "with-string-transform",
			fwd: &Forwarder{
				Name:          "x",
				Description:   &desc,
				ForwarderType: ForwarderTypeHTTP,
				Enabled:       true,
				Filter:        map[string]interface{}{"==": []any{1, 1}},
				Transform:     "$",
				TransformType: &tt,
				Configuration: HttpConfiguration{
					Method:        HttpMethodPost,
					URL:           "https://x",
					Headers:       []HttpHeader{{Name: "H", Value: "v"}},
					SuccessStatus: "2xx",
				},
			},
		},
		{
			name: "with-object-transform",
			fwd: &Forwarder{
				Name:          "x",
				ForwarderType: ForwarderTypeHTTP,
				Configuration: HttpConfiguration{URL: "https://x"},
				// Transform is engine-defined; a future engine could
				// carry a structured payload. The wrapper passes it
				// through untyped.
				Transform:     map[string]interface{}{"event": "event_type"},
				TransformType: &tt,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := forwarderResourceFromForwarder("11111111-2222-3333-4444-555555555555", c.fwd)
			if r.Attributes.Name != c.fwd.Name {
				t.Errorf("name not propagated")
			}
		})
	}
}

// forwarderResourceFromForwarder leaves the resource id nil when the
// forwarder id is empty (the id == "" branch).
func TestForwarderResourceFromForwarder_EmptyID(t *testing.T) {
	r := forwarderResourceFromForwarder("", &Forwarder{
		Name:          "x",
		ForwarderType: ForwarderTypeHTTP,
		Configuration: HttpConfiguration{URL: "https://x"},
	})
	if r.Id != nil {
		t.Fatalf("expected nil resource id for empty forwarder id, got %v", *r.Id)
	}
}

// Save sends the per-environment override map on the wire, with the base
// `enabled` omitted (server-pinned false per ADR-055). A per-environment
// configuration override is serialized as a full HttpConfiguration.
func TestForwarder_Save_SendsEnvironments(t *testing.T) {
	var capturedBody []byte
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeForwarderResource(w, http.StatusCreated, "x", "")
	})
	defer cleanup()
	prodCfg := HttpConfiguration{
		Method:  HttpMethodPost,
		URL:     "https://prod.example.com/in",
		Headers: []HttpHeader{{Name: "DD-API-KEY", Value: "secret"}},
	}
	fwd := fwds.New("x", "x", ForwarderTypeDatadog, HttpConfiguration{URL: "https://base"},
		WithForwarderEnvironments(map[string]ForwarderEnvironment{
			"production": {Enabled: true, Configuration: &prodCfg},
			"staging":    {Enabled: false},
		}),
	)
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body := string(capturedBody)
	if !strings.Contains(body, `"environments"`) {
		t.Fatalf("expected environments in request body, got: %s", body)
	}
	if !strings.Contains(body, `"production"`) || !strings.Contains(body, `"enabled":true`) {
		t.Fatalf("expected production enabled in body, got: %s", body)
	}
	if !strings.Contains(body, `"https://prod.example.com/in"`) {
		t.Fatalf("expected per-env configuration override on wire, got: %s", body)
	}
	// The base `enabled` is server-pinned false (ADR-055) and must never
	// be sent on the wire — enablement travels only via `environments`.
	var parsed struct {
		Data struct {
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(capturedBody, &parsed); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	if _, present := parsed.Data.Attributes["enabled"]; present {
		t.Fatalf("did not expect base `enabled` on wire, got: %s", body)
	}
}

// Get reads the per-environment override map back into the wrapper,
// including a per-environment configuration override.
func TestForwarder_Get_ReadsEnvironments(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		body := map[string]any{
			"data": map[string]any{
				"id": fwdIDStr, "type": "forwarder",
				"attributes": map[string]any{
					"name": "n", "forwarder_type": "datadog", "enabled": false,
					"configuration": map[string]any{"url": "https://base", "headers": []any{}},
					"environments": map[string]any{
						"production": map[string]any{
							"enabled": true,
							"configuration": map[string]any{
								"url":     "https://prod.example.com/in",
								"method":  "POST",
								"headers": []map[string]string{{"name": "DD-API-KEY", "value": "<redacted>"}},
							},
						},
						"staging": map[string]any{"enabled": false},
					},
					"created_at": "2026-05-07T12:00:00+00:00",
					"updated_at": "2026-05-07T12:00:00+00:00",
					"version":    1,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	defer cleanup()
	fwd, err := fwds.Get(context.Background(), fwdIDStr)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fwd.Enabled {
		t.Errorf("expected base Enabled=false (read-only), got true")
	}
	if len(fwd.Environments) != 2 {
		t.Fatalf("expected 2 environments, got %d (%+v)", len(fwd.Environments), fwd.Environments)
	}
	prod, ok := fwd.Environments["production"]
	if !ok || !prod.Enabled {
		t.Fatalf("expected production enabled=true, got %+v", fwd.Environments["production"])
	}
	if prod.Configuration == nil || prod.Configuration.URL != "https://prod.example.com/in" {
		t.Fatalf("expected production config override read back, got %+v", prod.Configuration)
	}
	if prod.Configuration.Headers[0].Value != "<redacted>" {
		t.Fatalf("expected redacted per-env header, got %+v", prod.Configuration.Headers)
	}
	if stg := fwd.Environments["staging"]; stg.Enabled || stg.Configuration != nil {
		t.Fatalf("expected staging enabled=false, no config, got %+v", stg)
	}
}

// ---------------------------------------------------------------------------
// Per-environment setters: SetConfiguration / SetEnabled (base + override)
// ---------------------------------------------------------------------------

// SetConfiguration with an empty environment replaces the base Configuration.
func TestForwarder_SetConfiguration_Base(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://old"})
	fwd.SetConfiguration(HttpConfiguration{URL: "https://new"}, "")
	if fwd.Configuration.URL != "https://new" {
		t.Errorf("expected base configuration replaced, got %q", fwd.Configuration.URL)
	}
	if len(fwd.Environments) != 0 {
		t.Errorf("expected no per-environment overrides created, got %+v", fwd.Environments)
	}
}

// SetEnabled with an empty environment sets the (read-only) base Enabled.
func TestForwarder_SetEnabled_Base(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	fwd.SetEnabled(true, "")
	if !fwd.Enabled {
		t.Error("expected base Enabled=true after SetEnabled(true, \"\")")
	}
	if len(fwd.Environments) != 0 {
		t.Errorf("expected no per-environment overrides created, got %+v", fwd.Environments)
	}
}

// SetConfiguration with an environment creates the override entry (the
// Environments==nil → allocate, and the absent-key → create-empty paths in
// environmentOverride) and sets only its configuration.
func TestForwarder_SetConfiguration_PerEnvironment_CreatesOverride(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://base"})
	fwd.SetConfiguration(HttpConfiguration{URL: "https://prod"}, "production")
	prod, ok := fwd.Environments["production"]
	if !ok {
		t.Fatal("expected production override created")
	}
	if prod.Configuration == nil || prod.Configuration.URL != "https://prod" {
		t.Errorf("expected production configuration override, got %+v", prod.Configuration)
	}
	if prod.Enabled {
		t.Error("expected Enabled untouched (false) when only configuration is set")
	}
}

// SetEnabled with an environment creates the override entry and sets only its
// Enabled.
func TestForwarder_SetEnabled_PerEnvironment_CreatesOverride(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://base"})
	fwd.SetEnabled(true, "production")
	prod, ok := fwd.Environments["production"]
	if !ok {
		t.Fatal("expected production override created")
	}
	if !prod.Enabled {
		t.Error("expected production Enabled=true")
	}
	if prod.Configuration != nil {
		t.Errorf("expected no configuration set, got %+v", prod.Configuration)
	}
}

// The per-environment setters reach through environmentOverride, which
// preserves the other field on an existing override: setting Enabled then
// Configuration (and vice-versa) on the same environment keeps both.
func TestForwarder_PerEnvironmentSetters_PreserveExistingOverride(t *testing.T) {
	fwds := &AuditForwarders{}

	// Enabled first, then Configuration — Configuration must not wipe Enabled.
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://base"})
	fwd.SetEnabled(true, "production")
	fwd.SetConfiguration(HttpConfiguration{URL: "https://prod"}, "production")
	prod := fwd.Environments["production"]
	if !prod.Enabled {
		t.Error("expected Enabled preserved after a later SetConfiguration")
	}
	if prod.Configuration == nil || prod.Configuration.URL != "https://prod" {
		t.Errorf("expected configuration set, got %+v", prod.Configuration)
	}

	// Configuration first, then Enabled — Enabled must not wipe Configuration.
	fwd2 := fwds.New("y", "y", ForwarderTypeHTTP, HttpConfiguration{URL: "https://base"})
	fwd2.SetConfiguration(HttpConfiguration{URL: "https://stg"}, "staging")
	fwd2.SetEnabled(true, "staging")
	stg := fwd2.Environments["staging"]
	if stg.Configuration == nil || stg.Configuration.URL != "https://stg" {
		t.Errorf("expected configuration preserved after a later SetEnabled, got %+v", stg.Configuration)
	}
	if !stg.Enabled {
		t.Error("expected Enabled set after SetEnabled")
	}
}

// SetConfiguration on a forwarder that already carries an Environments map
// (from WithForwarderEnvironments) updates the existing entry rather than
// allocating a new map (covers the Environments != nil, key present path).
func TestForwarder_SetConfiguration_UpdatesExistingMapEntry(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://base"},
		WithForwarderEnvironments(map[string]ForwarderEnvironment{
			"production": {Enabled: true},
		}),
	)
	fwd.SetConfiguration(HttpConfiguration{URL: "https://prod"}, "production")
	prod := fwd.Environments["production"]
	if !prod.Enabled {
		t.Error("expected pre-existing Enabled preserved")
	}
	if prod.Configuration == nil || prod.Configuration.URL != "https://prod" {
		t.Errorf("expected configuration set on existing entry, got %+v", prod.Configuration)
	}
}

// A per-environment setter result round-trips through Save onto the wire.
func TestForwarder_SetEnabled_PerEnvironment_SavedToWire(t *testing.T) {
	var capturedBody []byte
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeForwarderResource(w, http.StatusCreated, "x", "")
	})
	defer cleanup()
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://base"})
	fwd.SetEnabled(true, "production")
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body := string(capturedBody)
	if !strings.Contains(body, `"production"`) || !strings.Contains(body, `"enabled":true`) {
		t.Fatalf("expected per-env enablement on wire, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Transform validation invariants
// ---------------------------------------------------------------------------

// Save rejects a forwarder whose Transform is set without a
// TransformType — both must be specified together.
func TestForwarder_Save_TransformWithoutType(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	fwd.Transform = "$"
	fwd.TransformType = nil

	err := fwd.Save(context.Background())
	if err == nil || !strings.Contains(err.Error(), "TransformType is not") {
		t.Fatalf("expected transform-without-type error, got %v", err)
	}
}

// Save rejects a forwarder whose TransformType is set without a
// Transform — both must be specified together.
func TestForwarder_Save_TypeWithoutTransform(t *testing.T) {
	fwds := &AuditForwarders{}
	tt := ForwarderTransformTypeJSONata
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	fwd.Transform = nil
	fwd.TransformType = &tt

	err := fwd.Save(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Transform is not") {
		t.Fatalf("expected type-without-transform error, got %v", err)
	}
}

// Save rejects a JSONATA forwarder whose Transform is not a string.
func TestForwarder_Save_JSONataTransformMustBeString(t *testing.T) {
	fwds := &AuditForwarders{}
	tt := ForwarderTransformTypeJSONata
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	fwd.Transform = map[string]interface{}{"event": "event_type"}
	fwd.TransformType = &tt

	err := fwd.Save(context.Background())
	if err == nil || !strings.Contains(err.Error(), "must be a string when TransformType is JSONATA") {
		t.Fatalf("expected JSONATA-must-be-string error, got %v", err)
	}
}

// Save accepts a JSONATA forwarder whose Transform is an empty string
// (still a string — server-side validation owns content rules).
func TestForwarder_Save_JSONataEmptyStringAllowed(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		writeForwarderResource(w, http.StatusCreated, "x", "")
	})
	defer cleanup()
	tt := ForwarderTransformTypeJSONata
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	fwd.Transform = ""
	fwd.TransformType = &tt

	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("expected empty-string transform to validate, got %v", err)
	}
}

// Save accepts a forwarder with neither Transform nor TransformType
// (the common "no transform — pass event through unchanged" case).
func TestForwarder_Save_NoTransformBothNil(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		writeForwarderResource(w, http.StatusCreated, "x", "")
	})
	defer cleanup()
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("expected save to succeed with no transform, got %v", err)
	}
}

// Save threads TlsVerify and CaCert through to the wire so customers
// can opt out of certificate verification (or pin a private CA) on a
// per-forwarder basis. The fields are pointer-valued; nil leaves them
// off the wire entirely so the server's default applies.
func TestForwarder_Save_SendsTlsVerifyAndCaCert(t *testing.T) {
	var capturedBody []byte
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeForwarderResource(w, http.StatusCreated, "x", "")
	})
	defer cleanup()
	tlsOff := false
	caCert := "-----BEGIN CERTIFICATE-----\nfoo\n-----END CERTIFICATE-----"
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{
		URL:       "https://x",
		TlsVerify: &tlsOff,
		CaCert:    &caCert,
	})
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("save: %v", err)
	}
	body := string(capturedBody)
	if !strings.Contains(body, `"tls_verify":false`) {
		t.Errorf("expected tls_verify on wire, got: %s", body)
	}
	if !strings.Contains(body, `"ca_cert":"-----BEGIN CERTIFICATE-----`) {
		t.Errorf("expected ca_cert on wire, got: %s", body)
	}
}

// Get reads TlsVerify and CaCert back from the wire into the wrapper.
func TestForwarder_Get_ReadsTlsVerifyAndCaCert(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		body := map[string]any{
			"data": map[string]any{
				"id": fwdIDStr, "type": "forwarder",
				"attributes": map[string]any{
					"name": "n", "forwarder_type": "http", "enabled": true,
					"configuration": map[string]any{
						"method": "POST", "url": "https://x", "headers": []any{},
						"success_status": "2xx",
						"tls_verify":     false,
						"ca_cert":        "-----BEGIN CERTIFICATE-----\nfoo\n-----END CERTIFICATE-----",
					},
					"created_at": "2026-05-07T12:00:00+00:00",
					"updated_at": "2026-05-07T12:00:00+00:00",
					"version":    1,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	defer cleanup()
	fwd, err := fwds.Get(context.Background(), fwdIDStr)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fwd.Configuration.TlsVerify == nil || *fwd.Configuration.TlsVerify != false {
		t.Errorf("expected TlsVerify=false, got %v", fwd.Configuration.TlsVerify)
	}
	if fwd.Configuration.CaCert == nil || !strings.Contains(*fwd.Configuration.CaCert, "BEGIN CERTIFICATE") {
		t.Errorf("expected CaCert with PEM body, got %v", fwd.Configuration.CaCert)
	}
}

// Get leaves TlsVerify and CaCert nil when the wire omits them — the
// wrapper treats absence as "leave the server default in place" rather
// than synthesizing a value.
func TestForwarder_Get_NilTlsFieldsWhenWireOmitsThem(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		writeForwarderResource(w, http.StatusOK, "n", "")
	})
	defer cleanup()
	fwd, err := fwds.Get(context.Background(), fwdIDStr)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fwd.Configuration.TlsVerify != nil {
		t.Errorf("expected TlsVerify nil, got %v", *fwd.Configuration.TlsVerify)
	}
	if fwd.Configuration.CaCert != nil {
		t.Errorf("expected CaCert nil, got %q", *fwd.Configuration.CaCert)
	}
}

// ---------------------------------------------------------------------------
// forward_smplkit_events — base-level opt-in for platform change events
// ---------------------------------------------------------------------------

// WithForwardSmplkitEvents records the opt-in on an unsaved forwarder.
func TestAuditForwarders_New_WithForwardSmplkitEvents(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"},
		WithForwardSmplkitEvents(true),
	)
	if fwd.ForwardSmplkitEvents == nil || !*fwd.ForwardSmplkitEvents {
		t.Errorf("expected ForwardSmplkitEvents=true, got %v", fwd.ForwardSmplkitEvents)
	}
}

// Omitting the option leaves the field nil, so the create body never
// carries forward_smplkit_events — existing callers stay unaffected.
func TestAuditForwarders_New_ForwardSmplkitEventsDefaultNil(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if fwd.ForwardSmplkitEvents != nil {
		t.Errorf("expected ForwardSmplkitEvents=nil by default, got %v", *fwd.ForwardSmplkitEvents)
	}
}

// Create sends forward_smplkit_events when set; the omitted case leaves
// it off the wire entirely.
func TestForwarder_Save_CreateSendsForwardSmplkitEvents(t *testing.T) {
	var captured string
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		writeForwarderResource(w, http.StatusCreated, "x", "")
	})
	defer cleanup()

	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"},
		WithForwardSmplkitEvents(true),
	)
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	if !strings.Contains(captured, `"forward_smplkit_events":true`) {
		t.Errorf("expected forward_smplkit_events:true in create body, got %s", captured)
	}
}

func TestForwarder_Save_CreateOmitsForwardSmplkitEventsWhenUnset(t *testing.T) {
	var captured string
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		writeForwarderResource(w, http.StatusCreated, "x", "")
	})
	defer cleanup()

	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	if strings.Contains(captured, "forward_smplkit_events") {
		t.Errorf("expected no forward_smplkit_events key in create body, got %s", captured)
	}
}

// Update changes the field: create with false, flip to true, and confirm
// the PUT body carries the new value.
func TestForwarder_Save_UpdateChangesForwardSmplkitEvents(t *testing.T) {
	var putBody string
	calls := 0
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeForwarderResource(w, http.StatusCreated, "x", "")
			return
		}
		b, _ := io.ReadAll(r.Body)
		putBody = string(b)
		if r.Method != http.MethodPut {
			t.Errorf("second call expected PUT, got %s", r.Method)
		}
		writeForwarderResource(w, http.StatusOK, "x", "")
	})
	defer cleanup()

	fwd := fwds.New("x", "x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"},
		WithForwardSmplkitEvents(false),
	)
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	fwd.ForwardSmplkitEvents = boolPtr(true)
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	if !strings.Contains(putBody, `"forward_smplkit_events":true`) {
		t.Errorf("expected forward_smplkit_events:true in update body, got %s", putBody)
	}
}

// Read surfaces forward_smplkit_events from the server response.
func TestForwarder_Get_SurfacesForwardSmplkitEvents(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"` + fwdIDStr + `","type":"forwarder","attributes":{` +
			`"name":"X","forwarder_type":"HTTP","enabled":false,` +
			`"configuration":{"url":"https://x","method":"POST","success_status":"2xx"},` +
			`"forward_smplkit_events":true` +
			`}}}`))
	})
	defer cleanup()
	fwd, err := fwds.Get(context.Background(), fwdIDStr)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fwd.ForwardSmplkitEvents == nil || !*fwd.ForwardSmplkitEvents {
		t.Errorf("expected ForwardSmplkitEvents=true from read, got %v", fwd.ForwardSmplkitEvents)
	}
}

// Read leaves the field nil when the wire omits it — absence stays
// absence rather than collapsing to false.
func TestForwarder_Get_ForwardSmplkitEventsNilWhenWireOmits(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		writeForwarderResource(w, http.StatusOK, "n", "")
	})
	defer cleanup()
	fwd, err := fwds.Get(context.Background(), fwdIDStr)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fwd.ForwardSmplkitEvents != nil {
		t.Errorf("expected ForwardSmplkitEvents nil, got %v", *fwd.ForwardSmplkitEvents)
	}
}

func boolPtr(b bool) *bool { return &b }
