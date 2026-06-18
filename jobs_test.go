package smplkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	genjobs "github.com/smplkit/go-sdk/v3/internal/generated/jobs"
)

const (
	testJobID = "showcase-mgmt-abcd1234"
	testRunID = "8f2b1c4a-0000-4a1b-9c3d-1e2f3a4b5c6d"
)

var nowForTest = time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

// newTestJobs wires a JobsClient (no configured environment) against an
// httptest server.
func newTestJobs(t *testing.T, handler http.HandlerFunc) (*JobsClient, func()) {
	t.Helper()
	return newTestJobsEnv(t, "", handler)
}

// newTestJobsEnv wires a JobsClient with the given configured environment
// against an httptest server.
func newTestJobsEnv(t *testing.T, environment string, handler http.HandlerFunc) (*JobsClient, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gen, err := genjobs.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genjobs.NewClient: %v", err)
	}
	withResp := &genjobs.ClientWithResponses{ClientInterface: gen}
	return newJobsClient(withResp, environment), func() { srv.Close() }
}

// newClosedJobs returns a JobsClient whose backing server has been closed,
// exercising transport-error branches.
func newClosedJobs(t *testing.T) *JobsClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close()
	gen, _ := genjobs.NewClient(url)
	withResp := &genjobs.ClientWithResponses{ClientInterface: gen}
	return newJobsClient(withResp, "")
}

// capturedReq records the most recent request seen by a recording server.
type capturedReq struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   map[string]any
}

// recordingJobs wires a JobsClient (with the given environment) against a
// server that records the last request into rec and replies via reply.
func recordingJobs(t *testing.T, environment string, rec *capturedReq, reply http.HandlerFunc) (*JobsClient, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.query = r.URL.Query()
		rec.header = r.Header.Clone()
		rec.body = nil
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &rec.body)
			}
		}
		reply(w, r)
	}))
	gen, err := genjobs.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("genjobs.NewClient: %v", err)
	}
	withResp := &genjobs.ClientWithResponses{ClientInterface: gen}
	return newJobsClient(withResp, environment), func() { srv.Close() }
}

// httpCfgWire is a canned http configuration body for a given url.
func httpCfgWire(url string) map[string]any {
	return map[string]any{
		"method":         "POST",
		"url":            url,
		"headers":        []map[string]string{{"name": "X-Api-Key", "value": "secret"}},
		"body":           "{}",
		"success_status": "2xx",
		"timeout":        30,
		"tls_verify":     true,
		"ca_cert":        nil,
	}
}

// jobResource builds a recurring-job JSON:API resource. devEnabled drives the
// `development` override (which carries a configuration override); prodEnabled
// drives the `production` override (which has no configuration override). The
// base `enabled` roll-up is true when either environment is enabled.
func jobResource(id string, created bool, version int, devEnabled, prodEnabled bool) map[string]any {
	attrs := map[string]any{
		"name":          "My Job",
		"description":   "does a thing",
		"enabled":       devEnabled || prodEnabled,
		"recurring":     true,
		"type":          "http",
		"schedule":      "0 * * * *",
		"configuration": httpCfgWire("https://api.example.com/hook"),
		"environments": map[string]any{
			"development": map[string]any{
				"enabled":       devEnabled,
				"configuration": httpCfgWire("https://development.example.com/cache/warm"),
			},
			"production": map[string]any{"enabled": prodEnabled},
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

func runResource(runID, status, trigger, environment string, rerunOf *string) map[string]any {
	attrs := map[string]any{
		"job":                 testJobID,
		"job_version":         1,
		"environment":         environment,
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
		writeJSON(w, 201, map[string]any{"data": jobResource(testJobID, true, 1, false, true)})
	case path == "/api/v1/jobs" && m == "GET":
		writeJSON(w, 200, map[string]any{
			"data": []any{jobResource("a", true, 1, false, true), jobResource("b", true, 1, false, true)},
			"meta": map[string]any{"pagination": map[string]any{"page": 1, "size": 50}},
		})
	case path == "/api/v1/jobs/"+testJobID+"/actions/run":
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "PENDING", "MANUAL", "production", nil)})
	case path == "/api/v1/jobs/"+testJobID && m == "GET":
		writeJSON(w, 200, map[string]any{"data": jobResource(testJobID, true, 1, false, true)})
	case path == "/api/v1/jobs/"+testJobID && m == "PUT":
		writeJSON(w, 200, map[string]any{"data": jobResource(testJobID, true, 2, true, true)})
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
			"data": []any{runResource(testRunID, "SUCCEEDED", "SCHEDULE", "production", nil)},
			"meta": map[string]any{"page_size": 50},
		})
	case path == "/api/v1/runs/"+testRunID+"/actions/cancel":
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "CANCELED", "RERUN", "production", strPtr(testRunID))})
	case path == "/api/v1/runs/"+testRunID+"/actions/rerun":
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "PENDING", "RERUN", "production", strPtr(testRunID))})
	case path == "/api/v1/runs/"+testRunID && m == "GET":
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "SUCCEEDED", "SCHEDULE", "production", nil)})
	default:
		http.Error(w, "unexpected "+m+" "+path, http.StatusInternalServerError)
	}
}

