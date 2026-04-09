//go:build ignore

// Config Management Showcase — end-to-end walkthrough of the Smpl Config
// management API in the Go SDK.
//
// Demonstrates the full management surface:
//   - Client initialization
//   - Update the common config with base values and environment overrides
//   - Create configs (user_service, auth_module as child)
//   - List all configs
//   - Get config by key
//   - Update a config (description + production value)
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
//	go run examples/config_management_showcase.go examples/helpers.go
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
	step("Config sub-client ready")

	// Pre-flight: delete any configs left over from a previous run.
	for _, key := range []string{"auth_module", "user_service"} {
		_ = client.Config().Delete(ctx, key)
	}

	// ====================================================================
	// 2a. UPDATE THE COMMON CONFIG
	// ====================================================================
	section("2a. Update the Common Config")

	common, err := client.Config().Get(ctx, "common")
	if err != nil {
		fatal("failed to fetch common config", err)
	}
	step(fmt.Sprintf("Fetched common config: id=%s, key=%q", common.ID, common.Key))

	common.Description = strPtr("Organization-wide shared configuration")
	common.Items = map[string]interface{}{
		"app_name":                     "Acme SaaS Platform",
		"support_email":                "support@acme.dev",
		"max_retries":                  3,
		"request_timeout_ms":           5000,
		"pagination_default_page_size": 25,
	}
	err = common.Save(ctx)
	if err != nil {
		fatal("failed to update common config", err)
	}
	step("Common config base values set")

	// ====================================================================
	// 2b. ENVIRONMENT OVERRIDES
	// ====================================================================
	section("2b. Environment Overrides")

	common.Environments = map[string]map[string]interface{}{
		"production": {
			"max_retries":        5,
			"request_timeout_ms": 10000,
		},
		"staging": {
			"max_retries": 2,
		},
	}
	err = common.Save(ctx)
	if err != nil {
		fatal("failed to set common environment overrides", err)
	}
	step("Common config production overrides set")
	step("Common config staging overrides set")

	// ====================================================================
	// 3a. CREATE USER SERVICE CONFIG
	// ====================================================================
	section("3a. Create User Service Config")

	userService := client.Config().New("user_service",
		smplkit.WithConfigName("User Service"),
		smplkit.WithConfigDescription("Configuration for the user microservice."),
		smplkit.WithConfigItems(map[string]interface{}{
			"database": map[string]interface{}{
				"host":      "localhost",
				"port":      5432,
				"name":      "users_dev",
				"pool_size": 5,
			},
			"cache_ttl_seconds":            300,
			"enable_signup":                true,
			"pagination_default_page_size": 50,
		}),
		smplkit.WithConfigEnvironments(map[string]map[string]interface{}{
			"production": {
				"database": map[string]interface{}{
					"host":      "prod-users-rds.internal.acme.dev",
					"name":      "users_prod",
					"pool_size": 20,
				},
				"cache_ttl_seconds": 600,
				"enable_signup":     false,
			},
		}),
	)
	err = userService.Save(ctx)
	if err != nil {
		fatal("failed to create user_service config", err)
	}
	step(fmt.Sprintf("Created user_service config: id=%s", userService.ID))

	// ====================================================================
	// 3b. CREATE AUTH MODULE CONFIG (child of user_service)
	// ====================================================================
	section("3b. Create Auth Module Config (child of User Service)")

	authModule := client.Config().New("auth_module",
		smplkit.WithConfigName("Auth Module"),
		smplkit.WithConfigDescription("Authentication module within the user service."),
		smplkit.WithConfigParent(userService.ID),
		smplkit.WithConfigItems(map[string]interface{}{
			"session_ttl_minutes": 60,
			"mfa_enabled":        false,
		}),
		smplkit.WithConfigEnvironments(map[string]map[string]interface{}{
			"production": {
				"session_ttl_minutes": 30,
				"mfa_enabled":        true,
			},
		}),
	)
	err = authModule.Save(ctx)
	if err != nil {
		_ = client.Config().Delete(ctx, "user_service")
		fatal("failed to create auth_module config", err)
	}
	step(fmt.Sprintf("Created auth_module config: id=%s (parent=%s)", authModule.ID, userService.ID))

	// ====================================================================
	// 4a. LIST ALL CONFIGS
	// ====================================================================
	section("4a. List All Configs")

	configs, err := client.Config().List(ctx)
	if err != nil {
		fatal("failed to list configs", err)
	}
	step(fmt.Sprintf("List: %d configs found", len(configs)))
	for _, cfg := range configs {
		parent := "(root)"
		if cfg.Parent != nil {
			parent = fmt.Sprintf("(parent: %s)", *cfg.Parent)
		}
		step(fmt.Sprintf("  %s %s", cfg.Key, parent))
	}

	// ====================================================================
	// 4b. GET CONFIG BY KEY
	// ====================================================================
	section("4b. Get Config by Key")

	fetched, err := client.Config().Get(ctx, "user_service")
	if err != nil {
		fatal("failed to get user_service by key", err)
	}
	step(fmt.Sprintf("Get(key=%q): id=%s name=%q", fetched.Key, fetched.ID, fetched.Name))
	if fetched.Description != nil {
		step(fmt.Sprintf("  description=%q", *fetched.Description))
	}

	// ====================================================================
	// 5. UPDATE A CONFIG
	// ====================================================================
	section("5. Update a Config (user_service)")

	userService.Description = strPtr("Configuration for the user microservice (updated).")
	if userService.Environments == nil {
		userService.Environments = map[string]map[string]interface{}{}
	}
	if userService.Environments["production"] == nil {
		userService.Environments["production"] = map[string]interface{}{}
	}
	userService.Environments["production"]["cache_ttl_seconds"] = 900
	err = userService.Save(ctx)
	if err != nil {
		fatal("failed to update user_service", err)
	}
	step("Updated user_service description")
	step("Updated cache_ttl_seconds to 900 in production")

	// ====================================================================
	// 6. CLEANUP
	// ====================================================================
	section("6. Cleanup")

	if err := client.Config().Delete(ctx, "auth_module"); err != nil {
		fmt.Printf("  Warning: failed to delete auth_module: %v\n", err)
	} else {
		step(fmt.Sprintf("Deleted auth_module (%s)", authModule.ID))
	}

	if err := client.Config().Delete(ctx, "user_service"); err != nil {
		fmt.Printf("  Warning: failed to delete user_service: %v\n", err)
	} else {
		step(fmt.Sprintf("Deleted user_service (%s)", userService.ID))
	}

	common.Description = strPtr("")
	common.Items = map[string]interface{}{}
	common.Environments = map[string]map[string]interface{}{}
	err = common.Save(ctx)
	if err != nil {
		fmt.Printf("  Warning: failed to reset common config: %v\n", err)
	} else {
		step("Common config reset to empty")
	}

	// ====================================================================
	// ALL DONE
	// ====================================================================
	section("ALL DONE")
	fmt.Println("  The Config Management showcase completed successfully.")
	fmt.Println()
	fmt.Println("Features exercised:")
	fmt.Println("  [x] Client initialization")
	fmt.Println("  [x] Update common config with base values")
	fmt.Println("  [x] Environment overrides (production, staging)")
	fmt.Println("  [x] Create config (user_service)")
	fmt.Println("  [x] Create child config (auth_module)")
	fmt.Println("  [x] List all configs")
	fmt.Println("  [x] Get config by key")
	fmt.Println("  [x] Update config (description + production value)")
	fmt.Println("  [x] Delete configs")
	fmt.Println("  [x] Reset common config")
}
