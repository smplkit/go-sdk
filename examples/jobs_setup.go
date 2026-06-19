//go:build ignore

// Setup / cleanup helpers for jobs_showcase.go.
package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// Every job the jobs showcase creates. Start-of-run cleanup removes residue
// from a prior run; the matching cleanup in the showcase's defer tears the
// showcase's jobs down even when it fails mid-way, so a failed run never leaves
// orphans behind.
var jobsDemoJobIDs = []string{
	"showcase-recurring",
	"showcase-manual",
	"showcase-oneoff",
}

func setupJobsShowcase(ctx context.Context, jobs *smplkit.JobsClient) {
	cleanupJobsShowcase(ctx, jobs)
}

func cleanupJobsShowcase(ctx context.Context, jobs *smplkit.JobsClient) {
	for _, id := range jobsDemoJobIDs {
		if err := jobs.Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete job "+id, err)
			}
		}
	}
}
