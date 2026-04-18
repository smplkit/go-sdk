//go:build ignore

// Flags Management Showcase — end-to-end walkthrough of the Smpl Flags
// management API in the Go SDK.
//
// Demonstrates the full management surface:
//   - Client initialization
//   - Flag management: create (boolean, string, numeric), get, list, delete
//   - Typed flag values and defaults
//   - Environment configuration with convenience methods
//   - Flag.Save() for persisting changes
//   - Flag.AddRule() for appending a single rule
//   - Context type management: create, update, list, delete
//   - Cleanup
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//   - The smplkit flags service running and reachable
//
// Usage:
//
//	go run examples/flags_management_showcase.go examples/flags_runtime_setup.go
package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	ctx := context.Background()

	// ====================================================================
	// 1. SDK INITIALIZATION
	// ====================================================================
	section("1. SDK Initialization")

	// The Config struct resolves required parameters from multiple sources:
	// defaults -> config file (~/.smplkit) -> env vars -> struct fields.
	//
	// To pass the API key explicitly:
	//
	//   client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_api_...", Environment: "production", Service: "showcase-service"})
	//
	client, err := smplkit.NewClient(smplkit.Config{Environment: "production", Service: "showcase-service"})
	if err != nil {
		fatal("failed to create client", err)
	}
	step("smplkit.Client initialized")

	flags := client.Flags()
	step("Flags sub-client ready")

	// ====================================================================
	// 2. CREATE FLAGS — using the demo setup helper
	// ====================================================================
	section("2. Create Demo Flags")

	demoFlags, err := setupDemoFlags(ctx, client)
	if err != nil {
		fatal("failed to set up demo flags", err)
	}
	checkoutFlag := demoFlags[0]
	bannerFlag := demoFlags[1]
	retryFlag := demoFlags[2]

	// banner-color is a constrained flag — its values parameter defines a
	// closed set. The Console UI shows dropdowns for value selection.
	step(fmt.Sprintf("checkout-v2  id=%s  type=%s  default=%v", checkoutFlag.ID, checkoutFlag.Type, checkoutFlag.Default))
	step(fmt.Sprintf("banner-color id=%s  type=%s  default=%v", bannerFlag.ID, bannerFlag.Type, bannerFlag.Default))

	// max-retries is an unconstrained flag — no predefined values.
	// Any number is valid as a default or rule serve-value. This is
	// useful for tunables like thresholds, retry counts, and timeouts
	// where the value space is open-ended.
	// Omitting the values option creates an unconstrained flag.
	step(fmt.Sprintf("max-retries  id=%s  type=%s  default=%v", retryFlag.ID, retryFlag.Type, retryFlag.Default))

	// ====================================================================
	// 3. GET & LIST FLAGS
	// ====================================================================
	section("3. Get & List Flags")

	// Get by key.
	fetched, err := flags.Management().Get(ctx, "checkout-v2")
	if err != nil {
		fatal("failed to get flag", err)
	}
	step(fmt.Sprintf("Get(id=%q): name=%q type=%s", fetched.ID, fetched.Name, fetched.Type))

	// List all.
	allFlags, err := flags.Management().List(ctx)
	if err != nil {
		fatal("failed to list flags", err)
	}
	step(fmt.Sprintf("List: %d flags found", len(allFlags)))
	for _, f := range allFlags {
		desc := "<nil>"
		if f.Description != nil {
			desc = *f.Description
		}
		step(fmt.Sprintf("  %-15s  type=%-8s  default=%-6v  desc=%q", f.ID, f.Type, f.Default, desc))
	}

	// ====================================================================
	// 4. UPDATE A FLAG — add a new rule via local mutation + Save
	// ====================================================================
	section("4. Update a Flag (checkout-v2)")

	checkoutFlag.AddRule(smplkit.NewRule("Enable for technology companies").
		Environment("staging").
		When("account.industry", "==", "technology").
		Serve(true).
		Build())
	if err := checkoutFlag.Save(ctx); err != nil {
		fatal("failed to update checkout-v2", err)
	}
	step("Added 'technology companies' rule to checkout-v2 staging")

	// ====================================================================
	// 5. ADD RULE — append a single rule (local mutation) then Save
	// ====================================================================
	section("5. AddRule (banner-color)")

	bannerFlag.AddRule(smplkit.NewRule("Red for healthcare").
		Environment("staging").
		When("account.industry", "==", "healthcare").
		Serve("red").
		Build())
	if err := bannerFlag.Save(ctx); err != nil {
		fatal("failed to save banner-color after adding rule", err)
	}
	step("Appended 'healthcare' rule to banner-color staging")

	// Verify: re-fetch and count rules.
	bannerRefreshed, err := flags.Management().Get(ctx, "banner-color")
	if err != nil {
		fatal("failed to re-fetch banner-color", err)
	}
	if stagingEnv, ok := bannerRefreshed.Environments["staging"].(map[string]interface{}); ok {
		if rules, ok := stagingEnv["rules"].([]interface{}); ok {
			step(fmt.Sprintf("banner-color staging now has %d rules", len(rules)))
		}
	}

	// ====================================================================
	// 6. CONTEXT TYPE MANAGEMENT
	// ====================================================================
	section("6. Context Type Management")

	// Create context types.
	userCT, err := flags.Management().CreateContextType(ctx, "user", "User")
	if err != nil {
		step(fmt.Sprintf("CreateContextType('user') returned error (may already exist): %v", err))
	} else {
		step(fmt.Sprintf("Created context type: id=%s name=%q", userCT.ID, userCT.Name))
	}

	accountCT, err := flags.Management().CreateContextType(ctx, "account", "Account")
	if err != nil {
		step(fmt.Sprintf("CreateContextType('account') returned error (may already exist): %v", err))
	} else {
		step(fmt.Sprintf("Created context type: id=%s name=%q", accountCT.ID, accountCT.Name))
	}

	// List context types.
	ctList, err := flags.Management().ListContextTypes(ctx)
	if err != nil {
		fatal("failed to list context types", err)
	}
	step(fmt.Sprintf("ListContextTypes: %d types", len(ctList)))
	for _, ct := range ctList {
		step(fmt.Sprintf("  id=%s name=%q", ct.ID, ct.Name))
	}

	// ====================================================================
	// 7. CLEANUP
	// ====================================================================
	section("7. Cleanup")

	teardownDemoFlags(ctx, client, demoFlags)
	step("Demo flags and context types deleted")

	// ====================================================================
	// DONE
	// ====================================================================
	section("ALL DONE")
	fmt.Println("  The Flags Management showcase completed successfully.")
	fmt.Println()
	fmt.Println("Features exercised:")
	fmt.Println("  [x] Client initialization")
	fmt.Println("  [x] Flags sub-client")
	fmt.Println("  [x] Create flags (boolean, string, numeric) via factory + Save")
	fmt.Println("  [x] Get flag by key")
	fmt.Println("  [x] List all flags")
	fmt.Println("  [x] Update flag + Save")
	fmt.Println("  [x] AddRule + Save")
	fmt.Println("  [x] Context type management (create, list)")
	fmt.Println("  [x] Delete flags by key")
	fmt.Println("  [x] Delete context types")
}
