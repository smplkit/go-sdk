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
	// defaults the one-off birth env on create, the run-now env header, and
	// the filter[environment] scope on Runs().List.
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
// Enablement is per environment: pass WithJobEnvironments (or call SetEnabled
// after construction) to choose where the job is scheduled. The remaining
// fields — description, base timezone (WithJobTimezone), base retry policy
// (WithJobRetryPolicy), and concurrency policy — are set via the WithJob*
// options.
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
// Enablement is per environment: pass WithJobEnvironments (or call SetEnabled
// after construction) to choose where the job is triggerable. The remaining
// fields — description, retry policy (WithJobRetryPolicy), and concurrency
// policy — are set via the WithJob* options. A manual job has no cron, so
// WithJobTimezone does not apply.
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
	return j.newJob(id, name, schedule.Format(time.RFC3339), configuration, opts...)
}

// JobOption configures an unsaved Job returned by JobsClient.NewRecurringJob,
// NewManualJob, or Schedule.
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
// Defaults to the client's configured environment. Ignored for recurring and
// manual jobs, whose environments come from WithJobEnvironments / SetEnabled.
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

// IsRecurring reports whether this is a recurring (cron-scheduled) job.
func (job *Job) IsRecurring() bool { return job.Kind != nil && *job.Kind == JobKindRecurring }

// IsManual reports whether this is a manual job — no schedule; runs only when
// triggered.
func (job *Job) IsManual() bool { return job.Kind != nil && *job.Kind == JobKindManual }

// IsOneOff reports whether this is a one-off job — a single "now" / datetime
// run.
func (job *Job) IsOneOff() bool { return job.Kind != nil && *job.Kind == JobKindOneOff }

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
// Called with an empty environment (or none), it sets the base Schedule: the
// cadence every environment inherits unless it overrides it. Called with an
// environment, it sets that environment's per-environment cron override on
// Environments, creating the override entry if it doesn't exist yet (preserving
// any already-set Enabled / Timezone / Configuration on it). A per-environment
// schedule varies the cadence for just that environment (recurring jobs only);
// clear it (set it back to the base cadence) to fall back to the base schedule.
//
// Because the timezone is an integral part of a cron cadence, a timezone may be
// supplied alongside the schedule; when non-empty it sets the same scope's
// timezone too (equivalent to a follow-up SetTimezone). Pass "" to leave the
// timezone untouched. For a timezone-only change, use SetTimezone.
//
// At most one environment may be named; extra arguments are ignored. Call Save
// to persist.
func (job *Job) SetSchedule(schedule string, timezone string, environment ...string) {
	env := ""
	if len(environment) > 0 {
		env = environment[0]
	}
	if env == "" {
		job.Schedule = schedule
	} else {
		override := job.environmentOverride(env)
		override.Schedule = schedule
		job.Environments[env] = *override
	}
	if timezone != "" {
		job.SetTimezone(timezone, env)
	}
}

// SetTimezone sets the IANA timezone the cron schedule is evaluated in — base
// or per-environment.
//
// Called with no environment (or an empty environment), it sets the base
// Timezone every environment inherits unless it overrides it. Called with an
// environment, it sets that environment's per-environment timezone override on
// Environments, creating the override entry if it doesn't exist yet (preserving
// any already-set Enabled / Schedule / Configuration on it). A timezone is only
// valid on a recurring (cron) job; an empty value means UTC (base) or "inherit
// the base" (per-environment). At most one environment may be named; extra
// arguments are ignored. Call Save to persist.
func (job *Job) SetTimezone(timezone string, environment ...string) {
	env := ""
	if len(environment) > 0 {
		env = environment[0]
	}
	if env == "" {
		job.Timezone = timezone
		return
	}
	override := job.environmentOverride(env)
	override.Timezone = timezone
	job.Environments[env] = *override
}

