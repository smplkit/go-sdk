package smplkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// newTestAuditClient wires the full AuditClient (events + forwarders +
// functions) against an httptest server so the tests exercise both the
// per-route wrappers and the client constructor wiring.
func newTestAuditClient(t *testing.T, handler http.HandlerFunc) (*AuditClient, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := newAuditClientPointingAt(t, srv.URL)
	cleanup := func() {
		c.events.close()
		srv.Close()
	}
	return c, cleanup
}

// newClosedAuditClient returns an AuditClient whose backing server has
// already been closed, so every request returns a transport-level error
// (connection refused). Used to exercise the ``if err != nil`` branches
// of every wrapper method.
func newClosedAuditClient(t *testing.T) *AuditClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close()
	c := newAuditClientPointingAt(t, url)
	t.Cleanup(c.events.close)
	return c
}

func newAuditClientPointingAt(t *testing.T, url string) *AuditClient {
	t.Helper()
	gen, err := genaudit.NewClient(url)
	if err != nil {
		t.Fatalf("genaudit.NewClient: %v", err)
	}
	wrapped := &genaudit.ClientWithResponses{ClientInterface: gen}
	deliveries := &AuditForwarderDeliveries{
		gen:     wrapped,
		actions: &AuditDeliveryActions{gen: wrapped},
	}
	forwarders := &AuditForwarders{
		gen:        wrapped,
		deliveries: deliveries,
		actions:    &AuditForwarderActions{gen: wrapped},
	}
	functions := &AuditFunctions{
		gen: wrapped,
		testForwarder: &AuditTestForwarder{
			actions: &AuditTestForwarderActions{gen: wrapped},
		},
	}
	events := &AuditEvents{gen: wrapped, buffer: newAuditEventBuffer(wrapped)}
	return &AuditClient{
		gen: wrapped, events: events, forwarders: forwarders, functions: functions,
	}
}

const fwdIDStr = "11111111-2222-3333-4444-555555555555"
const deliveryIDStr = "22222222-3333-4444-5555-666666666666"

func parseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

