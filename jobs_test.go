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
	testJobID         = "showcase-mgmt-abcd1234"
	testRunID         = "8f2b1c4a-0000-4a1b-9c3d-1e2f3a4b5c6d"
	testRetryPolicyID = "showcase-retry"
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
// `development` override (which carries a configuration override plus a
// per-environment schedule override and a read-only next_run_at); prodEnabled
// drives the `production` override (which has no configuration override). The
// base `enabled` roll-up is derived wrapper-side from the environments, so it is
// not present on the wire.
func jobResource(id string, created bool, version int, devEnabled, prodEnabled bool) map[string]any {
	attrs := map[string]any{
		"name":          "My Job",
		"description":   "does a thing",
		"kind":          "recurring",
		"type":          "http",
		"schedule":      "0 * * * *",
		"configuration": httpCfgWire("https://api.example.com/hook"),
		"environments": map[string]any{
			"development": map[string]any{
				"enabled":       devEnabled,
				"schedule":      "0 3 * * *",
				"configuration": httpCfgWire("https://development.example.com/cache/warm"),
				"next_run_at":   "2026-06-05T03:00:00Z",
			},
			"production": map[string]any{"enabled": prodEnabled},
		},
		"concurrency_policy": "ALLOW",
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
	if c.Jobs().RetryPolicies() == nil {
		t.Fatal("Jobs().RetryPolicies() returned nil")
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
	job := j.NewRecurringJob(testJobID, "Nightly cache warm", "0 2 * * *", HttpConfig{
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
	if !job.IsRecurring() {
		t.Errorf("expected recurring job, got kind=%v", job.Kind)
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

	// list jobs, filtered to recurring jobs
	kind := JobKindRecurring
	jobs, err := j.List(ctx, ListJobsInput{Kind: &kind, PageNumber: 1, PageSize: 10})
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
	job := j.NewRecurringJob("id", "n", "0 * * * *", HttpConfig{URL: "https://base"})

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

	// SetSchedule with no options sets the base schedule and leaves the timezone
	// untouched.
	job.SetSchedule("*/5 * * * *")
	if job.Schedule != "*/5 * * * *" {
		t.Errorf("base schedule not set: %s", job.Schedule)
	}
	if job.Timezone != "" {
		t.Errorf("no timezone option must leave the base timezone untouched, got %q", job.Timezone)
	}
	// SetSchedule with WithScheduleTimezone and no environment also sets the base
	// timezone (equivalent to a follow-up SetTimezone).
	job.SetSchedule("*/10 * * * *", WithScheduleTimezone("America/Denver"))
	if job.Schedule != "*/10 * * * *" {
		t.Errorf("base schedule not set: %s", job.Schedule)
	}
	if job.Timezone != "America/Denver" {
		t.Errorf("base timezone should be set alongside the schedule, got %q", job.Timezone)
	}

	// SetSchedule with WithScheduleEnvironment sets a per-environment cron
	// override, preserving the already-set Enabled on that environment's entry,
	// and does not touch the base schedule.
	job.SetSchedule("0 4 * * *", WithScheduleEnvironment("production"))
	prod := job.Environments["production"]
	if prod.Schedule != "0 4 * * *" {
		t.Errorf("production per-env schedule not set: %q", prod.Schedule)
	}
	if !prod.Enabled {
		t.Error("SetSchedule must preserve Enabled on the existing production override")
	}
	if job.Schedule != "*/10 * * * *" {
		t.Errorf("per-env SetSchedule must not change the base schedule: %q", job.Schedule)
	}

	// SetSchedule with both a timezone and an environment sets both on that
	// environment's override.
	job.SetSchedule("0 6 * * *", WithScheduleTimezone("Europe/Paris"), WithScheduleEnvironment("production"))
	prodTzSched := job.Environments["production"]
	if prodTzSched.Schedule != "0 6 * * *" || prodTzSched.Timezone != "Europe/Paris" {
		t.Errorf("per-env SetSchedule with timezone mismatch: %+v", prodTzSched)
	}

	// SetSchedule on a brand-new environment creates the override entry.
	job.SetSchedule("0 5 * * *", WithScheduleEnvironment("qa"))
	if qa := job.Environments["qa"]; qa.Schedule != "0 5 * * *" || qa.Enabled {
		t.Errorf("qa per-env schedule override mismatch: %+v", qa)
	}

	// SetTimezone with no environment (and with an explicit empty environment)
	// sets the base timezone.
	job.SetTimezone("America/New_York")
	if job.Timezone != "America/New_York" {
		t.Errorf("base timezone not set: %s", job.Timezone)
	}
	job.SetTimezone("America/Chicago", "")
	if job.Timezone != "America/Chicago" {
		t.Errorf("base timezone (explicit empty env) not set: %s", job.Timezone)
	}

	// SetTimezone with an environment sets a per-environment override, preserving
	// the already-set per-env schedule, and does not touch the base timezone.
	job.SetTimezone("Europe/London", "production")
	prodTz := job.Environments["production"]
	if prodTz.Timezone != "Europe/London" {
		t.Errorf("production per-env timezone not set: %q", prodTz.Timezone)
	}
	if prodTz.Schedule != "0 6 * * *" {
		t.Error("SetTimezone must preserve Schedule on the existing production override")
	}
	if job.Timezone != "America/Chicago" {
		t.Errorf("per-env SetTimezone must not change the base timezone: %q", job.Timezone)
	}

	// SetTimezone on a brand-new environment creates the override entry.
	job.SetTimezone("Asia/Tokyo", "edge")
	if edge := job.Environments["edge"]; edge.Timezone != "Asia/Tokyo" || edge.Enabled {
		t.Errorf("edge per-env timezone override mismatch: %+v", edge)
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
	job := j.NewRecurringJob(testJobID, "n", "0 * * * *", HttpConfig{
		URL:           "https://base",
		SuccessStatus: "2xx",
		TlsVerify:     &tlsVerify,
		CaCert:        &caCert,
	}, WithJobBirthEnvironment("development"))
	job.SetConfiguration(HttpConfig{URL: "https://dev"}, "development")
	job.SetEnabled(false, "development")
	job.SetSchedule("0 3 * * *", WithScheduleEnvironment("development"))
	job.SetTimezone("America/New_York")             // base timezone
	job.SetTimezone("Europe/London", "development") // per-env override
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
	// The base timezone is sent when set.
	if attrs["timezone"] != "America/New_York" {
		t.Errorf("base timezone should be on the wire, got %v", attrs["timezone"])
	}
	if _, ok := attrs["next_run_at"]; ok {
		t.Error("write body must NOT include the removed top-level `next_run_at`")
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
	// The per-environment schedule override is sent; the read-only next_run_at
	// is never sent.
	if dev["schedule"] != "0 3 * * *" {
		t.Errorf("development per-env schedule should be on the wire, got %v", dev["schedule"])
	}
	if dev["timezone"] != "Europe/London" {
		t.Errorf("development per-env timezone should be on the wire, got %v", dev["timezone"])
	}
	if _, ok := dev["next_run_at"]; ok {
		t.Error("read-only per-env next_run_at must never be sent on the wire")
	}
	prod := envs["production"].(map[string]any)
	if prod["enabled"] != true {
		t.Errorf("production enabled should be true, got %v", prod["enabled"])
	}
	if _, ok := prod["configuration"]; ok {
		t.Error("production override has no configuration; it must be omitted")
	}
	// production has no schedule override; it must be omitted from the wire.
	if _, ok := prod["schedule"]; ok {
		t.Error("production override has no schedule; it must be omitted")
	}
	// production has no timezone override; it must be omitted from the wire.
	if _, ok := prod["timezone"]; ok {
		t.Error("production override has no timezone; it must be omitted")
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

	job := j.NewManualJob(testJobID, "n", HttpConfig{URL: "https://base"})
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertEnvHeader(t, &rec, "")
	attrs := rec.body["data"].(map[string]any)["attributes"].(map[string]any)
	if _, ok := attrs["environments"]; ok {
		t.Error("empty environments must be omitted from the write body")
	}
}

// Schedule defaults the birth environment to the client's configured
// environment.
func TestJobs_NewDefaultBirthEnvironment(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "production", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"data": jobResource(testJobID, true, 1, false, false)})
	})
	defer cleanup()

	job := j.Schedule(testJobID, "n", nowForTest, HttpConfig{URL: "https://base"})
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
			job := j.NewRecurringJob(testJobID, "n", "0 * * * *", HttpConfig{URL: "https://base"})
			job.CreatedAt = &nowForTest // force the update (PUT) branch
			if err := job.Save(ctx); err != nil {
				t.Fatalf("Save (update): %v", err)
			}
			assertEnvHeader(t, &rec, tc.want)
		})
	}
}

// ---------------------------------------------------------------------------
// Parse-from-wire: environments (with and without an override), kind
// ---------------------------------------------------------------------------

func TestJobs_ParseEnvironments(t *testing.T) {
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{
			"id": testJobID, "type": "job",
			"attributes": map[string]any{
				"name": "n", "schedule": "0 * * * *", "kind": "recurring",
				"timezone":      "America/New_York",
				"configuration": map[string]any{"url": "https://base"},
				"environments": map[string]any{
					"development": map[string]any{
						"enabled":       true,
						"schedule":      "0 3 * * *",
						"timezone":      "Europe/London",
						"configuration": map[string]any{"url": "https://dev", "method": "POST"},
						"next_run_at":   "2026-06-05T03:00:00Z",
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
	// The base roll-up is derived locally from the environments (development is
	// enabled), not read from a top-level wire field.
	if !job.Enabled || !job.IsEnabled("") {
		t.Error("rollup enabled should be true (derived from environments)")
	}
	if !job.IsRecurring() {
		t.Errorf("kind should be recurring, got %v", job.Kind)
	}
	// The base timezone decodes from the wire.
	if job.Timezone != "America/New_York" {
		t.Errorf("base timezone mismatch: %q", job.Timezone)
	}
	dev, ok := job.Environments["development"]
	if !ok || !dev.Enabled || dev.Configuration == nil || dev.Configuration.URL != "https://dev" {
		t.Errorf("development override mismatch: %+v", dev)
	}
	// The per-environment schedule override and read-only next_run_at surface.
	if dev.Schedule != "0 3 * * *" {
		t.Errorf("development per-env schedule mismatch: %q", dev.Schedule)
	}
	if dev.Timezone != "Europe/London" {
		t.Errorf("development per-env timezone mismatch: %q", dev.Timezone)
	}
	wantNext := time.Date(2026, 6, 5, 3, 0, 0, 0, time.UTC)
	if dev.NextRunAt == nil || !dev.NextRunAt.Equal(wantNext) {
		t.Errorf("development next_run_at mismatch: %v", dev.NextRunAt)
	}
	prod, ok := job.Environments["production"]
	if !ok || prod.Enabled || prod.Configuration != nil {
		t.Errorf("production override (no configuration) mismatch: %+v", prod)
	}
	// production is not enabled, so it has no per-env schedule/timezone/next_run_at.
	if prod.Schedule != "" || prod.Timezone != "" || prod.NextRunAt != nil {
		t.Errorf("production override should have no schedule/timezone/next_run_at: %+v", prod)
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
	job := j.NewRecurringJob(testJobID, "n", "0 * * * *", HttpConfig{URL: "https://base"})

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

	// Jobs list: kind / scheduled / name filters + offset pagination. (The
	// dropped `filter[recurring]` and `filter[enabled]` params are never
	// emitted.)
	kind := JobKindManual
	scheduled := true
	name := "health"
	if _, err := j.List(ctx, ListJobsInput{Kind: &kind, Scheduled: &scheduled, Name: &name, PageNumber: 3, PageSize: 25}); err != nil {
		t.Fatalf("List: %v", err)
	}
	for key, want := range map[string]string{
		"filter[kind]":      "manual", // JobKind serialized to its value
		"filter[scheduled]": "true",
		"filter[name]":      "health",
		"page[number]":      "3",
		"page[size]":        "25",
	} {
		if got := rec.query.Get(key); got != want {
			t.Errorf("jobs list %s = %q, want %q", key, got, want)
		}
	}
	// The dropped recurring / enabled filters must never appear on the wire.
	for _, key := range []string{"filter[recurring]", "filter[enabled]"} {
		if _, ok := rec.query[key]; ok {
			t.Errorf("jobs list must not send the dropped %s param", key)
		}
	}

	// Zero values omit the optional params entirely.
	if _, err := j.List(ctx, ListJobsInput{}); err != nil {
		t.Fatalf("List (defaults): %v", err)
	}
	for _, key := range []string{"filter[kind]", "filter[scheduled]", "filter[name]", "page[number]", "page[size]"} {
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
// Kind predicates + manual / one-off constructors
// ---------------------------------------------------------------------------

// Kind predicates reflect the server-derived kind; a job with no kind leaves it
// nil and every predicate false.
func TestJob_KindPredicates(t *testing.T) {
	get := func(kind string) *Job {
		j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
			attrs := map[string]any{"name": "n", "configuration": map[string]any{"url": "https://x"}}
			if kind != "" {
				attrs["kind"] = kind
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"id": "id", "type": "job", "attributes": attrs}})
		})
		defer cleanup()
		job, err := j.Get(context.Background(), "id")
		if err != nil {
			t.Fatalf("Get(%q): %v", kind, err)
		}
		return job
	}

	rec := get("recurring")
	if rec.Kind == nil || *rec.Kind != JobKindRecurring || !rec.IsRecurring() || rec.IsManual() || rec.IsOneOff() {
		t.Errorf("recurring predicates wrong: kind=%v", rec.Kind)
	}
	man := get("manual")
	if !man.IsManual() || man.IsRecurring() || man.IsOneOff() {
		t.Errorf("manual predicates wrong: kind=%v", man.Kind)
	}
	off := get("one_off")
	if !off.IsOneOff() || off.IsRecurring() || off.IsManual() {
		t.Errorf("one_off predicates wrong: kind=%v", off.Kind)
	}
	// a resource with no kind leaves it nil — every predicate is false.
	none := get("")
	if none.Kind != nil || none.IsRecurring() || none.IsManual() || none.IsOneOff() {
		t.Errorf("no-kind predicates wrong: kind=%v", none.Kind)
	}
}

// A manual job carries no schedule: NewManualJob leaves Schedule empty, the
// create body omits schedule entirely, and the server echoes back kind="manual"
// with no schedule.
func TestJobs_ManualJobRoundTrip(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		attrs := map[string]any{
			"name": "Manual", "kind": "manual", "type": "http",
			"configuration":      map[string]any{"url": "https://x"},
			"environments":       map[string]any{"production": map[string]any{"enabled": true}},
			"concurrency_policy": "ALLOW", "version": 1,
			"created_at": "2026-06-04T00:00:00Z", "updated_at": "2026-06-04T00:00:00Z",
		}
		writeJSON(w, 201, map[string]any{"data": map[string]any{"id": "manual-job", "type": "job", "attributes": attrs}})
	})
	defer cleanup()

	job := j.NewManualJob("manual-job", "Manual", HttpConfig{URL: "https://x"})
	if job.Schedule != "" {
		t.Errorf("manual job should have no schedule, got %q", job.Schedule)
	}
	job.SetEnabled(true, "production")
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !job.IsManual() || job.Kind == nil || *job.Kind != JobKindManual || job.Schedule != "" {
		t.Errorf("after save: expected manual job with no schedule, got kind=%v schedule=%q", job.Kind, job.Schedule)
	}
	// The create body omits schedule entirely (a manual job has none).
	attrs := rec.body["data"].(map[string]any)["attributes"].(map[string]any)
	if _, ok := attrs["schedule"]; ok {
		t.Errorf("manual create body must omit schedule, got %v", attrs["schedule"])
	}
}

// Schedule serializes the one-off datetime to ISO-8601 on the wire and sends
// the birth environment as the create header.
func TestJobs_ScheduleOneOff(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"data": jobResource(testJobID, true, 1, true, false)})
	})
	defer cleanup()

	when := time.Date(2030, 1, 1, 12, 30, 0, 0, time.UTC)
	job := j.Schedule("one-off", "One", when, HttpConfig{URL: "https://x"}, WithJobBirthEnvironment("staging"))
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertEnvHeader(t, &rec, "staging") // birth environment
	attrs := rec.body["data"].(map[string]any)["attributes"].(map[string]any)
	if got := attrs["schedule"]; got != when.Format(time.RFC3339) {
		t.Errorf("schedule on the wire = %v, want %q (datetime -> ISO-8601)", got, when.Format(time.RFC3339))
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
	job := j.NewManualJob("id", "n", HttpConfig{URL: "https://x"},
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
	job := j.NewManualJob("id", "n", HttpConfig{URL: "https://x"})
	if err := job.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// jobFromResource: zero-value optional attributes (nil enabled/type/policy,
// no id, no environments/kind) must fall back to defaults.
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
	if job.Kind != nil || job.Environments != nil {
		t.Errorf("expected nil kind/environments, got kind=%v envs=%v", job.Kind, job.Environments)
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
	// trigger is a raw string, equal to its RunTrigger constant value.
	if run.Trigger != string(RunTriggerSchedule) {
		t.Errorf("trigger should be RunTriggerSchedule, got %q", run.Trigger)
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
	job := j.NewManualJob("dup", "D", HttpConfig{URL: "https://x"})
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
	newJob := j.NewManualJob(testJobID, "n", HttpConfig{URL: "https://x"})
	if err := newJob.Save(ctx); err == nil {
		t.Error("Save (create) should error on 500")
	}
	// update branch (existing job → PUT).
	job := j.NewManualJob(testJobID, "n", HttpConfig{URL: "https://x"})
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
	job := j.NewManualJob(testJobID, "n", HttpConfig{URL: "https://x"})
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
	job := j.NewManualJob(testJobID, "n", HttpConfig{URL: "https://x"})
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
	job := j.NewManualJob(testJobID, "n", HttpConfig{URL: "https://x"})
	if err := job.Save(ctx); err == nil {
		t.Error("Save (create) should error on transport failure")
	}
	job.CreatedAt = &nowForTest
	if err := job.Save(ctx); err == nil {
		t.Error("Save (update) should error on transport failure")
	}
}

// ---------------------------------------------------------------------------
// Retry policies
// ---------------------------------------------------------------------------

// retryPolicyResource builds a retry-policy JSON:API resource that exercises the
// full attribute set: exponential backoff with a max-delay cap and a populated
// retry_on (both statuses and reasons).
func retryPolicyResource(id string, created bool, version int) map[string]any {
	attrs := map[string]any{
		"name":              "Retry on server errors",
		"max_retries":       5,
		"backoff":           "exponential",
		"delay_seconds":     2,
		"max_delay_seconds": 60,
		"retry_on": map[string]any{
			"statuses": []any{429, 503},
			"reasons":  []any{"TIMEOUT"},
		},
		"deleted_at": nil,
		"version":    version,
	}
	if created {
		attrs["created_at"] = "2026-06-04T00:00:00Z"
		attrs["updated_at"] = "2026-06-04T00:00:00Z"
	}
	return map[string]any{"id": id, "type": "retry_policy", "attributes": attrs}
}

// retryFullHandler routes every retry-policy endpoint to a canned response.
func retryFullHandler(w http.ResponseWriter, r *http.Request) {
	m, path := r.Method, r.URL.Path
	switch {
	case path == "/api/v1/retry-policies" && m == "POST":
		writeJSON(w, 201, map[string]any{"data": retryPolicyResource(testRetryPolicyID, true, 1)})
	case path == "/api/v1/retry-policies" && m == "GET":
		writeJSON(w, 200, map[string]any{
			"data": []any{retryPolicyResource(testRetryPolicyID, true, 1)},
			"meta": map[string]any{"pagination": map[string]any{"page": 1, "size": 1000}},
		})
	case path == "/api/v1/retry-policies/"+testRetryPolicyID && m == "GET":
		writeJSON(w, 200, map[string]any{"data": retryPolicyResource(testRetryPolicyID, true, 1)})
	case path == "/api/v1/retry-policies/"+testRetryPolicyID && m == "PUT":
		writeJSON(w, 200, map[string]any{"data": retryPolicyResource(testRetryPolicyID, true, 2)})
	case path == "/api/v1/retry-policies/"+testRetryPolicyID && m == "DELETE":
		w.WriteHeader(204)
	default:
		http.Error(w, "unexpected "+m+" "+path, http.StatusInternalServerError)
	}
}

// Full retry-policy lifecycle: create (New + Save), parse the populated fields,
// list, get, full-replace update, then delete both ways.
func TestRetryPolicies_Lifecycle(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, retryFullHandler)
	defer cleanup()
	rp := j.RetryPolicies()

	policy := rp.New(testRetryPolicyID, "Retry on server errors", 5, BackoffExponential, 2,
		WithRetryPolicyMaxDelaySeconds(60),
		WithRetryPolicyRetryOn(RetryOn{Statuses: []int{429, 503}, Reasons: []RetryReason{RetryReasonTimeout}}))
	if policy.CreatedAt != nil {
		t.Fatal("unsaved policy should have nil CreatedAt")
	}
	if err := policy.Save(ctx); err != nil {
		t.Fatalf("Save (create): %v", err)
	}
	if policy.CreatedAt == nil || policy.Version == nil || *policy.Version != 1 {
		t.Fatalf("after create: version=%v createdAt=%v", policy.Version, policy.CreatedAt)
	}
	if policy.Backoff != BackoffExponential || policy.MaxDelaySeconds == nil || *policy.MaxDelaySeconds != 60 {
		t.Errorf("policy fields mismatch: %+v", policy)
	}
	if len(policy.RetryOn.Statuses) != 2 || len(policy.RetryOn.Reasons) != 1 || policy.RetryOn.Reasons[0] != RetryReasonTimeout {
		t.Errorf("retry_on parse mismatch: %+v", policy.RetryOn)
	}

	listed, err := rp.List(ctx, ListRetryPoliciesInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, p := range listed {
		if p.ID == testRetryPolicyID {
			found = true
		}
	}
	if !found {
		t.Error("created policy not present in the listing")
	}

	got, err := rp.Get(ctx, testRetryPolicyID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != testRetryPolicyID {
		t.Errorf("get id mismatch: %s", got.ID)
	}

	// full-replace update bumps the version
	policy.Name = "Retry (v2)"
	if err := policy.Save(ctx); err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	if policy.Version == nil || *policy.Version != 2 {
		t.Fatalf("after update: version=%v", policy.Version)
	}

	// delete via the instance, then via the client
	if err := policy.Delete(ctx); err != nil {
		t.Fatalf("policy.Delete: %v", err)
	}
	if err := rp.Delete(ctx, testRetryPolicyID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// New without options leaves the max delay unset and the retry-on empty (retries
// nothing).
func TestRetryPolicies_NewDefaults(t *testing.T) {
	j, cleanup := newTestJobs(t, retryFullHandler)
	defer cleanup()
	policy := j.RetryPolicies().New("p", "n", 0, BackoffFixed, 10)
	if policy.MaxDelaySeconds != nil {
		t.Errorf("expected nil MaxDelaySeconds, got %v", *policy.MaxDelaySeconds)
	}
	if len(policy.RetryOn.Statuses) != 0 || len(policy.RetryOn.Reasons) != 0 {
		t.Errorf("expected empty RetryOn default, got %+v", policy.RetryOn)
	}
	if policy.Backoff != BackoffFixed {
		t.Errorf("backoff: %s", policy.Backoff)
	}
}

// The create body carries the resource id/type and the full attribute set;
// max_delay_seconds is omitted when unset, and an empty RetryOn still emits both
// lists as empty arrays.
func TestRetryPolicies_CreateWire(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"data": retryPolicyResource(testRetryPolicyID, true, 1)})
	})
	defer cleanup()

	policy := j.RetryPolicies().New(testRetryPolicyID, "Retry on server errors", 5, BackoffExponential, 2,
		WithRetryPolicyMaxDelaySeconds(60),
		WithRetryPolicyRetryOn(RetryOn{Statuses: []int{429, 503}, Reasons: []RetryReason{RetryReasonTimeout}}))
	if err := policy.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data := rec.body["data"].(map[string]any)
	if data["id"] != testRetryPolicyID || data["type"] != "retry_policy" {
		t.Errorf("resource id/type mismatch: id=%v type=%v", data["id"], data["type"])
	}
	attrs := data["attributes"].(map[string]any)
	if attrs["name"] != "Retry on server errors" || attrs["backoff"] != "exponential" {
		t.Errorf("name/backoff mismatch: %v / %v", attrs["name"], attrs["backoff"])
	}
	if attrs["max_retries"].(float64) != 5 || attrs["delay_seconds"].(float64) != 2 || attrs["max_delay_seconds"].(float64) != 60 {
		t.Errorf("numeric attrs mismatch: %+v", attrs)
	}
	retryOn := attrs["retry_on"].(map[string]any)
	if statuses := retryOn["statuses"].([]any); len(statuses) != 2 || statuses[0].(float64) != 429 {
		t.Errorf("statuses wire mismatch: %v", retryOn["statuses"])
	}
	if reasons := retryOn["reasons"].([]any); len(reasons) != 1 || reasons[0] != "TIMEOUT" {
		t.Errorf("reasons wire mismatch: %v", retryOn["reasons"])
	}

	// No max delay → omitted; empty RetryOn → both lists present but empty.
	plain := j.RetryPolicies().New(testRetryPolicyID, "n", 3, BackoffFixed, 5)
	if err := plain.Save(ctx); err != nil {
		t.Fatalf("Save (plain): %v", err)
	}
	attrs = rec.body["data"].(map[string]any)["attributes"].(map[string]any)
	if _, ok := attrs["max_delay_seconds"]; ok {
		t.Error("max_delay_seconds must be omitted when unset")
	}
	retryOn = attrs["retry_on"].(map[string]any)
	if s := retryOn["statuses"].([]any); len(s) != 0 {
		t.Errorf("empty statuses expected, got %v", s)
	}
	if rs := retryOn["reasons"].([]any); len(rs) != 0 {
		t.Errorf("empty reasons expected, got %v", rs)
	}
}

// List filters map to filter[name] / page[number] / page[size]; zero values omit
// them.
func TestRetryPolicies_ListQueryParams(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{
			"data": []any{}, "meta": map[string]any{"pagination": map[string]any{"page": 1, "size": 1000}},
		})
	})
	defer cleanup()

	name := "server"
	if _, err := j.RetryPolicies().List(ctx, ListRetryPoliciesInput{Name: &name, PageNumber: 2, PageSize: 25}); err != nil {
		t.Fatalf("List: %v", err)
	}
	for key, want := range map[string]string{"filter[name]": "server", "page[number]": "2", "page[size]": "25"} {
		if got := rec.query.Get(key); got != want {
			t.Errorf("retry policies list %s = %q, want %q", key, got, want)
		}
	}
	if _, err := j.RetryPolicies().List(ctx, ListRetryPoliciesInput{}); err != nil {
		t.Fatalf("List (defaults): %v", err)
	}
	for _, key := range []string{"filter[name]", "page[number]", "page[size]"} {
		if _, ok := rec.query[key]; ok {
			t.Errorf("retry policies list must omit %s at its zero value", key)
		}
	}
}

