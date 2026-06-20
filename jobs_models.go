package smplkit

import (
	"time"
)

// JobHttpMethod is the HTTP verb a job uses when it fires. The jobs
// service rejects any value outside the constants below with a 400.
type JobHttpMethod string

// Supported JobHttpMethod values, alphabetical.
const (
	JobHttpMethodDelete = JobHttpMethod("DELETE")
	JobHttpMethodGet    = JobHttpMethod("GET")
	JobHttpMethodPatch  = JobHttpMethod("PATCH")
	JobHttpMethodPost   = JobHttpMethod("POST")
	JobHttpMethodPut    = JobHttpMethod("PUT")
)

// JobKind is how a job runs, derived from its schedule (read-only).
//
//   - JobKindManual: No schedule — never auto-fires; runs only when triggered.
//   - JobKindOneOff: A "now" or datetime schedule — runs a single time, then is
//     spent.
//   - JobKindRecurring: A cron schedule — fires on a repeating cadence.
type JobKind string

// JobKind values.
const (
	JobKindManual    = JobKind("manual")
	JobKindOneOff    = JobKind("one_off")
	JobKindRecurring = JobKind("recurring")
)

// RunTrigger is what started a run (read-only).
//
//   - RunTriggerManual: A Run / Trigger call started it on demand.
//   - RunTriggerRerun: It repeats an earlier run.
//   - RunTriggerSchedule: The job's schedule fired.
type RunTrigger string

// RunTrigger values.
const (
	RunTriggerManual   = RunTrigger("MANUAL")
	RunTriggerRerun    = RunTrigger("RERUN")
	RunTriggerSchedule = RunTrigger("SCHEDULE")
)

// HttpConfig is the HTTP request a job performs when it fires (the job's
// "configuration"). It mirrors the shared HttpConfiguration used by audit
// forwarders but adds the two fields a scheduled job needs: a request Body
// and a per-run Timeout.
type HttpConfig struct {
	// Method is the HTTP verb used when the job fires. Defaults to
	// JobHttpMethodPost when zero-valued.
	Method JobHttpMethod
	// URL is the absolute http:// or https:// destination the job calls.
	URL string
	// Headers are attached to every run's request. Values carry
	// credentials; supply them plaintext on writes. Reads return them
	// plaintext too, so a Get → mutate → Save round-trip preserves them.
	Headers []HttpHeader
	// Body is the request body sent on each run. Nil sends an empty body
	// (suitable for a connectivity ping). Sent verbatim — pair with a
	// matching Content-Type header.
	Body *string
	// SuccessStatus is the response status that counts as success — an
	// exact code ("200", "204") or a class ("2xx", "5xx"). Defaults to
	// "2xx" server-side when zero-valued.
	SuccessStatus string
	// Timeout is the per-run timeout in seconds. A run that does not
	// complete within this many seconds fails with reason TIMEOUT.
	// Defaults to 30 server-side when zero-valued.
	Timeout int
	// TlsVerify controls whether the destination's TLS certificate chain
	// is verified. Pointer-valued so the zero-value HttpConfig is
	// unambiguous: nil means "leave at the server default of true", &false
	// means "skip verification", &true means "verify explicitly".
	TlsVerify *bool
	// CaCert is an optional PEM-encoded certificate (or bundle) trusted in
	// addition to the system CA store. Ignored when TlsVerify points to
	// false. Nil means "use system CAs only".
	CaCert *string
}

// JobEnvironment is a per-environment override for a job's enablement,
// schedule, and configuration.
//
// A job runs in a given environment only when that environment has an entry in
// Job.Environments with Enabled=true (scheduled there for a recurring job,
// triggerable there for a manual one); an environment with no entry (or
// Enabled=false) is disabled there.
type JobEnvironment struct {
	// Enabled controls whether the job is enabled in this environment.
	// Defaults to false.
	Enabled bool
	// Schedule is an optional per-environment cron override that varies the
	// cadence for just this environment (recurring jobs only). Empty inherits
	// the job's base Schedule. When set, it must be a 5-field cron expression
	// evaluated in UTC; it cannot appear on a manual or one-off job, and cannot
	// change a job's kind. Settable; sent on writes only when non-empty.
	Schedule string
	// Timezone is an optional per-environment IANA timezone override for
	// evaluating this environment's cron Schedule (recurring jobs only). Empty
	// inherits the base Timezone, else UTC. When set, it must be a valid IANA
	// zone key (e.g. "America/New_York"); it may be set on an environment that
	// inherits the base schedule (it need not also override Schedule). Settable;
	// sent on writes only when non-empty.
	Timezone string
	// Configuration is an optional per-environment request configuration that
	// fully replaces the job's base Configuration for this environment. Nil
	// (the default) inherits the base configuration. As with the base
	// configuration, header values are plaintext on both writes and reads, so
	// a Get → mutate → Save round-trip preserves them.
	Configuration *HttpConfig
	// NextRunAt is the read-only next scheduled fire time in this environment.
	// Nil when the environment is not enabled, or once a one-off run has fired.
	// Populated on reads; never sent on writes.
	NextRunAt *time.Time
}