// SetRetryPolicy sets the retry policy for failed runs — base or
// per-environment.
//
// Called with an empty environment (or none), it sets the job's base
// RetryPolicy, the policy every environment inherits unless it overrides it.
// Called with an environment, it sets a per-environment override for just that
// environment, creating the override entry if it doesn't exist yet (preserving
// any already-set Enabled / Schedule / Timezone / Configuration on it).
//
// policyOrID accepts either a *RetryPolicy (or RetryPolicy) instance — its ID
// is used — or a policy id string; pass "Default" for the built-in never-retry
// policy. Any other type is ignored. At most one environment may be named;
// extra arguments are ignored. Call Save to persist.
func (job *Job) SetRetryPolicy(policyOrID any, environment ...string) {
	var policyID string
	switch p := policyOrID.(type) {
	case *RetryPolicy:
		policyID = p.ID
	case RetryPolicy:
		policyID = p.ID
	case string:
		policyID = p
	default:
		return
	}
	env := ""
	if len(environment) > 0 {
		env = environment[0]
	}
	if env == "" {
		job.RetryPolicy = policyID
		return
	}
	override := job.environmentOverride(env)
	override.RetryPolicy = policyID
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
	job.Enabled = other.Enabled
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
// implies it). The job must be enabled in the chosen environment. It is sent as
// the X-Smplkit-Environment header.
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

// WithRetryPolicyRetryOn sets which failures to retry (see RetryOn). Unset (the
// default) retries nothing.
func WithRetryPolicyRetryOn(retryOn RetryOn) RetryPolicyOption {
	return func(p *RetryPolicy) { p.RetryOn = retryOn }
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
// retry are set via WithRetryPolicyMaxDelaySeconds / WithRetryPolicyRetryOn.
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
	policy.RetryOn = other.RetryOn
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
	// A one-off job is born in the environment named here (recurring and manual
	// jobs ignore it server-side; their environments come from the map).
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
	// for recurring and manual jobs, whose environments come from the map).
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
		if env.Timezone != "" {
			timezone := env.Timezone
			ge.Timezone = &timezone
		}
		if env.RetryPolicy != "" {
			retryPolicy := env.RetryPolicy
			ge.RetryPolicy = &retryPolicy
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
		if ge.Timezone != nil {
			env.Timezone = *ge.Timezone
		}
		if ge.RetryPolicy != nil {
			env.RetryPolicy = *ge.RetryPolicy
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
	// The base `enabled` is a read-only roll-up: enablement now lives strictly
	// per-environment on the wire, so derive it locally as "enabled in at least
	// one environment" rather than reading a top-level field.
	out.Enabled = anyEnvironmentEnabled(out.Environments)
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

// retryOnToWire converts the wrapper RetryOn to the generated model. Both lists
// are always emitted (as empty arrays when empty), mirroring the server's
// {statuses, reasons} shape; reasons serialize as their string values.
func retryOnToWire(r RetryOn) genjobs.RetryOn {
	statuses := make([]int, 0, len(r.Statuses))
	statuses = append(statuses, r.Statuses...)
	reasons := make([]genjobs.RetryOnReasons, 0, len(r.Reasons))
	for _, reason := range r.Reasons {
		reasons = append(reasons, genjobs.RetryOnReasons(reason))
	}
	return genjobs.RetryOn{Statuses: &statuses, Reasons: &reasons}
}

// retryOnFromWire converts the generated RetryOn back into the wrapper shape. A
// nil or absent retry_on yields the zero RetryOn (retries nothing).
func retryOnFromWire(r *genjobs.RetryOn) RetryOn {
	out := RetryOn{}
	if r == nil {
		return out
	}
	if r.Statuses != nil {
		out.Statuses = append([]int(nil), *r.Statuses...)
	}
	if r.Reasons != nil {
		out.Reasons = make([]RetryReason, 0, len(*r.Reasons))
		for _, reason := range *r.Reasons {
			out.Reasons = append(out.Reasons, RetryReason(reason))
		}
	}
	return out
}

// retryPolicyAttributes builds the shared retry-policy attribute payload sent on
// create and update. max_delay_seconds is emitted only when set (exponential
// backoff only); retry_on is always present.
func retryPolicyAttributes(policy *RetryPolicy) genjobs.RetryPolicy {
	retryOn := retryOnToWire(policy.RetryOn)
	attrs := genjobs.RetryPolicy{
		Name:         policy.Name,
		MaxRetries:   policy.MaxRetries,
		Backoff:      genjobs.RetryPolicyBackoff(policy.Backoff),
		DelaySeconds: policy.DelaySeconds,
		RetryOn:      &retryOn,
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
	return &RetryPolicy{
		ID:              id,
		Name:            a.Name,
		MaxRetries:      a.MaxRetries,
		Backoff:         Backoff(a.Backoff),
		DelaySeconds:    a.DelaySeconds,
		MaxDelaySeconds: a.MaxDelaySeconds,
		RetryOn:         retryOnFromWire(a.RetryOn),
		CreatedAt:       a.CreatedAt,
		UpdatedAt:       a.UpdatedAt,
		DeletedAt:       a.DeletedAt,
		Version:         a.Version,
		client:          client,
	}
}