// Non-2xx, not-found, unexpected-create-status, empty-201, and transport errors
// all surface from the retry-policy surface.
func TestRetryPolicies_Errors(t *testing.T) {
	ctx := context.Background()

	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 500, map[string]any{"errors": []any{map[string]any{"detail": "boom"}}})
	})
	defer cleanup()
	rp := j.RetryPolicies()
	if _, err := rp.List(ctx, ListRetryPoliciesInput{}); err == nil {
		t.Error("List should error on 500")
	}
	if _, err := rp.Get(ctx, "p"); err == nil {
		t.Error("Get should error on 500")
	}
	if err := rp.Delete(ctx, "p"); err == nil {
		t.Error("Delete should error on 500")
	}
	create := rp.New("p", "n", 1, BackoffFixed, 5)
	if err := create.Save(ctx); err == nil {
		t.Error("Save (create) should error on 500")
	}
	update := rp.New("p", "n", 1, BackoffFixed, 5)
	update.CreatedAt = &nowForTest
	if err := update.Save(ctx); err == nil {
		t.Error("Save (update) should error on 500")
	}

	// 404 → NotFoundError
	j404, cleanup404 := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]any{"errors": []any{map[string]any{"detail": "missing"}}})
	})
	defer cleanup404()
	var notFound *NotFoundError
	if _, err := j404.RetryPolicies().Get(ctx, "missing"); !errors.As(err, &notFound) {
		t.Errorf("expected NotFoundError, got %v", err)
	}

	// create returning 2xx-but-not-201 → "unexpected status"
	jUnexp, cu := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	defer cu()
	if err := jUnexp.RetryPolicies().New("p", "n", 1, BackoffFixed, 5).Save(ctx); err == nil {
		t.Error("expected error when create returns non-201 2xx")
	}

	// create 201 with a non-JSON body → "empty 201 body"
	jEmpty, ce := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("ok"))
	})
	defer ce()
	if err := jEmpty.RetryPolicies().New("p", "n", 1, BackoffFixed, 5).Save(ctx); err == nil {
		t.Error("expected error when create 201 body is empty")
	}

	// transport errors (closed server)
	jc := newClosedJobs(t)
	rpc := jc.RetryPolicies()
	if _, err := rpc.List(ctx, ListRetryPoliciesInput{}); err == nil {
		t.Error("List should error on transport failure")
	}
	if _, err := rpc.Get(ctx, "p"); err == nil {
		t.Error("Get should error on transport failure")
	}
	if err := rpc.Delete(ctx, "p"); err == nil {
		t.Error("Delete should error on transport failure")
	}
	tc := rpc.New("p", "n", 1, BackoffFixed, 5)
	if err := tc.Save(ctx); err == nil {
		t.Error("Save (create) should error on transport failure")
	}
	tc.CreatedAt = &nowForTest
	if err := tc.Save(ctx); err == nil {
		t.Error("Save (update) should error on transport failure")
	}
}

