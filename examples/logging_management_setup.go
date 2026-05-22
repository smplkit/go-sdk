//go:build ignore

package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

var loggingMgmtDemoLoggerIDs = []string{
	"showcase",
	"showcase.db",
	"showcase.payments",
}

func setupLoggingManagementShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	cleanupLoggingManagementShowcase(ctx, mgmt)
}

func cleanupLoggingManagementShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	for _, id := range loggingMgmtDemoLoggerIDs {
		if err := mgmt.Loggers().Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete logger "+id, err)
			}
		}
	}
}