func strPtr(s string) *string { return &s }

// assertEnvHeader asserts the X-Smplkit-Environment header equals want, or —
// when want is empty — that the header is absent entirely (not merely
// present-but-empty), using map presence rather than Header.Get.
func assertEnvHeader(t *testing.T, rec *capturedReq, want string) {
	t.Helper()
	values, present := rec.header["X-Smplkit-Environment"]
	if want == "" {
		if present {
			t.Errorf("expected no X-Smplkit-Environment header, got %v", values)
		}
		return
	}
	if !present || len(values) != 1 || values[0] != want {
		t.Errorf("X-Smplkit-Environment = %v (present=%t), want %q", values, present, want)
	}
}

// ---------------------------------------------------------------------------
// Accessors / construction
// ---------------------------------------------------------------------------

// JobsClient is reachable from the one-client SmplClient via Jobs(), and the
// run sub-client via Jobs().Runs().
func TestSmplClient_JobsAccessor(t *testing.T) {
	c, err := NewClient(Config{APIKey: "sk_api_test", Environment: "dev", Service: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.Jobs() == nil {
		t.Fatal("Jobs() returned nil")
	}
	if c.Jobs().Runs() == nil {
		t.Fatal("Jobs().Runs() returned nil")
	}
	// The configured environment threads onto the jobs + runs sub-clients.
	if c.Jobs().environment != "dev" || c.Jobs().Runs().environment != "dev" {
		t.Errorf("environment not threaded: jobs=%q runs=%q", c.Jobs().environment, c.Jobs().Runs().environment)
	}
}

// NewJobsClient builds a standalone JobsClient that owns its own transport.
// Driving a real List call through it exercises NewJobsClient end-to-end,
// including the request-editor closures wired by buildJobsGenClient.
func TestNewJobsClient_Standalone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.api+json" {
			t.Errorf("missing Accept header: %q", r.Header.Get("Accept"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent header")
		}
		writeJSON(w, 200, map[string]any{
			"data": []any{},
			"meta": map[string]any{"pagination": map[string]any{"page": 1, "size": 50}},
		})
	}))
	t.Cleanup(server.Close)

	jobs, err := NewJobsClient(
		Config{APIKey: "sk_test", Environment: "production", DisableTelemetry: true},
		WithBaseURL(server.URL),
	)
	if err != nil {
		t.Fatalf("NewJobsClient: %v", err)
	}
	if jobs.Runs() == nil {
		t.Fatal("Runs() returned nil")
	}
	if jobs.environment != "production" || jobs.Runs().environment != "production" {
		t.Errorf("environment not threaded onto standalone client: %q / %q", jobs.environment, jobs.Runs().environment)
	}

	list, err := jobs.List(context.Background(), ListJobsInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

// NewJobsClient surfaces the missing-API-key error from resolveConfig.
func TestNewJobsClient_MissingAPIKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	if _, err := NewJobsClient(Config{}); err == nil {
		t.Fatal("expected error when no API key is configured")
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle (mirrors the showcase)
// ---------------------------------------------------------------------------

func TestJobs_Lifecycle(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()

	// create a recurring job, enabled in production with a development override
	body := `{"scope": "all"}`
	devBody := `{"scope": "all"}`
	job := j.New(testJobID, "Nightly cache warm", "0 2 * * *", HttpConfig{
		Method:  JobHttpMethodPost,
		URL:     "https://api.example.com/cache/warm",
		Headers: []HttpHeader{{Name: "Authorization", Value: "Bearer s3cr3t"}},
		Body:    &body,
		Timeout: 30,
	}, WithJobDescription("desc"), WithJobConcurrencyPolicy("ALLOW"))
	job.SetConfiguration(HttpConfig{
		Method:  JobHttpMethodPost,
		URL:     "https://development.example.com/cache/warm",
		Headers: []HttpHeader{{Name: "Authorization", Value: "Bearer development-s3cr3t"}},
		Body:    &devBody,
	}, "development")
	job.SetEnabled(false, "development")
	job.SetEnabled(true, "production")

	if job.CreatedAt != nil {
		t.Fatal("unsaved job should have nil CreatedAt")
	}
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	if job.CreatedAt == nil || job.Version == nil || *job.Version != 1 {
		t.Fatalf("after create: version=%v createdAt=%v", job.Version, job.CreatedAt)
	}
	if job.IsEnabled("development") || !job.IsEnabled("production") {
		t.Errorf("post-create enablement mismatch: dev=%t prod=%t", job.IsEnabled("development"), job.IsEnabled("production"))
	}
	if job.Recurring == nil || !*job.Recurring {
		t.Errorf("expected recurring=true, got %v", job.Recurring)
	}

	// get a job
	fetched, err := j.Get(ctx, testJobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.IsEnabled("development") || !fetched.IsEnabled("production") {
		t.Errorf("fetched enablement mismatch: dev=%t prod=%t", fetched.IsEnabled("development"), fetched.IsEnabled("production"))
	}
	if fetched.GetConfiguration("development").URL != "https://development.example.com/cache/warm" {
		t.Errorf("dev config url mismatch: %s", fetched.GetConfiguration("development").URL)
	}
	// production has no override, so GetConfiguration falls back to the base.
	if fetched.GetConfiguration("production").URL != "https://api.example.com/hook" {
		t.Errorf("prod config should fall back to base, got %s", fetched.GetConfiguration("production").URL)
	}

	// list jobs
	disabled := false
	recurring := true
	jobs, err := j.List(ctx, ListJobsInput{Enabled: &disabled, Recurring: &recurring, PageNumber: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	// update a job (the schedule is environment-agnostic)
	job.Name = "renamed"
	job.SetSchedule("30 2 * * *")
	job.SetEnabled(true, "development")
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	if job.Version == nil || *job.Version != 2 || !job.IsEnabled("development") {
		t.Fatalf("after update: version=%v dev-enabled=%t", job.Version, job.IsEnabled("development"))
	}

	// trigger an immediate run
	run, err := job.Trigger(ctx, "production")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if run.Trigger != "MANUAL" || run.Environment != "production" {
		t.Errorf("run mismatch: trigger=%s env=%s", run.Trigger, run.Environment)
	}

	// get this job's runs
	runs, err := job.ListRuns(ctx, ListJobRunsInput{Environment: "production", PageSize: 2, After: "cur"})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
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

	// get a run
	got, err := j.Runs().Get(ctx, testRunID)
	if err != nil {
		t.Fatalf("Runs.Get: %v", err)
	}
	if got.ID != testRunID || got.Environment != "production" {
		t.Errorf("run id/env mismatch: %s / %s", got.ID, got.Environment)
	}

	// re-run a prior run (inherits its environment)
	rerun, err := got.Rerun(ctx)
	if err != nil {
		t.Fatalf("run.Rerun: %v", err)
	}
	if rerun.Trigger != "RERUN" || rerun.RerunOf == nil || *rerun.RerunOf != testRunID || rerun.Environment != got.Environment {
		t.Errorf("rerun mismatch: trigger=%s rerunOf=%v env=%s", rerun.Trigger, rerun.RerunOf, rerun.Environment)
	}

	// cancel a run
	canceled, err := rerun.Cancel(ctx)
	if err != nil {
		t.Fatalf("run.Cancel: %v", err)
	}
	if canceled.Status != "CANCELED" {
		t.Errorf("expected CANCELED, got %s", canceled.Status)
	}

	usage, err := j.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.RunsUsed != 12 || usage.ActiveJobs != 2 {
		t.Errorf("usage mismatch: %+v", usage)
	}

	if err := job.Delete(ctx); err != nil {
		t.Fatalf("job.Delete: %v", err)
	}
	if err := j.Delete(ctx, testJobID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Per-environment mutators / getters (in-memory)
// ---------------------------------------------------------------------------

func TestJob_PerEnvMutatorsAndGetters(t *testing.T) {
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()
	job := j.New("id", "n", "0 * * * *", HttpConfig{URL: "https://base"})

	// IsEnabled on a fresh job: rollup false, unknown env false.
	if job.IsEnabled("") || job.IsEnabled("production") {
		t.Errorf("fresh job should be enabled nowhere")
	}

	// SetEnabled on a fresh job creates the override (exercises the nil-map and
	// !ok branches of environmentOverride).
	job.SetEnabled(true, "production")
	if !job.IsEnabled("production") {
		t.Error("production should be enabled")
	}
	// SetConfiguration on the same env preserves the already-set Enabled.
	prodOverride := HttpConfig{URL: "https://prod-override"}
	job.SetConfiguration(prodOverride, "production")
	if !job.IsEnabled("production") {
		t.Error("SetConfiguration must preserve Enabled on the existing override")
	}
	if job.GetConfiguration("production").URL != "https://prod-override" {
		t.Errorf("expected per-env override, got %s", job.GetConfiguration("production").URL)
	}

	// An env entry that exists but has no configuration override falls back to
	// the base configuration.
	job.SetEnabled(true, "staging")
	if job.GetConfiguration("staging").URL != "https://base" {
		t.Errorf("staging (no override) should fall back to base, got %s", job.GetConfiguration("staging").URL)
	}

	// GetConfiguration("") and an unknown env both return the base.
	if job.GetConfiguration("").URL != "https://base" || job.GetConfiguration("unknown").URL != "https://base" {
		t.Error("base configuration expected for empty / unknown environment")
	}

	// SetEnabled is strictly per-environment (no base form, mirroring Python):
	// it sets the named environment's override, not the read-only base roll-up.
	job.SetEnabled(true, "production")
	if !job.IsEnabled("production") {
		t.Error("production override should reflect SetEnabled(true, \"production\")")
	}

	// SetConfiguration("") replaces the base configuration.
	job.SetConfiguration(HttpConfig{URL: "https://new-base"}, "")
	if job.GetConfiguration("").URL != "https://new-base" {
		t.Errorf("base configuration not replaced: %s", job.GetConfiguration("").URL)
	}

	// SetSchedule is environment-agnostic.
	job.SetSchedule("*/5 * * * *")
	if job.Schedule != "*/5 * * * *" {
		t.Errorf("schedule not set: %s", job.Schedule)
	}
}

// ---------------------------------------------------------------------------
// Build-to-wire: drops base enabled, emits environments + the env header
// ---------------------------------------------------------------------------

func TestJobs_CreateWireAndHeader(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"data": jobResource(testJobID, true, 1, false, true)})
	})
	defer cleanup()

	tlsVerify := false
	caCert := "-----BEGIN CERTIFICATE-----\nPEM\n-----END CERTIFICATE-----"
	job := j.New(testJobID, "n", "0 * * * *", HttpConfig{
		URL:           "https://base",
		SuccessStatus: "2xx",
		TlsVerify:     &tlsVerify,
		CaCert:        &caCert,
	}, WithJobBirthEnvironment("development"))
	job.SetConfiguration(HttpConfig{URL: "https://dev"}, "development")
	job.SetEnabled(false, "development")
	job.SetEnabled(true, "production")
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Birth environment is sent as the X-Smplkit-Environment header.
	assertEnvHeader(t, &rec, "development")

	attrs := rec.body["data"].(map[string]any)["attributes"].(map[string]any)
	if _, ok := attrs["enabled"]; ok {
		t.Error("write body must NOT include the read-only base `enabled`")
	}
	envs, ok := attrs["environments"].(map[string]any)
	if !ok {
		t.Fatalf("write body must include environments, got %v", attrs["environments"])
	}
	dev := envs["development"].(map[string]any)
	if dev["enabled"] != false {
		t.Errorf("development enabled should be false on the wire, got %v", dev["enabled"])
	}
	if _, ok := dev["configuration"]; !ok {
		t.Error("development override must carry its configuration on the wire")
	}
	prod := envs["production"].(map[string]any)
	if prod["enabled"] != true {
		t.Errorf("production enabled should be true, got %v", prod["enabled"])
	}
	if _, ok := prod["configuration"]; ok {
		t.Error("production override has no configuration; it must be omitted")
	}
}

// A create with no per-environment overrides and no birth env sends neither
// an environments map nor the X-Smplkit-Environment header.
func TestJobs_CreateNoEnvironments(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"data": jobResource(testJobID, true, 1, false, false)})
	})
	defer cleanup()

	job := j.New(testJobID, "n", "now", HttpConfig{URL: "https://base"})
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertEnvHeader(t, &rec, "")
	attrs := rec.body["data"].(map[string]any)["attributes"].(map[string]any)
	if _, ok := attrs["environments"]; ok {
		t.Error("empty environments must be omitted from the write body")
	}
}

