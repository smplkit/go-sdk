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

	smplkit "github.com/smplkit/go-sdk/v3"
)

const (
	recurringJobID = "showcase-recurring"
	oneoffJobID    = "showcase-oneoff"
)

func main() {
	ctx := context.Background()

	// the standalone jobs client; the same surface is also reachable as
	// client.Jobs() on a SmplClient
	jobs, err := smplkit.NewJobsClient(smplkit.Config{})
	fatalIfErr("create jobs client", err)

	setupJobsShowcase(ctx, jobs)
	defer cleanupJobsShowcase(ctx, jobs)

	// create a recurring job, enabled in production with a development override
	body := `{"scope": "all"}`
	job := jobs.New(recurringJobID, "Nightly cache warm", "0 2 * * *", smplkit.HttpConfig{
		Method:  smplkit.JobHttpMethodPost,
		URL:     "https://httpbin.org/post",
		Headers: []smplkit.HttpHeader{{Name: "Authorization", Value: "Bearer s3cr3t"}},
		Body:    &body,
		Timeout: 30,
	}, smplkit.WithJobDescription("Warms the product cache every night at 02:00 UTC."))
	devBody := `{"scope": "all"}`
	job.SetConfiguration(smplkit.HttpConfig{
		Method:  smplkit.JobHttpMethodPost,
		URL:     "https://development.example.com/cache/warm",
		Headers: []smplkit.HttpHeader{{Name: "Authorization", Value: "Bearer development-s3cr3t"}},
		Body:    &devBody,
	}, "development")
	job.SetEnabled(false, "development")
	job.SetEnabled(true, "production")
	fatalIfErr("save recurring job", job.Save(ctx))
	if job.Version == nil || *job.Version != 1 {
		fatalIfErr("assertion", fmt.Errorf("expected version 1, got %v", job.Version))
	}
	if job.IsEnabled("development") {
		fatalIfErr("assertion", fmt.Errorf("expected development disabled"))
	}
	if !job.IsEnabled("production") {
		fatalIfErr("assertion", fmt.Errorf("expected production enabled"))
	}
	fmt.Printf("Created recurring job %q (v%d)\n", job.ID, *job.Version)

	// get a job
	fetched, err := jobs.Get(ctx, recurringJobID)
	fatalIfErr("get recurring job", err)
	if fetched.IsEnabled("development") {
		fatalIfErr("assertion", fmt.Errorf("expected development disabled on fetch"))
	}
	if !fetched.IsEnabled("production") {
		fatalIfErr("assertion", fmt.Errorf("expected production enabled on fetch"))
	}
	if fetched.GetConfiguration("development").URL != "https://development.example.com/cache/warm" {
		fatalIfErr("assertion", fmt.Errorf("dev config url mismatch: %s", fetched.GetConfiguration("development").URL))
	}
	fmt.Printf("Fetched job %q\n", recurringJobID)

	// list jobs
	listing, err := jobs.List(ctx, smplkit.ListJobsInput{})
	fatalIfErr("list jobs", err)
	if !containsJob(listing, recurringJobID) {
		fatalIfErr("assertion", fmt.Errorf("job %s not found in listing", recurringJobID))
	}
	fmt.Printf("Found job %q in the listing\n", recurringJobID)

	// update a job (the schedule is environment-agnostic)
	job.Name = "Nightly cache warm (v2)"
	job.SetSchedule("30 2 * * *")
	job.SetEnabled(true, "development")
	fatalIfErr("update recurring job", job.Save(ctx))
	if job.Version == nil || *job.Version != 2 || !job.IsEnabled("development") {
		fatalIfErr("assertion", fmt.Errorf("expected v2 and development enabled, got v=%v", job.Version))
	}
	fmt.Printf("Updated job to v%d: now enabled in production and development\n", *job.Version)

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

	// create a one-off job, born in a single environment
	oneoff := jobs.New(oneoffJobID, "One-shot reindex", "now", smplkit.HttpConfig{
		Method: smplkit.JobHttpMethodPost,
		URL:    "https://httpbin.org/post",
	}, smplkit.WithJobBirthEnvironment("development"))
	fatalIfErr("save one-off job", oneoff.Save(ctx))
	if oneoff.Version == nil || *oneoff.Version != 1 || !oneoff.IsEnabled("development") {
		fatalIfErr("assertion", fmt.Errorf("expected v1 and development enabled, got v=%v", oneoff.Version))
	}
	fmt.Printf("Created one-off job %q born in development\n", oneoff.ID)

	// delete a job
	fatalIfErr("delete recurring job", job.Delete(ctx))
	remaining, err := jobs.List(ctx, smplkit.ListJobsInput{})
	fatalIfErr("list jobs after delete", err)
	if containsJob(remaining, recurringJobID) {
		fatalIfErr("assertion", fmt.Errorf("job %s still present after delete", recurringJobID))
	}
	fmt.Printf("Deleted job %q — jobs showcase complete.\n", recurringJobID)
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
