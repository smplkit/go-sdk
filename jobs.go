// Package smplkit — Smpl Jobs SDK client (client.Jobs() on SmplClient, or
// standalone JobsClient).
//
// Smpl Jobs runs an HTTP call on a schedule — a 5-field cron expression, a
// one-off datetime, or "now" — and records the history of each fire: the
// request sent, the response received, timing, and outcome. It is reachable
// two ways:
//
//	client.Jobs().* on SmplClient
//	directly — NewJobsClient(...) — for callers that only need jobs.
//
// A Job is an active record: build it with JobsClient.New, set fields, and
// call Save(ctx) (create when new, full-replace update when it already
// exists) or Delete(ctx). Runs are read-only views; run actions live on
// client.Jobs().Runs().
package smplkit

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	genjobs "github.com/smplkit/go-sdk/v3/internal/generated/jobs"
)

// JobsClient is the Smpl Jobs client.
//
// Reachable as client.Jobs() (SmplClient) or constructed directly:
//
//	jobs, err := smplkit.NewJobsClient(smplkit.Config{APIKey: "sk_..."})
//	list, err := jobs.List(ctx, smplkit.ListJobsInput{})
//	for _, job := range list {
//		fmt.Println(job.ID)
//	}
//
// The surface is active-record job CRUD (New / Get / List / Delete), the
// run-now action (Run), usage counters (Usage), and run history plus run
// actions (Runs).
type JobsClient struct {
	gen  *genjobs.ClientWithResponses
	runs *RunsClient
	// environment is the SDK's configured environment (empty when unset). It
	// defaults the one-off birth env on create, the run-now env header, and
	// the filter[environment] scope on Runs().List.
	environment string
}

// newJobsClient wires a JobsClient (and its Runs sub-client) onto a pre-built
// jobs transport (the wired path used by SmplClient). The environment scopes
// environment-aware writes/reads (one-off birth, run-now, runs filter).
func newJobsClient(gen *genjobs.ClientWithResponses, environment string) *JobsClient {
	return &JobsClient{
		gen:         gen,
		runs:        &RunsClient{gen: gen, environment: environment},
		environment: environment,
	}
}

// NewJobsClient creates a standalone Smpl Jobs client that resolves and owns
// its own jobs transport.
//
// cfg.Environment is the default environment for environment-scoped
// operations — the environment a one-off job created through this client is
// born in, the default a manual run executes in, and the default scope for
// Runs().List. Leave it empty to leave these unset (the credential's permitted
// environment is implied where unambiguous).
func NewJobsClient(cfg Config, opts ...ClientOption) (*JobsClient, error) {
	rc, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}
	optCfg := defaultConfig()
	for _, opt := range opts {
		opt(&optCfg)
	}
	httpClient, _ := buildGenClients(optCfg, rc)
	return newJobsClient(buildJobsGenClient(optCfg, rc, httpClient), rc.environment), nil
}

// Runs returns the run history and run-action sub-client.
func (j *JobsClient) Runs() *RunsClient { return j.runs }

// RunsClient is the client.Jobs().Runs() surface: read-only run history plus
// the cancel / rerun run actions.
type RunsClient struct {
	gen *genjobs.ClientWithResponses
	// environment is the SDK's configured environment (empty when unset); it
	// scopes List as the default filter[environment].
	environment string
}

// ---------------------------------------------------------------------------
// Job active-record surface
// ---------------------------------------------------------------------------