// Job is a unit of work: an HTTP request, run on a schedule or triggered on
// demand.
//
// Active-record style: mutate fields directly and call Save(ctx) to
// persist, or Delete(ctx) to remove. The Job's id is caller-supplied,
// unique within the account, and immutable. A job is enabled per
// environment via Environments. A job's Kind follows from its Schedule: a
// recurring (cron) job may be enabled in several environments at once; a
// manual job (no schedule) runs only when triggered; a one-off ("now" /
// datetime) job is born in a single environment and is then spent.
type Job struct {
	// ID is the caller-supplied unique identifier for the job. Required
	// at create time (the jobs service does not auto-generate it) and
	// immutable thereafter.
	ID string
	// Name is the human-readable name for the job.
	Name string
	// Description is an optional free-text description.
	Description *string
	// Enabled reports whether the job is enabled in at least one environment.
	// Read-only roll-up derived from Environments[*].Enabled (true iff any
	// environment is enabled); the wrapper computes it locally and never sends
	// it. Set enablement per environment via Environments / SetEnabled.
	Enabled bool
	// Environments holds per-environment overrides keyed by environment key
	// (e.g. "production", "development"). A job fires in an environment only
	// when Environments[env].Enabled is true. Each entry may carry an optional
	// HttpConfig override; leave it nil to inherit the base Configuration. For
	// a recurring job, supply this map to choose where it runs; a one-off job
	// records the single environment it was created in. Every referenced
	// environment must exist for the account.
	Environments map[string]JobEnvironment
	// Kind is how the job runs, derived server-side from Schedule (read-only):
	// JobKindRecurring (cron), JobKindManual (no schedule), or JobKindOneOff
	// ("now" / datetime). Nil on an unsaved Job; use IsRecurring / IsManual /
	// IsOneOff.
	Kind *JobKind
	// Type is the job type. Only "http" is supported today.
	Type string
	// Schedule is the base schedule every environment inherits unless it
	// overrides it, and the field that determines the job's Kind: empty for a
	// manual job (no schedule), a 5-field cron expression evaluated in UTC for
	// a recurring job, or an ISO-8601 datetime / "now" for a one-off job.
	Schedule string
	// Timezone is the base IANA timezone the cron Schedule is evaluated in
	// (e.g. "America/New_York"); empty means UTC. The base every environment
	// inherits unless it sets its own Timezone. The cron fires on this zone's
	// wall clock (DST-aware) while NextRunAt is still reported as a UTC instant.
	// Only valid on a recurring (cron) job — empty for a manual or one-off job.
	// Settable; sent on writes only when non-empty.
	Timezone string
	// Configuration is the HTTP request the job performs when it fires.
	Configuration HttpConfig
	// ConcurrencyPolicy is how overlapping runs are handled. "ALLOW" (the
	// default and only value today) permits a new run to start while a
	// previous one is still in flight.
	ConcurrencyPolicy string
	// CreatedAt is when the job was created. Nil for an unsaved Job.
	CreatedAt *time.Time
	// UpdatedAt is when the job was last modified.
	UpdatedAt *time.Time
	// DeletedAt is when the job was deleted. Nil for live jobs.
	DeletedAt *time.Time
	// Version is a monotonic counter incremented on every update,
	// starting at 1.
	Version *int

	// birthEnvironment is creation-time only: the environment a one-off job is
	// born in, sent as the X-Smplkit-Environment header by JobsClient.create.
	// Ignored for recurring and manual jobs, whose environments come from
	// Environments.
	birthEnvironment string

	client *JobsClient
}

