//go:build ignore

// Demonstrates the smplkit SDK for Smpl Jobs.
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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func main() {
	ctx := context.Background()

	// Jobs has no runtime/management split — one client. Here we use the
	// standalone JobsClient; the same surface is also reachable as
	// client.Jobs() on a SmplClient.
	jobs, err := smplkit.NewJobsClient(smplkit.Config{})
	fatalIfErr("create jobs client", err)

	jobID := "showcase-mgmt-" + randomHexJobs(4)

	// tear-down: never leave the showcase job behind, even on failure
	defer func() {
		err := jobs.Delete(ctx, jobID)
		var notFound *smplkit.NotFoundError
		if err != nil && !errors.As(err, &notFound) {
			fatalIfErr("jobs.Delete (teardown)", err)
		}
	}()

	// create a job
	authHeader := "Bearer s3cr3t"
	body := `{"scope": "all"}`
	job := jobs.New(
		jobID,
		"Nightly cache warm",
		"0 2 * * *", // 5-field cron, UTC
		smplkit.HttpConfig{
			Method:  smplkit.JobHttpMethodPost,
			URL:     "https://api.example.com/cache/warm",
			Headers: []smplkit.HttpHeader{{Name: "Authorization", Value: authHeader}},
			Body:    &body,
			Timeout: 30,
		},
		smplkit.WithJobEnabled(false),
		smplkit.WithJobDescription("Warms the product cache every night at 02:00 UTC."),
	)
	fatalIfErr("jobs.Save", job.Save(ctx))
	if job.Version == nil || *job.Version != 1 {
		fatalIfErr("assertion", fmt.Errorf("expected version 1, got %v", job.Version))
	}
	fmt.Printf("Created job %q (v%d)\n", job.ID, *job.Version)

	// get a job
	fetched, err := jobs.Get(ctx, jobID)
	fatalIfErr("jobs.Get", err)
	if fetched.Configuration.URL != "https://api.example.com/cache/warm" {
		fatalIfErr("assertion", fmt.Errorf("fetched job url mismatch: %s", fetched.Configuration.URL))
	}
	fmt.Printf("Fetched job %q\n", jobID)

	// list jobs
	disabled := false
	listing, err := jobs.List(ctx, smplkit.ListJobsInput{Enabled: &disabled})
	fatalIfErr("jobs.List", err)
	found := false
	for _, j := range listing {
		if j.ID == jobID {
			found = true
			break
		}
	}
	if !found {
		fatalIfErr("assertion", fmt.Errorf("job %s not found in listing", jobID))
	}
	fmt.Printf("Found job %q and in the listing\n", jobID)

	// update a job
	job.Name = "Nightly cache warm (v2)"
	job.Schedule = "30 2 * * *"
	job.Enabled = true
	fatalIfErr("jobs.Save", job.Save(ctx))
	if job.Version == nil || *job.Version != 2 || !job.Enabled {
		fatalIfErr("assertion", fmt.Errorf("expected version 2 and enabled, got v=%v enabled=%t", job.Version, job.Enabled))
	}
	fmt.Printf("Updated job to v%d: schedule=%q\n", *job.Version, job.Schedule)

	// trigger an immediate run (a MANUAL run)
	run, err := jobs.Run(ctx, jobID)
	fatalIfErr("jobs.Run", err)
	if run.Trigger != "MANUAL" || run.Job != jobID {
		fatalIfErr("assertion", fmt.Errorf("expected MANUAL trigger for %s, got trigger=%s job=%s", jobID, run.Trigger, run.Job))
	}
	fmt.Printf("Triggered run %s (trigger=%s, status=%s)\n", run.ID, run.Trigger, run.Status)

	// read run history for this job, and fetch a single run
	runs, err := jobs.Runs().List(ctx, smplkit.ListRunsInput{Job: jobID})
	fatalIfErr("jobs.Runs.List", err)
	seen := false
	for _, r := range runs {
		if r.ID == run.ID {
			seen = true
			break
		}
	}
	if !seen {
		fatalIfErr("assertion", fmt.Errorf("run %s not found in runs listing", run.ID))
	}
	got, err := jobs.Runs().Get(ctx, run.ID)
	fatalIfErr("jobs.Runs.Get", err)
	if got.ID != run.ID {
		fatalIfErr("assertion", fmt.Errorf("fetched run id mismatch: %s != %s", got.ID, run.ID))
	}
	fmt.Printf("Listed %d run(s); fetched run %s (status=%s)\n", len(runs), got.ID, got.Status)

	// re-run from a prior run, then cancel it while it's still pending
	rerun, err := jobs.Runs().Rerun(ctx, run.ID)
	fatalIfErr("jobs.Runs.Rerun", err)
	if rerun.Trigger != "RERUN" || rerun.RerunOf == nil || *rerun.RerunOf != run.ID {
		fatalIfErr("assertion", fmt.Errorf("rerun mismatch: trigger=%s rerun_of=%v", rerun.Trigger, rerun.RerunOf))
	}
	canceled, err := jobs.Runs().Cancel(ctx, rerun.ID)
	fatalIfErr("jobs.Runs.Cancel", err)
	if canceled.Status != "CANCELED" {
		fatalIfErr("assertion", fmt.Errorf("expected CANCELED, got %s", canceled.Status))
	}
	fmt.Printf("Re-ran (%s) then canceled it -> %s\n", rerun.ID, canceled.Status)

	// delete a job
	fatalIfErr("jobs.Delete", job.Delete(ctx))
	remaining, err := jobs.List(ctx, smplkit.ListJobsInput{})
	fatalIfErr("jobs.List", err)
	for _, j := range remaining {
		if j.ID == jobID {
			fatalIfErr("assertion", fmt.Errorf("job %s still present after delete", jobID))
		}
	}
	fmt.Printf("Deleted job %q — jobs showcase complete.\n", jobID)
}

func randomHexJobs(nBytes int) string {
	buf := make([]byte, nBytes)
	_, err := rand.Read(buf)
	fatalIfErr("rand.Read", err)
	return hex.EncodeToString(buf)
}
