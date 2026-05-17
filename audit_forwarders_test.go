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

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

const fwdIDStr = "11111111-2222-3333-4444-555555555555"

func parseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

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
				"forwarder_type": "DATADOG",
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
// ManagementClient.Audit() accessor
// ---------------------------------------------------------------------------

func TestManagementClient_AuditAccessor(t *testing.T) {
	mgmt, err := NewManagementClient(ManagementConfig{APIKey: "sk_api_test"})
	if err != nil {
		t.Fatalf("NewManagementClient: %v", err)
	}
	if mgmt.Audit() == nil {
		t.Fatal("Audit() returned nil")
	}
	if mgmt.Audit().Forwarders() == nil {
		t.Fatal("Forwarders() returned nil")
	}
}

func TestClient_ManageAuditAccessor(t *testing.T) {
	c, err := NewClient(Config{APIKey: "sk_api_test", Environment: "dev", Service: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if c.Manage().Audit() == nil {
		t.Fatal("Manage().Audit() returned nil")
	}
	if c.Manage().Audit().Forwarders() == nil {
		t.Fatal("Manage().Audit().Forwarders() returned nil")
	}
}

// ---------------------------------------------------------------------------
// AuditClient.ResourceTypes() and AuditClient.Actions() accessors
// ---------------------------------------------------------------------------

func TestAuditClient_ResourceTypesAndActionsAccessors(t *testing.T) {
	c, err := NewClient(Config{APIKey: "sk_api_test", Environment: "dev", Service: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if c.Audit().ResourceTypes() == nil {
		t.Fatal("ResourceTypes() returned nil")
	}
	if c.Audit().Actions() == nil {
		t.Fatal("Actions() returned nil")
	}
}

// ---------------------------------------------------------------------------
// Forwarder CRUD
// ---------------------------------------------------------------------------

func TestAuditForwarders_Create_RoundTrip(t *testing.T) {
	var captured struct {
		method string
		body   string
	}
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured.method = r.Method
		captured.body = string(b)
		writeForwarderResource(w, http.StatusCreated, "Datadog production", "datadog_production")
	})
	defer cleanup()

	fwd, err := fwds.Create(context.Background(), CreateForwarderInput{
		Name:          "Datadog production",
		ForwarderType: ForwarderTypeDatadog,
		Configuration: HttpConfiguration{
			Method: "POST",
			URL:    "https://siem.example.com/in",
			Headers: []HttpHeader{
				{Name: "DD-API-KEY", Value: "real-secret"},
			},
			SuccessStatus: "2xx",
		},
		Filter:    map[string]interface{}{"==": []any{"x", "x"}},
		Transform: "$",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if fwd.Name != "Datadog production" {
		t.Errorf("expected Name round-tripped, got %q", fwd.Name)
	}
	if captured.method != http.MethodPost {
		t.Errorf("expected POST, got %s", captured.method)
	}
}

func TestAuditForwarders_Create_NonSuccessReturnsError(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	_, err := fwds.Create(context.Background(), CreateForwarderInput{
		Name: "x", ForwarderType: ForwarderTypeHTTP,
		Configuration: HttpConfiguration{URL: "https://x", SuccessStatus: "2xx"},
	})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got %v", err)
	}
}

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

	enabled := true
	first, err := fwds.List(context.Background(), ListForwardersInput{
		ForwarderType: ForwarderTypeDatadog, Enabled: &enabled,
		PageNumber: 1, PageSize: 1, MetaTotal: true,
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
	_, err := fwds.Get(context.Background(), parseUUID(t, fwdIDStr))
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
	fwd, err := fwds.Get(context.Background(), parseUUID(t, fwdIDStr))
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

func TestAuditForwarders_Update(t *testing.T) {
	var method string
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		writeForwarderResource(w, http.StatusOK, "Renamed", "renamed")
	})
	defer cleanup()
	fwd, err := fwds.Update(context.Background(), parseUUID(t, fwdIDStr), UpdateForwarderInput{
		Name:          "Renamed",
		ForwarderType: ForwarderTypeDatadog,
		Configuration: HttpConfiguration{URL: "https://x"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("expected PUT, got %s", method)
	}
	if fwd.Name != "Renamed" {
		t.Errorf("expected Renamed, got %q", fwd.Name)
	}
}

func TestAuditForwarders_Delete(t *testing.T) {
	var method string
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()
	if err := fwds.Delete(context.Background(), parseUUID(t, fwdIDStr)); err != nil {
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
	if err := fwds.Delete(context.Background(), parseUUID(t, fwdIDStr)); err == nil {
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

func TestAuditForwarders_Update_NonSuccess(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()
	if _, err := fwds.Update(context.Background(), parseUUID(t, fwdIDStr), UpdateForwarderInput{
		Name: "x", ForwarderType: ForwarderTypeHTTP,
		Configuration: HttpConfiguration{URL: "https://x"},
	}); err == nil {
		t.Fatal("expected error on 404")
	}
}

// ---------------------------------------------------------------------------
// Transport-error paths
// ---------------------------------------------------------------------------

func TestAuditForwarders_Create_TransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	if _, err := fwds.Create(context.Background(), CreateForwarderInput{
		Name: "x", ForwarderType: ForwarderTypeHTTP,
		Configuration: HttpConfiguration{URL: "https://x", SuccessStatus: "2xx"},
	}); err == nil || !strings.Contains(err.Error(), "Forwarders.Create") {
		t.Fatalf("expected wrapped Create transport error, got %v", err)
	}
}

func TestAuditForwarders_List_TransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	if _, err := fwds.List(context.Background(), ListForwardersInput{}); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.List") {
		t.Fatalf("expected wrapped List transport error, got %v", err)
	}
}

func TestAuditForwarders_Get_TransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	if _, err := fwds.Get(context.Background(), parseUUID(t, fwdIDStr)); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.Get") {
		t.Fatalf("expected wrapped Get transport error, got %v", err)
	}
}

func TestAuditForwarders_Update_TransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	if _, err := fwds.Update(context.Background(), parseUUID(t, fwdIDStr), UpdateForwarderInput{
		Name: "x", ForwarderType: ForwarderTypeHTTP,
		Configuration: HttpConfiguration{URL: "https://x"},
	}); err == nil || !strings.Contains(err.Error(), "Forwarders.Update") {
		t.Fatalf("expected wrapped Update transport error, got %v", err)
	}
}

func TestAuditForwarders_Delete_TransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	if err := fwds.Delete(context.Background(), parseUUID(t, fwdIDStr)); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.Delete") {
		t.Fatalf("expected wrapped Delete transport error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Other error / branch paths
// ---------------------------------------------------------------------------

func TestAuditForwarders_Create_Empty201Body(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
	})
	defer cleanup()
	if _, err := fwds.Create(context.Background(), CreateForwarderInput{
		Name: "x", ForwarderType: ForwarderTypeHTTP,
		Configuration: HttpConfiguration{URL: "https://x", SuccessStatus: "2xx"},
	}); err == nil || !strings.Contains(err.Error(), "empty 201 body") {
		t.Fatalf("expected empty-201-body error, got %v", err)
	}
}

func TestAuditForwarders_Get_NonSuccessNon404(t *testing.T) {
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := fwds.Get(context.Background(), parseUUID(t, fwdIDStr)); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Fatalf("expected wrapped 500 error, got %v", err)
	}
}

