package smplkit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	genjobs "github.com/smplkit/go-sdk/v3/internal/generated/jobs"
)

const (
	testJobID = "showcase-mgmt-abcd1234"
	testRunID = "8f2b1c4a-0000-4a1b-9c3d-1e2f3a4b5c6d"
)

var nowForTest = time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

// newTestJobs wires a JobsManagement wrapper against an httptest server.
func newTestJobs(t *testing.T, handler http.HandlerFunc) (*JobsManagement, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genjobs.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genjobs.NewClient: %v", err)
	}
	withResp := &genjobs.ClientWithResponses{ClientInterface: gen}
	j := &JobsManagement{gen: withResp, runs: &RunsClient{gen: withResp}}
	return j, func() { srv.Close() }
}

// newClosedJobs returns a JobsManagement whose backing server has been
// closed, exercising transport-error branches.
func newClosedJobs(t *testing.T) *JobsManagement {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close()
	gen, _ := genjobs.NewClient(url)
	withResp := &genjobs.ClientWithResponses{ClientInterface: gen}
	return &JobsManagement{gen: withResp, runs: &RunsClient{gen: withResp}}
}

func jobResource(id string, created bool, version int, enabled bool) map[string]any {
	attrs := map[string]any{
		"name":        "My Job",
		"description": "does a thing",
		"enabled":     enabled,
		"type":        "http",
		"schedule":    "0 * * * *",
		"configuration": map[string]any{
			"method":         "POST",
			"url":            "https://api.example.com/hook",
			"headers":        []map[string]string{{"name": "X-Api-Key", "value": "secret"}},
			"body":           "{}",
			"success_status": "2xx",
			"timeout":        30,
			"tls_verify":     true,
			"ca_cert":        nil,
		},
		"concurrency_policy": "ALLOW",
		"next_run_at":        "2026-06-05T00:00:00Z",
		"deleted_at":         nil,
		"version":            version,
	}
	if created {
		attrs["created_at"] = "2026-06-04T00:00:00Z"
		attrs["updated_at"] = "2026-06-04T00:00:00Z"
	}
	return map[string]any{"id": id, "type": "job", "attributes": attrs}
}

func runResource(runID, status, trigger string, rerunOf *string) map[string]any {
	attrs := map[string]any{
		"job":                 testJobID,
		"job_version":         1,
		"trigger":             trigger,
		"rerun_of":            nil,
		"scheduled_for":       "2026-06-05T00:00:00Z",
		"status":              status,
		"started_at":          "2026-06-05T00:00:00Z",
		"finished_at":         "2026-06-05T00:00:00Z",
		"pending_duration_ms": 100,
		"run_duration_ms":     300,
		"total_duration_ms":   400,
		"failure_reason":      nil,
		"error":               nil,
		"request":             map[string]any{"method": "POST", "url": "https://api.example.com/hook"},
		"result":              map[string]any{"status": 200},
		"created_at":          "2026-06-05T00:00:00Z",
	}
	if rerunOf != nil {
		attrs["rerun_of"] = *rerunOf
	}
	return map[string]any{"id": runID, "type": "run", "attributes": attrs}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/vnd.api+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// fullHandler routes every jobs endpoint to a canned response, mirroring
// the showcase's lifecycle.
func fullHandler(w http.ResponseWriter, r *http.Request) {
	m, path := r.Method, r.URL.Path
	switch {
	case path == "/api/v1/jobs" && m == "POST":
		writeJSON(w, 201, map[string]any{"data": jobResource(testJobID, true, 1, false)})
	case path == "/api/v1/jobs" && m == "GET":
		writeJSON(w, 200, map[string]any{
			"data": []any{jobResource("a", true, 1, true), jobResource("b", true, 1, true)},
			"meta": map[string]any{"pagination": map[string]any{"page": 1, "size": 50}},
		})
	case path == "/api/v1/jobs/"+testJobID+"/actions/run":
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "PENDING", "MANUAL", nil)})
	case path == "/api/v1/jobs/"+testJobID && m == "GET":
		writeJSON(w, 200, map[string]any{"data": jobResource(testJobID, true, 1, false)})
	case path == "/api/v1/jobs/"+testJobID && m == "PUT":
		writeJSON(w, 200, map[string]any{"data": jobResource(testJobID, true, 2, true)})
	case path == "/api/v1/jobs/"+testJobID && m == "DELETE":
		w.WriteHeader(204)
	case path == "/api/v1/usage":
		writeJSON(w, 200, map[string]any{"data": map[string]any{
			"id": "current", "type": "usage",
			"attributes": map[string]any{
				"period": "2026-06", "runs_used": 12, "runs_included": 3000,
				"active_jobs": 2, "active_jobs_limit": 10,
			},
		}})
	case path == "/api/v1/runs" && m == "GET":
		writeJSON(w, 200, map[string]any{
			"data": []any{runResource(testRunID, "SUCCEEDED", "SCHEDULE", nil)},
			"meta": map[string]any{"page_size": 50},
		})
	case path == "/api/v1/runs/"+testRunID+"/actions/cancel":
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "CANCELED", "RERUN", strPtr(testRunID))})
	case path == "/api/v1/runs/"+testRunID+"/actions/rerun":
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "PENDING", "RERUN", strPtr(testRunID))})
	case path == "/api/v1/runs/"+testRunID && m == "GET":
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "SUCCEEDED", "SCHEDULE", nil)})
	default:
		http.Error(w, "unexpected "+m+" "+path, http.StatusInternalServerError)
	}
}

