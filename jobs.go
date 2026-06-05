// Package smplkit — Smpl Jobs management surface (mgmt.Jobs()).
//
// Unlike Config/Flags/Logging, Jobs has no live "phone-home" agent — no
// environment registration, no WebSocket — so its entire surface lives on
// the management client, exactly like audit forwarder CRUD. Defining a
// job, triggering a run, and reading run history are all plain
// request/response calls here:
//
//	mgmt.Jobs().New / Get / List / Delete / Run / Usage
//	mgmt.Jobs().Runs().List / Get / Cancel / Rerun
//	(*Job).Save / (*Job).Delete
//
// A Job is an active record: build it with JobsManagement.New, mutate
// fields, and call Save(ctx) (create when unsaved, full-replace update
// when it already exists) or Delete(ctx). Runs are read-only views; run
// actions live on mgmt.Jobs().Runs().
package smplkit

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	genjobs "github.com/smplkit/go-sdk/v3/internal/generated/jobs"
)

// JobsManagement is the mgmt.Jobs() surface: active-record job CRUD, the
// run-now action, run history (Runs), and usage. Obtained via
// ManagementClient.Jobs().
type JobsManagement struct {
	gen  *genjobs.ClientWithResponses
	runs *RunsClient
}

// Runs returns the run history and run-action sub-client.
func (j *JobsManagement) Runs() *RunsClient { return j.runs }

// RunsClient is the mgmt.Jobs().Runs() surface: read-only run history plus
// the cancel / rerun run actions.
type RunsClient struct {
	gen *genjobs.ClientWithResponses
}

// ---------------------------------------------------------------------------
// Job active-record surface
// ---------------------------------------------------------------------------

// New returns an unsaved Job bound to this client. Call (*Job).Save(ctx)
// to create it.
//
// id is the caller-supplied unique identifier for the job. Unique within
// the account and immutable; the service returns 409 if another live job
// already uses this id.
func (j *JobsManagement) New(
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
		Enabled:           true,
		Type:              "http",
		ConcurrencyPolicy: "ALLOW",
		client:            j,
	}
	for _, opt := range opts {
		opt(job)
	}
	return job
}

// JobOption configures an unsaved Job returned by JobsManagement.New.
type JobOption func(*Job)

// WithJobEnabled overrides the default Enabled=true.
func WithJobEnabled(enabled bool) JobOption {
	return func(job *Job) { job.Enabled = enabled }
}

// WithJobDescription sets the optional free-text description.
func WithJobDescription(description string) JobOption {
	return func(job *Job) { job.Description = &description }
}

// WithJobConcurrencyPolicy overrides the default concurrency policy
// ("ALLOW").
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

// Delete soft-deletes this job on the server.
func (job *Job) Delete(ctx context.Context) error {
	if job.client == nil || job.ID == "" {
		return &Error{Message: "job was constructed without a client or id; cannot delete"}
	}
	return job.client.Delete(ctx, job.ID)
}

func (job *Job) apply(other *Job) {
	job.ID = other.ID
	job.Name = other.Name
	job.Description = other.Description
	job.Enabled = other.Enabled
	job.Type = other.Type
	job.Schedule = other.Schedule
	job.Configuration = other.Configuration
	job.ConcurrencyPolicy = other.ConcurrencyPolicy
	job.NextRunAt = other.NextRunAt
	job.CreatedAt = other.CreatedAt
	job.UpdatedAt = other.UpdatedAt
	job.DeletedAt = other.DeletedAt
	job.Version = other.Version
}

// ---------------------------------------------------------------------------
// Job collection read surface
// ---------------------------------------------------------------------------

// List returns the jobs for the authenticated account. Offset pagination
// via PageNumber / PageSize (ADR-014).
func (j *JobsManagement) List(ctx context.Context, input ListJobsInput) ([]*Job, error) {
	params := &genjobs.ListJobsParams{}
	if input.Enabled != nil {
		params.FilterEnabled = input.Enabled
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
func (j *JobsManagement) Get(ctx context.Context, id string) (*Job, error) {
	resp, err := j.gen.GetJobWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("jobs Get: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	return jobFromResource(resp.ApplicationvndApiJSON200.Data, j), nil
}

// Delete soft-deletes a job by id.
func (j *JobsManagement) Delete(ctx context.Context, id string) error {
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
func (j *JobsManagement) Run(ctx context.Context, id string) (*Run, error) {
	resp, err := j.gen.RunJobNowWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("jobs Run: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := runFromResource(resp.ApplicationvndApiJSON200.Data)
	return &out, nil
}

// Usage returns the current-period usage counters for the account.
func (j *JobsManagement) Usage(ctx context.Context) (*Usage, error) {
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
// paginated (ADR-014): pass PageSize and the After cursor from the prior
// page. Pass Job to scope to a single job's history.
func (r *RunsClient) List(ctx context.Context, input ListRunsInput) ([]*Run, error) {
	params := &genjobs.ListRunsParams{}
	if input.Job != "" {
		job := input.Job
		params.FilterJob = &job
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
		run := runFromResource(res)
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
	out := runFromResource(resp.ApplicationvndApiJSON200.Data)
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
	out := runFromResource(resp.ApplicationvndApiJSON200.Data)
	return &out, nil
}

// Rerun re-runs a prior run, spawning a new RERUN run.
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
	out := runFromResource(resp.ApplicationvndApiJSON200.Data)
	return &out, nil
}

// ---------------------------------------------------------------------------
// Internal helpers (create / update / wire conversions)
// ---------------------------------------------------------------------------

// create posts a new job and returns the server-authoritative response.
// Called by (*Job).Save on unsaved instances.
func (j *JobsManagement) create(ctx context.Context, job *Job) (*Job, error) {
	body := genjobs.CreateJobApplicationVndAPIPlusJSONRequestBody{
		Data: jobCreateResourceFromJob(job),
	}
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
func (j *JobsManagement) update(ctx context.Context, job *Job) (*Job, error) {
	body := genjobs.UpdateJobApplicationVndAPIPlusJSONRequestBody{
		Data: jobResourceFromJob(job.ID, job),
	}
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
func jobAttributes(job *Job) genjobs.Job {
	enabled := job.Enabled
	attrs := genjobs.Job{
		Name:          job.Name,
		Schedule:      job.Schedule,
		Enabled:       &enabled,
		Configuration: httpConfigToWire(job.Configuration),
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
		m := h.Method
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
		out.Method = *h.Method
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

func jobFromResource(r genjobs.JobResource, client *JobsManagement) *Job {
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
		NextRunAt:     a.NextRunAt,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
		DeletedAt:     a.DeletedAt,
		Version:       a.Version,
		Type:          "http",
		client:        client,
	}
	if a.Enabled != nil {
		out.Enabled = *a.Enabled
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

func runFromResource(r genjobs.RunResource) Run {
	a := r.Attributes
	out := Run{
		ID:                r.Id,
		Job:               a.Job,
		JobVersion:        a.JobVersion,
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
