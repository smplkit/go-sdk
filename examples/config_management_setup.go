//go:build ignore

package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

var configMgmtDemoConfigIDs = []string{"showcase-user-service", "showcase-common"}

func setupConfigManagementShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	cleanupConfigManagementShowcase(ctx, mgmt)
}

func cleanupConfigManagementShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	for _, id := range configMgmtDemoConfigIDs {
		if err := mgmt.Config().Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete config "+id, err)
			}
		}
	}
}
