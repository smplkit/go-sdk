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

	// or NewClient for the full one-client surface
	logging, err := smplkit.NewLoggingClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-service",
	})
	fatalIfErr("create logging client", err)

	setupLoggingManagementShowcase(ctx, logging)

	// create a parent logger with a default level
	root := logging.Loggers().New("showcase")
	root.SetLevel(smplkit.LogLevelInfo, "")
	fatalIfErr("save root", root.Save(ctx))
	fmt.Printf("Created: %s (level=%v)\n", root.ID, *root.Level)
	if root.Level == nil || *root.Level != smplkit.LogLevelInfo {
		fatalIfErr("assertion", fmt.Errorf("expected root level INFO, got %v", root.Level))
	}

	// child logger with no level (inherits from parent)
	db := logging.Loggers().New("showcase.db")
	fatalIfErr("save db", db.Save(ctx))
	fmt.Printf("Created: %s (inherits)\n", db.ID)
	if db.Level != nil {
		fatalIfErr("assertion", fmt.Errorf("expected db level nil, got %v", *db.Level))
	}

	// child logger with explicit level (overrides parent)
	payments := logging.Loggers().New("showcase.payments")
	payments.SetLevel(smplkit.LogLevelWarn, "")
	fatalIfErr("save payments", payments.Save(ctx))
	fmt.Printf("Created: %s (level=%v)\n", payments.ID, *payments.Level)
	if payments.Level == nil || *payments.Level != smplkit.LogLevelWarn {
		fatalIfErr("assertion", fmt.Errorf("expected payments level WARN, got %v", payments.Level))
	}

	// override log level for the production environment
	root.SetLevel(smplkit.LogLevelError, "production")
	fatalIfErr("save env overrides", root.Save(ctx))
	fmt.Printf("Set environment overrides: %v\n", root.Environments)
	if prod, ok := root.Environments["production"].(map[string]interface{}); !ok || prod["level"] != string(smplkit.LogLevelError) {
		fatalIfErr("assertion", fmt.Errorf("expected production override ERROR, got %v", root.Environments["production"]))
	}

	// clear environment override (inherits from the default level again)
	root.ClearLevel("production")
	fatalIfErr("save after clear", root.Save(ctx))
	fmt.Printf("Cleared production override: %v\n", root.Environments)
	if _, present := root.Environments["production"]; present {
		fatalIfErr("assertion", fmt.Errorf("expected production override cleared, got %v", root.Environments))
	}

	// get a logger
	fetched, err := logging.Loggers().Get(ctx, "showcase")
	fatalIfErr("get showcase", err)
	if fetched.Level == nil || *fetched.Level != smplkit.LogLevelInfo {
		fatalIfErr("assertion", fmt.Errorf("expected fetched level INFO, got %v", fetched.Level))
	}

	cleanupLoggingManagementShowcase(ctx, logging)
	fmt.Println("Done!")
}