// Run is one occurrence of a job executing.
//
// Read-only apart from the Rerun / Cancel actions: a run is created and
// driven by the jobs service, not by clients. A Run returned by the SDK is
// bound to its runs client so run.Rerun(ctx) and run.Cancel(ctx) work.
type Run struct {
	// ID is the server-assigned UUID for this run.
	ID string
	// Job is the id of the job this run belongs to.
	Job string
	// JobVersion is the job's version at the time the run executed.
	JobVersion *int
	// Environment is the environment this run executed in. A scheduled run
	// inherits the firing job-environment; a manual run is created in the
	// environment named by the X-Smplkit-Environment header; a rerun copies
	// its source run's environment.
	Environment string
	// Trigger is why the run exists: "SCHEDULE", "MANUAL" (Run now), or
	// "RERUN". Raw trigger string; compare against the RunTrigger constants.
	Trigger string
	// RerunOf is the source run's id; set only when Trigger is "RERUN".
	RerunOf *string
	// ScheduledFor is the intended fire time for a scheduled run; nil for
	// manual / rerun runs.
	ScheduledFor *time.Time
	// Status is the lifecycle state of the run (e.g. "PENDING",
	// "SUCCEEDED", "FAILED", "CANCELED").
	Status string
	// StartedAt is when execution started.
	StartedAt *time.Time
	// FinishedAt is when execution finished.
	FinishedAt *time.Time
	// PendingDurationMs is the milliseconds the run waited as PENDING
	// before starting.
	PendingDurationMs *int
	// RunDurationMs is the milliseconds the run spent executing.
	RunDurationMs *int
	// TotalDurationMs is the milliseconds from enqueue to finish.
	TotalDurationMs *int
	// FailureReason is why a FAILED run failed; nil otherwise.
	FailureReason *string
	// Error is free-text failure detail, if any.
	Error *string
	// Request is a snapshot of the request that was sent (header values
	// redacted). Forensics only; nil when not available.
	Request map[string]interface{}
	// Result is the outcome of the call (e.g. status, headers, body).
	// Nil when not available.
	Result map[string]interface{}
	// CreatedAt is when the run was enqueued (became PENDING).
	CreatedAt *time.Time

	// runs is the backref to the runs client, so a returned Run can Rerun /
	// Cancel itself. Nil on a Run constructed without a client.
	runs *RunsClient
}

// Usage is the current-period usage against the account's plan
// allotments (read-only).
type Usage struct {
	// Period is the usage period this report covers, as "YYYY-MM" (UTC).
	Period string
	// RunsUsed is the runs metered so far this period.
	RunsUsed int
	// RunsIncluded is the runs included in the plan this period (-1 means
	// unlimited).
	RunsIncluded int
	// ActiveJobs is the number of permanent jobs (recurring and manual)
	// counted against the plan's job limit.
	ActiveJobs int
	// ActiveJobsLimit is the maximum permanent jobs the plan allows (-1
	// means unlimited).
	ActiveJobsLimit int
}

// ListJobsInput passes filters and pagination to JobsClient.List.
type ListJobsInput struct {
	// Kind filters to jobs of this JobKind. Nil lists recurring and manual
	// jobs; one-off jobs are omitted unless you pass JobKindOneOff.
	Kind *JobKind
	// Scheduled filters to jobs that have an upcoming fire in some environment
	// (true) or none (false) — the feed for an upcoming-runs view, which
	// includes one-offs. Nil does not filter on scheduling.
	Scheduled *bool
	// Name filters to jobs whose name contains this text (case-insensitive).
	// Nil returns all.
	Name *string
	// PageNumber is the 1-based page index. Zero defers to the server
	// default (page 1).
	PageNumber int
	// PageSize is items per page. Zero defers to the server default.
	PageSize int
}

// ListRunsInput passes filters and cursor pagination to RunsClient.List.
type ListRunsInput struct {
	// Job scopes the listing to a single job's run history. Empty returns
	// runs across all jobs.
	Job string
	// Environments restricts the listing to runs stamped with any of these
	// environment keys, sent as a single comma-separated filter[environment].
	// Empty falls back to the client's configured environment (if any),
	// otherwise covers every environment you can access.
	Environments []string
	// PageSize is runs per page. Zero defers to the server default.
	PageSize int
	// After is the opaque cursor returned by the previous call. Empty
	// fetches the first page.
	After string
}

// ListJobRunsInput passes the optional single-environment filter and cursor
// pagination to (*Job).ListRuns.
type ListJobRunsInput struct {
	// Environment restricts the listing to runs stamped with this environment.
	// Empty covers every environment you can access (subject to the client's
	// configured environment default).
	Environment string
	// PageSize is runs per page. Zero defers to the server default.
	PageSize int
	// After is the opaque cursor returned by the previous call. Empty
	// fetches the first page.
	After string
}