func writeForwarderResource(w http.ResponseWriter, status int, name, slug string) {
	w.Header().Set("Content-Type", "application/vnd.api+json")
	w.WriteHeader(status)
	body := map[string]any{
		"data": map[string]any{
			"id":   fwdIDStr,
			"type": "forwarder",
			"attributes": map[string]any{
				"name":           name,
				"slug":           slug,
				"forwarder_type": "datadog",
				"enabled":        true,
				"http": map[string]any{
					"method": "POST",
					"url":    "https://siem.example.com/in",
					"headers": []map[string]string{
						{"name": "DD-API-KEY", "value": "<redacted>"},
					},
					"success_status": "2xx",
				},
				"data":       map[string]any{},
				"created_at": "2026-05-07T12:00:00+00:00",
				"updated_at": "2026-05-07T12:00:00+00:00",
				"version":    1,
			},
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeDeliveryResource(w http.ResponseWriter, status int, deliveryStatus string) {
	w.Header().Set("Content-Type", "application/vnd.api+json")
	w.WriteHeader(status)
	body := map[string]any{
		"data": map[string]any{
			"id":   deliveryIDStr,
			"type": "forwarder_delivery",
			"attributes": map[string]any{
				"forwarder_id":   fwdIDStr,
				"event_id":       "33333333-4444-5555-6666-777777777777",
				"attempt_number": 1,
				"status":         deliveryStatus,
				"request": map[string]any{
					"method": "POST",
					"url":    "https://siem.example.com/in",
					"headers": []map[string]string{
						{"name": "X-K", "value": "<redacted>"},
					},
				},
				"response_status": 202,
				"response_body":   "ok",
				"latency_ms":      42,
				"created_at":      "2026-05-07T12:00:01+00:00",
			},
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

func TestAuditForwarders_Create_RoundTrip(t *testing.T) {
	var captured struct {
		method string
		body   string
	}
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured.method = r.Method
		captured.body = string(b)
		writeForwarderResource(w, http.StatusCreated, "Datadog production", "datadog_production")
	})
	defer cleanup()

	body := `{"action":"user.created"}`
	fwd, err := c.Forwarders().Create(context.Background(), CreateForwarderInput{
		Name:          "Datadog production",
		ForwarderType: "datadog",
		HTTP: ForwarderHttp{
			Method: "POST",
			URL:    "https://siem.example.com/in",
			Headers: []HttpHeader{
				{Name: "DD-API-KEY", Value: "real-secret"},
			},
			SuccessStatus: "2xx",
			Body:          &body,
		},
		Filter:    map[string]interface{}{"==": []any{"x", "x"}},
		Transform: "$",
		Data:      map[string]interface{}{"team": "platform"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if fwd.Slug != "datadog_production" {
		t.Errorf("expected slug=datadog_production, got %q", fwd.Slug)
	}
	if captured.method != http.MethodPost {
		t.Errorf("expected POST, got %s", captured.method)
	}
}

func TestAuditForwarders_Create_NonSuccessReturnsError(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"errors":[{"status":"402"}]}`))
	})
	defer cleanup()
	_, err := c.Forwarders().Create(context.Background(), CreateForwarderInput{
		Name: "x", ForwarderType: "http",
		HTTP: ForwarderHttp{URL: "https://x", SuccessStatus: "2xx"},
	})
	if err == nil || !strings.Contains(err.Error(), "402") {
		t.Fatalf("expected 402 wrapped error, got %v", err)
	}
}

func TestAuditForwarders_List_PaginatesAndExtractsCursor(t *testing.T) {
	calls := 0
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":[{"id":"` + fwdIDStr + `","type":"forwarder","attributes":{"name":"A","slug":"a","forwarder_type":"http","enabled":true,"http":{"url":"https://x"}}}],"links":{"next":"/api/v1/forwarders?page[size]=1&page[after]=tok-2"},"meta":{"page_size":1}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":[{"id":"` + fwdIDStr + `","type":"forwarder","attributes":{"name":"B","slug":"b","forwarder_type":"http","enabled":true,"http":{"url":"https://y"}}}],"meta":{"page_size":1}}`))
		}
	})
	defer cleanup()

	enabled := true
	first, err := c.Forwarders().List(context.Background(), ListForwardersInput{
		ForwarderType: "datadog", Enabled: &enabled, PageSize: 1,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if first.NextCursor != "tok-2" {
		t.Errorf("expected next cursor tok-2, got %q", first.NextCursor)
	}
	second, err := c.Forwarders().List(context.Background(), ListForwardersInput{PageAfter: first.NextCursor})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if second.NextCursor != "" {
		t.Errorf("expected no next cursor on last page, got %q", second.NextCursor)
	}
}

func TestAuditForwarders_Get_404Handled(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()
	_, err := c.Forwarders().Get(context.Background(), parseUUID(t, fwdIDStr))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestAuditForwarders_Get_Success(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeForwarderResource(w, http.StatusOK, "x", "x")
	})
	defer cleanup()
	fwd, err := c.Forwarders().Get(context.Background(), parseUUID(t, fwdIDStr))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fwd.Name != "x" {
		t.Errorf("expected name=x, got %q", fwd.Name)
	}
	if len(fwd.HTTP.Headers) != 1 || fwd.HTTP.Headers[0].Value != "<redacted>" {
		t.Errorf("expected redacted header, got %+v", fwd.HTTP.Headers)
	}
}

func TestAuditForwarders_Update(t *testing.T) {
	var method string
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		writeForwarderResource(w, http.StatusOK, "Renamed", "renamed")
	})
	defer cleanup()
	fwd, err := c.Forwarders().Update(context.Background(), parseUUID(t, fwdIDStr), UpdateForwarderInput{
		Name:          "Renamed",
		ForwarderType: "datadog",
		HTTP:          ForwarderHttp{URL: "https://x"},
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
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()
	if err := c.Forwarders().Delete(context.Background(), parseUUID(t, fwdIDStr)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if method != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", method)
	}
}

func TestAuditForwarders_Delete_NonSuccess(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()
	if err := c.Forwarders().Delete(context.Background(), parseUUID(t, fwdIDStr)); err == nil {
		t.Fatal("expected error on 404 delete")
	}
}

// ---------------------------------------------------------------------------
// Deliveries
// ---------------------------------------------------------------------------

func TestAuditForwarders_Deliveries_List(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"` + deliveryIDStr + `","type":"forwarder_delivery","attributes":{"forwarder_id":"` + fwdIDStr + `","event_id":"33333333-4444-5555-6666-777777777777","attempt_number":1,"status":"succeeded","response_status":202}}],"meta":{"page_size":1}}`))
	})
	defer cleanup()
	page, err := c.Forwarders().Deliveries().List(context.Background(), parseUUID(t, fwdIDStr), ListDeliveriesInput{
		Status:         ForwarderDeliverySucceeded,
		CreatedAtRange: "[2020-01-01T00:00:00Z,*)",
		PageSize:       1,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Deliveries) != 1 || page.Deliveries[0].Status != ForwarderDeliverySucceeded {
		t.Errorf("unexpected deliveries: %+v", page.Deliveries)
	}
}

func TestAuditForwarders_Deliveries_Retry(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeDeliveryResource(w, http.StatusOK, "succeeded")
	})
	defer cleanup()
	row, err := c.Forwarders().Deliveries().Actions().Retry(context.Background(),
		parseUUID(t, fwdIDStr), parseUUID(t, deliveryIDStr))
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if row.Status != ForwarderDeliverySucceeded {
		t.Errorf("expected succeeded, got %q", row.Status)
	}
}

func TestAuditForwarders_Actions_RetryFailedDeliveries(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"attempted":3,"succeeded":2,"failed":1}`))
	})
	defer cleanup()
	summary, err := c.Forwarders().Actions().RetryFailedDeliveries(context.Background(), parseUUID(t, fwdIDStr))
	if err != nil {
		t.Fatalf("RetryFailedDeliveries: %v", err)
	}
	if summary.Attempted != 3 || summary.Succeeded != 2 || summary.Failed != 1 {
		t.Errorf("unexpected summary: %+v", summary)
	}
}