func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------------

func TestManagementClient_JobsAccessor(t *testing.T) {
	mgmt, err := NewManagementClient(ManagementConfig{APIKey: "sk_api_test"})
	if err != nil {
		t.Fatalf("NewManagementClient: %v", err)
	}
	if mgmt.Jobs() == nil {
		t.Fatal("Jobs() returned nil")
	}
	if mgmt.Jobs().Runs() == nil {
		t.Fatal("Jobs().Runs() returned nil")
	}
}

func TestClient_ManageJobsAccessor(t *testing.T) {
	c, err := NewClient(Config{APIKey: "sk_api_test", Environment: "dev", Service: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if c.Manage().Jobs() == nil {
		t.Fatal("Manage().Jobs() returned nil")
	}
	if c.Manage().Jobs().Runs() == nil {
		t.Fatal("Manage().Jobs().Runs() returned nil")
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle (mirrors the showcase)
// ---------------------------------------------------------------------------

func TestJobs_Lifecycle(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()

	body := `{"scope": "all"}`
	tlsVerify := true
	caCert := "PEM"
	job := j.New(testJobID, "Nightly cache warm", "0 2 * * *", HttpConfig{
		Method:        JobHttpMethodPost,
		URL:           "https://api.example.com/cache/warm",
		Headers:       []HttpHeader{{Name: "Authorization", Value: "Bearer s3cr3t"}},
		Body:          &body,
		SuccessStatus: "2xx",
		Timeout:       30,
		TlsVerify:     &tlsVerify,
		CaCert:        &caCert,
	}, WithJobEnabled(false), WithJobDescription("desc"), WithJobConcurrencyPolicy("ALLOW"))

	if job.CreatedAt != nil {
		t.Fatal("unsaved job should have nil CreatedAt")
	}
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	if job.CreatedAt == nil || job.Version == nil || *job.Version != 1 {
		t.Fatalf("after create: version=%v createdAt=%v", job.Version, job.CreatedAt)
	}

	fetched, err := j.Get(ctx, testJobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.Configuration.URL != "https://api.example.com/hook" {
		t.Errorf("config url mismatch: %s", fetched.Configuration.URL)
	}
	if fetched.Configuration.Body == nil || *fetched.Configuration.Body != "{}" {
		t.Errorf("config body mismatch: %v", fetched.Configuration.Body)
	}
	if fetched.Configuration.Timeout != 30 || fetched.Configuration.SuccessStatus != "2xx" {
		t.Errorf("config timeout/success mismatch: %+v", fetched.Configuration)
	}
	if fetched.Configuration.TlsVerify == nil || !*fetched.Configuration.TlsVerify {
		t.Errorf("tls_verify mismatch: %v", fetched.Configuration.TlsVerify)
	}
	if len(fetched.Configuration.Headers) != 1 {
		t.Errorf("headers mismatch: %+v", fetched.Configuration.Headers)
	}

	disabled := false
	jobs, err := j.List(ctx, ListJobsInput{Enabled: &disabled, PageNumber: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	job.Name = "renamed"
	job.Schedule = "30 2 * * *"
	job.Enabled = true
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	if job.Version == nil || *job.Version != 2 || !job.Enabled {
		t.Fatalf("after update: version=%v enabled=%t", job.Version, job.Enabled)
	}

	run, err := j.Run(ctx, testJobID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Trigger != "MANUAL" || run.Job != testJobID {
		t.Errorf("run mismatch: trigger=%s job=%s", run.Trigger, run.Job)
	}

	usage, err := j.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.RunsUsed != 12 || usage.ActiveJobs != 2 {
		t.Errorf("usage mismatch: %+v", usage)
	}

	runs, err := j.Runs().List(ctx, ListRunsInput{Job: testJobID, PageSize: 2, After: "cur"})
	if err != nil {
		t.Fatalf("Runs.List: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "SUCCEEDED" {
		t.Errorf("runs list mismatch: %+v", runs)
	}
	if runs[0].Request == nil || runs[0].Result == nil {
		t.Errorf("run request/result not surfaced: %+v", runs[0])
	}
	if runs[0].TotalDurationMs == nil || *runs[0].TotalDurationMs != 400 {
		t.Errorf("run total duration mismatch: %v", runs[0].TotalDurationMs)
	}

	got, err := j.Runs().Get(ctx, testRunID)
	if err != nil {
		t.Fatalf("Runs.Get: %v", err)
	}
	if got.ID != testRunID {
		t.Errorf("run id mismatch: %s", got.ID)
	}

	rerun, err := j.Runs().Rerun(ctx, testRunID)
	if err != nil {
		t.Fatalf("Runs.Rerun: %v", err)
	}
	if rerun.Trigger != "RERUN" || rerun.RerunOf == nil || *rerun.RerunOf != testRunID {
		t.Errorf("rerun mismatch: trigger=%s rerunOf=%v", rerun.Trigger, rerun.RerunOf)
	}

	canceled, err := j.Runs().Cancel(ctx, testRunID)
	if err != nil {
		t.Fatalf("Runs.Cancel: %v", err)
	}
	if canceled.Status != "CANCELED" {
		t.Errorf("expected CANCELED, got %s", canceled.Status)
	}

	if err := job.Delete(ctx); err != nil {
		t.Fatalf("job.Delete: %v", err)
	}
	if err := j.Delete(ctx, testJobID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Defaults / construction variants
// ---------------------------------------------------------------------------

func TestJobs_NewDefaults(t *testing.T) {
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()
	job := j.New("id", "n", "now", HttpConfig{URL: "https://x"})
	if !job.Enabled || job.Type != "http" || job.ConcurrencyPolicy != "ALLOW" {
		t.Errorf("unexpected defaults: %+v", job)
	}
	if job.Description != nil {
		t.Errorf("expected nil description, got %v", job.Description)
	}
}

func TestJobs_MinimalConfigWire(t *testing.T) {
	// A configuration with no optional fields set must round-trip without
	// panicking and without emitting the optional wire fields.
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()
	job := j.New("id", "n", "now", HttpConfig{URL: "https://x"})
	if err := job.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// jobFromResource: zero-value optional attributes (nil enabled/type/policy,
// no id) must fall back to defaults.
func TestJobs_FromResourceDefaults(t *testing.T) {
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{
			"type":       "job",
			"attributes": map[string]any{"name": "n", "schedule": "now", "configuration": map[string]any{"url": "https://x"}},
		}})
	})
	defer cleanup()
	job, err := j.Get(context.Background(), "id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.ID != "" || job.Type != "http" || job.ConcurrencyPolicy != "ALLOW" || job.Enabled {
		t.Errorf("unexpected defaults from minimal resource: %+v", job)
	}
}

// A configuration that carries ca_cert on the wire must surface it.
func TestJobs_FromResourceCaCert(t *testing.T) {
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{
			"id": "id", "type": "job",
			"attributes": map[string]any{
				"name": "n", "schedule": "now",
				"configuration": map[string]any{
					"url":     "https://x",
					"ca_cert": "-----BEGIN CERTIFICATE-----\nPEM\n-----END CERTIFICATE-----",
				},
			},
		}})
	})
	defer cleanup()
	job, err := j.Get(context.Background(), "id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Configuration.CaCert == nil || *job.Configuration.CaCert == "" {
		t.Errorf("expected ca_cert surfaced, got %v", job.Configuration.CaCert)
	}
}

// run with failure_reason set must surface it as a string pointer.
func TestJobs_RunFailureReason(t *testing.T) {
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{
			"id": testRunID, "type": "run",
			"attributes": map[string]any{
				"job": testJobID, "trigger": "SCHEDULE", "status": "FAILED",
				"failure_reason": "TIMEOUT", "error": "deadline exceeded",
			},
		}})
	})
	defer cleanup()
	run, err := j.Runs().Get(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if run.FailureReason == nil || *run.FailureReason != "TIMEOUT" {
		t.Errorf("failure reason mismatch: %v", run.FailureReason)
	}
	if run.Error == nil || *run.Error != "deadline exceeded" {
		t.Errorf("error mismatch: %v", run.Error)
	}
}

// ---------------------------------------------------------------------------
// Active-record guards
// ---------------------------------------------------------------------------

func TestJobs_UnsavedGuards(t *testing.T) {
	ctx := context.Background()
	job := &Job{ID: "x", Name: "X", Schedule: "now"}
	if err := job.Save(ctx); err == nil {
		t.Error("Save without client should error")
	}
	if err := job.Delete(ctx); err == nil {
		t.Error("Delete without client should error")
	}

	// Bound client but empty id → Delete guard.
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()
	noID := &Job{Name: "X", Schedule: "now", client: j}
	if err := noID.Delete(ctx); err == nil {
		t.Error("Delete with empty id should error")
	}
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

func TestJobs_ErrorMapping(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			writeJSON(w, 404, map[string]any{"errors": []any{map[string]any{"detail": "missing"}}})
			return
		}
		writeJSON(w, 409, map[string]any{"errors": []any{map[string]any{"detail": "dup"}}})
	})
	defer cleanup()

	var notFound *NotFoundError
	if _, err := j.Get(ctx, "missing"); !errors.As(err, &notFound) {
		t.Errorf("expected NotFoundError, got %v", err)
	}

	var conflict *ConflictError
	job := j.New("dup", "D", "now", HttpConfig{URL: "https://x"})
	if err := job.Save(ctx); !errors.As(err, &conflict) {
		t.Errorf("expected ConflictError on create, got %v", err)
	}
}