// New returns an unsaved Job bound to this client. Call (*Job).Save(ctx)
// to create it.
//
// id is the caller-supplied unique identifier for the job — unique within
// the account and immutable; the service returns 409 if another live job
// already uses this id. name is the human-readable name. schedule is when
// the job runs: a 5-field cron expression evaluated in UTC (recurring), an
// ISO-8601 datetime (a one-off run at that instant), or the literal "now"
// (run once, as soon as possible) — a datetime or "now" job disables itself
// after it fires. configuration is the HTTP request the job sends each time
// it fires.
//
// Enablement is per environment, not a writable base flag: pass
// WithJobEnvironments (or call SetEnabled after construction) to choose where
// a recurring job runs. A one-off ("now" / datetime) job is born in a single
// environment — WithJobBirthEnvironment, defaulting to the client's configured
// environment. The remaining fields — description and concurrency policy — are
// set via the WithJob* options.
func (j *JobsClient) New(
	id string,
	name string,
	schedule string,
	configuration HttpConfig,
	opts ...JobOption,
) *Job {
	job := &Job{
		ID:                id,
		Name:              name,
		Schedule:          schedule,
		Configuration:     configuration,
		Type:              "http",
		ConcurrencyPolicy: "ALLOW",
		birthEnvironment:  j.environment,
		client:            j,
	}
	for _, opt := range opts {
		opt(job)
	}
	return job
}

// JobOption configures an unsaved Job returned by JobsClient.New.
type JobOption func(*Job)

// WithJobEnvironments sets the per-environment override map that drives
// enablement. A job fires in an environment only when that environment's entry
// has Enabled=true; each entry may carry an optional HttpConfig override (nil
// inherits the base configuration). Every referenced environment must exist for
// the account. Without this option (and without SetEnabled), a recurring job is
// enabled nowhere.
func WithJobEnvironments(environments map[string]JobEnvironment) JobOption {
	return func(job *Job) { job.Environments = environments }
}

// WithJobBirthEnvironment sets the single environment a one-off ("now" /
// datetime) job is born in, sent as the X-Smplkit-Environment header on create.
// Defaults to the client's configured environment. Ignored for a recurring job,
// whose environments come from WithJobEnvironments / SetEnabled.
func WithJobBirthEnvironment(environment string) JobOption {
	return func(job *Job) { job.birthEnvironment = environment }
}

// WithJobDescription sets the optional free-text description.
func WithJobDescription(description string) JobOption {
	return func(job *Job) { job.Description = &description }
}

// WithJobConcurrencyPolicy overrides how overlapping runs are handled.
// "ALLOW" (the default and only value today) permits a new run to start
// while a previous one is still in flight.
func WithJobConcurrencyPolicy(policy string) JobOption {
	return func(job *Job) { job.ConcurrencyPolicy = policy }
}

// Save creates this job on the server, or full-replaces it if it already
// exists. Upsert behavior keyed on CreatedAt: nil → create (POST), set →
// full-replace update (PUT). After the call, every field is refreshed
// from the server response (including CreatedAt, UpdatedAt, Version).
func (job *Job) Save(ctx context.Context) error {
	if job.client == nil {
		return &Error{Message: "job was constructed without a client; cannot save"}
	}
	if job.CreatedAt == nil {
		updated, err := job.client.create(ctx, job)
		if err != nil {
			return err
		}
		job.apply(updated)
		return nil
	}
	updated, err := job.client.update(ctx, job)
	if err != nil {
		return err
	}
	job.apply(updated)
	return nil
}

// Delete removes this job on the server.
func (job *Job) Delete(ctx context.Context) error {
	if job.client == nil || job.ID == "" {
		return &Error{Message: "job was constructed without a client or id; cannot delete"}
	}
	return job.client.Delete(ctx, job.ID)
}

// environmentOverride returns the override for environment, creating an empty
// one if absent.
//
// The per-environment mutators reach through here so an existing override's
// other field is preserved when only one of Enabled / Configuration is being
// set.
func (job *Job) environmentOverride(environment string) *JobEnvironment {
	if job.Environments == nil {
		job.Environments = map[string]JobEnvironment{}
	}
	env, ok := job.Environments[environment]
	if !ok {
		env = JobEnvironment{}
		job.Environments[environment] = env
	}
	return &env
}

// SetEnabled sets this job's enablement in a single environment, in memory.
//
// Enablement is strictly per-environment: this sets the per-environment
// override's Enabled on Environments, creating the override entry if it doesn't
// exist yet (preserving any already-set Configuration on it). The base Enabled
// is a read-only roll-up the server derives and cannot be set here. Call Save
// to persist.
func (job *Job) SetEnabled(enabled bool, environment string) {
	env := job.environmentOverride(environment)
	env.Enabled = enabled
	job.Environments[environment] = *env
}