// New defaults the birth environment to the client's configured environment.
func TestJobs_NewDefaultBirthEnvironment(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "production", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"data": jobResource(testJobID, true, 1, false, false)})
	})
	defer cleanup()

	job := j.New(testJobID, "n", "now", HttpConfig{URL: "https://base"})
	if job.birthEnvironment != "production" {
		t.Errorf("birthEnvironment should default to client env, got %q", job.birthEnvironment)
	}
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertEnvHeader(t, &rec, "production")
}

// Update sends X-Smplkit-Environment = the client's configured environment, and
// omits it when the client has none.
func TestJobs_UpdateHeader(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		env  string
		want string
	}{
		{"with-env", "production", "production"},
		{"no-env", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec capturedReq
			j, cleanup := recordingJobs(t, tc.env, &rec, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, map[string]any{"data": jobResource(testJobID, true, 2, true, true)})
			})
			defer cleanup()
			job := j.New(testJobID, "n", "0 * * * *", HttpConfig{URL: "https://base"})
			job.CreatedAt = &nowForTest // force the update (PUT) branch
			if err := job.Save(ctx); err != nil {
				t.Fatalf("Save (update): %v", err)
			}
			assertEnvHeader(t, &rec, tc.want)
		})
	}
}

// ---------------------------------------------------------------------------
// Parse-from-wire: environments (with and without an override), recurring
// ---------------------------------------------------------------------------