// Non-2xx on every endpoint maps through checkStatus.
func TestJobs_Non2xxBranches(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 500, map[string]any{"errors": []any{map[string]any{"detail": "boom"}}})
	})
	defer cleanup()

	if _, err := j.List(ctx, ListJobsInput{}); err == nil {
		t.Error("List should error on 500")
	}
	if err := j.Delete(ctx, testJobID); err == nil {
		t.Error("Delete should error on 500")
	}
	if _, err := j.Run(ctx, testJobID); err == nil {
		t.Error("Run should error on 500")
	}
	if _, err := j.Usage(ctx); err == nil {
		t.Error("Usage should error on 500")
	}
	if _, err := j.Runs().List(ctx, ListRunsInput{}); err == nil {
		t.Error("Runs.List should error on 500")
	}
	if _, err := j.Runs().Get(ctx, testRunID); err == nil {
		t.Error("Runs.Get should error on 500")
	}
	if _, err := j.Runs().Cancel(ctx, testRunID); err == nil {
		t.Error("Runs.Cancel should error on 500")
	}
	if _, err := j.Runs().Rerun(ctx, testRunID); err == nil {
		t.Error("Runs.Rerun should error on 500")
	}
	// update branch (existing job → PUT)
	job := j.New(testJobID, "n", "now", HttpConfig{URL: "https://x"})
	job.CreatedAt = &nowForTest
	if err := job.Save(ctx); err == nil {
		t.Error("Save (update) should error on 500")
	}
}

