// Config Showcase — end-to-end walkthrough of the Smpl Config Go SDK.
//
// This script exercises the SDK's management-plane operations against a live
// smplkit environment: create, read, list, and delete configs with inheritance.
//
// Prerequisites:
//   - A valid smplkit API key exported as SMPLKIT_API_KEY
//   - At least one config in your account (every account has "common" by default)
//
// Usage:
//
//	export SMPLKIT_API_KEY="sk_api_..."
//	go run examples/config_showcase.go
package main

import (
	"context"
	"fmt"
	"os"

	smplkit "github.com/smplkit/go-sdk"
)

// section prints a prominent section header.
func section(n int, title string) {
	fmt.Printf("\n%s\n", "════════════════════════════════════════════════════════════════")
	fmt.Printf("  Section %d: %s\n", n, title)
	fmt.Printf("%s\n\n", "════════════════════════════════════════════════════════════════")
}

// step prints a numbered step within a section.
func step(id, title string) {
	fmt.Printf("--- %s: %s ---\n", id, title)
}

// fatal prints an error and exits.
func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
	os.Exit(1)
}

// strPtr returns a pointer to a string.
func strPtr(s string) *string { return &s }

func main() {
	ctx := context.Background()

	// ================================================================
	// Section 1: SDK Initialization
	// ================================================================
	section(1, "SDK Initialization")

	apiKey := os.Getenv("SMPLKIT_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: SMPLKIT_API_KEY environment variable is required")
		fmt.Fprintln(os.Stderr, "  export SMPLKIT_API_KEY=\"sk_api_...\"")
		os.Exit(1)
	}

	client := smplkit.NewClient(apiKey)
	fmt.Println("SmplClient initialized successfully")

	// ================================================================
	// Section 2: Management Plane — Config hierarchy
	// ================================================================
	section(2, "Management Plane — Set up config hierarchy")

	// 2a. Fetch the built-in "common" config
	step("2a", "Fetch the built-in 'common' config")
	common, err := client.Config().GetByKey(ctx, "common")
	if err != nil {
		fatal("failed to fetch common config", err)
	}
	fmt.Printf("Fetched config: key=%q  id=%s\n", common.Key, common.ID)
	fmt.Printf("  Values:       %v\n", common.Values)
	fmt.Printf("  Environments: %v\n", common.Environments)

	// --- SKIPPED: Update/SetValues on common config ---
	// The Go SDK does not yet have Update, SetValues, or SetValue methods,
	// nor does CreateConfigParams support Environments. These operations
	// (setting base values, production overrides, and staging overrides on
	// common) are exercised in the Python showcase but cannot be performed
	// here until the SDK exposes update capabilities.
	fmt.Println("\n  [SKIPPED] Update common config with base values and environment overrides")
	fmt.Println("           (SDK does not yet implement Update/SetValues)")

	// 2b. Create "user_service" config
	step("2b", "Create 'user_service' config")
	userService, err := client.Config().Create(ctx, smplkit.CreateConfigParams{
		Name: "User Service",
		Key:  strPtr("user_service"),
		Values: map[string]interface{}{
			"service_name": "user-service",
			"max_retries":  3,
			"database": map[string]interface{}{
				"host":             "localhost",
				"port":             5432,
				"name":             "users_dev",
				"max_connections":  10,
				"connection_timeout": 30,
			},
		},
	})
	if err != nil {
		fatal("failed to create user_service config", err)
	}
	fmt.Printf("Created config: key=%q  id=%s\n", userService.Key, userService.ID)
	parentStr := "<none>"
	if userService.Parent != nil {
		parentStr = *userService.Parent
	}
	fmt.Printf("  Parent: %s (inherits from common by default)\n", parentStr)
	fmt.Printf("  Values: %v\n", userService.Values)

	// --- SKIPPED: Environment overrides and SetValue on user_service ---
	fmt.Println("\n  [SKIPPED] Set production/staging/development environment overrides")
	fmt.Println("           (SDK does not yet implement Update/SetValues/SetValue)")

	// 2c. Create "auth_module" config (child of user_service)
	step("2c", "Create 'auth_module' config (child of user_service)")
	authModule, err := client.Config().Create(ctx, smplkit.CreateConfigParams{
		Name:   "Auth Module",
		Key:    strPtr("auth_module"),
		Parent: &userService.ID,
		Values: map[string]interface{}{
			"token_expiry_minutes": 60,
			"algorithm":           "RS256",
			"issuer":              "acme-auth",
		},
	})
	if err != nil {
		// Clean up user_service before exiting.
		_ = client.Config().Delete(ctx, userService.ID)
		fatal("failed to create auth_module config", err)
	}
	fmt.Printf("Created config: key=%q  id=%s\n", authModule.Key, authModule.ID)
	fmt.Printf("  Parent: %s (inherits from user_service → common)\n", *authModule.Parent)
	fmt.Printf("  Values: %v\n", authModule.Values)

	// --- SKIPPED: Environment overrides on auth_module ---
	fmt.Println("\n  [SKIPPED] Set production environment overrides on auth_module")
	fmt.Println("           (SDK does not yet implement Update/SetValues)")

	// 2d. List all configs
	step("2d", "List all configs")
	configs, err := client.Config().List(ctx)
	if err != nil {
		fmt.Printf("  Warning: failed to list configs: %v\n", err)
	} else {
		for _, cfg := range configs {
			parent := "<none>"
			if cfg.Parent != nil {
				parent = *cfg.Parent
			}
			fmt.Printf("  %-20s (id=%s, parent=%s)\n", cfg.Key, cfg.ID, parent)
		}
	}

	// ================================================================
	// Section 3: Runtime Plane — Resolve configuration
	// ================================================================
	section(3, "Runtime Plane — Resolve configuration")

	// --- SKIPPED: Runtime plane ---
	// The Go SDK does not yet implement runtime operations:
	//   - Connect(environment) — eagerly fetch, resolve inheritance + env
	//     overrides, populate cache, open WebSocket
	//   - Get(key) / GetString(key) / GetInt(key) / GetBool(key)
	//   - Exists(key) / GetAll()
	//   - Stats() — cache statistics
	//   - Refresh() / Close() / ConnectionStatus()
	//
	// These features are exercised in the Python showcase and will be
	// added to this showcase once the Go SDK exposes them.
	fmt.Println("[SKIPPED] Runtime plane not yet implemented in Go SDK")
	fmt.Println("  Skipped steps:")
	fmt.Println("    3a. Connect to config in an environment")
	fmt.Println("    3b. Read resolved values (Get, typed accessors, Exists)")
	fmt.Println("    3c. Verify local caching (Stats)")
	fmt.Println("    3d. Get all resolved values (GetAll)")
	fmt.Println("    3e. Multi-level inheritance resolution")

	// ================================================================
	// Section 4: Real-Time Updates via WebSocket
	// ================================================================
	section(4, "Real-Time Updates via WebSocket")

	// --- SKIPPED: Real-time updates ---
	// The Go SDK does not yet implement:
	//   - OnChange(callback) — global change listener
	//   - OnChange(key, callback) — key-specific change listener
	//   - WebSocket connection for push updates
	fmt.Println("[SKIPPED] Real-time updates not yet implemented in Go SDK")
	fmt.Println("  Skipped steps:")
	fmt.Println("    4a. Register change listeners (OnChange)")
	fmt.Println("    4b. Simulate config change and verify cache update")
	fmt.Println("    4c. Connection lifecycle (ConnectionStatus, Refresh)")

	// ================================================================
	// Section 5: Environment Comparison
	// ================================================================
	section(5, "Environment Comparison")

	// --- SKIPPED: Environment comparison ---
	// Requires runtime Connect() to resolve values per environment.
	fmt.Println("[SKIPPED] Environment comparison requires runtime Connect()")

	// ================================================================
	// Section 6: Cleanup
	// ================================================================
	section(6, "Cleanup")

	// Delete auth_module first (child), then user_service (parent).
	step("6a", "Delete auth_module")
	if err := client.Config().Delete(ctx, authModule.ID); err != nil {
		fmt.Printf("  Warning: failed to delete auth_module: %v\n", err)
	} else {
		fmt.Printf("  Deleted config: key=%q  id=%s\n", authModule.Key, authModule.ID)
	}

	step("6b", "Delete user_service")
	if err := client.Config().Delete(ctx, userService.ID); err != nil {
		fmt.Printf("  Warning: failed to delete user_service: %v\n", err)
	} else {
		fmt.Printf("  Deleted config: key=%q  id=%s\n", userService.Key, userService.ID)
	}

	// --- SKIPPED: Reset common to empty values ---
	// Requires Update/SetValues which are not yet implemented.
	fmt.Println("\n  [SKIPPED] Reset common config to empty values")
	fmt.Println("           (SDK does not yet implement Update/SetValues)")

	// ================================================================
	// Section 7: Done!
	// ================================================================
	section(7, "Complete")
	fmt.Println("ALL DONE — Config Showcase finished successfully!")
	fmt.Println()
	fmt.Println("Features exercised:")
	fmt.Println("  [x] Client initialization")
	fmt.Println("  [x] Fetch config by key (GetByKey)")
	fmt.Println("  [x] Create config with values")
	fmt.Println("  [x] Create config with parent (inheritance)")
	fmt.Println("  [x] List all configs")
	fmt.Println("  [x] Delete configs")
	fmt.Println()
	fmt.Println("Features pending SDK implementation:")
	fmt.Println("  [ ] Update/SetValues/SetValue")
	fmt.Println("  [ ] Environment overrides")
	fmt.Println("  [ ] Runtime Connect + value resolution")
	fmt.Println("  [ ] Typed accessors (GetString, GetInt, GetBool)")
	fmt.Println("  [ ] Real-time updates (WebSocket + OnChange)")
	fmt.Println("  [ ] Stats, Refresh, ConnectionStatus")
}