// Save / Delete on a policy with no client (or no id) are guarded.
func TestRetryPolicies_UnsavedGuards(t *testing.T) {
	ctx := context.Background()
	orphan := &RetryPolicy{ID: "p", Name: "n"}
	if err := orphan.Save(ctx); err == nil {
		t.Error("Save without client should error")
	}
	if err := orphan.Delete(ctx); err == nil {
		t.Error("Delete without client should error")
	}
	j, cleanup := newTestJobs(t, retryFullHandler)
	defer cleanup()
	noID := &RetryPolicy{Name: "n", client: j.RetryPolicies()}
	if err := noID.Delete(ctx); err == nil {
		t.Error("Delete with empty id should error")
	}
}

// RetryOn serializes to / from the wire {statuses, reasons} shape; empty round
// trips to empty and a nil retry_on yields the zero RetryOn.
func TestRetryOn_WireConversions(t *testing.T) {
	// empty → both lists present and empty (not nil)
	w := retryOnToWire(RetryOn{})
	if w.Statuses == nil || len(*w.Statuses) != 0 {
		t.Errorf("empty statuses should be non-nil empty: %v", w.Statuses)
	}
	if w.Reasons == nil || len(*w.Reasons) != 0 {
		t.Errorf("empty reasons should be non-nil empty: %v", w.Reasons)
	}

	// populated
	w = retryOnToWire(RetryOn{Statuses: []int{429}, Reasons: []RetryReason{RetryReasonTimeout, RetryReasonConnectionError}})
	if len(*w.Statuses) != 1 || (*w.Statuses)[0] != 429 {
		t.Errorf("statuses: %v", w.Statuses)
	}
	if len(*w.Reasons) != 2 || (*w.Reasons)[0] != genjobs.RetryOnReasons("TIMEOUT") {
		t.Errorf("reasons: %v", w.Reasons)
	}

	// fromWire nil → empty
	if r := retryOnFromWire(nil); len(r.Statuses) != 0 || len(r.Reasons) != 0 {
		t.Errorf("nil fromWire: %+v", r)
	}
	// fromWire with nil inner slices → empty (the nil-slice branches)
	if r := retryOnFromWire(&genjobs.RetryOn{}); len(r.Statuses) != 0 || len(r.Reasons) != 0 {
		t.Errorf("empty-ptr fromWire: %+v", r)
	}
	// fromWire populated
	statuses := []int{503}
	reasons := []genjobs.RetryOnReasons{"NON_SUCCESS_STATUS"}
	r := retryOnFromWire(&genjobs.RetryOn{Statuses: &statuses, Reasons: &reasons})
	if len(r.Statuses) != 1 || r.Statuses[0] != 503 {
		t.Errorf("statuses fromWire: %v", r.Statuses)
	}
	if len(r.Reasons) != 1 || r.Reasons[0] != RetryReasonNonSuccessStatus {
		t.Errorf("reasons fromWire: %v", r.Reasons)
	}
}