// Create forwards the optional Description through to the wire body.
func TestAuditForwarders_Create_ForwardsDescription(t *testing.T) {
	var captured string
	fwds, cleanup := newTestAuditForwarders(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		writeForwarderResource(w, http.StatusCreated, "Datadog production", "")
	})
	defer cleanup()

	_, err := fwds.Create(context.Background(), CreateForwarderInput{
		Name:          "Datadog production",
		Description:   "ships every event to datadog",
		ForwarderType: ForwarderTypeDatadog,
		Configuration: HttpConfiguration{URL: "https://x", SuccessStatus: "2xx"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(captured, `"description":"ships every event to datadog"`) {
		t.Errorf("expected description in request body, got %s", captured)
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
	fwd, err := fwds.Get(context.Background(), parseUUID(t, fwdIDStr))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fwd.Filter == nil {
		t.Errorf("expected Filter populated, got nil")
	}
	if fwd.Transform == nil || *fwd.Transform != "$" {
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

func newTestAuditClient(t *testing.T, handler http.HandlerFunc) (*AuditClient, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	events := &AuditEvents{gen: wrapped, buffer: newAuditEventBuffer(wrapped)}
	c := &AuditClient{
		gen:           wrapped,
		events:        events,
		resourceTypes: &AuditResourceTypes{gen: wrapped},
		actions:       &AuditActions{gen: wrapped},
	}
	cleanup := func() {
		events.close()
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
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"action":"x.created","resource_type":"x","resource_id":"1","do_not_forward":true}}}`))
	})
	defer cleanup()

	if err := c.Events().Record(CreateEventInput{
		Action:       "user.created",
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

// ---------------------------------------------------------------------------
// ResourceTypes and Actions
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

func newTestAuditActions(t *testing.T, handler http.HandlerFunc) (*AuditActions, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genaudit.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	ac := &AuditActions{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	return ac, func() { srv.Close() }
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

func TestAuditActions_List_ReturnsSlugs(t *testing.T) {
	ac, cleanup := newTestAuditActions(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"invoice.created","type":"action","attributes":{"action":"invoice.created","created_at":"2026-05-01T00:00:00Z"}},{"id":"user.updated","type":"action","attributes":{"action":"user.updated","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	page, err := ac.List(context.Background(), ListActionsInput{})
	if err != nil {
		t.Fatalf("Actions.List: %v", err)
	}
	if len(page.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(page.Actions))
	}
	ids := make(map[string]bool)
	for _, a := range page.Actions {
		ids[a.ID] = true
	}
	if !ids["invoice.created"] {
		t.Error("expected invoice.created in actions")
	}
	if page.Pagination.Page != 1 || page.Pagination.Size != 1000 {
		t.Errorf("expected pagination page=1 size=1000, got %+v", page.Pagination)
	}
}

func TestAuditActions_List_FilterResourceType(t *testing.T) {
	var capturedQuery string
	ac, cleanup := newTestAuditActions(t, func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"invoice.created","type":"action","attributes":{"action":"invoice.created","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":1,"size":1000}}}`))
	})
	defer cleanup()

	if _, err := ac.List(context.Background(), ListActionsInput{FilterResourceType: "invoice"}); err != nil {
		t.Fatalf("Actions.List: %v", err)
	}
	if !strings.Contains(capturedQuery, "invoice") {
		t.Errorf("expected filter[resource_type] in query, got %q", capturedQuery)
	}
}

