package smplkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// Wire-body shape tests for the audit wrapper. These tests intercept
// the actual HTTP request the SDK posts and assert on the JSON envelope
// key by key. They guard against the failure mode that shipped
// smplkit-sdk@3.2.21 / @smplkit/sdk@3.0.19: the generated client
// compiled cleanly after the spec dropped a field, but the wrapper
// kept emitting it, and CI was none the wiser because no test
// inspected the bytes.
//
// The whitelists below come from the audit service's OpenAPI spec
// directly (openapi/audit.json: components.schemas.Event /
// .Forwarder), not from the generated client (which is itself a
// projection of the spec).

// eventPostAttrs is the whitelist of attribute keys allowed in a
// POST /api/v1/events request body. created_at, actor_*, and
// idempotency_key are readOnly.
var eventPostAttrs = map[string]bool{
	"action":         true,
	"resource_type":  true,
	"resource_id":    true,
	"occurred_at":    true,
	"data":           true,
	"do_not_forward": true,
}

// forwarderPostAttrs is the whitelist of attribute keys allowed in
// POST /api/v1/forwarders or PUT /api/v1/forwarders/{id} request
// bodies. slug is x-immutable (server-derived); created_at,
// updated_at, deleted_at, and version are readOnly.
var forwarderPostAttrs = map[string]bool{
	"name":           true,
	"forwarder_type": true,
	"http":           true,
	"enabled":        true,
	"filter":         true,
	"transform":      true,
	"data":           true,
}

// capturedRequest holds the method, path, headers, and parsed body of
// the request the audit client posted to the test server.
type capturedRequest struct {
	method  string
	path    string
	headers http.Header
	body    map[string]any
}

func newCapturingClient(t *testing.T, status int, responseBody []byte) (*AuditClient, *capturedRequest, func()) {
	t.Helper()
	captured := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &captured.body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write(responseBody)
	}))
	c := newAuditClientPointingAt(t, srv.URL)
	cleanup := func() {
		c.events.close()
		srv.Close()
	}
	return c, captured, cleanup
}

func extractAttributes(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("body.data missing or wrong type: %#v", body["data"])
	}
	attrs, ok := data["attributes"].(map[string]any)
	if !ok {
		t.Fatalf("body.data.attributes missing or wrong type: %#v", data["attributes"])
	}
	return attrs
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fixtures ------------------------------------------------------------------

const eventResponseBytes = `{
  "data": {
    "id": "00000000-0000-0000-0000-000000000001",
    "type": "event",
    "attributes": {
      "action": "invoice.created",
      "resource_type": "invoice",
      "resource_id": "inv-1",
      "occurred_at": "2026-05-06T12:00:00Z",
      "created_at": "2026-05-06T12:00:01Z",
      "actor_type": "API_KEY",
      "actor_id": null,
      "actor_label": "",
      "data": {},
      "idempotency_key": "k-1"
    }
  }
}`

func forwarderResponseBytes(name string) []byte {
	body := map[string]any{
		"data": map[string]any{
			"id":   fwdIDStr,
			"type": "forwarder",
			"attributes": map[string]any{
				"name":           name,
				"slug":           "x",
				"forwarder_type": "datadog",
				"enabled":        true,
				"http": map[string]any{
					"method":         "POST",
					"url":            "https://siem.example.com/in",
					"headers":        []map[string]string{{"name": "DD-API-KEY", "value": "<redacted>"}},
					"success_status": "2xx",
				},
				"data":       map[string]any{},
				"created_at": "2026-05-07T12:00:00+00:00",
				"updated_at": "2026-05-07T12:00:00+00:00",
				"version":    1,
			},
		},
	}
	out, _ := json.Marshal(body)
	return out
}

// ---------------------------------------------------------------------------
// events.Record — wire body shape
// ---------------------------------------------------------------------------