// The resource builders carry the id only when set and emit max_delay_seconds
// only when present.
func TestRetryPolicy_ResourceBuilders(t *testing.T) {
	maxDelay := 60
	policy := &RetryPolicy{ID: "p", Name: "n", MaxRetries: 5, Backoff: BackoffExponential, DelaySeconds: 2, MaxDelaySeconds: &maxDelay}
	attrs := retryPolicyAttributes(policy)
	if attrs.MaxDelaySeconds == nil || *attrs.MaxDelaySeconds != 60 {
		t.Errorf("max delay attr: %v", attrs.MaxDelaySeconds)
	}
	if attrs.Backoff != genjobs.RetryPolicyBackoff("exponential") {
		t.Errorf("backoff attr: %v", attrs.Backoff)
	}

	plain := &RetryPolicy{ID: "p", Name: "n", MaxRetries: 1, Backoff: BackoffFixed, DelaySeconds: 5}
	if a := retryPolicyAttributes(plain); a.MaxDelaySeconds != nil {
		t.Errorf("max delay should be nil: %v", a.MaxDelaySeconds)
	}

	// update resource with id present
	if res := retryPolicyResourceFromPolicy(policy); res.Id == nil || *res.Id != "p" {
		t.Errorf("resource id should be set: %v", res.Id)
	}
	// update resource with empty id → nil Id pointer
	empty := &RetryPolicy{Name: "n", Backoff: BackoffFixed}
	if r := retryPolicyResourceFromPolicy(empty); r.Id != nil {
		t.Errorf("empty id should produce nil Id pointer, got %v", *r.Id)
	}
}