func TestAuditActions_List_ParsesPaginationWithTotals(t *testing.T) {
	ac, cleanup := newTestAuditActions(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"invoice.created","type":"action","attributes":{"action":"invoice.created","created_at":"2026-05-01T00:00:00Z"}}],"meta":{"pagination":{"page":2,"size":1,"total":3,"total_pages":3}}}`))
	})
	defer cleanup()

	page, err := ac.List(context.Background(), ListActionsInput{PageNumber: 2, PageSize: 1, MetaTotal: true})
	if err != nil {
		t.Fatalf("Actions.List: %v", err)
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

func TestAuditActions_List_Error(t *testing.T) {
	ac, cleanup := newTestAuditActions(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := ac.List(context.Background(), ListActionsInput{}); err == nil {
		t.Fatal("expected error from 500")
	}
}

func TestAuditActions_List_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()
	gen, _ := genaudit.NewClient(url)
	ac := &AuditActions{gen: &genaudit.ClientWithResponses{ClientInterface: gen}}
	if _, err := ac.List(context.Background(), ListActionsInput{}); err == nil {
		t.Fatal("expected transport error")
	}
}

// ---------------------------------------------------------------------------
// ResourceTypes.List and Actions.List — PageNumber/PageSize query coverage
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

func TestAuditActions_List_WithPageNumber(t *testing.T) {
	var capturedQuery string
	ac, cleanup := newTestAuditActions(t, func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":3,"size":1}}}`))
	})
	defer cleanup()

	if _, err := ac.List(context.Background(), ListActionsInput{PageNumber: 3, PageSize: 1}); err != nil {
		t.Fatalf("Actions.List: %v", err)
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
// buildAuditGenClient header editor — exercised via NewManagementClient + httptest
// ---------------------------------------------------------------------------

func TestBuildAuditGenClient_HeaderEditorFires(t *testing.T) {
	gotAccept := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAccept <- r.Header.Get("Accept"):
		default:
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":1,"size":1000}}}`))
	}))
	defer srv.Close()

	// withBaseURLOverride routes all service URLs (including audit) to the test server.
	mgmt, err := NewManagementClient(ManagementConfig{APIKey: "sk_api_test"}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewManagementClient: %v", err)
	}

	// A real request triggers the header-editor closure inside buildAuditGenClient.
	ctx := context.Background()
	if _, err := mgmt.Audit().Forwarders().List(ctx, ListForwardersInput{}); err != nil {
		t.Logf("List error (ok — server returns forwarder-shaped body): %v", err)
	}

	select {
	case accept := <-gotAccept:
		if !strings.Contains(accept, "application/vnd.api+json") {
			t.Fatalf("expected Accept header from buildAuditGenClient editor, got %q", accept)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received request from management client")
	}
}