// Create that returns a 2xx-but-not-201 status maps to an "unexpected
// status" error (no body to apply).
func TestJobs_CreateUnexpectedStatus(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // not 201, and not an error code
	})
	defer cleanup()
	job := j.New(testJobID, "n", "now", HttpConfig{URL: "https://x"})
	if err := job.Save(ctx); err == nil {
		t.Error("expected error when create returns non-201 2xx")
	}
}

// Create that returns 201 with a non-JSON Content-Type leaves the 201
// slot nil (the generated decoder only fills it for JSON) → "empty 201
// body".
func TestJobs_CreateEmpty201Body(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("ok"))
	})
	defer cleanup()
	job := j.New(testJobID, "n", "now", HttpConfig{URL: "https://x"})
	if err := job.Save(ctx); err == nil {
		t.Error("expected error when create 201 body is empty")
	}
}

// Invalid run-id strings short-circuit before any HTTP call.
func TestJobs_InvalidRunID(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()

	var ve *ValidationError
	if _, err := j.Runs().Get(ctx, "not-a-uuid"); !errors.As(err, &ve) {
		t.Errorf("Get: expected ValidationError, got %v", err)
	}
	if _, err := j.Runs().Cancel(ctx, "not-a-uuid"); !errors.As(err, &ve) {
		t.Errorf("Cancel: expected ValidationError, got %v", err)
	}
	if _, err := j.Runs().Rerun(ctx, "not-a-uuid"); !errors.As(err, &ve) {
		t.Errorf("Rerun: expected ValidationError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Wiring closures (header editors hit the wire)
// ---------------------------------------------------------------------------

// Exercise the jobs headerEditor closure in NewManagementClient by making
// a real list call against an httptest server.
func TestJobs_ManagementHeaderEditorExercised(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.api+json" {
			t.Errorf("missing Accept header: %q", r.Header.Get("Accept"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent header")
		}
		writeJSON(w, 200, map[string]any{"data": []any{}, "meta": map[string]any{"pagination": map[string]any{"page": 1, "size": 50}}})
	}))
	t.Cleanup(server.Close)

	mgmt, err := NewManagementClient(ManagementConfig{APIKey: "sk_test"}, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewManagementClient: %v", err)
	}
	defer func() { _ = mgmt.Close() }()

	jobs, err := mgmt.Jobs().List(context.Background(), ListJobsInput{})
	if err != nil {
		t.Fatalf("Jobs.List: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected empty list, got %d", len(jobs))
	}
}

// Exercise the runtime-path jobs editor closures (extra-headers + standard
// headers) by constructing a runtime Client and making a real jobs call.
func TestJobs_RuntimeEditorsExercised(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		writeJSON(w, 200, map[string]any{"data": []any{}, "meta": map[string]any{"pagination": map[string]any{"page": 1, "size": 50}}})
	}))
	defer server.Close()

	c, err := NewClient(
		Config{
			APIKey:           "sk_test_key",
			Environment:      "test",
			Service:          "test-svc",
			DisableTelemetry: true,
			ExtraHeaders:     map[string]string{"X-Extra": "1"},
		},
		WithBaseURL(server.URL),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Manage().Jobs().List(context.Background(), ListJobsInput{}); err != nil {
		t.Fatalf("Jobs.List: %v", err)
	}
	if seen.Get("X-Extra") != "1" {
		t.Errorf("extra header missing: %q", seen.Get("X-Extra"))
	}
	if seen.Get("Accept") != "application/vnd.api+json" {
		t.Errorf("Accept header missing: %q", seen.Get("Accept"))
	}
}

