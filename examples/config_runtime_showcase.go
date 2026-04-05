//go:build ignore

// Config Runtime Showcase — end-to-end walkthrough of the Smpl Config
// prescriptive runtime tier in the Go SDK.
//
// Demonstrates the full runtime surface:
//   - Client initialization and config creation (via demo helpers)
//   - Connect / prescriptive access via GetValue
//   - Typed accessors: GetString, GetInt, GetBool
//   - Multi-level inheritance (common -> user_service -> auth_module)
//   - Change listeners (global + key-specific)
//   - Manual refresh after management-plane mutation
//   - Cleanup
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk
//   - A valid smplkit API key, provided via one of:
//       - SMPLKIT_API_KEY environment variable
//       - ~/.smplkit configuration file (see SDK docs)
//   - The smplkit config service running and reachable
//
// Usage:
//
//	go run examples/config_runtime_showcase.go examples/config_runtime_setup.go examples/helpers.go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	ctx := context.Background()

	// ====================================================================
	// 1. SDK INITIALIZATION & CONFIG SETUP
	// ====================================================================
	section("1. SDK Initialization & Config Setup")

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
	step("smplkit.Client initialized (environment=production)")

	demo, err := setupDemoConfigs(ctx, client)
	if err != nil {
		fatal("failed to set up demo configs", err)
	}
	step("Demo configs created (common, user_service, auth_module)")

	// ====================================================================
	// 2. CONNECT AND READ RESOLVED VALUES
	// ====================================================================
	section("2. Connect and Read Resolved Values")

	err = client.Connect(ctx)
	if err != nil {
		fatal("failed to connect", err)
	}
	step("client.Connect() completed — all configs fetched and cached")

	dbConfig, _ := client.Config().GetValue("user_service", "database")
	dbJSON, _ := json.Marshal(dbConfig)
	step(fmt.Sprintf("database = %s", dbJSON))

	retries, _ := client.Config().GetValue("user_service", "max_retries")
	step(fmt.Sprintf("max_retries = %v", retries))

	cacheTTL, _ := client.Config().GetValue("user_service", "cache_ttl_seconds")
	step(fmt.Sprintf("cache_ttl_seconds = %v", cacheTTL))

	pageSize, _ := client.Config().GetValue("user_service", "pagination_default_page_size")
	step(fmt.Sprintf("pagination_default_page_size = %v", pageSize))

	missing, _ := client.Config().GetValue("user_service", "nonexistent_key")
	step(fmt.Sprintf("nonexistent key = %v", missing))

	allValues, _ := client.Config().GetValue("user_service")
	allMap := allValues.(map[string]interface{})
	step(fmt.Sprintf("Total resolved keys for user_service: %d", len(allMap)))

	// ====================================================================
	// 3. TYPED ACCESSORS
	// ====================================================================
	section("3. Typed Accessors")

	appName, _ := client.Config().GetString("user_service", "app_name", "Unknown")
	step(fmt.Sprintf("app_name (string) = %s", appName))

	timeoutMs, _ := client.Config().GetInt("user_service", "request_timeout_ms", 3000)
	step(fmt.Sprintf("request_timeout_ms (number) = %d", timeoutMs))

	signup, _ := client.Config().GetBool("user_service", "enable_signup", true)
	step(fmt.Sprintf("enable_signup (bool) = %v", signup))

	// ====================================================================
	// 4. MULTI-LEVEL INHERITANCE (auth_module)
	// ====================================================================
	section("4. Multi-Level Inheritance (auth_module)")

	sessionTTL, _ := client.Config().GetValue("auth_module", "session_ttl_minutes")
	step(fmt.Sprintf("session_ttl_minutes = %v", sessionTTL))

	mfa, _ := client.Config().GetValue("auth_module", "mfa_enabled")
	step(fmt.Sprintf("mfa_enabled = %v", mfa))

	inheritedApp, _ := client.Config().GetValue("auth_module", "app_name")
	step(fmt.Sprintf("app_name (inherited from common) = %v", inheritedApp))

	// ====================================================================
	// 5a. CHANGE LISTENERS
	// ====================================================================
	section("5a. Change Listeners")

	var changes []*smplkit.ConfigChangeEvent
	client.Config().OnChange(func(evt *smplkit.ConfigChangeEvent) {
		changes = append(changes, evt)
		fmt.Printf("    [CHANGE] %s.%s: %v -> %v\n", evt.ConfigKey, evt.ItemKey, evt.OldValue, evt.NewValue)
	})
	step("Global change listener registered")

	var retriesChanges []*smplkit.ConfigChangeEvent
	client.Config().OnChange(func(evt *smplkit.ConfigChangeEvent) {
		retriesChanges = append(retriesChanges, evt)
	}, smplkit.WithConfigKey("common"), smplkit.WithItemKey("max_retries"))
	step("Key-specific listener registered for common.max_retries")

	// ====================================================================
	// 5b. REFRESH AFTER MANAGEMENT CHANGE
	// ====================================================================
	section("5b. Refresh After Management Change")

	err = demo.Common.SetValue(ctx, "max_retries", 7, "production")
	if err != nil {
		fatal("failed to update max_retries", err)
	}
	step("Updated max_retries to 7 on common (production)")

	err = client.Config().Refresh(ctx)
	if err != nil {
		fatal("manual refresh failed", err)
	}
	step("client.Config().Refresh(ctx) completed")

	newRetries, _ := client.Config().GetValue("user_service", "max_retries")
	step(fmt.Sprintf("max_retries after refresh = %v", newRetries))

	step(fmt.Sprintf("Global changes received: %d", len(changes)))
	step(fmt.Sprintf("Retries-specific changes received: %d", len(retriesChanges)))

	// ====================================================================
	// 6. CLEANUP
	// ====================================================================
	section("6. Cleanup")

	teardownDemoConfigs(ctx, client, demo)
	step("Demo configs deleted and common reset")

	// ====================================================================
	// ALL DONE
	// ====================================================================
	section("ALL DONE")
	fmt.Println("  The Config Runtime showcase completed successfully.")
	fmt.Println()
	fmt.Println("Features exercised:")
	fmt.Println("  [x] Client initialization")
	fmt.Println("  [x] Config hierarchy setup (common, user_service, auth_module)")
	fmt.Println("  [x] Connect and prescriptive access via GetValue")
	fmt.Println("  [x] Typed accessors (GetString, GetInt, GetBool)")
	fmt.Println("  [x] Multi-level inheritance")
	fmt.Println("  [x] Change listeners (global + key-specific)")
	fmt.Println("  [x] Manual refresh after management mutation")
	fmt.Println("  [x] Cleanup")
}
