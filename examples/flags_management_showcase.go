//go:build ignore

// Flags Management Showcase — end-to-end walkthrough of the Smpl Flags
// management API in the Go SDK.
//
// Demonstrates the full management surface:
//   - Client initialization
//   - Flag CRUD: create (boolean, string, numeric), get, list, delete
//   - Typed flag values and defaults
//   - Environment configuration with rules
//   - Flag.Update() for bulk environment/rule changes
//   - Flag.AddRule() for appending a single rule
//   - Context type management: create, update, list, delete
//   - Cleanup
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk
//   - A valid smplkit API key, provided via one of:
//       - SMPLKIT_API_KEY environment variable
//       - ~/.smplkit configuration file (see SDK docs)
//   - The smplkit flags service running and reachable
//
// Usage:
//
//	go run examples/flags_management_showcase.go examples/flags_demo_setup.go
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

	// The SmplClient constructor resolves three required parameters:
	//
	//   apiKey       — passed as "" here; resolved automatically from the
	//                  SMPLKIT_API_KEY environment variable or the
	//                  ~/.smplkit configuration file.
	//
	//   environment  — the target environment. Falls back to
	//                  SMPLKIT_ENVIRONMENT if empty.
	//
	//   service      — identifies this SDK instance. Can also be resolved
	//                  from SMPLKIT_SERVICE if not passed via WithService().
	//
	// To pass the API key explicitly, pass it as the first arg:
	//
	//   client, err := smplkit.NewClient("sk_api_...", "production", smplkit.WithService("showcase-service"))
	//
	client, err := smplkit.NewClient("", "production", smplkit.WithService("showcase-service"))
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

	step(fmt.Sprintf("checkout-v2  id=%s  type=%s  default=%v", checkoutFlag.ID, checkoutFlag.Type, checkoutFlag.Default))
	step(fmt.Sprintf("banner-color id=%s  type=%s  default=%v", bannerFlag.ID, bannerFlag.Type, bannerFlag.Default))
	step(fmt.Sprintf("max-retries  id=%s  type=%s  default=%v", retryFlag.ID, retryFlag.Type, retryFlag.Default))

	// ====================================================================
	// 3. GET & LIST FLAGS
	// ====================================================================
	section("3. Get & List Flags")

	// Get by ID.
	fetched, err := flags.Get(ctx, checkoutFlag.ID)
	if err != nil {
		fatal("failed to get flag", err)
	}
	step(fmt.Sprintf("Get(id=%s): key=%q name=%q", fetched.ID, fetched.Key, fetched.Name))

	// List all.
	allFlags, err := flags.List(ctx)
	if err != nil {
		fatal("failed to list flags", err)
	}
	step(fmt.Sprintf("List: %d flags found", len(allFlags)))
	for _, f := range allFlags {
		desc := "<nil>"
		if f.Description != nil {
			desc = *f.Description
		}
		step(fmt.Sprintf("  %-15s  type=%-8s  default=%-6v  desc=%q", f.Key, f.Type, f.Default, desc))
	}

	// ====================================================================
	// 4. UPDATE A FLAG — add a new environment rule
	// ====================================================================
	section("4. Update a Flag (checkout-v2)")

	err = checkoutFlag.Update(ctx, smplkit.UpdateFlagParams{
		Environments: map[string]interface{}{
			"staging": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					smplkit.NewRule("Enable for enterprise users in US region").
						When("user.plan", "==", "enterprise").
						When("account.region", "==", "us").
						Serve(true).
						Build(),
					smplkit.NewRule("Enable for beta testers").
						When("user.beta_tester", "==", true).
						Serve(true).
						Build(),
					smplkit.NewRule("Enable for technology companies").
						When("account.industry", "==", "technology").
						Serve(true).
						Build(),
				},
			},
			"production": map[string]interface{}{
				"enabled": false,
				"default": false,
				"rules":   []interface{}{},
			},
		},
	})
	if err != nil {
		fatal("failed to update checkout-v2", err)
	}
	step("Added 'technology companies' rule to checkout-v2 staging")

	// ====================================================================
	// 5. ADD RULE — append a single rule to an existing environment
	// ====================================================================
	section("5. AddRule (banner-color)")

	err = bannerFlag.AddRule(ctx, smplkit.NewRule("Red for healthcare").
		Environment("staging").
		When("account.industry", "==", "healthcare").
		Serve("red").
		Build())
	if err != nil {
		fatal("failed to add rule to banner-color", err)
	}
	step("Appended 'healthcare' rule to banner-color staging")

	// Verify: re-fetch and count rules.
	bannerRefreshed, err := flags.Get(ctx, bannerFlag.ID)
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
	userCT, err := flags.CreateContextType(ctx, "user", "User")
	if err != nil {
		step(fmt.Sprintf("CreateContextType('user') returned error (may already exist): %v", err))
	} else {
		step(fmt.Sprintf("Created context type: id=%s key=%q name=%q", userCT.ID, userCT.Key, userCT.Name))
	}

	accountCT, err := flags.CreateContextType(ctx, "account", "Account")
	if err != nil {
		step(fmt.Sprintf("CreateContextType('account') returned error (may already exist): %v", err))
	} else {
		step(fmt.Sprintf("Created context type: id=%s key=%q name=%q", accountCT.ID, accountCT.Key, accountCT.Name))
	}

	// List context types.
	ctList, err := flags.ListContextTypes(ctx)
	if err != nil {
		fatal("failed to list context types", err)
	}
	step(fmt.Sprintf("ListContextTypes: %d types", len(ctList)))
	for _, ct := range ctList {
		step(fmt.Sprintf("  id=%s key=%q name=%q", ct.ID, ct.Key, ct.Name))
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
	fmt.Println("  [x] Create flags (boolean, string, numeric)")
	fmt.Println("  [x] Get flag by ID")
	fmt.Println("  [x] List all flags")
	fmt.Println("  [x] Update flag (environments + rules)")
	fmt.Println("  [x] AddRule (append single rule)")
	fmt.Println("  [x] Context type CRUD (create, list)")
	fmt.Println("  [x] Delete flags")
	fmt.Println("  [x] Delete context types")
}