func TestAuditForwarders_Actions_RetryFailed_NonSuccess(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()
	if _, err := c.Forwarders().Actions().RetryFailedDeliveries(context.Background(), parseUUID(t, fwdIDStr)); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestAuditForwarders_Deliveries_Retry_NonSuccess(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()
	if _, err := c.Forwarders().Deliveries().Actions().Retry(context.Background(),
		parseUUID(t, fwdIDStr), parseUUID(t, deliveryIDStr)); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestAuditForwarders_Deliveries_List_NonSuccess(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := c.Forwarders().Deliveries().List(context.Background(), parseUUID(t, fwdIDStr), ListDeliveriesInput{}); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestAuditForwarders_List_NonSuccess(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := c.Forwarders().List(context.Background(), ListForwardersInput{}); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestAuditForwarders_Update_NonSuccess(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()
	if _, err := c.Forwarders().Update(context.Background(), parseUUID(t, fwdIDStr), UpdateForwarderInput{
		Name: "x", ForwarderType: "http",
		HTTP: ForwarderHttp{URL: "https://x"},
	}); err == nil {
		t.Fatal("expected error on 404")
	}
}

// ---------------------------------------------------------------------------
// functions.test_forwarder.actions.Execute
// ---------------------------------------------------------------------------

func TestAuditFunctions_TestForwarder_Execute(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, r *http.Request) {
		// The proxy endpoint takes plain JSON.
		if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Errorf("expected application/json, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"succeeded":true,"response_status":202,"response_headers":{"X-Echo":"y"},"response_body":"accepted","latency_ms":12,"error":null}`))
	})
	defer cleanup()

	timeout := 5000
	body := `{"hello":"world"}`
	r, err := c.Functions().TestForwarder().Actions().Execute(context.Background(), TestForwarderInput{
		URL:           "https://siem.example.com/in",
		Method:        "POST",
		Headers:       []HttpHeader{{Name: "X-K", Value: "v"}},
		Body:          &body,
		SuccessStatus: "2xx",
		TimeoutMs:     &timeout,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !r.Succeeded || r.ResponseStatus == nil || *r.ResponseStatus != 202 {
		t.Errorf("unexpected result: %+v", r)
	}
	if r.ResponseHeaders["X-Echo"] != "y" {
		t.Errorf("expected X-Echo=y, got %q", r.ResponseHeaders["X-Echo"])
	}
	if r.ResponseBody != "accepted" {
		t.Errorf("expected accepted, got %q", r.ResponseBody)
	}
}

func TestAuditFunctions_TestForwarder_Execute_NonSuccess(t *testing.T) {
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := c.Functions().TestForwarder().Actions().Execute(context.Background(), TestForwarderInput{
		URL: "https://x",
	}); err == nil {
		t.Fatal("expected error on 500")
	}
}

// ---------------------------------------------------------------------------
// do_not_forward
// ---------------------------------------------------------------------------

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
// Transport-error paths
//
// Every wrapper has an ``if err != nil`` short-circuit on the underlying
// transport call. These tests point the SDK at a closed httptest server
// so each call returns "connection refused" and exercises that branch.
// ---------------------------------------------------------------------------

func TestAuditForwarders_Create_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if _, err := c.Forwarders().Create(context.Background(), CreateForwarderInput{
		Name: "x", ForwarderType: "http",
		HTTP: ForwarderHttp{URL: "https://x", SuccessStatus: "2xx"},
	}); err == nil || !strings.Contains(err.Error(), "Forwarders.Create") {
		t.Fatalf("expected wrapped Create transport error, got %v", err)
	}
}

func TestAuditForwarders_List_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if _, err := c.Forwarders().List(context.Background(), ListForwardersInput{}); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.List") {
		t.Fatalf("expected wrapped List transport error, got %v", err)
	}
}

func TestAuditForwarders_Get_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if _, err := c.Forwarders().Get(context.Background(), parseUUID(t, fwdIDStr)); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.Get") {
		t.Fatalf("expected wrapped Get transport error, got %v", err)
	}
}

