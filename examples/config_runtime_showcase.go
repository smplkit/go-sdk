//go:build ignore

// Config Runtime Showcase — end-to-end walkthrough of the Smpl Config
// runtime in the Go SDK.
//
// Demonstrates the full runtime surface:
//   - Client initialization and config creation (via demo helpers)
//   - Get / GetInto for reading config values
//   - Subscribe for live config updates
//   - Multi-level inheritance (common -> user_service -> auth_module)
//   - Change listeners (global + key-specific)
//   - Manual refresh after server-side changes
//   - Cleanup
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
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

// UserServiceConfig is an example struct for ResolveInto demonstration.
type UserServiceConfig struct {
	Database                  map[string]interface{} `json:"database"`
	CacheTTLSeconds           int                    `json:"cache_ttl_seconds"`
	EnableSignup              bool                   `json:"enable_signup"`
	PaginationDefaultPageSize int                    `json:"pagination_default_page_size"`
	AppName                   string                 `json:"app_name"`
	SupportEmail              string                 `json:"support_email"`
	MaxRetries                int                    `json:"max_retries"`
	RequestTimeoutMs          int                    `json:"request_timeout_ms"`
}

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
	//                  from SMPLKIT_SERVICE if not passed as a positional arg.
	//
	// To pass the API key explicitly, pass it as the first arg:
	//
	//   client, err := smplkit.NewClient("sk_api_...", "production", "showcase-service")
	//
	client, err := smplkit.NewClient("", "production", "showcase-service")
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
	// 2. GET — READ RESOLVED VALUES AS A MAP
	// ====================================================================
	section("2. Get — Read Resolved Values as a Map")

	resolved, err := client.Config().Get(ctx, "user_service")
	if err != nil {
		fatal("failed to resolve user_service", err)
	}
	step(fmt.Sprintf("Total resolved keys for user_service: %d", len(resolved)))

	dbConfig := resolved["database"]
	dbJSON, _ := json.Marshal(dbConfig)
	step(fmt.Sprintf("database = %s", dbJSON))

	step(fmt.Sprintf("max_retries = %v", resolved["max_retries"]))
	step(fmt.Sprintf("cache_ttl_seconds = %v", resolved["cache_ttl_seconds"]))
	step(fmt.Sprintf("pagination_default_page_size = %v", resolved["pagination_default_page_size"]))

	// ====================================================================
	// 3. GET INTO — UNMARSHAL INTO A STRUCT
	// ====================================================================
	section("3. GetInto — Unmarshal Into a Struct")

	var usCfg UserServiceConfig
	err = client.Config().GetInto(ctx, "user_service", &usCfg)
	if err != nil {
		fatal("failed to resolve into struct", err)
	}
	step(fmt.Sprintf("app_name = %s", usCfg.AppName))
	step(fmt.Sprintf("request_timeout_ms = %d", usCfg.RequestTimeoutMs))
	step(fmt.Sprintf("enable_signup = %v", usCfg.EnableSignup))
	step(fmt.Sprintf("cache_ttl_seconds = %d", usCfg.CacheTTLSeconds))

	// ====================================================================
	// 4. MULTI-LEVEL INHERITANCE (auth_module)
	// ====================================================================
	section("4. Multi-Level Inheritance (auth_module)")

	authResolved, err := client.Config().Get(ctx, "auth_module")
	if err != nil {
		fatal("failed to resolve auth_module", err)
	}

	step(fmt.Sprintf("session_ttl_minutes = %v", authResolved["session_ttl_minutes"]))
	step(fmt.Sprintf("mfa_enabled = %v", authResolved["mfa_enabled"]))
	step(fmt.Sprintf("app_name (inherited from common) = %v", authResolved["app_name"]))

	// ====================================================================
	// 5. SUBSCRIBE — LIVE CONFIG UPDATES
	// ====================================================================
	section("5. Subscribe — Live Config Updates")

	live, err := client.Config().Subscribe(ctx, "user_service")
	if err != nil {
		fatal("failed to subscribe to user_service", err)
	}
	step("Subscribed to user_service — LiveConfig active")

	snapshot := live.Value()
	step(fmt.Sprintf("Initial snapshot keys: %d", len(snapshot)))
	step(fmt.Sprintf("max_retries from live = %v", snapshot["max_retries"]))

	// ====================================================================
	// 6a. CHANGE LISTENERS
	// ====================================================================
	section("6a. Change Listeners")

	var changes []*smplkit.ConfigChangeEvent
	client.Config().OnChange(func(evt *smplkit.ConfigChangeEvent) {
		changes = append(changes, evt)
		fmt.Printf("    [CHANGE] %s.%s: %v -> %v\n", evt.ConfigID, evt.ItemKey, evt.OldValue, evt.NewValue)
	})
	step("Global change listener registered")

	var retriesChanges []*smplkit.ConfigChangeEvent
	client.Config().OnChange(func(evt *smplkit.ConfigChangeEvent) {
		retriesChanges = append(retriesChanges, evt)
	}, smplkit.WithConfigID("common"), smplkit.WithItemKey("max_retries"))
	step("Key-specific listener registered for common.max_retries")

	// ====================================================================
	// 6b. REFRESH AFTER MANAGEMENT CHANGE
	// ====================================================================
	section("6b. Refresh After Management Change")

	if demo.Common.Environments == nil {
		demo.Common.Environments = map[string]map[string]interface{}{}
	}
	if demo.Common.Environments["production"] == nil {
		demo.Common.Environments["production"] = map[string]interface{}{}
	}
	demo.Common.Environments["production"]["max_retries"] = 7
	err = demo.Common.Save(ctx)
	if err != nil {
		fatal("failed to update max_retries", err)
	}
	step("Updated max_retries to 7 on common (production)")

	err = client.Config().Refresh(ctx)
	if err != nil {
		fatal("manual refresh failed", err)
	}
	step("client.Config().Refresh(ctx) completed")

	refreshed, err := client.Config().Get(ctx, "user_service")
	if err != nil {
		fatal("failed to resolve user_service after refresh", err)
	}
	step(fmt.Sprintf("max_retries after refresh = %v", refreshed["max_retries"]))

	step(fmt.Sprintf("Global changes received: %d", len(changes)))
	step(fmt.Sprintf("Retries-specific changes received: %d", len(retriesChanges)))

	// ====================================================================
	// 7. CLEANUP
	// ====================================================================
	section("7. Cleanup")

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
	fmt.Println("  [x] Get — read resolved values as a map")
	fmt.Println("  [x] GetInto — unmarshal into a struct")
	fmt.Println("  [x] Multi-level inheritance")
	fmt.Println("  [x] Subscribe — live config updates")
	fmt.Println("  [x] Change listeners (global + key-specific)")
	fmt.Println("  [x] Manual refresh after management mutation")
	fmt.Println("  [x] Cleanup")
}
