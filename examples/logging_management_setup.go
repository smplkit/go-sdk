//go:build ignore

// Setup / cleanup helpers for logging_management_showcase.go.
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

func setupLoggingManagementShowcase(ctx context.Context, logging *smplkit.LoggingClient) {
	cleanupLoggingManagementShowcase(ctx, logging)
}

func cleanupLoggingManagementShowcase(ctx context.Context, logging *smplkit.LoggingClient) {
	for _, id := range loggingMgmtDemoLoggerIDs {
		if err := logging.Loggers().Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete logger "+id, err)
			}
		}
	}
}
