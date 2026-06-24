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
//   - RunTriggerRetry: An automatic retry of a failed run, per the job's retry
//     policy.
//   - RunTriggerSchedule: The job's schedule fired.
type RunTrigger string

// RunTrigger values.
const (
	RunTriggerManual   = RunTrigger("MANUAL")
	RunTriggerRerun    = RunTrigger("RERUN")
	RunTriggerRetry    = RunTrigger("RETRY")
	RunTriggerSchedule = RunTrigger("SCHEDULE")
)

// Backoff is how the wait between retries grows (a retry policy's backoff
// strategy).
//
//   - BackoffExponential: Double the wait each retry — DelaySeconds, then 2×,
//     4×, … — capped at MaxDelaySeconds.
//   - BackoffFixed: Wait a constant DelaySeconds before every retry.
type Backoff string

// Backoff values.
const (
	BackoffExponential = Backoff("exponential")
	BackoffFixed       = Backoff("fixed")
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
	// Headers are attached to every run's request, as a name→value map
	// (e.g. {"Authorization": "Bearer s3cr3t"}). Values carry credentials;
	// supply them plaintext on writes. Reads return them plaintext too, so a
	// Get → mutate → Save round-trip preserves them. Use SetHeader / GetHeader
	// to read and write individual headers without worrying about a nil map.
	Headers map[string]string
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

// SetHeader sets (or replaces) a single request header by name, allocating the
// Headers map on first use.
func (h *HttpConfig) SetHeader(name, value string) {
	if h.Headers == nil {
		h.Headers = map[string]string{}
	}
	h.Headers[name] = value
}

// GetHeader returns the value of header name and whether it is set.
func (h *HttpConfig) GetHeader(name string) (string, bool) {
	v, ok := h.Headers[name]
	return v, ok
}

// JobEnvironment is one environment's sparse override for a job (ADR-056).
//
// A job's Environments map holds one of these per environment. Only the leaves
// you set are sent on save; everything you leave unset is inherited from the
// job's base definition, and the server resolves base ⊕ overrides when the job
// fires. The base definition is disabled everywhere, so a job runs in an
// environment only when that environment's override sets Enabled=true.
//
// Reach (and lazily create) an environment's override via (*Job).Environment,
// then set leaves directly, e.g.
//
//	job.Environment("production").Enabled = true
//	job.Environment("production").URL = "https://prod.example.com/warm"
//	job.Environment("production").SetHeader("Authorization", "Bearer prod")
//
// Reads are pure override: a leaf this environment does not override reads back
// as its zero value / nil, NOT the base value — the SDK does not merge in the
// base (jobs resolve server-side). To see a base value, read the job's base
// definition (job.Configuration, job.Schedule, …).
type JobEnvironment struct {
	// Enabled controls whether the job runs in this environment. Defaults to
	// false.
	Enabled bool
	// Schedule is an optional per-environment cron override that varies the
	// cadence for just this environment (recurring jobs only). Empty does not
	// override (inherits the job's base Schedule). When set, it must be a
	// 5-field cron expression; it cannot appear on a manual or one-off job, and
	// cannot change a job's kind.
	Schedule string
	// Timezone is an optional per-environment IANA timezone override for
	// evaluating this environment's cron Schedule (recurring jobs only). Empty
	// does not override (inherits the base Timezone, else UTC). When set, it
	// must be a valid IANA zone key (e.g. "America/New_York").
	Timezone string
	// RetryPolicy is an optional per-environment retry-policy override — the id
	// of a RetryPolicy (or "Default"); pass a saved policy's ID. Empty does not
	// override (inherits the job's base RetryPolicy).
	RetryPolicy string
	// URL optionally overrides the base destination URL for this environment.
	// Empty does not override.
	URL string
	// Method optionally overrides the base HTTP verb for this environment.
	// Empty does not override.
	Method JobHttpMethod
	// Timeout optionally overrides the base per-run timeout (seconds) for this
	// environment. Zero does not override.
	Timeout int
	// Body optionally overrides the base request body for this environment. Nil
	// does not override.
	Body *string
	// SuccessStatus optionally overrides the base success-status pattern for
	// this environment. Empty does not override.
	SuccessStatus string
	// TlsVerify optionally overrides base TLS verification for this environment.
	// Nil does not override; &false / &true overrides explicitly.
	TlsVerify *bool
	// CaCert optionally overrides the base CA certificate for this environment.
	// Nil does not override.
	CaCert *string
	// Headers holds per-environment header overrides as a name→value map. Each
	// entry overrides (or adds) that one header by name on top of the base
	// headers, leaving the rest inherited. Use SetHeader / GetHeader.
	Headers map[string]string
	// NextRunAt is the read-only next scheduled fire time in this environment.
	// Nil when the environment is not enabled, or once a one-off run has fired.
	// Populated on reads; never sent on writes.
	NextRunAt *time.Time
}

// SetHeader overrides (or adds) a single header by name in this environment,
// allocating the Headers map on first use.
func (e *JobEnvironment) SetHeader(name, value string) {
	if e.Headers == nil {
		e.Headers = map[string]string{}
	}
	e.Headers[name] = value
}

// GetHeader returns this environment's override for header name and whether it
// overrides that header.
func (e *JobEnvironment) GetHeader(name string) (string, bool) {
	v, ok := e.Headers[name]
	return v, ok
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
	// Environments holds per-environment sparse overrides keyed by environment
	// key (e.g. "production", "development"). A job fires in an environment only
	// when Environments[env].Enabled is true. Each entry is a flat overlay that
	// overrides only the leaves it sets, inheriting the base definition for the
	// rest. Reach (and lazily create) an entry via Environment. For a recurring
	// job, supply this map to choose where it runs; a one-off job records the
	// single environment it was created in. Every referenced environment must
	// exist for the account. Enablement has no top-level field — use Enabled()
	// for the roll-up.
	Environments map[string]*JobEnvironment
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
	// RetryPolicy is the base retry policy for failed runs — the id of a
	// RetryPolicy (or the built-in "Default", which never retries), overridable
	// per environment via JobEnvironment.RetryPolicy. Empty (omitted on the
	// wire) means Default. Settable; sent on writes only when non-empty.
	RetryPolicy string
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
	// born in. JobsClient.Schedule seeds it into the Environments map as an
	// enabled entry, so it travels in the create request body (there is no
	// X-Smplkit-Environment header). Ignored for recurring and manual jobs,
	// whose environments come from Environments.
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
	// Retry is the run's position in its retry chain; set (non-nil) only when
	// Trigger is "RETRY", nil otherwise.
	Retry *RunRetry
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

// RunRetry is where a RETRY run sits in its retry chain (read-only).
type RunRetry struct {
	// Of is the id of the chain's original run — the first attempt that failed
	// and started the chain.
	Of string
	// Attempt is which retry this run is — 1 for the first retry, 2 for the
	// second, and so on.
	Attempt int
}

// RetryPolicy is a named, reusable retry policy.
//
// A policy decides whether and how a failed run is retried. Reference it from a
// job's RetryPolicy (and optionally override it per environment). A job that
// references nothing uses the built-in "Default" policy, which never retries.
// Retry policies are account-global — never environment-scoped.
//
// Active-record style: build one with RetryPoliciesClient.New, mutate fields,
// and call Save(ctx) to persist (create when new, full-replace update when it
// already exists), or Delete(ctx) to remove.
type RetryPolicy struct {
	// ID is the caller-supplied unique identifier for the policy. Unique within
	// the account and immutable; the service returns 409 if another live policy
	// already uses this id.
	ID string
	// Name is the human-readable name for the policy.
	Name string
	// MaxRetries is how many times a failed run is retried after the initial
	// attempt — 3 means up to 4 attempts total. 0 disables retries. Maximum 10.
	MaxRetries int
	// Backoff is how the wait between retries grows (see Backoff).
	Backoff Backoff
	// DelaySeconds is the wait before a retry, in seconds — the constant wait
	// for fixed backoff, or the base that doubles each retry for exponential.
	DelaySeconds int
	// MaxDelaySeconds is the ceiling on the wait between retries, for
	// exponential backoff only. Nil (the default) leaves it uncapped; omit it
	// for fixed backoff. Sent on writes only when non-nil.
	MaxDelaySeconds *int
	// RetryOnTimeout retries a run that failed because the request did not
	// complete within the job's timeout. Defaults to false (timeouts are not
	// retried).
	RetryOnTimeout bool
	// RetryOnConnectionError retries a run that failed because the destination
	// could not be reached (DNS, connection refused, TLS, or transport error).
	// Defaults to false (connection errors are not retried).
	RetryOnConnectionError bool
	// RetryStatuses is an allowlist of response status patterns to retry when a
	// run fails because the response did not match the job's success status.
	// Each element is either an exact 3-digit HTTP code (e.g. "429") or a status
	// class ("1xx", "2xx", "3xx", "4xx", "5xx") — e.g. []string{"429", "5xx"} to
	// retry on rate-limit and any server error. Empty (the default) matches no
	// status, so nothing is retried on a non-success response.
	RetryStatuses []string
	// RetryStatusesExcept subtracts from RetryStatuses, using the same exact-code
	// or class syntax. A status matching both lists is not retried — except wins
	// on overlap — so RetryStatuses of []string{"5xx"} with RetryStatusesExcept
	// of []string{"501"} retries every server error except 501. Empty (the
	// default) subtracts nothing.
	RetryStatusesExcept []string
	// CreatedAt is when the policy was created. Nil for an unsaved RetryPolicy.
	CreatedAt *time.Time
	// UpdatedAt is when the policy was last modified.
	UpdatedAt *time.Time
	// DeletedAt is when the policy was deleted. Nil for live policies.
	DeletedAt *time.Time
	// Version is a monotonic counter incremented on every update, starting at 1.
	Version *int

	client *RetryPoliciesClient
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
	// Triggers restricts the listing to runs started by any of these triggers
	// (see RunTrigger) — e.g. []RunTrigger{RunTriggerRetry} for automatic
	// retries. Sent as a comma-separated filter[trigger]. Empty covers every
	// trigger.
	Triggers []RunTrigger
	// LastRunOnly, when true, collapses the result to the last completed
	// (succeeded / failed / canceled) run per job-and-environment; in-flight
	// runs are excluded. The other filters apply first, then the collapse. The
	// query param is sent only when true.
	LastRunOnly bool
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
	// Triggers restricts the listing to runs started by any of these triggers
	// (see RunTrigger) — e.g. []RunTrigger{RunTriggerRetry} for automatic
	// retries. Empty covers every trigger.
	Triggers []RunTrigger
	// LastRunOnly, when true, returns only the last completed run per
	// environment (in-flight runs excluded). Defaults to false.
	LastRunOnly bool
	// PageSize is runs per page. Zero defers to the server default.
	PageSize int
	// After is the opaque cursor returned by the previous call. Empty
	// fetches the first page.
	After string
}

// ListRetryPoliciesInput passes filters and pagination to
// RetryPoliciesClient.List.
type ListRetryPoliciesInput struct {
	// Name filters to policies whose name contains this text (case-insensitive).
	// Nil returns all.
	Name *string
	// PageNumber is the 1-based page index. Zero defers to the server default
	// (page 1).
	PageNumber int
	// PageSize is items per page. Zero defers to the server default.
	PageSize int
}