// retryPolicyFromResource handles the absent-id / absent-retry_on / absent-max
// delay branches as well as the fully-populated case.
func TestRetryPolicy_FromResourceBranches(t *testing.T) {
	min := genjobs.RetryPolicyResource{Attributes: genjobs.RetryPolicy{Name: "n", Backoff: "fixed", DelaySeconds: 5, MaxRetries: 0}}
	p := retryPolicyFromResource(min, nil)
	if p.ID != "" {
		t.Errorf("expected empty id, got %q", p.ID)
	}
	if p.MaxDelaySeconds != nil {
		t.Errorf("expected nil max delay, got %v", *p.MaxDelaySeconds)
	}
	if len(p.RetryOn.Statuses) != 0 || len(p.RetryOn.Reasons) != 0 {
		t.Errorf("expected empty RetryOn, got %+v", p.RetryOn)
	}

	id := "p"
	maxDelay := 60
	statuses := []int{429}
	reasons := []genjobs.RetryOnReasons{"TIMEOUT"}
	full := genjobs.RetryPolicyResource{
		Id: &id,
		Attributes: genjobs.RetryPolicy{
			Name: "n", Backoff: "exponential", DelaySeconds: 2, MaxRetries: 5,
			MaxDelaySeconds: &maxDelay,
			RetryOn:         &genjobs.RetryOn{Statuses: &statuses, Reasons: &reasons},
		},
	}
	p = retryPolicyFromResource(full, nil)
	if p.ID != "p" || p.MaxDelaySeconds == nil || *p.MaxDelaySeconds != 60 || p.Backoff != BackoffExponential {
		t.Errorf("full parse mismatch: %+v", p)
	}
	if len(p.RetryOn.Statuses) != 1 || len(p.RetryOn.Reasons) != 1 {
		t.Errorf("retry_on parse: %+v", p.RetryOn)
	}
}

