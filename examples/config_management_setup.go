//go:build ignore

// Setup / cleanup helpers for config_management_showcase.go.
package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

var configMgmtDemoConfigIDs = []string{"showcase-user-service", "showcase-common"}

func setupConfigManagementShowcase(ctx context.Context, config *smplkit.ConfigClient) {
	cleanupConfigManagementShowcase(ctx, config)
}

func cleanupConfigManagementShowcase(ctx context.Context, config *smplkit.ConfigClient) {
	for _, id := range configMgmtDemoConfigIDs {
		if err := config.Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete config "+id, err)
			}
		}
	}
}