// ---------------------------------------------------------------------------
// Transport errors (closed server)
// ---------------------------------------------------------------------------

func TestJobs_TransportErrors(t *testing.T) {
	ctx := context.Background()
	j := newClosedJobs(t)

	if _, err := j.List(ctx, ListJobsInput{}); err == nil {
		t.Error("List should error on transport failure")
	}
	if _, err := j.Get(ctx, testJobID); err == nil {
		t.Error("Get should error on transport failure")
	}
	if err := j.Delete(ctx, testJobID); err == nil {
		t.Error("Delete should error on transport failure")
	}
	if _, err := j.Run(ctx, testJobID); err == nil {
		t.Error("Run should error on transport failure")
	}
	if _, err := j.Usage(ctx); err == nil {
		t.Error("Usage should error on transport failure")
	}
	if _, err := j.Runs().List(ctx, ListRunsInput{}); err == nil {
		t.Error("Runs.List should error on transport failure")
	}
	if _, err := j.Runs().Get(ctx, testRunID); err == nil {
		t.Error("Runs.Get should error on transport failure")
	}
	if _, err := j.Runs().Cancel(ctx, testRunID); err == nil {
		t.Error("Runs.Cancel should error on transport failure")
	}
	if _, err := j.Runs().Rerun(ctx, testRunID); err == nil {
		t.Error("Runs.Rerun should error on transport failure")
	}
	// create + update transport errors
	job := j.New(testJobID, "n", "now", HttpConfig{URL: "https://x"})
	if err := job.Save(ctx); err == nil {
		t.Error("Save (create) should error on transport failure")
	}
	job.CreatedAt = &nowForTest
	if err := job.Save(ctx); err == nil {
		t.Error("Save (update) should error on transport failure")
	}
}