func TestJobs_ParseEnvironments(t *testing.T) {
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{
			"id": testJobID, "type": "job",
			"attributes": map[string]any{
				"name": "n", "schedule": "0 * * * *", "enabled": true, "recurring": true,
				"configuration": map[string]any{"url": "https://base"},
				"environments": map[string]any{
					"development": map[string]any{
						"enabled":       true,
						"configuration": map[string]any{"url": "https://dev", "method": "POST"},
					},
					"production": map[string]any{"enabled": false},
				},
			},
		}})
	})
	defer cleanup()
	job, err := j.Get(context.Background(), testJobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !job.Enabled {
		t.Error("rollup enabled should be true")
	}
	if job.Recurring == nil || !*job.Recurring {
		t.Errorf("recurring should be true, got %v", job.Recurring)
	}
	dev, ok := job.Environments["development"]
	if !ok || !dev.Enabled || dev.Configuration == nil || dev.Configuration.URL != "https://dev" {
		t.Errorf("development override mismatch: %+v", dev)
	}
	prod, ok := job.Environments["production"]
	if !ok || prod.Enabled || prod.Configuration != nil {
		t.Errorf("production override (no configuration) mismatch: %+v", prod)
	}
}

// ---------------------------------------------------------------------------
// Run-now header resolution: explicit env / client default / omitted
// ---------------------------------------------------------------------------

