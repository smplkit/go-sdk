//go:build ignore

// Demonstrates the smplkit management SDK for Smpl Jobs.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//
// Usage:
//
//	make jobs_showcase
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	smplkit "github.com/smplkit/go-sdk/v3"
)

const (
	recurringJobID = "showcase-recurring"
	manualJobID    = "showcase-manual"
	oneoffJobID    = "showcase-oneoff"
	retryPolicyID  = "showcase-retry"
)

func main() {
	ctx := context.Background()

	// the standalone jobs client; the same surface is also reachable as
	// client.Jobs() on a SmplClient
	jobs, err := smplkit.NewJobsClient(smplkit.Config{})
	fatalIfErr("create jobs client", err)

	setupJobsShowcase(ctx, jobs)
	defer cleanupJobsShowcase(ctx, jobs)

	// create a retry policy
	retryPolicy := jobs.RetryPolicies().New(retryPolicyID, "Retry on server errors", 5, smplkit.BackoffExponential, 2,
		smplkit.WithRetryPolicyMaxDelaySeconds(60),
		smplkit.WithRetryPolicyRetryOnTimeout(true),
		smplkit.WithRetryPolicyRetryOnConnectionError(true),
		smplkit.WithRetryPolicyRetryStatuses([]string{"429", "5xx"}),
		smplkit.WithRetryPolicyRetryStatusesExcept([]string{"501"}))
	fatalIfErr("save retry policy", retryPolicy.Save(ctx))
	policies, err := jobs.RetryPolicies().List(ctx, smplkit.ListRetryPoliciesInput{})
	fatalIfErr("list retry policies", err)
	if !containsRetryPolicy(policies, retryPolicyID) {
		fatalIfErr("assertion", fmt.Errorf("retry policy %s not found in listing", retryPolicyID))
	}
	fmt.Printf("Created retry policy %q\n", retryPolicy.ID)

	// create a recurring job
	body := `{"scope": "all"}`
	job := jobs.NewRecurringJob(recurringJobID, "Nightly cache warm", "0 2 * * *", smplkit.HttpConfig{
		Method:  smplkit.JobHttpMethodPost,
		URL:     "https://httpbin.org/post",
		Headers: []smplkit.HttpHeader{{Name: "Authorization", Value: "Bearer s3cr3t"}},
		Body:    &body,
		Timeout: 30,
	}, smplkit.WithJobDescription("Warms the product cache nightly."))
	job.SetEnabled(true, "development")
	job.SetEnabled(true, "production")
	job.SetSchedule("0 */6 * * *", smplkit.WithScheduleTimezone("America/New_York"), smplkit.WithScheduleEnvironment("development"))
	devBody := `{"scope": "all"}`
	job.SetConfiguration(smplkit.HttpConfig{
		Method:  smplkit.JobHttpMethodPost,
		URL:     "https://development.example.com/cache/warm",
		Headers: []smplkit.HttpHeader{{Name: "Authorization", Value: "Bearer development-s3cr3t"}},
		Body:    &devBody,
	}, "development")
	fatalIfErr("save recurring job", job.Save(ctx))
	if !job.IsRecurring() {
		fatalIfErr("assertion", fmt.Errorf("expected recurring job"))
	}
	if !job.IsEnabled("development") {
		fatalIfErr("assertion", fmt.Errorf("expected development enabled"))
	}
	if !job.IsEnabled("production") {
		fatalIfErr("assertion", fmt.Errorf("expected production enabled"))
	}
	if job.Environments["development"].Timezone != "America/New_York" {
		fatalIfErr("assertion", fmt.Errorf("dev timezone mismatch: %s", job.Environments["development"].Timezone))
	}
	if job.GetConfiguration("development").URL != "https://development.example.com/cache/warm" {
		fatalIfErr("assertion", fmt.Errorf("dev config url mismatch: %s", job.GetConfiguration("development").URL))
	}
	fmt.Printf("Created recurring job %q (v%d)\n", job.ID, *job.Version)

	// get a job
	fetched, err := jobs.Get(ctx, recurringJobID)
	fatalIfErr("get recurring job", err)
	if fetched.Environments["development"].Schedule != "0 */6 * * *" {
		fatalIfErr("assertion", fmt.Errorf("dev schedule mismatch: %s", fetched.Environments["development"].Schedule))
	}
	fmt.Printf("Fetched job %q\n", recurringJobID)

	// list jobs, filtered to recurring jobs
	recurringKind := smplkit.JobKindRecurring
	listing, err := jobs.List(ctx, smplkit.ListJobsInput{Kind: &recurringKind})
	fatalIfErr("list jobs", err)
	if !containsJob(listing, recurringJobID) {
		fatalIfErr("assertion", fmt.Errorf("job %s not found in listing", recurringJobID))
	}
	fmt.Printf("Found job %q in the listing\n", recurringJobID)

	// update a job
	job.Name = "Nightly cache warm (v2)"
	job.SetRetryPolicy(retryPolicy, "production")
	job.SetSchedule("30 2 * * *", smplkit.WithScheduleTimezone("America/Los_Angeles"), smplkit.WithScheduleEnvironment("production"))
	fatalIfErr("update recurring job", job.Save(ctx))
	if job.Version == nil || *job.Version != 2 {
		fatalIfErr("assertion", fmt.Errorf("expected version 2, got %v", job.Version))
	}
	fmt.Printf("Updated job to v%d\n", *job.Version)

	// trigger an immediate run
	run, err := job.Trigger(ctx, "production")
	fatalIfErr("trigger run", err)
	if run.Trigger != "MANUAL" || run.Environment != "production" {
		fatalIfErr("assertion", fmt.Errorf("expected MANUAL/production, got trigger=%s env=%s", run.Trigger, run.Environment))
	}
	fmt.Printf("Triggered run %s (trigger=%s, env=%s)\n", run.ID, run.Trigger, run.Environment)

	// get this job's runs
	runs, err := job.ListRuns(ctx, smplkit.ListJobRunsInput{Environment: "production"})
	fatalIfErr("list job runs", err)
	if !containsRun(runs, run.ID) {
		fatalIfErr("assertion", fmt.Errorf("run %s not found in run history", run.ID))
	}
	fmt.Printf("Listed %d production run(s)\n", len(runs))

	// get the last completed run in production
	recent, err := job.ListRuns(ctx, smplkit.ListJobRunsInput{Environment: "production", LastRunOnly: true})
	fatalIfErr("list last completed run", err)
	fmt.Printf("Last completed production run(s): %d\n", len(recent))

	// get a run
	run, err = jobs.Runs().Get(ctx, run.ID)
	fatalIfErr("get run", err)
	if run.Environment != "production" {
		fatalIfErr("assertion", fmt.Errorf("expected production env, got %s", run.Environment))
	}
	fmt.Printf("Fetched run %s (env=%s)\n", run.ID, run.Environment)

	// re-run a prior run (inherits its environment)
	rerun, err := run.Rerun(ctx)
	fatalIfErr("rerun", err)
	if rerun.Trigger != "RERUN" || rerun.Environment != run.Environment {
		fatalIfErr("assertion", fmt.Errorf("expected RERUN/%s, got trigger=%s env=%s", run.Environment, rerun.Trigger, rerun.Environment))
	}
	fmt.Printf("Re-ran %s -> %s (env=%s)\n", run.ID, rerun.ID, rerun.Environment)

	// cancel a run (best-effort: a finished run can no longer be canceled)
	canceled, err := rerun.Cancel(ctx)
	if err != nil {
		var conflict *smplkit.ConflictError
		if errors.As(err, &conflict) {
			fmt.Printf("Run %s already finished before it could be canceled\n", rerun.ID)
		} else {
			fatalIfErr("cancel run", err)
		}
	} else {
		fmt.Printf("Canceled run %s -> %s\n", canceled.ID, canceled.Status)
	}

	// create a manual job (no schedule, runs only when triggered)
	manual := jobs.NewManualJob(manualJobID, "On-demand reindex", smplkit.HttpConfig{
		Method: smplkit.JobHttpMethodPost,
		URL:    "https://httpbin.org/post",
	})
	manual.SetEnabled(true, "production")
	fatalIfErr("save manual job", manual.Save(ctx))
	if !manual.IsManual() {
		fatalIfErr("assertion", fmt.Errorf("expected manual job"))
	}
	manualRun, err := manual.Trigger(ctx, "production")
	fatalIfErr("trigger manual run", err)
	if manualRun.Trigger != string(smplkit.RunTriggerManual) {
		fatalIfErr("assertion", fmt.Errorf("expected MANUAL trigger, got %s", manualRun.Trigger))
	}
	fmt.Printf("Created manual job %q and triggered it on demand\n", manual.ID)

	// schedule a one-off job to run tomorrow
	tomorrow := time.Now().Add(24 * time.Hour)
	oneoff := jobs.Schedule(oneoffJobID, "One-shot reindex", tomorrow, smplkit.HttpConfig{
		Method: smplkit.JobHttpMethodPost,
		URL:    "https://httpbin.org/post",
	}, smplkit.WithJobBirthEnvironment("development"))
	fatalIfErr("save one-off job", oneoff.Save(ctx))
	if !oneoff.IsOneOff() {
		fatalIfErr("assertion", fmt.Errorf("expected one-off job"))
	}
	if !oneoff.IsEnabled("development") {
		fatalIfErr("assertion", fmt.Errorf("expected development enabled"))
	}
	if oneoff.Environments["development"].NextRunAt == nil {
		fatalIfErr("assertion", fmt.Errorf("expected next_run_at to be set"))
	}
	fmt.Printf("Created one-off job %q to run in development\n", oneoff.ID)

	// delete a job
	fatalIfErr("delete recurring job", job.Delete(ctx))
	remaining, err := jobs.List(ctx, smplkit.ListJobsInput{})
	fatalIfErr("list jobs after delete", err)
	if containsJob(remaining, recurringJobID) {
		fatalIfErr("assertion", fmt.Errorf("job %s still present after delete", recurringJobID))
	}

	// delete the retry policy
	fatalIfErr("delete retry policy", retryPolicy.Delete(ctx))
	fmt.Printf("Deleted job %q and retry policy — jobs showcase complete.\n", recurringJobID)
}

func containsJob(jobs []*smplkit.Job, id string) bool {
	for _, j := range jobs {
		if j.ID == id {
			return true
		}
	}
	return false
}

func containsRun(runs []*smplkit.Run, id string) bool {
	for _, r := range runs {
		if r.ID == id {
			return true
		}
	}
	return false
}

func containsRetryPolicy(policies []*smplkit.RetryPolicy, id string) bool {
	for _, p := range policies {
		if p.ID == id {
			return true
		}
	}
	return false
}
