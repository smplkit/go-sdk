//go:build ignore

// Logging Management Showcase
//
// Demonstrates the smplkit Go SDK's logging management API:
//   - Logger and LogGroup management (create, read, update, delete)
//   - Level management (base + environment-specific)
//   - Group assignment and hierarchy
//
// Prerequisites:
//   - Go 1.21+
//   - SMPLKIT_API_KEY set (or ~/.smplkit config file)
//
// Usage:
//
//	go run examples/logging_management_showcase.go examples/helpers.go
package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	ctx := context.Background()

	// ── Section 1: SDK Initialization ─────────────────────────────────
	section("1. SDK Initialization")

	step("Creating smplkit client")
	client, err := smplkit.NewClient(smplkit.Config{Environment: "production", Service: "showcase-service"})
	if err != nil {
		fatal("NewClient failed", err)
	}
	defer client.Close()
	fmt.Println("  Client created successfully")

	logging := client.Logging()

	// Pre-flight cleanup.
	step("Cleaning up leftover resources from previous runs")
	for _, key := range []string{"showcase.api", "showcase.worker", "showcase.db"} {
		_ = logging.Management().Delete(ctx, key)
	}
	for _, key := range []string{"showcase-infra", "showcase-backend"} {
		_ = logging.Management().DeleteGroup(ctx, key)
	}
	fmt.Println("  Cleanup complete")

	// ── Section 2: Create Log Groups ─────────────────────────────────
	section("2. Create Log Groups")

	step("Creating parent group: showcase-infra")
	infraGroup := logging.Management().NewGroup("showcase-infra", smplkit.WithLogGroupName("Infrastructure"))
	infraGroup.SetLevel(smplkit.LogLevelWarn)
	if err := infraGroup.Save(ctx); err != nil {
		fatal("Failed to create infra group", err)
	}
	fmt.Printf("  Created: id=%s, name=%s, level=%s\n", infraGroup.ID, infraGroup.Name, *infraGroup.Level)

	step("Creating group: showcase-backend")
	backendGroup := logging.Management().NewGroup("showcase-backend", smplkit.WithLogGroupName("Backend Services"))
	backendGroup.SetLevel(smplkit.LogLevelInfo)
	if err := backendGroup.Save(ctx); err != nil {
		fatal("Failed to create backend group", err)
	}
	fmt.Printf("  Created: id=%s, name=%s, level=%s\n",
		backendGroup.ID, backendGroup.Name, *backendGroup.Level)

	// ── Section 3: Create Loggers ────────────────────────────────────
	section("3. Create Loggers")

	step("Creating logger: showcase.api (in backend group)")
	apiLogger := logging.Management().New("showcase.api", smplkit.WithLoggerName("API Service"))
	apiLogger.Group = &backendGroup.ID
	apiLogger.SetLevel(smplkit.LogLevelDebug)
	if err := apiLogger.Save(ctx); err != nil {
		fatal("Failed to create API logger", err)
	}
	fmt.Printf("  Created: id=%s, level=%s, group=%s\n", apiLogger.ID, *apiLogger.Level, *apiLogger.Group)

	step("Creating logger: showcase.worker (no group, environment-specific levels)")
	workerLogger := logging.Management().New("showcase.worker", smplkit.WithLoggerName("Worker"))
	workerLogger.SetLevel(smplkit.LogLevelInfo)
	workerLogger.SetEnvironmentLevel("production", smplkit.LogLevelWarn)
	workerLogger.SetEnvironmentLevel("staging", smplkit.LogLevelDebug)
	if err := workerLogger.Save(ctx); err != nil {
		fatal("Failed to create worker logger", err)
	}
	fmt.Printf("  Created: id=%s, level=%s\n", workerLogger.ID, *workerLogger.Level)

	step("Creating logger: showcase.db (unmanaged — level inherited from group/platform)")
	dbLogger := logging.Management().New("showcase.db", smplkit.WithLoggerName("Database"), smplkit.WithLoggerManaged(false))
	if err := dbLogger.Save(ctx); err != nil {
		fatal("Failed to create DB logger", err)
	}
	levelStr := "<inherit>"
	if dbLogger.Level != nil {
		levelStr = string(*dbLogger.Level)
	}
	fmt.Printf("  Created: id=%s, level=%s, managed=%v\n", dbLogger.ID, levelStr, dbLogger.Managed)

	// ── Section 4: List and Get ──────────────────────────────────────
	section("4. List and Get")

	step("Listing all loggers")
	loggers, err := logging.Management().List(ctx)
	if err != nil {
		fatal("Failed to list loggers", err)
	}
	fmt.Printf("  Found %d loggers\n", len(loggers))
	for _, l := range loggers {
		levelStr := "<inherit>"
		if l.Level != nil {
			levelStr = string(*l.Level)
		}
		fmt.Printf("    - %s (level=%s, managed=%v)\n", l.ID, levelStr, l.Managed)
	}

	step("Getting logger by key: showcase.api")
	fetched, err := logging.Management().Get(ctx, "showcase.api")
	if err != nil {
		fatal("Failed to get logger", err)
	}
	fmt.Printf("  Got: id=%s, name=%s\n", fetched.ID, fetched.Name)

	step("Listing all log groups")
	groups, err := logging.Management().ListGroups(ctx)
	if err != nil {
		fatal("Failed to list groups", err)
	}
	fmt.Printf("  Found %d groups\n", len(groups))
	for _, g := range groups {
		levelStr := "<inherit>"
		if g.Level != nil {
			levelStr = string(*g.Level)
		}
		fmt.Printf("    - %s (level=%s)\n", g.ID, levelStr)
	}

	step("Getting group by key: showcase-backend")
	fetchedGroup, err := logging.Management().GetGroup(ctx, "showcase-backend")
	if err != nil {
		fatal("Failed to get group", err)
	}
	fmt.Printf("  Got: id=%s, name=%s\n", fetchedGroup.ID, fetchedGroup.Name)

	// ── Section 5: Update Loggers and Groups ─────────────────────────
	section("5. Update Loggers and Groups")

	step("Updating API logger: change level to TRACE")
	apiLogger.SetLevel(smplkit.LogLevelTrace)
	if err := apiLogger.Save(ctx); err != nil {
		fatal("Failed to update API logger", err)
	}
	fmt.Printf("  Updated: id=%s, level=%s\n", apiLogger.ID, *apiLogger.Level)

	step("Updating infra group: add environment level for production")
	infraGroup.SetEnvironmentLevel("production", smplkit.LogLevelError)
	if err := infraGroup.Save(ctx); err != nil {
		fatal("Failed to update infra group", err)
	}
	fmt.Printf("  Updated: id=%s\n", infraGroup.ID)

	step("Clearing worker logger environment levels")
	workerLogger.ClearAllEnvironmentLevels()
	if err := workerLogger.Save(ctx); err != nil {
		fatal("Failed to update worker logger", err)
	}
	fmt.Printf("  Cleared all environment levels for: %s\n", workerLogger.ID)

	// ── Section 6: Cleanup ───────────────────────────────────────────
	section("6. Cleanup")

	step("Deleting loggers")
	for _, key := range []string{"showcase.api", "showcase.worker", "showcase.db"} {
		if err := logging.Management().Delete(ctx, key); err != nil {
			fmt.Printf("  Warning: failed to delete %s: %v\n", key, err)
		} else {
			fmt.Printf("  Deleted: %s\n", key)
		}
	}

	step("Deleting groups (child first)")
	for _, key := range []string{"showcase-backend", "showcase-infra"} {
		if err := logging.Management().DeleteGroup(ctx, key); err != nil {
			fmt.Printf("  Warning: failed to delete %s: %v\n", key, err)
		} else {
			fmt.Printf("  Deleted: %s\n", key)
		}
	}

	// ── Summary ──────────────────────────────────────────────────────
	section("Logging Management Showcase Complete")
	fmt.Println("  Features exercised:")
	fmt.Println("  [x] New() + Save() — create loggers")
	fmt.Println("  [x] NewGroup() + Save() — create log groups")
	fmt.Println("  [x] SetLevel / ClearLevel — base level management")
	fmt.Println("  [x] SetEnvironmentLevel / ClearAllEnvironmentLevels — env levels")
	fmt.Println("  [x] Group assignment (flat groups — no nesting in v1)")
	fmt.Println("  [x] Get(key) / List() — retrieve loggers")
	fmt.Println("  [x] GetGroup(key) / ListGroups() — retrieve groups")
	fmt.Println("  [x] Delete(key) / DeleteGroup(key) — cleanup")
	fmt.Println("  [x] WithLoggerManaged — unmanaged loggers")
}