// IsEnabled reports whether the job is enabled.
//
// With environment empty, returns the roll-up — enabled in at least one
// environment — derived locally from the Environments map (so it stays correct
// for in-memory edits made via SetEnabled before Save). With environment given,
// returns whether the job is enabled in that specific environment.
func (job *Job) IsEnabled(environment string) bool {
	if environment == "" {
		return anyEnvironmentEnabled(job.Environments)
	}
	env, ok := job.Environments[environment]
	return ok && env.Enabled
}

// anyEnvironmentEnabled is the base-enabled roll-up: true iff at least one
// environment override has Enabled=true. The jobs API no longer exposes a
// top-level enabled flag, so the wrapper derives it from the environments map.
func anyEnvironmentEnabled(environments map[string]JobEnvironment) bool {
	for _, env := range environments {
		if env.Enabled {
			return true
		}
	}
	return false
}

// SetConfiguration sets this job's configuration in memory.
//
// With environment empty, replaces the base Configuration. With environment
// given, sets the per-environment override's configuration on Environments,
// creating the override entry if it doesn't exist yet (preserving any
// already-set Enabled on it). Call Save to persist.
func (job *Job) SetConfiguration(configuration HttpConfig, environment string) {
	if environment == "" {
		job.Configuration = configuration
		return
	}
	env := job.environmentOverride(environment)
	env.Configuration = &configuration
	job.Environments[environment] = *env
}

// GetConfiguration returns the job's effective configuration.
//
// With environment empty, returns the base Configuration. With environment
// given, returns that environment's configuration override when it has one,
// else the base configuration — the request the job actually sends when it
// fires in that environment.
func (job *Job) GetConfiguration(environment string) HttpConfig {
	if environment != "" {
		if env, ok := job.Environments[environment]; ok && env.Configuration != nil {
			return *env.Configuration
		}
	}
	return job.Configuration
}

// SetSchedule sets the job's schedule in memory — base or per-environment.
//
// Called with no environment (or an empty environment), it sets the base
// Schedule: the cadence every environment inherits unless it overrides it.
// Called with an environment, it sets that environment's per-environment cron
// override on Environments, creating the override entry if it doesn't exist yet
// (preserving any already-set Enabled / Configuration on it). A per-environment
// schedule varies the cadence for just that environment (recurring jobs only);
// clear it (set it back to the base cadence) to fall back to the base schedule.
// At most one environment may be named; extra arguments are ignored. Call Save
// to persist.
func (job *Job) SetSchedule(schedule string, environment ...string) {
	env := ""
	if len(environment) > 0 {
		env = environment[0]
	}
	if env == "" {
		job.Schedule = schedule
		return
	}
	override := job.environmentOverride(env)
	override.Schedule = schedule
	job.Environments[env] = *override
}

// Trigger starts one immediate, manual run of this job (a MANUAL run) and
// returns it. environment is the environment the run executes in; empty
// defaults to the client's configured environment.
func (job *Job) Trigger(ctx context.Context, environment string) (*Run, error) {
	if job.client == nil {
		return nil, &Error{Message: "job was constructed without a client; cannot trigger a run"}
	}
	return job.client.Run(ctx, job.ID, environment)
}

// ListRuns returns this job's run history, most recent first.
//
// input.Environment restricts the listing to runs stamped with that single
// environment (empty covers every environment you can access). PageSize / After
// drive cursor pagination.
func (job *Job) ListRuns(ctx context.Context, input ListJobRunsInput) ([]*Run, error) {
	if job.client == nil {
		return nil, &Error{Message: "job was constructed without a client; cannot list runs"}
	}
	runsInput := ListRunsInput{
		Job:      job.ID,
		PageSize: input.PageSize,
		After:    input.After,
	}
	if input.Environment != "" {
		runsInput.Environments = []string{input.Environment}
	}
	return job.client.runs.List(ctx, runsInput)
}