// ---------------------------------------------------------------------------
// Job retry-policy setter + wire round-trip; run-list trigger filters
// ---------------------------------------------------------------------------

// SetRetryPolicy accepts a *RetryPolicy, a RetryPolicy value, or a bare id
// string, sets base vs per-environment, preserves a sibling field, and ignores
// an unsupported type.
func TestJob_SetRetryPolicy(t *testing.T) {
	j, cleanup := newTestJobs(t, fullHandler)
	defer cleanup()
	job := j.NewRecurringJob("id", "n", "0 * * * *", HttpConfig{URL: "https://base"})

	// base, via id string
	job.SetRetryPolicy("policy-a")
	if job.RetryPolicy != "policy-a" {
		t.Errorf("base retry policy (string) not set: %q", job.RetryPolicy)
	}
	// base, via *RetryPolicy (its ID is used)
	policy := j.RetryPolicies().New("policy-b", "n", 1, BackoffFixed, 5)
	job.SetRetryPolicy(policy)
	if job.RetryPolicy != "policy-b" {
		t.Errorf("base retry policy (object) not set: %q", job.RetryPolicy)
	}
	// base, via RetryPolicy value
	job.SetRetryPolicy(RetryPolicy{ID: "policy-c"})
	if job.RetryPolicy != "policy-c" {
		t.Errorf("base retry policy (value) not set: %q", job.RetryPolicy)
	}

	// per-env, preserving the already-set Enabled and leaving the base alone
	job.SetEnabled(true, "production")
	job.SetRetryPolicy("policy-prod", "production")
	prod := job.Environments["production"]
	if prod.RetryPolicy != "policy-prod" {
		t.Errorf("per-env retry policy not set: %q", prod.RetryPolicy)
	}
	if !prod.Enabled {
		t.Error("SetRetryPolicy must preserve Enabled on the existing override")
	}
	if job.RetryPolicy != "policy-c" {
		t.Errorf("per-env SetRetryPolicy must not change the base: %q", job.RetryPolicy)
	}

	// per-env on a brand-new environment creates the entry (object form)
	job.SetRetryPolicy(policy, "qa")
	if qa := job.Environments["qa"]; qa.RetryPolicy != "policy-b" {
		t.Errorf("qa retry policy mismatch: %+v", qa)
	}

	// an unsupported type is ignored (no panic, no change)
	job.SetRetryPolicy(42)
	if job.RetryPolicy != "policy-c" {
		t.Errorf("unsupported type must be ignored, base=%q", job.RetryPolicy)
	}
}