func TestAuditForwarders_Update_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if _, err := c.Forwarders().Update(context.Background(), parseUUID(t, fwdIDStr), UpdateForwarderInput{
		Name: "x", ForwarderType: "http",
		HTTP: ForwarderHttp{URL: "https://x"},
	}); err == nil || !strings.Contains(err.Error(), "Forwarders.Update") {
		t.Fatalf("expected wrapped Update transport error, got %v", err)
	}
}

func TestAuditForwarders_Delete_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if err := c.Forwarders().Delete(context.Background(), parseUUID(t, fwdIDStr)); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.Delete") {
		t.Fatalf("expected wrapped Delete transport error, got %v", err)
	}
}

func TestAuditForwarders_Deliveries_List_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if _, err := c.Forwarders().Deliveries().List(context.Background(), parseUUID(t, fwdIDStr), ListDeliveriesInput{}); err == nil ||
		!strings.Contains(err.Error(), "Deliveries.List") {
		t.Fatalf("expected wrapped Deliveries.List transport error, got %v", err)
	}
}

func TestAuditForwarders_Deliveries_Retry_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if _, err := c.Forwarders().Deliveries().Actions().Retry(context.Background(),
		parseUUID(t, fwdIDStr), parseUUID(t, deliveryIDStr)); err == nil ||
		!strings.Contains(err.Error(), "Deliveries.Actions.Retry") {
		t.Fatalf("expected wrapped Retry transport error, got %v", err)
	}
}

func TestAuditForwarders_Actions_RetryFailed_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if _, err := c.Forwarders().Actions().RetryFailedDeliveries(context.Background(), parseUUID(t, fwdIDStr)); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.Actions.RetryFailedDeliveries") {
		t.Fatalf("expected wrapped RetryFailedDeliveries transport error, got %v", err)
	}
}

