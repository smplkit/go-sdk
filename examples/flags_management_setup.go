//go:build ignore

// Setup / cleanup helpers for flags_management_showcase.go.
package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

var flagsMgmtDemoFlagIDs = []string{
	"checkout-v2",
	"banner-color",
	"max-retries",
	"ui-theme",
}

func setupFlagsManagementShowcase(ctx context.Context, flags *smplkit.FlagsClient) {
	cleanupFlagsManagementShowcase(ctx, flags)
}

func cleanupFlagsManagementShowcase(ctx context.Context, flags *smplkit.FlagsClient) {
	for _, id := range flagsMgmtDemoFlagIDs {
		if err := flags.Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete flag "+id, err)
			}
		}
	}
}
