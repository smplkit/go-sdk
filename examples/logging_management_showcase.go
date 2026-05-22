//go:build ignore

// Demonstrates the smplkit management SDK for Smpl Logging.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//
// Usage:
//
//	make logging_management_showcase
package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func main() {
	ctx := context.Background()

	// create the client (management-only — zero side effects)
	mgmt, err := smplkit.NewManagementClient(smplkit.ManagementConfig{})
	fatalIfErr("create management client", err)
	defer mgmt.Close()

	setupLoggingManagementShowcase(ctx, mgmt)

	// create a parent logger with a default level
	root := mgmt.Loggers().New("showcase")
	root.SetLevel(smplkit.LogLevelInfo, "")
	fatalIfErr("save root", root.Save(ctx))
	fmt.Printf("Created: %s (level=%v)\n", root.ID, *root.Level)

	// child logger with no level (inherits from parent)
	db := mgmt.Loggers().New("showcase.db")
	fatalIfErr("save db", db.Save(ctx))
	fmt.Printf("Created: %s (inherits)\n", db.ID)

	// child logger with explicit level (overrides parent)
	payments := mgmt.Loggers().New("showcase.payments")
	payments.SetLevel(smplkit.LogLevelWarn, "")
	fatalIfErr("save payments", payments.Save(ctx))
	fmt.Printf("Created: %s (level=%v)\n", payments.ID, *payments.Level)

	// override log level for the production environment
	root.SetLevel(smplkit.LogLevelError, "production")
	fatalIfErr("save env overrides", root.Save(ctx))
	fmt.Printf("Set environment overrides: %v\n", root.Environments)

	// clear environment override (inherits from the default level again)
	root.ClearLevel("production")
	fatalIfErr("save after clear", root.Save(ctx))
	fmt.Printf("Cleared production override: %v\n", root.Environments)

	// fetch a logger by id
	fetched, err := mgmt.Loggers().Get(ctx, "showcase")
	fatalIfErr("get showcase", err)
	if fetched.Level == nil || *fetched.Level != smplkit.LogLevelInfo {
		fatalIfErr("fetched.Level", fmt.Errorf("expected INFO, got %v", fetched.Level))
	}

	cleanupLoggingManagementShowcase(ctx, mgmt)
	fmt.Println("Done!")
}