func TestJobs_RunNowHeader(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		clientEnv  string
		explicit   string
		wantHeader string
	}{
		{"explicit-wins", "production", "staging", "staging"},
		{"client-default", "production", "", "production"},
		{"omitted", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec capturedReq
			j, cleanup := recordingJobs(t, tc.clientEnv, &rec, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "PENDING", "MANUAL", "production", nil)})
			})
			defer cleanup()
			if _, err := j.Run(ctx, testJobID, tc.explicit); err != nil {
				t.Fatalf("Run: %v", err)
			}
			assertEnvHeader(t, &rec, tc.wantHeader)
		})
	}
}

// ---------------------------------------------------------------------------
// filter[environment] resolution on Runs().List
// ---------------------------------------------------------------------------

func TestRuns_FilterEnvironment(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		clientEnv string
		input     []string
		wantQuery string
	}{
		{"explicit-list", "production", []string{"production", "staging"}, "production,staging"},
		{"client-default", "production", nil, "production"},
		{"omitted", "", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec capturedReq
			j, cleanup := recordingJobs(t, tc.clientEnv, &rec, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, map[string]any{
					"data": []any{}, "meta": map[string]any{"page_size": 50},
				})
			})
			defer cleanup()
			if _, err := j.Runs().List(ctx, ListRunsInput{Environments: tc.input}); err != nil {
				t.Fatalf("Runs.List: %v", err)
			}
			got := ""
			if v, ok := rec.query["filter[environment]"]; ok {
				got = v[0]
			}
			if got != tc.wantQuery {
				t.Errorf("filter[environment] = %q, want %q", got, tc.wantQuery)
			}
		})
	}
}

