//go:build ignore

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

func setupFlagsManagementShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	cleanupFlagsManagementShowcase(ctx, mgmt)
}

func cleanupFlagsManagementShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	for _, id := range flagsMgmtDemoFlagIDs {
		if err := mgmt.Flags().Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete flag "+id, err)
			}
		}
	}
}