// ---------------------------------------------------------------------------
// Active-record surface: New, Save, Delete, options
// ---------------------------------------------------------------------------

func TestAuditForwarders_New_DefaultsAndOptions(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New(
		"my-forwarder",
		ForwarderTypeDatadog,
		HttpConfiguration{URL: "https://x"},
		WithForwarderDescription("a description"),
		WithForwarderEnabled(false),
		WithForwarderFilter(map[string]interface{}{"==": []any{"x", "x"}}),
		WithForwarderTransform("$"),
	)
	if fwd.Name != "my-forwarder" {
		t.Errorf("expected Name=my-forwarder, got %q", fwd.Name)
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
	if fwd.Enabled {
		t.Errorf("expected Enabled=false (override), got true")
	}
	if fwd.Filter == nil {
		t.Errorf("expected Filter set, got nil")
	}
	if fwd.Transform == nil || *fwd.Transform != "$" {
		t.Errorf("expected Transform=$, got %v", fwd.Transform)
	}
	if fwd.TransformType == nil || *fwd.TransformType != ForwarderTransformTypeJSONata {
		t.Errorf("expected TransformType=JSONATA, got %v", fwd.TransformType)
	}
	if fwd.client == nil {
		t.Errorf("expected client back-reference set")
	}
}

func TestAuditForwarders_New_EnabledDefaultsTrue(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if !fwd.Enabled {
		t.Errorf("expected Enabled=true by default")
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

	fwd := fwds.New("my-forwarder", ForwarderTypeDatadog,
		HttpConfiguration{Method: HttpMethodPost, URL: "https://x"},
		WithForwarderDescription("hi"),
		WithForwarderFilter(map[string]interface{}{"==": []any{1, 1}}),
		WithForwarderTransform("$"),
	)
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if fwd.ID == uuid.Nil {
		t.Errorf("expected ID populated after save, got zero")
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

	fwd := fwds.New("my-forwarder", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
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
	fwd := fwds.New("x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
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
	fwd := fwds.New("x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
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
	fwd := fwds.New("x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err == nil || !strings.Contains(err.Error(), "empty 201 body") {
		t.Fatalf("expected empty-201 error, got %v", err)
	}
}

func TestForwarder_Save_CreateTransportError(t *testing.T) {
	fwds := newClosedAuditForwarders(t)
	fwd := fwds.New("x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
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
	fwd := fwds.New("x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	if err := fwd.Save(context.Background()); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	srv.Close()
	if err := fwd.Save(context.Background()); err == nil {
		t.Fatal("expected transport error from Save (update)")
	}
}

func TestForwarder_Delete_NoClient(t *testing.T) {
	fwd := &Forwarder{Name: "x", ID: parseUUID(t, fwdIDStr)}
	err := fwd.Delete(context.Background())
	if err == nil || !strings.Contains(err.Error(), "without a client or id") {
		t.Fatalf("expected no-client-or-id error, got %v", err)
	}
}

func TestForwarder_Delete_NoID(t *testing.T) {
	fwds := &AuditForwarders{}
	fwd := fwds.New("x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
	// ID is zero — the active-record's Delete refuses to call the server.
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
	fwd := fwds.New("x", ForwarderTypeHTTP, HttpConfiguration{URL: "https://x"})
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
	transform := "$"
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
			name: "with-all-fields",
			fwd: &Forwarder{
				Name:          "x",
				Description:   &desc,
				ForwarderType: ForwarderTypeHTTP,
				Enabled:       true,
				Filter:        map[string]interface{}{"==": []any{1, 1}},
				Transform:     &transform,
				TransformType: &tt,
				Configuration: HttpConfiguration{
					Method:        HttpMethodPost,
					URL:           "https://x",
					Headers:       []HttpHeader{{Name: "H", Value: "v"}},
					SuccessStatus: "2xx",
				},
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