// Job.ListRuns scopes by both the job id and the single environment, and the
// no-environment case omits the filter.
func TestJob_ListRunsScoping(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []any{}, "meta": map[string]any{"page_size": 50}})
	})
	defer cleanup()
	job := j.New(testJobID, "n", "0 * * * *", HttpConfig{URL: "https://base"})

	if _, err := job.ListRuns(ctx, ListJobRunsInput{Environment: "production"}); err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if rec.query.Get("filter[job]") != testJobID {
		t.Errorf("expected filter[job]=%s, got %q", testJobID, rec.query.Get("filter[job]"))
	}
	if rec.query.Get("filter[environment]") != "production" {
		t.Errorf("expected filter[environment]=production, got %q", rec.query.Get("filter[environment]"))
	}

	if _, err := job.ListRuns(ctx, ListJobRunsInput{}); err != nil {
		t.Fatalf("ListRuns (no env): %v", err)
	}
	if _, ok := rec.query["filter[environment]"]; ok {
		t.Error("no-environment ListRuns must omit filter[environment]")
	}
}

// JobsClient.List and Runs().List translate their inputs into the documented
// query params, and omit the page params at their zero values.
func TestJobs_ListQueryParams(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runs" {
			writeJSON(w, 200, map[string]any{"data": []any{}, "meta": map[string]any{"page_size": 50}})
			return
		}
		writeJSON(w, 200, map[string]any{
			"data": []any{}, "meta": map[string]any{"pagination": map[string]any{"page": 1, "size": 50}},
		})
	})
	defer cleanup()

	// Jobs list: enabled / recurring / name filters + offset pagination.
	enabled := true
	recurring := false
	name := "health"
	if _, err := j.List(ctx, ListJobsInput{Enabled: &enabled, Recurring: &recurring, Name: &name, PageNumber: 3, PageSize: 25}); err != nil {
		t.Fatalf("List: %v", err)
	}
	for key, want := range map[string]string{
		"filter[enabled]":   "true",
		"filter[recurring]": "false",
		"filter[name]":      "health",
		"page[number]":      "3",
		"page[size]":        "25",
	} {
		if got := rec.query.Get(key); got != want {
			t.Errorf("jobs list %s = %q, want %q", key, got, want)
		}
	}

	// Zero values omit the optional params entirely.
	if _, err := j.List(ctx, ListJobsInput{}); err != nil {
		t.Fatalf("List (defaults): %v", err)
	}
	for _, key := range []string{"filter[enabled]", "filter[recurring]", "page[number]", "page[size]"} {
		if _, ok := rec.query[key]; ok {
			t.Errorf("jobs list must omit %s at its zero value", key)
		}
	}

	// Runs list: job filter + cursor pagination.
	if _, err := j.Runs().List(ctx, ListRunsInput{Job: testJobID, PageSize: 7, After: "tok"}); err != nil {
		t.Fatalf("Runs.List: %v", err)
	}
	if rec.query.Get("filter[job]") != testJobID || rec.query.Get("page[size]") != "7" || rec.query.Get("page[after]") != "tok" {
		t.Errorf("runs list query mismatch: %v", rec.query)
	}

	// Zero values omit the cursor params.
	if _, err := j.Runs().List(ctx, ListRunsInput{}); err != nil {
		t.Fatalf("Runs.List (defaults): %v", err)
	}
	if _, ok := rec.query["page[size]"]; ok {
		t.Error("runs list must omit page[size] at its zero value")
	}
	if _, ok := rec.query["page[after]"]; ok {
		t.Error("runs list must omit page[after] when no cursor is given")
	}
}