// The base and per-environment retry_policy (plus base timezone) ride the create
// body and parse back from the response.
func TestJob_RetryPolicyWireRoundTrip(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		attrs := map[string]any{
			"name": "n", "kind": "recurring", "type": "http",
			"schedule": "0 * * * *", "timezone": "America/New_York", "retry_policy": "base-policy",
			"configuration": httpCfgWire("https://base"),
			"environments": map[string]any{
				"production": map[string]any{"enabled": true, "retry_policy": "prod-policy"},
			},
			"concurrency_policy": "ALLOW", "version": 1,
			"created_at": "2026-06-04T00:00:00Z", "updated_at": "2026-06-04T00:00:00Z",
		}
		writeJSON(w, 201, map[string]any{"data": map[string]any{"id": testJobID, "type": "job", "attributes": attrs}})
	})
	defer cleanup()

	job := j.NewRecurringJob(testJobID, "n", "0 * * * *", HttpConfig{URL: "https://base"},
		WithJobTimezone("America/New_York"), WithJobRetryPolicy("base-policy"))
	job.SetEnabled(true, "production")
	job.SetRetryPolicy("prod-policy", "production")
	if err := job.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}

	attrs := rec.body["data"].(map[string]any)["attributes"].(map[string]any)
	if attrs["retry_policy"] != "base-policy" {
		t.Errorf("base retry_policy wire mismatch: %v", attrs["retry_policy"])
	}
	if attrs["timezone"] != "America/New_York" {
		t.Errorf("base timezone wire mismatch: %v", attrs["timezone"])
	}
	prod := attrs["environments"].(map[string]any)["production"].(map[string]any)
	if prod["retry_policy"] != "prod-policy" {
		t.Errorf("per-env retry_policy wire mismatch: %v", prod["retry_policy"])
	}

	// parse-back from the server response
	if job.RetryPolicy != "base-policy" {
		t.Errorf("base retry_policy parse mismatch: %q", job.RetryPolicy)
	}
	if job.Environments["production"].RetryPolicy != "prod-policy" {
		t.Errorf("per-env retry_policy parse mismatch: %q", job.Environments["production"].RetryPolicy)
	}
}

