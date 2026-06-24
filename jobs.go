// Package smplkit — Smpl Jobs SDK client (client.Jobs() on SmplClient, or
// standalone JobsClient).
//
// Smpl Jobs runs an HTTP call — on a schedule (a 5-field cron expression, a
// one-off datetime, or "now") or on demand (a manual job with no schedule) —
// and records the history of each fire: the request sent, the response
// received, timing, and outcome. It is reachable two ways:
//
//	client.Jobs().* on SmplClient
//	directly — NewJobsClient(...) — for callers that only need jobs.
//
// A Job is an active record: build it with JobsClient.NewRecurringJob,
// NewManualJob, or Schedule, set fields, and call Save(ctx) (create when new,
// full-replace update when it already exists) or Delete(ctx). Runs are
// read-only views; run actions live on client.Jobs().Runs().
package smplkit

import (
	"context"
	"fmt"
	"strings"
	"time"

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
// The surface is active-record job CRUD (NewRecurringJob / NewManualJob /
// Schedule / Get / List / Delete), the run-now action (Run), usage counters
// (Usage), and run history plus run actions (Runs).
type JobsClient struct {
	gen           *genjobs.ClientWithResponses
	runs          *RunsClient
	retryPolicies *RetryPoliciesClient
	// environment is the SDK's configured environment (empty when unset). It
	// defaults the one-off birth env on create, the run-now body environment,
	// and the filter[environment] scope on Runs().List.
	environment string
}

// newJobsClient wires a JobsClient (and its Runs / RetryPolicies sub-clients)
// onto a pre-built jobs transport (the wired path used by SmplClient). The
// environment scopes environment-aware writes/reads (one-off birth, run-now,
// runs filter); retry policies are account-global and take no environment.
func newJobsClient(gen *genjobs.ClientWithResponses, environment string) *JobsClient {
	return &JobsClient{
		gen:           gen,
		runs:          &RunsClient{gen: gen, environment: environment},
		retryPolicies: &RetryPoliciesClient{gen: gen},
		environment:   environment,
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

// RetryPolicies returns the retry-policy management sub-client. Retry policies
// are account-global — never environment-scoped.
func (j *JobsClient) RetryPolicies() *RetryPoliciesClient { return j.retryPolicies }

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

// newJob builds an unsaved Job with the shared defaults; the three public
// constructors (NewRecurringJob / NewManualJob / Schedule) supply the
// kind-specific schedule and forward the caller's options. birthEnvironment
// defaults to the client's configured environment; WithJobBirthEnvironment
// overrides it.
func (j *JobsClient) newJob(
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

// NewRecurringJob returns an unsaved recurring Job bound to this client. Call
// (*Job).Save(ctx) to create it.
//
// id is the caller-supplied unique identifier for the job — unique within the
// account and immutable; the service returns 409 if another live job already
// uses this id. name is the human-readable name. schedule is the base cadence —
// a 5-field cron expression evaluated in UTC (e.g. "0 2 * * *") — that every
// environment inherits unless it sets its own override. configuration is the
// HTTP request the job sends each time it fires.
//
// Enablement is per environment: pass WithJobEnvironments, or set
// job.Environment(env).Enabled = true after construction, to choose where the
// job is scheduled. The remaining fields — description, base timezone
// (WithJobTimezone), base retry policy (WithJobRetryPolicy), and concurrency
// policy — are set via the WithJob* options.
func (j *JobsClient) NewRecurringJob(
	id string,
	name string,
	schedule string,
	configuration HttpConfig,
	opts ...JobOption,
) *Job {
	return j.newJob(id, name, schedule, configuration, opts...)
}

// NewManualJob returns an unsaved manual Job bound to this client. Call
// (*Job).Save(ctx) to create it.
//
// A manual job has no schedule — it never auto-fires and runs only when
// triggered via Run / (*Job).Trigger.
//
// id is the caller-supplied unique identifier for the job — unique within the
// account and immutable; the service returns 409 if another live job already
// uses this id. name is the human-readable name. configuration is the HTTP
// request the job sends each time it runs.
//
// Enablement is per environment: pass WithJobEnvironments, or set
// job.Environment(env).Enabled = true after construction, to choose where the
// job is triggerable. The remaining fields — description, retry policy
// (WithJobRetryPolicy), and concurrency policy — are set via the WithJob*
// options. A manual job has no cron, so WithJobTimezone does not apply.
func (j *JobsClient) NewManualJob(
	id string,
	name string,
	configuration HttpConfig,
	opts ...JobOption,
) *Job {
	return j.newJob(id, name, "", configuration, opts...)
}

// Schedule returns an unsaved one-off Job bound to this client. Call
// (*Job).Save(ctx) to create it.
//
// A one-off job runs a single time at schedule and is then spent.
//
// id is the caller-supplied unique identifier for the job — unique within the
// account and immutable; the service returns 409 if another live job already
// uses this id. name is the human-readable name. schedule is the instant the
// single run fires. configuration is the HTTP request the job sends when it
// runs.
//
// The job is born in a single environment — WithJobBirthEnvironment, defaulting
// to the client's configured environment. A retry policy may be set via
// WithJobRetryPolicy; a one-off job has no cron, so WithJobTimezone does not
// apply.
func (j *JobsClient) Schedule(
	id string,
	name string,
	schedule time.Time,
	configuration HttpConfig,
	opts ...JobOption,
) *Job {
	job := j.newJob(id, name, schedule.Format(time.RFC3339), configuration, opts...)
	// A one-off job's birth environment now travels in the request body as an
	// enabled entry of the environments map (there is no X-Smplkit-Environment
	// header). birthEnvironment is resolved after the options run, so an explicit
	// WithJobBirthEnvironment wins over the client default; when neither is known
	// the map is left empty and a single-environment credential implies it.
	if job.birthEnvironment != "" {
		job.Environment(job.birthEnvironment).Enabled = true
	}
	return job
}

// JobOption configures an unsaved Job returned by JobsClient.NewRecurringJob,
// NewManualJob, or Schedule.
type JobOption func(*Job)

// WithJobEnvironments sets the per-environment override map that drives
// enablement. A job fires in an environment only when that environment's entry
// has Enabled=true; each entry is a flat overlay that overrides only the leaves
// it sets (URL, Method, Schedule, headers, …), inheriting the base
// configuration for the rest. Every referenced environment must exist for the
// account. Without this option (and without setting any Environment), a
// recurring job is enabled nowhere. The entries are copied, so later mutations
// of the passed map do not affect the job; mutate via job.Environment(env)
// instead.
func WithJobEnvironments(environments map[string]JobEnvironment) JobOption {
	return func(job *Job) {
		job.Environments = make(map[string]*JobEnvironment, len(environments))
		for key, env := range environments {
			env := env
			job.Environments[key] = &env
		}
	}
}

// WithJobBirthEnvironment sets the single environment a one-off ("now" /
// datetime) job is born in. On create it travels in the request body as an
// enabled entry of the environments map ({env: {enabled: true}}); there is no
// X-Smplkit-Environment header. Defaults to the client's configured
// environment. Ignored for recurring and manual jobs, whose environments come
// from WithJobEnvironments / SetEnabled.
func WithJobBirthEnvironment(environment string) JobOption {
	return func(job *Job) { job.birthEnvironment = environment }
}

// WithJobDescription sets the optional free-text description.
func WithJobDescription(description string) JobOption {
	return func(job *Job) { job.Description = &description }
}

// WithJobTimezone sets the base IANA timezone the cron schedule is evaluated in
// (e.g. "America/New_York"), DST-aware; empty means UTC. Every environment
// inherits it unless it overrides it. Only meaningful on a recurring (cron)
// job — manual and one-off jobs have no cron.
func WithJobTimezone(timezone string) JobOption {
	return func(job *Job) { job.Timezone = timezone }
}

// WithJobRetryPolicy sets the base retry policy for failed runs — the id of a
// RetryPolicy, overridable per environment. Empty (the default) uses the
// built-in "Default" policy, which never retries.
func WithJobRetryPolicy(retryPolicy string) JobOption {
	return func(job *Job) { job.RetryPolicy = retryPolicy }
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

// Environment returns the per-environment override for environment — the
// single place to read or set what this job overrides there (ADR-056).
//
// It returns the JobEnvironment for environment, creating an empty one (and
// inserting it into Environments) on first access, so overrides can be set
// directly on the returned pointer:
//
//	job.Environment("production").Enabled = true
//	job.Environment("production").URL = "https://prod.example.com/warm"
//	job.Environment("production").SetHeader("Authorization", "Bearer prod")
//
// Only the leaves you set are sent on save; everything else inherits the base
// definition (the server resolves base ⊕ overrides when the job fires).
func (job *Job) Environment(environment string) *JobEnvironment {
	if job.Environments == nil {
		job.Environments = map[string]*JobEnvironment{}
	}
	env, ok := job.Environments[environment]
	if !ok {
		env = &JobEnvironment{}
		job.Environments[environment] = env
	}
	return env
}

// Enabled reports the read-only roll-up: true when the job is enabled in at
// least one environment. The jobs API has no top-level enabled field, so the
// wrapper derives it from the Environments map (and it stays correct for
// in-memory edits made before Save). Enable per environment via
// job.Environment(env).Enabled = true.
func (job *Job) Enabled() bool {
	for _, env := range job.Environments {
		if env.Enabled {
			return true
		}
	}
	return false
}

// IsRecurring reports whether this is a recurring (cron-scheduled) job.
func (job *Job) IsRecurring() bool { return job.Kind != nil && *job.Kind == JobKindRecurring }

// IsManual reports whether this is a manual job — no schedule; runs only when
// triggered.
func (job *Job) IsManual() bool { return job.Kind != nil && *job.Kind == JobKindManual }

// IsOneOff reports whether this is a one-off job — a single "now" / datetime
// run.
func (job *Job) IsOneOff() bool { return job.Kind != nil && *job.Kind == JobKindOneOff }

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
// environment (empty covers every environment you can access). input.Triggers
// restricts to runs started by any of those triggers; input.LastRunOnly
// collapses the result to the last completed run per environment. PageSize /
// After drive cursor pagination.
func (job *Job) ListRuns(ctx context.Context, input ListJobRunsInput) ([]*Run, error) {
	if job.client == nil {
		return nil, &Error{Message: "job was constructed without a client; cannot list runs"}
	}
	runsInput := ListRunsInput{
		Job:         job.ID,
		Triggers:    input.Triggers,
		LastRunOnly: input.LastRunOnly,
		PageSize:    input.PageSize,
		After:       input.After,
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
	job.Environments = other.Environments
	job.Kind = other.Kind
	job.Type = other.Type
	job.Schedule = other.Schedule
	job.Timezone = other.Timezone
	job.RetryPolicy = other.RetryPolicy
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
	if input.Kind != nil {
		kind := string(*input.Kind)
		params.FilterKind = &kind
	}
	if input.Scheduled != nil {
		params.FilterScheduled = input.Scheduled
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
// implies it). The job must be enabled in the chosen environment. It is sent in
// the run-now request body; when no environment is resolved the body's
// environment is left unset and the service implies it.
func (j *JobsClient) Run(ctx context.Context, id string, environment string) (*Run, error) {
	env := environment
	if env == "" {
		env = j.environment
	}
	body := genjobs.RunNowRequest{}
	if env != "" {
		body.Environment = &env
	}
	resp, err := j.gen.RunJobNowWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, id, body)
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
// Pass Triggers to restrict to runs with any of those triggers, and
// LastRunOnly to collapse to the last completed run per job-and-environment.
func (r *RunsClient) List(ctx context.Context, input ListRunsInput) ([]*Run, error) {
	params := &genjobs.ListRunsParams{}
	if input.Job != "" {
		job := input.Job
		params.FilterJob = &job
	}
	if env := resolveEnvironmentFilter(input.Environments, r.environment); env != "" {
		params.FilterEnvironment = &env
	}
	if trigger := joinTriggers(input.Triggers); trigger != "" {
		params.FilterTrigger = &trigger
	}
	// last_run_only is sent only when requested; the generated default (false)
	// would otherwise emit last_run_only=false on every call.
	if input.LastRunOnly {
		lastRunOnly := true
		params.LastRunOnly = &lastRunOnly
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
// Retry-policy active-record + management surface
// ---------------------------------------------------------------------------

// RetryPoliciesClient is the client.Jobs().RetryPolicies() surface: manage
// reusable retry policies. A RetryPolicy is an active record — build one with
// New, set fields, and call Save(ctx); then reference it from a job's
// RetryPolicy (see WithJobRetryPolicy and (*Job).SetRetryPolicy). Retry
// policies are account-global — never environment-scoped.
type RetryPoliciesClient struct {
	gen *genjobs.ClientWithResponses
}

// RetryPolicyOption configures an unsaved RetryPolicy returned by
// RetryPoliciesClient.New.
type RetryPolicyOption func(*RetryPolicy)

// WithRetryPolicyMaxDelaySeconds sets the ceiling on the wait between retries,
// for exponential backoff only. Unset (the default) leaves it uncapped; omit it
// for fixed backoff.
func WithRetryPolicyMaxDelaySeconds(maxDelaySeconds int) RetryPolicyOption {
	return func(p *RetryPolicy) { p.MaxDelaySeconds = &maxDelaySeconds }
}

// WithRetryPolicyRetryOnTimeout sets whether to retry a run that failed because
// the request did not complete within the job's timeout. Unset (the default)
// does not retry timeouts.
func WithRetryPolicyRetryOnTimeout(retryOnTimeout bool) RetryPolicyOption {
	return func(p *RetryPolicy) { p.RetryOnTimeout = retryOnTimeout }
}

// WithRetryPolicyRetryOnConnectionError sets whether to retry a run that failed
// because the destination could not be reached (DNS, connection refused, TLS,
// or transport error). Unset (the default) does not retry connection errors.
func WithRetryPolicyRetryOnConnectionError(retryOnConnectionError bool) RetryPolicyOption {
	return func(p *RetryPolicy) { p.RetryOnConnectionError = retryOnConnectionError }
}

// WithRetryPolicyRetryStatuses sets the allowlist of response status patterns to
// retry when a run fails because the response did not match the job's success
// status. Each element is an exact 3-digit HTTP code (e.g. "429") or a status
// class ("1xx"–"5xx"). Unset (the default) matches no status.
func WithRetryPolicyRetryStatuses(retryStatuses []string) RetryPolicyOption {
	return func(p *RetryPolicy) { p.RetryStatuses = retryStatuses }
}

// WithRetryPolicyRetryStatusesExcept sets patterns subtracted from the
// RetryStatuses allowlist, using the same exact-code or class syntax; except
// wins on overlap. Unset (the default) subtracts nothing.
func WithRetryPolicyRetryStatusesExcept(retryStatusesExcept []string) RetryPolicyOption {
	return func(p *RetryPolicy) { p.RetryStatusesExcept = retryStatusesExcept }
}

// New returns an unsaved RetryPolicy bound to this client. Call
// (*RetryPolicy).Save(ctx) to create it.
//
// id is the caller-supplied unique identifier for the policy — unique within
// the account and immutable; the service returns 409 if another live policy
// already uses this id. name is the human-readable name. maxRetries is how many
// times a failed run is retried after the initial attempt (3 means up to 4
// attempts total; 0 disables retries; maximum 10). backoff is how the wait
// between retries grows (see Backoff). delaySeconds is the wait before a retry —
// the constant wait for fixed backoff, or the base that doubles each retry for
// exponential. The optional max delay (exponential only) and the failures to
// retry are set via WithRetryPolicyMaxDelaySeconds,
// WithRetryPolicyRetryOnTimeout, WithRetryPolicyRetryOnConnectionError,
// WithRetryPolicyRetryStatuses, and WithRetryPolicyRetryStatusesExcept.
func (c *RetryPoliciesClient) New(
	id string,
	name string,
	maxRetries int,
	backoff Backoff,
	delaySeconds int,
	opts ...RetryPolicyOption,
) *RetryPolicy {
	policy := &RetryPolicy{
		ID:           id,
		Name:         name,
		MaxRetries:   maxRetries,
		Backoff:      backoff,
		DelaySeconds: delaySeconds,
		client:       c,
	}
	for _, opt := range opts {
		opt(policy)
	}
	return policy
}

// List returns the retry policies for the authenticated account. Offset
// pagination via PageNumber / PageSize; Name filters on a case-insensitive
// substring of the policy name.
func (c *RetryPoliciesClient) List(ctx context.Context, input ListRetryPoliciesInput) ([]*RetryPolicy, error) {
	params := &genjobs.ListRetryPoliciesParams{}
	if input.Name != nil {
		params.FilterName = input.Name
	}
	if input.PageNumber > 0 {
		params.PageNumber = &input.PageNumber
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	resp, err := c.gen.ListRetryPoliciesWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("jobs RetryPolicies.List: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	body := resp.ApplicationvndApiJSON200
	policies := make([]*RetryPolicy, 0, len(body.Data))
	for _, r := range body.Data {
		policies = append(policies, retryPolicyFromResource(r, c))
	}
	return policies, nil
}

// Get returns one retry policy by id; the returned instance is bound to this
// client so policy.Save(ctx) and policy.Delete(ctx) work.
func (c *RetryPoliciesClient) Get(ctx context.Context, id string) (*RetryPolicy, error) {
	resp, err := c.gen.GetRetryPolicyWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("jobs RetryPolicies.Get: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	return retryPolicyFromResource(resp.ApplicationvndApiJSON200.Data, c), nil
}

// Delete removes a retry policy by id.
func (c *RetryPoliciesClient) Delete(ctx context.Context, id string) error {
	resp, err := c.gen.DeleteRetryPolicyWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("jobs RetryPolicies.Delete: %w", err)
	}
	if resp.StatusCode() != 204 {
		return checkStatus(resp.StatusCode(), resp.Body)
	}
	return nil
}

// Save creates this retry policy on the server, or full-replaces it if it
// already exists. Upsert behavior keyed on CreatedAt: nil → create (POST), set
// → full-replace update (PUT). After the call, every field is refreshed from
// the server response (including CreatedAt, UpdatedAt, Version).
func (policy *RetryPolicy) Save(ctx context.Context) error {
	if policy.client == nil {
		return &Error{Message: "retry policy was constructed without a client; cannot save"}
	}
	if policy.CreatedAt == nil {
		updated, err := policy.client.create(ctx, policy)
		if err != nil {
			return err
		}
		policy.apply(updated)
		return nil
	}
	updated, err := policy.client.update(ctx, policy)
	if err != nil {
		return err
	}
	policy.apply(updated)
	return nil
}

// Delete removes this retry policy on the server.
func (policy *RetryPolicy) Delete(ctx context.Context) error {
	if policy.client == nil || policy.ID == "" {
		return &Error{Message: "retry policy was constructed without a client or id; cannot delete"}
	}
	return policy.client.Delete(ctx, policy.ID)
}

func (policy *RetryPolicy) apply(other *RetryPolicy) {
	policy.ID = other.ID
	policy.Name = other.Name
	policy.MaxRetries = other.MaxRetries
	policy.Backoff = other.Backoff
	policy.DelaySeconds = other.DelaySeconds
	policy.MaxDelaySeconds = other.MaxDelaySeconds
	policy.RetryOnTimeout = other.RetryOnTimeout
	policy.RetryOnConnectionError = other.RetryOnConnectionError
	policy.RetryStatuses = other.RetryStatuses
	policy.RetryStatusesExcept = other.RetryStatusesExcept
	policy.CreatedAt = other.CreatedAt
	policy.UpdatedAt = other.UpdatedAt
	policy.DeletedAt = other.DeletedAt
	policy.Version = other.Version
}

// create posts a new retry policy and returns the server-authoritative
// response. Called by (*RetryPolicy).Save on unsaved instances.
func (c *RetryPoliciesClient) create(ctx context.Context, policy *RetryPolicy) (*RetryPolicy, error) {
	body := genjobs.CreateRetryPolicyApplicationVndAPIPlusJSONRequestBody{
		Data: retryPolicyCreateResourceFromPolicy(policy),
	}
	resp, err := c.gen.CreateRetryPolicyWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("jobs RetryPolicies.Create: %w", err)
	}
	if resp.StatusCode() != 201 {
		if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("jobs RetryPolicies.Create: unexpected status %d", resp.StatusCode())
	}
	if resp.ApplicationvndApiJSON201 == nil {
		return nil, fmt.Errorf("jobs RetryPolicies.Create: empty 201 body")
	}
	return retryPolicyFromResource(resp.ApplicationvndApiJSON201.Data, c), nil
}

// update PUTs a full-replace and returns the server-authoritative response.
// Called by (*RetryPolicy).Save on saved instances.
func (c *RetryPoliciesClient) update(ctx context.Context, policy *RetryPolicy) (*RetryPolicy, error) {
	body := genjobs.UpdateRetryPolicyApplicationVndAPIPlusJSONRequestBody{
		Data: retryPolicyResourceFromPolicy(policy),
	}
	resp, err := c.gen.UpdateRetryPolicyWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, policy.ID, body)
	if err != nil {
		return nil, fmt.Errorf("jobs RetryPolicies.Update: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	return retryPolicyFromResource(resp.ApplicationvndApiJSON200.Data, c), nil
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
	// The environment travels entirely in the body now: a one-off job's birth
	// environment is seeded into the environments map by Schedule (recurring and
	// manual jobs get their environments from the map directly). There is no
	// X-Smplkit-Environment header.
	resp, err := j.gen.CreateJobWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, body)
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
	// Update carries no environment header: a job's environments travel entirely
	// in the body's environments map.
	resp, err := j.gen.UpdateJobWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, job.ID, body)
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
		Configuration: httpConfigToWire(job.Configuration),
	}
	// Schedule is the field that determines the job's kind: a non-empty value
	// is a cron expression (recurring) or a datetime / "now" (one-off); an empty
	// Schedule is omitted, creating a manual job with no schedule.
	if job.Schedule != "" {
		schedule := job.Schedule
		attrs.Schedule = &schedule
	}
	// Timezone is the IANA zone the cron is evaluated in (recurring jobs only);
	// an empty Timezone is omitted, leaving the server default of UTC.
	if job.Timezone != "" {
		timezone := job.Timezone
		attrs.Timezone = &timezone
	}
	// RetryPolicy is the policy id; an empty RetryPolicy is omitted, leaving the
	// server default (the built-in "Default" policy, which never retries).
	if job.RetryPolicy != "" {
		retryPolicy := job.RetryPolicy
		attrs.RetryPolicy = &retryPolicy
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
// the flat, sparse leaf-path overlay the jobs API expects (ADR-056): each
// environment emits "enabled" plus only the leaves it overrides, with every
// header addressed individually as headers.<name>. The read-only NextRunAt is
// never sent.
func jobEnvironmentsToWire(envs map[string]*JobEnvironment) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{}, len(envs))
	for key, env := range envs {
		overlay := map[string]interface{}{"enabled": env.Enabled}
		if env.Schedule != "" {
			overlay["schedule"] = env.Schedule
		}
		if env.Timezone != "" {
			overlay["timezone"] = env.Timezone
		}
		if env.RetryPolicy != "" {
			overlay["retry_policy"] = env.RetryPolicy
		}
		if env.URL != "" {
			overlay["url"] = env.URL
		}
		if env.Method != "" {
			overlay["method"] = string(env.Method)
		}
		if env.Timeout != 0 {
			overlay["timeout"] = env.Timeout
		}
		if env.Body != nil {
			overlay["body"] = *env.Body
		}
		if env.SuccessStatus != "" {
			overlay["success_status"] = env.SuccessStatus
		}
		if env.TlsVerify != nil {
			overlay["tls_verify"] = *env.TlsVerify
		}
		if env.CaCert != nil {
			overlay["ca_cert"] = *env.CaCert
		}
		for name, value := range env.Headers {
			overlay["headers."+name] = value
		}
		out[key] = overlay
	}
	return out
}

// jobEnvironmentsFromWire parses the flat leaf-path overlay the jobs API
// returns (ADR-056) back into the wrapper shape. Header leaves arrive as
// headers.<name> (split on the first dot, so a dotted header name is
// preserved); next_run_at is surfaced as the read-only NextRunAt rather than an
// override leaf; unknown leaves are ignored for forward compatibility. Reads
// are pure override — an unset leaf stays the wrapper's zero value, never the
// base.
func jobEnvironmentsFromWire(envs map[string]map[string]interface{}) map[string]*JobEnvironment {
	out := make(map[string]*JobEnvironment, len(envs))
	for key, raw := range envs {
		env := &JobEnvironment{
			Enabled:       overlayBool(raw, "enabled"),
			Schedule:      overlayString(raw, "schedule"),
			Timezone:      overlayString(raw, "timezone"),
			RetryPolicy:   overlayString(raw, "retry_policy"),
			URL:           overlayString(raw, "url"),
			Method:        JobHttpMethod(overlayString(raw, "method")),
			Timeout:       overlayInt(raw, "timeout"),
			Body:          overlayStringPtr(raw, "body"),
			SuccessStatus: overlayString(raw, "success_status"),
			TlsVerify:     overlayBoolPtr(raw, "tls_verify"),
			CaCert:        overlayStringPtr(raw, "ca_cert"),
			Headers:       overlayHeaders(raw),
		}
		if next, ok := raw["next_run_at"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, next); err == nil {
				env.NextRunAt = &parsed
			}
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
		headers := make(map[string]string, len(h.Headers))
		for name, value := range h.Headers {
			headers[name] = value
		}
		out.Headers = &headers
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
		out.Headers = make(map[string]string, len(*h.Headers))
		for name, value := range *h.Headers {
			out.Headers[name] = value
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
		Configuration: httpConfigFromWire(a.Configuration),
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
		DeletedAt:     a.DeletedAt,
		Version:       a.Version,
		Type:          "http",
		client:        client,
	}
	// Schedule is optional on the wire: a manual job carries none, leaving the
	// wrapper Schedule empty.
	if a.Schedule != nil {
		out.Schedule = *a.Schedule
	}
	if a.Timezone != nil {
		out.Timezone = *a.Timezone
	}
	if a.RetryPolicy != nil {
		out.RetryPolicy = *a.RetryPolicy
	}
	if a.Environments != nil {
		out.Environments = jobEnvironmentsFromWire(*a.Environments)
	}
	if a.Kind != nil {
		kind := JobKind(*a.Kind)
		out.Kind = &kind
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
	// Retry is present on the wire only for RETRY runs; surface it as the
	// read-only RunRetry chain position, leaving non-RETRY runs with a nil Retry.
	if a.Retry != nil {
		out.Retry = &RunRetry{Of: a.Retry.Of.String(), Attempt: a.Retry.Attempt}
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

// joinTriggers comma-joins run triggers for the filter[trigger] query param
// (any-of). Empty when no triggers are given, so the param stays off the wire.
func joinTriggers(triggers []RunTrigger) string {
	if len(triggers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(triggers))
	for _, t := range triggers {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ",")
}

// retryStatusesToWire copies a wrapper status-pattern slice for the generated
// model. The lists are always emitted (as empty arrays when empty), mirroring
// the server's allowlist/subtraction shape.
func retryStatusesToWire(s []string) *[]string {
	out := make([]string, 0, len(s))
	out = append(out, s...)
	return &out
}

// retryStatusesFromWire copies a generated status-pattern slice into the wrapper
// shape. A nil or absent list yields an empty slice (matches nothing).
func retryStatusesFromWire(s *[]string) []string {
	if s == nil {
		return []string{}
	}
	return append([]string(nil), *s...)
}

// retryPolicyAttributes builds the shared retry-policy attribute payload sent on
// create and update. max_delay_seconds is emitted only when set (exponential
// backoff only); the four retry-on fields are always present.
func retryPolicyAttributes(policy *RetryPolicy) genjobs.RetryPolicy {
	retryOnTimeout := policy.RetryOnTimeout
	retryOnConnectionError := policy.RetryOnConnectionError
	attrs := genjobs.RetryPolicy{
		Name:                   policy.Name,
		MaxRetries:             policy.MaxRetries,
		Backoff:                genjobs.RetryPolicyBackoff(policy.Backoff),
		DelaySeconds:           policy.DelaySeconds,
		RetryOnTimeout:         &retryOnTimeout,
		RetryOnConnectionError: &retryOnConnectionError,
		RetryStatuses:          retryStatusesToWire(policy.RetryStatuses),
		RetryStatusesExcept:    retryStatusesToWire(policy.RetryStatusesExcept),
	}
	if policy.MaxDelaySeconds != nil {
		v := *policy.MaxDelaySeconds
		attrs.MaxDelaySeconds = &v
	}
	return attrs
}

func retryPolicyCreateResourceFromPolicy(policy *RetryPolicy) genjobs.RetryPolicyCreateResource {
	rt := genjobs.RetryPolicyCreateResourceTypeRetryPolicy
	return genjobs.RetryPolicyCreateResource{
		Id:         policy.ID,
		Type:       &rt,
		Attributes: retryPolicyAttributes(policy),
	}
}

func retryPolicyResourceFromPolicy(policy *RetryPolicy) genjobs.RetryPolicyResource {
	rt := "retry_policy"
	var idPtr *string
	if policy.ID != "" {
		id := policy.ID
		idPtr = &id
	}
	return genjobs.RetryPolicyResource{
		Id:         idPtr,
		Type:       &rt,
		Attributes: retryPolicyAttributes(policy),
	}
}

func retryPolicyFromResource(r genjobs.RetryPolicyResource, client *RetryPoliciesClient) *RetryPolicy {
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	a := r.Attributes
	retryOnTimeout := false
	if a.RetryOnTimeout != nil {
		retryOnTimeout = *a.RetryOnTimeout
	}
	retryOnConnectionError := false
	if a.RetryOnConnectionError != nil {
		retryOnConnectionError = *a.RetryOnConnectionError
	}
	return &RetryPolicy{
		ID:                     id,
		Name:                   a.Name,
		MaxRetries:             a.MaxRetries,
		Backoff:                Backoff(a.Backoff),
		DelaySeconds:           a.DelaySeconds,
		MaxDelaySeconds:        a.MaxDelaySeconds,
		RetryOnTimeout:         retryOnTimeout,
		RetryOnConnectionError: retryOnConnectionError,
		RetryStatuses:          retryStatusesFromWire(a.RetryStatuses),
		RetryStatusesExcept:    retryStatusesFromWire(a.RetryStatusesExcept),
		CreatedAt:              a.CreatedAt,
		UpdatedAt:              a.UpdatedAt,
		DeletedAt:              a.DeletedAt,
		Version:                a.Version,
		client:                 client,
	}
}