// ---------------------------------------------------------------------------
// Active-record run + job guards
// ---------------------------------------------------------------------------

func TestRun_NoClientGuards(t *testing.T) {
	ctx := context.Background()
	run := Run{ID: testRunID}
	if _, err := run.Rerun(ctx); err == nil {
		t.Error("Rerun without a runs client should error")
	}
	if _, err := run.Cancel(ctx); err == nil {
		t.Error("Cancel without a runs client should error")
	}
}

func TestJob_TriggerListRunsGuards(t *testing.T) {
	ctx := context.Background()
	job := &Job{ID: "x", Name: "X", Schedule: "now"}
	if _, err := job.Trigger(ctx, "production"); err == nil {
		t.Error("Trigger without a client should error")
	}
	if _, err := job.ListRuns(ctx, ListJobRunsInput{}); err == nil {
		t.Error("ListRuns without a client should error")
	}
}

// ---------------------------------------------------------------------------
// Defaults / wire conversions
// ---------------------------------------------------------------------------

func TestJobs_NewDefaults(t *testing.T) {
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()
	envs := map[string]JobEnvironment{"production": {Enabled: true}}
	job := j.New("id", "n", "now", HttpConfig{URL: "https://x"},
		WithJobEnvironments(envs), WithJobConcurrencyPolicy("ALLOW"))
	// Base roll-up defaults to false (no writable enabled flag); type/policy
	// keep their defaults; description stays nil.
	if job.Enabled || job.Type != "http" || job.ConcurrencyPolicy != "ALLOW" {
		t.Errorf("unexpected defaults: %+v", job)
	}
	if job.Description != nil {
		t.Errorf("expected nil description, got %v", job.Description)
	}
	if !job.IsEnabled("production") {
		t.Error("WithJobEnvironments should seed the environments map")
	}
}