func TestAuditEventsRecord_WireShape_AllParameters(t *testing.T) {
	c, captured, cleanup := newCapturingClient(t, http.StatusCreated, []byte(eventResponseBytes))
	defer cleanup()

	occurred := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	if err := c.Events().Record(CreateEventInput{
		Action:       "invoice.created",
		ResourceType: "invoice",
		ResourceID:   "inv-1",
		OccurredAt:   &occurred,
		Data: map[string]interface{}{
			"snapshot": map[string]interface{}{"total_cents": 4900},
			"req_id":   "abc",
		},
		IdempotencyKey: "k-1",
		DoNotForward:   true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	c.Events().Flush(2 * time.Second)

	if captured.body == nil {
		t.Fatal("server never received a body")
	}

	// Envelope.
	if got := sortedKeys(captured.body); len(got) != 1 || got[0] != "data" {
		t.Fatalf("expected only top-level 'data', got %v", got)
	}
	data := captured.body["data"].(map[string]any)
	if data["type"] != "event" {
		t.Fatalf("expected type=event, got %v", data["type"])
	}
	// ID is empty on POST -- server assigns.
	if data["id"] != "" {
		t.Fatalf("expected empty id on POST, got %v", data["id"])
	}

	attrs := extractAttributes(t, captured.body)
	if attrs["action"] != "invoice.created" {
		t.Errorf("action=%v", attrs["action"])
	}
	if attrs["resource_type"] != "invoice" {
		t.Errorf("resource_type=%v", attrs["resource_type"])
	}
	if attrs["resource_id"] != "inv-1" {
		t.Errorf("resource_id=%v", attrs["resource_id"])
	}
	if attrs["occurred_at"] != "2026-05-06T12:00:00Z" {
		t.Errorf("occurred_at=%v (want 2026-05-06T12:00:00Z)", attrs["occurred_at"])
	}
	wantData := map[string]any{
		"snapshot": map[string]any{"total_cents": float64(4900)},
		"req_id":   "abc",
	}
	gotData, _ := json.Marshal(attrs["data"])
	wantBytes, _ := json.Marshal(wantData)
	if string(gotData) != string(wantBytes) {
		t.Errorf("data mismatch:\n got %s\nwant %s", gotData, wantBytes)
	}
	if attrs["do_not_forward"] != true {
		t.Errorf("do_not_forward=%v", attrs["do_not_forward"])
	}

	// Idempotency-Key is a HEADER, not a body attribute.
	if _, ok := attrs["idempotency_key"]; ok {
		t.Errorf("idempotency_key should NOT be in body, found: %v", attrs["idempotency_key"])
	}
	if got := captured.headers.Get("Idempotency-Key"); got != "k-1" {
		t.Errorf("Idempotency-Key header=%q (want k-1)", got)
	}
}

func TestAuditEventsRecord_WireShape_MinimalCallOmitsOptionals(t *testing.T) {
	c, captured, cleanup := newCapturingClient(t, http.StatusCreated, []byte(eventResponseBytes))
	defer cleanup()

	if err := c.Events().Record(CreateEventInput{
		Action:       "invoice.created",
		ResourceType: "invoice",
		ResourceID:   "inv-1",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	c.Events().Flush(2 * time.Second)

	attrs := extractAttributes(t, captured.body)
	got := sortedKeys(attrs)
	want := []string{"action", "resource_id", "resource_type"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("expected minimal attrs %v, got %v", want, got)
	}
}

func TestAuditEventsRecord_WireShape_DoNotForwardFalseIsOmitted(t *testing.T) {
	c, captured, cleanup := newCapturingClient(t, http.StatusCreated, []byte(eventResponseBytes))
	defer cleanup()

	if err := c.Events().Record(CreateEventInput{
		Action:       "x",
		ResourceType: "y",
		ResourceID:   "z",
		DoNotForward: false,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	c.Events().Flush(2 * time.Second)

	attrs := extractAttributes(t, captured.body)
	if _, present := attrs["do_not_forward"]; present {
		t.Errorf("do_not_forward=false should be omitted, but found: %v", attrs["do_not_forward"])
	}
}

func TestAuditEventsRecord_WireShape_NoTopLevelSnapshot(t *testing.T) {
	// Regression guard for the smplkit-sdk@3.2.21 incident. Even when
	// the caller nests a snapshot inside Data, the wrapper must NOT lift
	// it to a top-level snapshot attribute.
	c, captured, cleanup := newCapturingClient(t, http.StatusCreated, []byte(eventResponseBytes))
	defer cleanup()

	if err := c.Events().Record(CreateEventInput{
		Action:       "invoice.created",
		ResourceType: "invoice",
		ResourceID:   "inv-1",
		Data:         map[string]interface{}{"snapshot": map[string]interface{}{"total_cents": 4900}},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	c.Events().Flush(2 * time.Second)

	attrs := extractAttributes(t, captured.body)
	if _, present := attrs["snapshot"]; present {
		t.Errorf("top-level 'snapshot' must not appear on the wire; found: %v", attrs["snapshot"])
	}
	// And it IS still nested in data.
	dataAttr, _ := attrs["data"].(map[string]any)
	if dataAttr == nil {
		t.Fatal("data attribute missing from wire body")
	}
	if _, ok := dataAttr["snapshot"]; !ok {
		t.Error("data.snapshot should round-trip; not found")
	}
}

func TestAuditEventsRecord_WireShape_NoExtraKeys(t *testing.T) {
	c, captured, cleanup := newCapturingClient(t, http.StatusCreated, []byte(eventResponseBytes))
	defer cleanup()

	occurred := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	if err := c.Events().Record(CreateEventInput{
		Action:         "invoice.created",
		ResourceType:   "invoice",
		ResourceID:     "inv-1",
		OccurredAt:     &occurred,
		Data:           map[string]interface{}{"k": "v"},
		IdempotencyKey: "k-1",
		DoNotForward:   true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	c.Events().Flush(2 * time.Second)

	attrs := extractAttributes(t, captured.body)
	for k := range attrs {
		if !eventPostAttrs[k] {
			t.Errorf("undocumented field on the wire: %q", k)
		}
	}
}

// ---------------------------------------------------------------------------
// forwarders.Create — wire body shape
// ---------------------------------------------------------------------------

func TestAuditForwardersCreate_WireShape_AllParameters(t *testing.T) {
	c, captured, cleanup := newCapturingClient(t, http.StatusCreated, forwarderResponseBytes("Datadog production"))
	defer cleanup()

	body := `{"action":"user.created"}`
	_, err := c.Forwarders().Create(context.Background(), CreateForwarderInput{
		Name:          "Datadog production",
		ForwarderType: "datadog",
		HTTP: ForwarderHttp{
			Method:        "POST",
			URL:           "https://siem.example.com/in",
			Headers:       []HttpHeader{{Name: "DD-API-KEY", Value: "real-secret"}},
			Body:          &body,
			SuccessStatus: "2xx",
		},
		Enabled:   false,
		Filter:    map[string]interface{}{"==": []any{"action", "user.created"}},
		Transform: "$",
		Data:      map[string]interface{}{"team": "platform"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if captured.method != http.MethodPost {
		t.Fatalf("method=%s want POST", captured.method)
	}
	data := captured.body["data"].(map[string]any)
	if data["type"] != "forwarder" {
		t.Errorf("type=%v want forwarder", data["type"])
	}
	if data["id"] != "" {
		t.Errorf("expected empty id on POST, got %v", data["id"])
	}

	attrs := extractAttributes(t, captured.body)
	if attrs["name"] != "Datadog production" {
		t.Errorf("name=%v", attrs["name"])
	}
	if attrs["forwarder_type"] != "datadog" {
		t.Errorf("forwarder_type=%v", attrs["forwarder_type"])
	}
	if attrs["enabled"] != false {
		t.Errorf("enabled=%v want false", attrs["enabled"])
	}
	if attrs["transform"] != "$" {
		t.Errorf("transform=%v", attrs["transform"])
	}

	gotData, _ := json.Marshal(attrs["data"])
	if string(gotData) != `{"team":"platform"}` {
		t.Errorf("data=%s want {\"team\":\"platform\"}", gotData)
	}

	httpAttr, _ := attrs["http"].(map[string]any)
	if httpAttr == nil {
		t.Fatal("http attribute missing from wire body")
	}
	if httpAttr["url"] != "https://siem.example.com/in" {
		t.Errorf("http.url=%v", httpAttr["url"])
	}
	headers, _ := httpAttr["headers"].([]any)
	if len(headers) != 1 {
		t.Fatalf("expected 1 header, got %d", len(headers))
	}
	hdr := headers[0].(map[string]any)
	if hdr["name"] != "DD-API-KEY" || hdr["value"] != "real-secret" {
		t.Errorf("header=%v want {DD-API-KEY: real-secret}", hdr)
	}

	// Read-only / immutable fields MUST NOT appear on the wire.
	for _, ro := range []string{"slug", "created_at", "updated_at", "deleted_at", "version"} {
		if _, present := attrs[ro]; present {
			t.Errorf("read-only field %q should not appear on the wire", ro)
		}
	}
}

func TestAuditForwardersCreate_WireShape_NoExtraKeys(t *testing.T) {
	c, captured, cleanup := newCapturingClient(t, http.StatusCreated, forwarderResponseBytes("x"))
	defer cleanup()

	if _, err := c.Forwarders().Create(context.Background(), CreateForwarderInput{
		Name:          "Datadog production",
		ForwarderType: "datadog",
		HTTP:          ForwarderHttp{URL: "https://x"},
		Enabled:       true,
		Filter:        map[string]interface{}{"x": 1},
		Transform:     "$",
		Data:          map[string]interface{}{"k": "v"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	attrs := extractAttributes(t, captured.body)
	for k := range attrs {
		if !forwarderPostAttrs[k] {
			t.Errorf("undocumented field on the wire: %q", k)
		}
	}
}

// ---------------------------------------------------------------------------
// forwarders.Update — wire body shape
// ---------------------------------------------------------------------------

func TestAuditForwardersUpdate_WireShape_AllParameters(t *testing.T) {
	c, captured, cleanup := newCapturingClient(t, http.StatusOK, forwarderResponseBytes("Renamed"))
	defer cleanup()

	fwdID := parseUUID(t, fwdIDStr)
	_, err := c.Forwarders().Update(context.Background(), fwdID, UpdateForwarderInput{
		Name:          "Renamed",
		ForwarderType: "datadog",
		HTTP: ForwarderHttp{
			URL:     "https://siem.example.com/in",
			Headers: []HttpHeader{{Name: "X-K", Value: "real-secret"}},
		},
		Enabled:   false,
		Filter:    map[string]interface{}{"==": []any{1, 1}},
		Transform: "$",
		Data:      map[string]interface{}{"k": "v"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if captured.method != http.MethodPut {
		t.Fatalf("method=%s want PUT", captured.method)
	}
	data := captured.body["data"].(map[string]any)
	// On PUT, the wrapper echoes the path id into the envelope id.
	if data["id"] != fwdIDStr {
		t.Errorf("id=%v want %s", data["id"], fwdIDStr)
	}

	attrs := extractAttributes(t, captured.body)
	if attrs["name"] != "Renamed" {
		t.Errorf("name=%v want Renamed", attrs["name"])
	}
	if attrs["enabled"] != false {
		t.Errorf("enabled=%v want false", attrs["enabled"])
	}
	httpAttr, _ := attrs["http"].(map[string]any)
	headers, _ := httpAttr["headers"].([]any)
	hdr, _ := headers[0].(map[string]any)
	// Headers carry the real plaintext value the caller supplied — the
	// wrapper does NOT round-trip the redacted GET response.
	if hdr["value"] != "real-secret" {
		t.Errorf("header value=%v want real-secret", hdr["value"])
	}

	for _, ro := range []string{"slug", "created_at", "updated_at", "deleted_at", "version"} {
		if _, present := attrs[ro]; present {
			t.Errorf("read-only field %q should not appear on the wire", ro)
		}
	}
}

func TestAuditForwardersUpdate_WireShape_NoExtraKeys(t *testing.T) {
	c, captured, cleanup := newCapturingClient(t, http.StatusOK, forwarderResponseBytes("Renamed"))
	defer cleanup()

	fwdID := parseUUID(t, fwdIDStr)
	if _, err := c.Forwarders().Update(context.Background(), fwdID, UpdateForwarderInput{
		Name:          "x",
		ForwarderType: "http",
		HTTP:          ForwarderHttp{URL: "https://x"},
		Enabled:       true,
		Filter:        map[string]interface{}{"x": 1},
		Transform:     "$",
		Data:          map[string]interface{}{"k": "v"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	attrs := extractAttributes(t, captured.body)
	for k := range attrs {
		if !forwarderPostAttrs[k] {
			t.Errorf("undocumented field on the wire: %q", k)
		}
	}
}