func TestAuditFunctions_TestForwarder_Execute_TransportError(t *testing.T) {
	c := newClosedAuditClient(t)
	if _, err := c.Functions().TestForwarder().Actions().Execute(context.Background(), TestForwarderInput{
		URL: "https://x",
	}); err == nil || !strings.Contains(err.Error(), "TestForwarder.Actions.Execute") {
		t.Fatalf("expected wrapped Execute transport error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Other error / branch paths
// ---------------------------------------------------------------------------

func TestAuditForwarders_Create_Empty201Body(t *testing.T) {
	// Return 201 with content type the generated client won't decode as
	// the JSON:API success body — leaves resp.ApplicationvndApiJSON201 nil.
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
	})
	defer cleanup()
	if _, err := c.Forwarders().Create(context.Background(), CreateForwarderInput{
		Name: "x", ForwarderType: "http",
		HTTP: ForwarderHttp{URL: "https://x", SuccessStatus: "2xx"},
	}); err == nil || !strings.Contains(err.Error(), "empty 201 body") {
		t.Fatalf("expected empty-201-body error, got %v", err)
	}
}

func TestAuditForwarders_Get_NonSuccessNon404(t *testing.T) {
	// 500 hits the second non-success branch (the 404 short-circuit
	// above is exercised by TestAuditForwarders_Get_404Handled).
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()
	if _, err := c.Forwarders().Get(context.Background(), parseUUID(t, fwdIDStr)); err == nil ||
		!strings.Contains(err.Error(), "Forwarders.Get failed") {
		t.Fatalf("expected wrapped 500 error, got %v", err)
	}
}

func TestAuditForwarders_Deliveries_List_AppliesPageAfterParam(t *testing.T) {
	var captured string
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page_size":1}}`))
	})
	defer cleanup()
	if _, err := c.Forwarders().Deliveries().List(context.Background(), parseUUID(t, fwdIDStr), ListDeliveriesInput{
		PageAfter: "tok-after",
	}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(captured, "page%5Bafter%5D=tok-after") &&
		!strings.Contains(captured, "page[after]=tok-after") {
		t.Errorf("expected page[after] in query, got %q", captured)
	}
}

// ---------------------------------------------------------------------------
// Helper-function branch coverage
// ---------------------------------------------------------------------------

func TestForwarderFromResource_PopulatesOptionalFields(t *testing.T) {
	// Server response that exercises the Filter / Transform / Data
	// branches in forwarderFromResource and the Body branch in
	// forwarderHttpFromWire — none of which the round-trip happy path
	// hit before.
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"` + fwdIDStr + `","type":"forwarder","attributes":{` +
			`"name":"X","slug":"x","forwarder_type":"http","enabled":true,` +
			`"http":{"url":"https://x","method":"POST","success_status":"2xx",` +
			`"body":"{\"hello\":\"world\"}","headers":[{"name":"H","value":"v"}]},` +
			`"filter":{"==":["a","a"]},"transform":"$","data":{"team":"x"}` +
			`}}}`))
	})
	defer cleanup()
	fwd, err := c.Forwarders().Get(context.Background(), parseUUID(t, fwdIDStr))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fwd.Filter == nil {
		t.Errorf("expected Filter populated, got nil")
	}
	if fwd.Transform == nil || *fwd.Transform != "$" {
		t.Errorf("expected Transform=\"$\", got %v", fwd.Transform)
	}
	if fwd.Data == nil {
		t.Errorf("expected Data populated, got nil")
	}
	if fwd.HTTP.Body == nil {
		t.Errorf("expected HTTP.Body populated, got nil")
	}
}

func TestNextCursorFromForwarderLinks_BranchCoverage(t *testing.T) {
	// page[after]= followed by &<other> — exercise the `if amp >= 0`
	// branch that trims the trailing query params.
	calls := 0
	c, cleanup := newTestAuditClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{"data":[],"links":{"next":"/api/v1/forwarders?page[after]=tok-mid&page[size]=1"},"meta":{"page_size":1}}`))
		case 2:
			// Next is set but contains no page[after]= — the function
			// must fall through to the trailing `return ""`.
			_, _ = w.Write([]byte(`{"data":[],"links":{"next":"/api/v1/forwarders?page[size]=1"},"meta":{"page_size":1}}`))
		}
	})
	defer cleanup()

	first, err := c.Forwarders().List(context.Background(), ListForwardersInput{})
	if err != nil {
		t.Fatalf("List call 1: %v", err)
	}
	if first.NextCursor != "tok-mid" {
		t.Errorf("expected cursor=tok-mid (trimmed at &), got %q", first.NextCursor)
	}

	second, err := c.Forwarders().List(context.Background(), ListForwardersInput{})
	if err != nil {
		t.Fatalf("List call 2: %v", err)
	}
	if second.NextCursor != "" {
		t.Errorf("expected empty cursor when page[after] missing, got %q", second.NextCursor)
	}
}