func TestJobs_MinimalConfigWire(t *testing.T) {
	// A configuration with no optional fields set must round-trip without
	// panicking and without emitting the optional wire fields. This drives
	// the zero-value branches of httpConfigToWire (no Method, no
	// SuccessStatus, no Timeout, nil Body/TlsVerify/CaCert, empty Headers).
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()
	job := j.New("id", "n", "now", HttpConfig{URL: "https://x"})
	if err := job.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// jobFromResource: zero-value optional attributes (nil enabled/type/policy,
// no id, no environments/recurring) must fall back to defaults.
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
	if job.Recurring != nil || job.Environments != nil {
		t.Errorf("expected nil recurring/environments, got recurring=%v envs=%v", job.Recurring, job.Environments)
	}
	// httpConfigFromWire on a configuration with no optional fields: every
	// optional must remain zero / nil.
	if job.Configuration.Method != "" || job.Configuration.SuccessStatus != "" ||
		job.Configuration.Timeout != 0 || job.Configuration.Body != nil ||
		job.Configuration.TlsVerify != nil || job.Configuration.CaCert != nil ||
		job.Configuration.Headers != nil {
		t.Errorf("expected zero-valued configuration, got %+v", job.Configuration)
	}
}

// A configuration that carries ca_cert (and an explicit tls_verify=false plus
// headers) on the wire must surface them via httpConfigFromWire.
func TestJobs_FromResourceCaCert(t *testing.T) {
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{
			"id": "id", "type": "job",
			"attributes": map[string]any{
				"name": "n", "schedule": "now",
				"configuration": map[string]any{
					"url":        "https://x",
					"tls_verify": false,
					"ca_cert":    "-----BEGIN CERTIFICATE-----\nPEM\n-----END CERTIFICATE-----",
					"headers":    []map[string]string{{"name": "X-Api-Key", "value": "secret"}},
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
	if job.Configuration.TlsVerify == nil || *job.Configuration.TlsVerify {
		t.Errorf("expected tls_verify=false surfaced, got %v", job.Configuration.TlsVerify)
	}
	if len(job.Configuration.Headers) != 1 || job.Configuration.Headers[0].Name != "X-Api-Key" {
		t.Errorf("expected headers surfaced, got %+v", job.Configuration.Headers)
	}
}

// run with failure_reason set must surface it as a string pointer, and the
// run's environment is always surfaced.
func TestJobs_RunFailureReason(t *testing.T) {
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{
			"id": testRunID, "type": "run",
			"attributes": map[string]any{
				"job": testJobID, "environment": "development", "trigger": "SCHEDULE", "status": "FAILED",
				"failure_reason": "TIMEOUT", "error": "deadline exceeded",
			},
		}})
	})
	defer cleanup()
	run, err := j.Runs().Get(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if run.Environment != "development" {
		t.Errorf("environment mismatch: %s", run.Environment)
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
	if _, err := j.Get(ctx, testJobID); err == nil {
		t.Error("Get should error on 500")
	}
	if err := j.Delete(ctx, testJobID); err == nil {
		t.Error("Delete should error on 500")
	}
	if _, err := j.Run(ctx, testJobID, "production"); err == nil {
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
	// create branch (new job → POST) returning 500 → checkStatus error.
	newJob := j.New(testJobID, "n", "now", HttpConfig{URL: "https://x"})
	if err := newJob.Save(ctx); err == nil {
		t.Error("Save (create) should error on 500")
	}
	// update branch (existing job → PUT).
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
	if _, err := j.Run(ctx, testJobID, "production"); err == nil {
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