// A RETRY run surfaces its retry-chain position; a non-RETRY run has a nil Retry.
func TestRun_RetryParsed(t *testing.T) {
	ctx := context.Background()
	j, cleanup := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		attrs := map[string]any{
			"job": testJobID, "environment": "production",
			"trigger": "RETRY", "status": "PENDING",
			"retry": map[string]any{"of": testRunID, "attempt": 2},
		}
		writeJSON(w, 200, map[string]any{"data": map[string]any{"id": testRunID, "type": "run", "attributes": attrs}})
	})
	defer cleanup()
	got, err := j.Runs().Get(ctx, testRunID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Trigger != string(RunTriggerRetry) {
		t.Errorf("expected RETRY trigger, got %s", got.Trigger)
	}
	if got.Retry == nil || got.Retry.Of != testRunID || got.Retry.Attempt != 2 {
		t.Errorf("retry chain not parsed: %+v", got.Retry)
	}

	// A non-RETRY run carries no retry chain.
	j2, cleanup2 := newTestJobs(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": runResource(testRunID, "SUCCEEDED", "SCHEDULE", "production", nil)})
	})
	defer cleanup2()
	got2, err := j2.Runs().Get(ctx, testRunID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got2.Retry != nil {
		t.Errorf("non-RETRY run should have nil Retry, got %+v", got2.Retry)
	}
}

// Runs().List and (*Job).ListRuns thread Triggers (comma-joined filter[trigger])
// and LastRunOnly (sent only when true) onto the wire; both are omitted by
// default.
func TestRuns_ListTriggerAndLastRunFilters(t *testing.T) {
	ctx := context.Background()
	var rec capturedReq
	j, cleanup := recordingJobs(t, "", &rec, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []any{}, "meta": map[string]any{"page_size": 50}})
	})
	defer cleanup()

	if _, err := j.Runs().List(ctx, ListRunsInput{
		Triggers:    []RunTrigger{RunTriggerSchedule, RunTriggerRetry},
		LastRunOnly: true,
	}); err != nil {
		t.Fatalf("Runs.List: %v", err)
	}
	if got := rec.query.Get("filter[trigger]"); got != "SCHEDULE,RETRY" {
		t.Errorf("filter[trigger] = %q, want SCHEDULE,RETRY", got)
	}
	if got := rec.query.Get("last_run_only"); got != "true" {
		t.Errorf("last_run_only = %q, want true", got)
	}

	// defaults omit both
	if _, err := j.Runs().List(ctx, ListRunsInput{}); err != nil {
		t.Fatalf("Runs.List (defaults): %v", err)
	}
	if _, ok := rec.query["filter[trigger]"]; ok {
		t.Error("filter[trigger] must be omitted when no triggers are given")
	}
	if _, ok := rec.query["last_run_only"]; ok {
		t.Error("last_run_only must be omitted when false")
	}

	// (*Job).ListRuns threads the same filters (plus the single-environment scope)
	job := &Job{ID: testJobID, client: j}
	if _, err := job.ListRuns(ctx, ListJobRunsInput{
		Environment: "production",
		Triggers:    []RunTrigger{RunTriggerRetry},
		LastRunOnly: true,
	}); err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if rec.query.Get("filter[trigger]") != "RETRY" ||
		rec.query.Get("last_run_only") != "true" ||
		rec.query.Get("filter[environment]") != "production" {
		t.Errorf("Job.ListRuns query mismatch: %v", rec.query)
	}
}