func (job *Job) apply(other *Job) {
	job.ID = other.ID
	job.Name = other.Name
	job.Description = other.Description
	job.Enabled = other.Enabled
	job.Environments = other.Environments
	job.Recurring = other.Recurring
	job.Type = other.Type
	job.Schedule = other.Schedule
	job.Configuration = other.Configuration
	job.ConcurrencyPolicy = other.ConcurrencyPolicy
	job.CreatedAt = other.CreatedAt
	job.UpdatedAt = other.UpdatedAt
	job.DeletedAt = other.DeletedAt
	job.Version = other.Version
}

// ---------------------------------------------------------------------------
// Job collection read surface
// ---------------------------------------------------------------------------

// List returns the jobs for the authenticated account. Offset pagination
// via PageNumber / PageSize.
func (j *JobsClient) List(ctx context.Context, input ListJobsInput) ([]*Job, error) {
	params := &genjobs.ListJobsParams{}
	if input.Recurring != nil {
		params.FilterRecurring = input.Recurring
	}
	if input.Name != nil {
		params.FilterName = input.Name
	}
	if input.PageNumber > 0 {
		params.PageNumber = &input.PageNumber
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	resp, err := j.gen.ListJobsWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("jobs List: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	body := resp.ApplicationvndApiJSON200
	jobs := make([]*Job, 0, len(body.Data))
	for _, r := range body.Data {
		jobs = append(jobs, jobFromResource(r, j))
	}
	return jobs, nil
}

// Get returns one job by id; the returned instance is bound to this client
// so job.Save(ctx) and job.Delete(ctx) work.
func (j *JobsClient) Get(ctx context.Context, id string) (*Job, error) {
	resp, err := j.gen.GetJobWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("jobs Get: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	return jobFromResource(resp.ApplicationvndApiJSON200.Data, j), nil
}

// Delete removes a job by id.
func (j *JobsClient) Delete(ctx context.Context, id string) error {
	resp, err := j.gen.DeleteJobWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("jobs Delete: %w", err)
	}
	if resp.StatusCode() != 204 {
		return checkStatus(resp.StatusCode(), resp.Body)
	}
	return nil
}

// Run triggers one immediate MANUAL run of the job and returns it.
//
// environment is the environment the manual run executes in; empty defaults to
// the client's configured environment (and a single-environment credential
// implies it). It is sent as the X-Smplkit-Environment header.
func (j *JobsClient) Run(ctx context.Context, id string, environment string) (*Run, error) {
	env := environment
	if env == "" {
		env = j.environment
	}
	params := &genjobs.RunJobNowParams{}
	if env != "" {
		params.XSmplkitEnvironment = &env
	}
	resp, err := j.gen.RunJobNowWithResponse(ctx, id, params)
	if err != nil {
		return nil, fmt.Errorf("jobs Run: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := runFromResource(resp.ApplicationvndApiJSON200.Data, j.runs)
	return &out, nil
}

// Usage returns the current-period usage counters for the account.
func (j *JobsClient) Usage(ctx context.Context) (*Usage, error) {
	resp, err := j.gen.GetUsageWithResponse(ctx, &genjobs.GetUsageParams{})
	if err != nil {
		return nil, fmt.Errorf("jobs Usage: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := usageFromResource(resp.ApplicationvndApiJSON200.Data)
	return &out, nil
}

// ---------------------------------------------------------------------------
// Runs read + action surface
// ---------------------------------------------------------------------------

// List returns runs for the authenticated account, newest first. Cursor
// paginated: pass PageSize and the After cursor from the prior page. Pass
// Job to scope to a single job's history, and Environments to scope to one or
// more environment keys (resolved as explicit list → client default → omitted).
func (r *RunsClient) List(ctx context.Context, input ListRunsInput) ([]*Run, error) {
	params := &genjobs.ListRunsParams{}
	if input.Job != "" {
		job := input.Job
		params.FilterJob = &job
	}
	if env := resolveEnvironmentFilter(input.Environments, r.environment); env != "" {
		params.FilterEnvironment = &env
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.After != "" {
		after := input.After
		params.PageAfter = &after
	}
	resp, err := r.gen.ListRunsWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("jobs Runs.List: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	body := resp.ApplicationvndApiJSON200
	runs := make([]*Run, 0, len(body.Data))
	for _, res := range body.Data {
		run := runFromResource(res, r)
		runs = append(runs, &run)
	}
	return runs, nil
}

// Get returns one run by id.
func (r *RunsClient) Get(ctx context.Context, runID string) (*Run, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return nil, &ValidationError{Base: Error{Message: fmt.Sprintf("invalid run id %q: %s", runID, err)}}
	}
	resp, err := r.gen.GetRunWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("jobs Runs.Get: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := runFromResource(resp.ApplicationvndApiJSON200.Data, r)
	return &out, nil
}

// Cancel cancels a pending run and returns its updated state.
func (r *RunsClient) Cancel(ctx context.Context, runID string) (*Run, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return nil, &ValidationError{Base: Error{Message: fmt.Sprintf("invalid run id %q: %s", runID, err)}}
	}
	resp, err := r.gen.CancelRunWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("jobs Runs.Cancel: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := runFromResource(resp.ApplicationvndApiJSON200.Data, r)
	return &out, nil
}

// Rerun re-runs a prior run, spawning a new RERUN run that inherits the source
// run's environment.
func (r *RunsClient) Rerun(ctx context.Context, runID string) (*Run, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return nil, &ValidationError{Base: Error{Message: fmt.Sprintf("invalid run id %q: %s", runID, err)}}
	}
	resp, err := r.gen.RerunRunWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("jobs Runs.Rerun: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := runFromResource(resp.ApplicationvndApiJSON200.Data, r)
	return &out, nil
}

// Rerun starts a new run that repeats this one (a RERUN), in the same
// environment.
func (run *Run) Rerun(ctx context.Context) (*Run, error) {
	if run.runs == nil {
		return nil, &Error{Message: "run was constructed without a client; cannot rerun"}
	}
	return run.runs.Rerun(ctx, run.ID)
}

// Cancel cancels this run if it has not finished yet.
func (run *Run) Cancel(ctx context.Context) (*Run, error) {
	if run.runs == nil {
		return nil, &Error{Message: "run was constructed without a client; cannot cancel"}
	}
	return run.runs.Cancel(ctx, run.ID)
}

// ---------------------------------------------------------------------------
// Internal helpers (create / update / wire conversions)
// ---------------------------------------------------------------------------

// create posts a new job and returns the server-authoritative response.
// Called by (*Job).Save on unsaved instances.
func (j *JobsClient) create(ctx context.Context, job *Job) (*Job, error) {
	body := genjobs.CreateJobApplicationVndAPIPlusJSONRequestBody{
		Data: jobCreateResourceFromJob(job),
	}
	// A one-off job is born in the environment named here (recurring jobs
	// ignore it server-side; their environments come from the map).
	params := &genjobs.CreateJobParams{}
	if job.birthEnvironment != "" {
		env := job.birthEnvironment
		params.XSmplkitEnvironment = &env
	}
	resp, err := j.gen.CreateJobWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, params, body)
	if err != nil {
		return nil, fmt.Errorf("jobs Create: %w", err)
	}
	if resp.StatusCode() != 201 {
		if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("jobs Create: unexpected status %d", resp.StatusCode())
	}
	if resp.ApplicationvndApiJSON201 == nil {
		return nil, fmt.Errorf("jobs Create: empty 201 body")
	}
	return jobFromResource(resp.ApplicationvndApiJSON201.Data, j), nil
}

// update PUTs a full-replace and returns the server-authoritative
// response. Called by (*Job).Save on saved instances.
func (j *JobsClient) update(ctx context.Context, job *Job) (*Job, error) {
	body := genjobs.UpdateJobApplicationVndAPIPlusJSONRequestBody{
		Data: jobResourceFromJob(job.ID, job),
	}
	// Name the client's configured environment on update (ignored server-side
	// for a recurring job, whose environments come from the map).
	params := &genjobs.UpdateJobParams{}
	if j.environment != "" {
		env := j.environment
		params.XSmplkitEnvironment = &env
	}
	resp, err := j.gen.UpdateJobWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, job.ID, params, body)
	if err != nil {
		return nil, fmt.Errorf("jobs Update: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	return jobFromResource(resp.ApplicationvndApiJSON200.Data, j), nil
}

// jobAttributes builds the shared Job attribute payload sent on create and
// update.
//
// The base `enabled` is a server-derived read-only roll-up — the wrapper never
// sends it. Enablement travels entirely through the `environments` map, which
// is included only when non-empty.
func jobAttributes(job *Job) genjobs.Job {
	attrs := genjobs.Job{
		Name:          job.Name,
		Schedule:      job.Schedule,
		Configuration: httpConfigToWire(job.Configuration),
	}
	if len(job.Environments) > 0 {
		envs := jobEnvironmentsToWire(job.Environments)
		attrs.Environments = &envs
	}
	if job.Description != nil {
		attrs.Description = job.Description
	}
	if job.Type != "" {
		t := genjobs.JobType(job.Type)
		attrs.Type = &t
	}
	if job.ConcurrencyPolicy != "" {
		cp := genjobs.JobConcurrencyPolicy(job.ConcurrencyPolicy)
		attrs.ConcurrencyPolicy = &cp
	}
	return attrs
}

// jobEnvironmentsToWire converts the wrapper per-environment override map to
// the generated model. Per-environment configuration overrides are sent as
// full HttpConfig payloads (plaintext headers in), mirroring the base
// configuration's round-trip semantics. A per-environment Schedule override is
// sent only when set; the read-only NextRunAt is never sent.
func jobEnvironmentsToWire(envs map[string]JobEnvironment) map[string]genjobs.JobEnvironment {
	out := make(map[string]genjobs.JobEnvironment, len(envs))
	for key, env := range envs {
		enabled := env.Enabled
		ge := genjobs.JobEnvironment{Enabled: &enabled}
		if env.Schedule != "" {
			schedule := env.Schedule
			ge.Schedule = &schedule
		}
		if env.Configuration != nil {
			cfg := httpConfigToWire(*env.Configuration)
			ge.Configuration = &cfg
		}
		out[key] = ge
	}
	return out
}

// jobEnvironmentsFromWire converts the generated per-environment override map
// back into the wrapper shape, surfacing the per-environment Schedule override
// and the read-only NextRunAt.
func jobEnvironmentsFromWire(envs map[string]genjobs.JobEnvironment) map[string]JobEnvironment {
	out := make(map[string]JobEnvironment, len(envs))
	for key, ge := range envs {
		env := JobEnvironment{}
		if ge.Enabled != nil {
			env.Enabled = *ge.Enabled
		}
		if ge.Schedule != nil {
			env.Schedule = *ge.Schedule
		}
		if ge.Configuration != nil {
			cfg := httpConfigFromWire(*ge.Configuration)
			env.Configuration = &cfg
		}
		if ge.NextRunAt != nil {
			env.NextRunAt = ge.NextRunAt
		}
		out[key] = env
	}
	return out
}

func jobCreateResourceFromJob(job *Job) genjobs.JobCreateResource {
	rt := genjobs.JobCreateResourceType("job")
	return genjobs.JobCreateResource{
		Id:         job.ID,
		Type:       &rt,
		Attributes: jobAttributes(job),
	}
}

func jobResourceFromJob(id string, job *Job) genjobs.JobResource {
	rt := "job"
	var idPtr *string
	if id != "" {
		idPtr = &id
	}
	return genjobs.JobResource{
		Id:         idPtr,
		Type:       &rt,
		Attributes: jobAttributes(job),
	}
}

func httpConfigToWire(h HttpConfig) genjobs.JobHttpConfiguration {
	out := genjobs.JobHttpConfiguration{
		Url: h.URL,
	}
	if h.Method != "" {
		m := genjobs.JobHttpConfigurationMethod(h.Method)
		out.Method = &m
	}
	if h.SuccessStatus != "" {
		s := h.SuccessStatus
		out.SuccessStatus = &s
	}
	if h.Timeout != 0 {
		t := h.Timeout
		out.Timeout = &t
	}
	if h.Body != nil {
		b := *h.Body
		out.Body = &b
	}
	if h.TlsVerify != nil {
		v := *h.TlsVerify
		out.TlsVerify = &v
	}
	if h.CaCert != nil {
		c := *h.CaCert
		out.CaCert = &c
	}
	if len(h.Headers) > 0 {
		hh := make([]genjobs.HttpHeader, 0, len(h.Headers))
		for _, hdr := range h.Headers {
			hh = append(hh, genjobs.HttpHeader{Name: hdr.Name, Value: hdr.Value})
		}
		out.Headers = &hh
	}
	return out
}

func httpConfigFromWire(h genjobs.JobHttpConfiguration) HttpConfig {
	out := HttpConfig{
		URL: h.Url,
	}
	if h.Method != nil {
		out.Method = JobHttpMethod(*h.Method)
	}
	if h.SuccessStatus != nil {
		out.SuccessStatus = *h.SuccessStatus
	}
	if h.Timeout != nil {
		out.Timeout = *h.Timeout
	}
	if h.Body != nil {
		b := *h.Body
		out.Body = &b
	}
	if h.TlsVerify != nil {
		v := *h.TlsVerify
		out.TlsVerify = &v
	}
	if h.CaCert != nil {
		c := *h.CaCert
		out.CaCert = &c
	}
	if h.Headers != nil {
		out.Headers = make([]HttpHeader, 0, len(*h.Headers))
		for _, hdr := range *h.Headers {
			out.Headers = append(out.Headers, HttpHeader{Name: hdr.Name, Value: hdr.Value})
		}
	}
	return out
}

func jobFromResource(r genjobs.JobResource, client *JobsClient) *Job {
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	a := r.Attributes
	out := &Job{
		ID:            id,
		Name:          a.Name,
		Description:   a.Description,
		Schedule:      a.Schedule,
		Configuration: httpConfigFromWire(a.Configuration),
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
		DeletedAt:     a.DeletedAt,
		Version:       a.Version,
		Type:          "http",
		client:        client,
	}
	if a.Environments != nil {
		out.Environments = jobEnvironmentsFromWire(*a.Environments)
	}
	// The base `enabled` is a read-only roll-up: enablement now lives strictly
	// per-environment on the wire, so derive it locally as "enabled in at least
	// one environment" rather than reading a top-level field.
	out.Enabled = anyEnvironmentEnabled(out.Environments)
	if a.Recurring != nil {
		out.Recurring = a.Recurring
	}
	if a.Type != nil {
		out.Type = string(*a.Type)
	}
	out.ConcurrencyPolicy = "ALLOW"
	if a.ConcurrencyPolicy != nil {
		out.ConcurrencyPolicy = string(*a.ConcurrencyPolicy)
	}
	return out
}

func runFromResource(r genjobs.RunResource, runs *RunsClient) Run {
	a := r.Attributes
	out := Run{
		ID:                r.Id,
		Job:               a.Job,
		JobVersion:        a.JobVersion,
		Environment:       a.Environment,
		Trigger:           string(a.Trigger),
		ScheduledFor:      a.ScheduledFor,
		Status:            string(a.Status),
		StartedAt:         a.StartedAt,
		FinishedAt:        a.FinishedAt,
		PendingDurationMs: a.PendingDurationMs,
		RunDurationMs:     a.RunDurationMs,
		TotalDurationMs:   a.TotalDurationMs,
		Error:             a.Error,
		CreatedAt:         a.CreatedAt,
		runs:              runs,
	}
	if a.RerunOf != nil {
		s := a.RerunOf.String()
		out.RerunOf = &s
	}
	if a.FailureReason != nil {
		s := string(*a.FailureReason)
		out.FailureReason = &s
	}
	if a.Request != nil {
		out.Request = *a.Request
	}
	if a.Result != nil {
		out.Result = *a.Result
	}
	return out
}

func usageFromResource(r genjobs.UsageResource) Usage {
	a := r.Attributes
	return Usage{
		Period:          a.Period,
		RunsUsed:        a.RunsUsed,
		RunsIncluded:    a.RunsIncluded,
		ActiveJobs:      a.ActiveJobs,
		ActiveJobsLimit: a.ActiveJobsLimit,
	}
}
