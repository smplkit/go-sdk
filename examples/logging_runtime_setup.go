//go:build ignore

// logging_runtime_setup.go provides shared setup and teardown helpers
// for the logging runtime showcase.
package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk"
)

// setupDemoLoggers creates demo loggers and a log group for the runtime showcase.
func setupDemoLoggers(ctx context.Context, client *smplkit.Client) ([]*smplkit.Logger, []*smplkit.LogGroup) {
	logging := client.Logging()

	// Pre-flight cleanup: delete leftover resources from previous runs.
	for _, key := range []string{"com.acme.payments", "com.acme.auth", "com.acme.db"} {
		_ = logging.Delete(ctx, key)
	}
	for _, key := range []string{"acme-core"} {
		_ = logging.DeleteGroup(ctx, key)
	}

	// Create a log group.
	group := logging.NewGroup("acme-core", smplkit.WithLogGroupName("Acme Core"))
	group.SetLevel(smplkit.LogLevelWarn)
	if err := group.Save(ctx); err != nil {
		fatal("Failed to create log group", err)
	}
	fmt.Printf("  Created group: %s (level=%s)\n", group.Key, *group.Level)

	// Create loggers.
	var loggers []*smplkit.Logger

	payments := logging.New("com.acme.payments", smplkit.WithLoggerName("Payments"))
	payments.Group = &group.ID
	payments.SetLevel(smplkit.LogLevelInfo)
	if err := payments.Save(ctx); err != nil {
		fatal("Failed to create payments logger", err)
	}
	loggers = append(loggers, payments)

	auth := logging.New("com.acme.auth", smplkit.WithLoggerName("Auth"))
	auth.Group = &group.ID
	if err := auth.Save(ctx); err != nil {
		fatal("Failed to create auth logger", err)
	}
	loggers = append(loggers, auth)

	db := logging.New("com.acme.db", smplkit.WithLoggerName("Database"))
	db.SetEnvironmentLevel("production", smplkit.LogLevelError)
	if err := db.Save(ctx); err != nil {
		fatal("Failed to create db logger", err)
	}
	loggers = append(loggers, db)

	return loggers, []*smplkit.LogGroup{group}
}

// teardownDemoLoggers cleans up demo resources.
func teardownDemoLoggers(ctx context.Context, client *smplkit.Client, loggers []*smplkit.Logger, groups []*smplkit.LogGroup) {
	logging := client.Logging()
	for _, l := range loggers {
		if err := logging.Delete(ctx, l.Key); err != nil {
			fmt.Printf("  Warning: failed to delete logger %s: %v\n", l.Key, err)
		}
	}
	for _, g := range groups {
		if err := logging.DeleteGroup(ctx, g.Key); err != nil {
			fmt.Printf("  Warning: failed to delete group %s: %v\n", g.Key, err)
		}
	}
}
