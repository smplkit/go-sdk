//go:build ignore

// Flags Runtime Showcase — end-to-end walkthrough of the Smpl Flags
// runtime in the Go SDK.
//
// Demonstrates the full runtime surface:
//   - Client initialization and flag creation (via demo helpers)
//   - Typed flag handles: BooleanFlag, StringFlag, NumberFlag
//   - Context providers
//   - Explicit context evaluation
//   - Evaluation statistics
//   - Real-time updates and change listeners
//   - OnChangeKey listener
//   - Manual refresh
//   - Context registration
//   - Tier 1 stateless Evaluate
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
//	go run examples/flags_runtime_showcase.go examples/flags_runtime_setup.go
package main

import (
	"context"
	"fmt"
	"time"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	ctx := context.Background()

	// ====================================================================
	// 1. SDK INITIALIZATION & FLAG SETUP
	// ====================================================================
	section("1. SDK Initialization & Flag Setup")

	// The SmplClient constructor takes three required positional parameters:
	//
	//   apiKey       — passed as "" here; resolved automatically from the
	//                  SMPLKIT_API_KEY environment variable or the
	//                  ~/.smplkit configuration file.
	//
	//   environment  — the target environment. Falls back to
	//                  SMPLKIT_ENVIRONMENT if empty.
	//
	//   service      — identifies this SDK instance. Falls back to
	//                  SMPLKIT_SERVICE if empty.
	//
	// To pass the API key explicitly, pass it as the first arg:
	//
	//   client, err := smplkit.NewClient("sk_api_...", "staging", "showcase-service")
	//
	client, err := smplkit.NewClient("", "staging", "showcase-service")
	if err != nil {
		fatal("failed to create client", err)
	}
	step("smplkit.Client initialized")

	flags := client.Flags()
	demoFlags, err := setupDemoFlags(ctx, client)
	if err != nil {
		fatal("failed to set up demo flags", err)
	}
	defer teardownDemoFlags(ctx, client, demoFlags)
	step("Demo flags created (checkout-v2, banner-color, max-retries)")

	// ====================================================================
	// 2. TYPED FLAG HANDLES
	// ====================================================================
	section("2. Typed Flag Handles")

	checkoutHandle := flags.BooleanFlag("checkout-v2", false)
	step("BooleanFlag handle: checkout-v2 (default=false)")

	bannerHandle := flags.StringFlag("banner-color", "red")
	step("StringFlag handle: banner-color (default=\"red\")")

	retryHandle := flags.NumberFlag("max-retries", 3)
	step("NumberFlag handle: max-retries (default=3)")

	// Before context is set, handles return defaults.
	step(fmt.Sprintf("checkout-v2 initial: %v (expect false)", checkoutHandle.Get(ctx)))
	step(fmt.Sprintf("banner-color initial: %q (expect \"red\")", bannerHandle.Get(ctx)))
	step(fmt.Sprintf("max-retries initial: %.0f (expect 3)", retryHandle.Get(ctx)))

	// ====================================================================
	// 3. CONTEXT PROVIDER
	// ====================================================================
	section("3. Context Provider")

	flags.SetContextProvider(func(goCtx context.Context) []smplkit.Context {
		return []smplkit.Context{
			smplkit.NewContext("user", "user-42", map[string]interface{}{
				"plan":        "enterprise",
				"beta_tester": true,
			}, smplkit.WithName("Alice Johnson")),
			smplkit.NewContext("account", "acme-corp", map[string]interface{}{
				"region":         "us",
				"industry":       "technology",
				"employee_count": 250,
			}, smplkit.WithName("Acme Corporation")),
		}
	})
	step("Context provider registered (enterprise user at Acme)")

	// ====================================================================
	// 4. EVALUATE FLAGS (provider context)
	// ====================================================================
	section("4. Evaluate Flags (via provider)")

	checkoutVal := checkoutHandle.Get(ctx)
	step(fmt.Sprintf("checkout-v2 = %v (expect true — enterprise + us region)", checkoutVal))

	bannerVal := bannerHandle.Get(ctx)
	step(fmt.Sprintf("banner-color = %q (expect \"blue\" — enterprise plan)", bannerVal))

	retryVal := retryHandle.Get(ctx)
	step(fmt.Sprintf("max-retries = %.0f (expect 5 — employee_count > 100)", retryVal))

	// ====================================================================
	// 5. EXPLICIT CONTEXT OVERRIDE
	// ====================================================================
	section("5. Explicit Context Override")

	// Override with a non-enterprise user — should get defaults/fallbacks.
	basicUser := smplkit.NewContext("user", "user-99", map[string]interface{}{
		"plan":        "free",
		"beta_tester": false,
	})
	smallCo := smplkit.NewContext("account", "small-co", map[string]interface{}{
		"region":         "eu",
		"industry":       "healthcare",
		"employee_count": 5,
	})

	checkoutExplicit := checkoutHandle.Get(ctx, basicUser, smallCo)
	step(fmt.Sprintf("checkout-v2 (free/eu user) = %v (expect false — no rule matches)", checkoutExplicit))

	bannerExplicit := bannerHandle.Get(ctx, basicUser, smallCo)
	step(fmt.Sprintf("banner-color (healthcare) = %q (expect fallback from env default)", bannerExplicit))

	retryExplicit := retryHandle.Get(ctx, basicUser, smallCo)
	step(fmt.Sprintf("max-retries (5 employees) = %.0f (expect fallback — count <= 100)", retryExplicit))

	// ====================================================================
	// 6. RESOLUTION CACHE
	// ====================================================================
	section("6. Resolution Cache Stats")

	statsBefore := flags.Stats()
	step(fmt.Sprintf("Cache hits: %d, misses: %d", statsBefore.CacheHits, statsBefore.CacheMisses))

	// Re-evaluate same flags — should hit cache.
	for i := 0; i < 50; i++ {
		checkoutHandle.Get(ctx)
		bannerHandle.Get(ctx)
		retryHandle.Get(ctx)
	}

	statsAfter := flags.Stats()
	step(fmt.Sprintf("After 150 re-evaluations — hits: %d, misses: %d", statsAfter.CacheHits, statsAfter.CacheMisses))
	step(fmt.Sprintf("New cache hits: %d (expect 150)", statsAfter.CacheHits-statsBefore.CacheHits))

	// ====================================================================
	// 7. CHANGE LISTENERS
	// ====================================================================
	section("7. Change Listeners")

	globalChanges := 0
	flags.OnChange(func(evt *smplkit.FlagChangeEvent) {
		globalChanges++
		fmt.Printf("    [GLOBAL CHANGE] id=%s source=%s\n", evt.ID, evt.Source)
	})
	step("Global change listener registered")

	checkoutChanges := 0
	checkoutHandle.OnChange(func(evt *smplkit.FlagChangeEvent) {
		checkoutChanges++
		fmt.Printf("    [checkout-v2 CHANGE via handle] source=%s\n", evt.Source)
	})
	step("Flag-specific listener registered for checkout-v2 (via handle)")

	keyChanges := 0
	flags.OnChangeKey("checkout-v2", func(evt *smplkit.FlagChangeEvent) {
		keyChanges++
		fmt.Printf("    [checkout-v2 CHANGE via key] source=%s\n", evt.Source)
	})
	step("Key-specific listener registered for checkout-v2 (via OnChangeKey)")

	// Trigger via manual refresh.
	err = flags.Refresh(ctx)
	if err != nil {
		fatal("refresh failed", err)
	}
	step(fmt.Sprintf("After Refresh: global=%d, handle=%d, key=%d", globalChanges, checkoutChanges, keyChanges))

	// ====================================================================
	// 8. CONTEXT REGISTRATION
	// ====================================================================
	section("8. Context Registration")

	flags.Register(ctx,
		smplkit.NewContext("device", "device-abc", map[string]interface{}{
			"os":        "iOS",
			"version":   "17.4",
			"app_build": "2024.03.1",
		}),
	)
	step("Registered device context for batch upload")

	flags.FlushContexts(ctx)
	step("Flushed pending contexts to server")

	// ====================================================================
	// 9. TIER 1 — STATELESS EVALUATE
	// ====================================================================
	section("9. Tier 1 — Stateless Evaluate")

	tier1Contexts := []smplkit.Context{
		smplkit.NewContext("user", "user-77", map[string]interface{}{
			"plan": "enterprise",
		}),
		smplkit.NewContext("account", "big-corp", map[string]interface{}{
			"region": "us",
		}),
	}

	result := flags.Evaluate(ctx, "checkout-v2", "staging", tier1Contexts)
	step(fmt.Sprintf("Evaluate('checkout-v2', 'staging', enterprise/us) = %v (expect true)", result))

	result2 := flags.Evaluate(ctx, "checkout-v2", "production", tier1Contexts)
	step(fmt.Sprintf("Evaluate('checkout-v2', 'production', ...) = %v (expect false — production disabled)", result2))

	// ====================================================================
	// 10. WEBSOCKET STATUS
	// ====================================================================
	section("10. WebSocket Status")

	step(fmt.Sprintf("Connection status: %s", flags.ConnectionStatus()))

	// Give a moment for any pending WS activity.
	time.Sleep(500 * time.Millisecond)

	// ====================================================================
	// 11. DISCONNECT
	// ====================================================================
	section("11. Disconnect")

	flags.Disconnect(ctx)
	step(fmt.Sprintf("Disconnected — status: %s", flags.ConnectionStatus()))

	// After disconnecting, handles return defaults again.
	step(fmt.Sprintf("checkout-v2 after disconnect: %v (expect false)", checkoutHandle.Get(ctx)))
	step(fmt.Sprintf("banner-color after disconnect: %q (expect \"red\")", bannerHandle.Get(ctx)))

	// ====================================================================
	// DONE
	// ====================================================================
	section("ALL DONE")
	fmt.Println("  The Flags Runtime showcase completed successfully.")
	fmt.Println()
	fmt.Println("Features exercised:")
	fmt.Println("  [x] Client initialization")
	fmt.Println("  [x] Typed flag handles (Boolean, String, Number)")
	fmt.Println("  [x] Context provider")
	fmt.Println("  [x] Evaluate flags via provider context")
	fmt.Println("  [x] Explicit context override")
	fmt.Println("  [x] Resolution cache (hits/misses)")
	fmt.Println("  [x] Change listeners (global + handle + OnChangeKey)")
	fmt.Println("  [x] Manual refresh")
	fmt.Println("  [x] Context registration and flush")
	fmt.Println("  [x] Tier 1 stateless Evaluate")
	fmt.Println("  [x] WebSocket connection status")
	fmt.Println("  [x] Disconnect lifecycle")
	fmt.Println("  [x] Cleanup (deferred)")
}
