//go:build ignore

// Config Showcase — end-to-end walkthrough of the Smpl Config Go SDK.
//
// Demonstrates the prescriptive SDK surface:
//   - Client initialization (`smplkit.NewClient`)
//   - Management-plane CRUD: create, update, list, delete
//   - Environment-specific overrides (SetValues, SetValue)
//   - Prescriptive value resolution via `client.Connect()` + `client.Config().GetValue()`
//   - Typed accessors: GetString, GetInt, GetBool
//   - Manual refresh: `client.Config().Refresh(ctx)`
//   - Change listeners: `client.Config().OnChange()`
//
// Prerequisites:
//   - A valid smplkit API key exported as SMPLKIT_API_KEY
//   - An environment exported as SMPLKIT_ENVIRONMENT (or passed directly)
//
// Usage:
//
//	export SMPLKIT_API_KEY="sk_api_..."
//	export SMPLKIT_ENVIRONMENT="production"
//	go run examples/config_showcase.go examples/helpers.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	ctx := context.Background()

	apiKey := os.Getenv("SMPLKIT_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: Set the SMPLKIT_API_KEY environment variable before running.")
		fmt.Fprintln(os.Stderr, `  export SMPLKIT_API_KEY="sk_api_..."`)
		os.Exit(1)
	}

	environment := os.Getenv("SMPLKIT_ENVIRONMENT")
	if environment == "" {
		environment = "production"
	}

	// ====================================================================
	// 1. SDK INITIALIZATION
	// ====================================================================
	section("1. SDK Initialization")

	client, err := smplkit.NewClient(apiKey, environment)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	step(fmt.Sprintf("smplkit.Client initialized (environment=%s)", environment))

	// ====================================================================
	// Pre-flight: delete any configs left over from a previous run.
	// ====================================================================
	for _, key := range []string{"auth_module", "user_service"} {
		if cfg, err := client.Config().GetByKey(ctx, key); err == nil {
			_ = client.Config().Delete(ctx, cfg.ID)
		}
	}

	// ====================================================================
	// 2. MANAGEMENT PLANE — Set up the configuration hierarchy
	// ====================================================================

	section("2a. Update the Common Config")

	common, err := client.Config().GetByKey(ctx, "common")
	if err != nil {
		fatal("failed to fetch common config", err)
	}
	step(fmt.Sprintf("Fetched common config: id=%s, key=%q", common.ID, common.Key))

	err = common.Update(ctx, smplkit.UpdateConfigParams{
		Description: strPtr("Organization-wide shared configuration"),
		Items: map[string]interface{}{
			"app_name":                     "Acme SaaS Platform",
			"support_email":                "support@acme.dev",
			"max_retries":                  3,
			"request_timeout_ms":           5000,
			"pagination_default_page_size": 25,
		},
	})
	if err != nil {
		fatal("failed to update common config", err)
	}
	step("Common config base values set")

	err = common.SetValues(ctx, map[string]interface{}{
		"max_retries":        5,
		"request_timeout_ms": 10000,
	}, "production")
	if err != nil {
		fatal("failed to set common production overrides", err)
	}
	step("Common config production overrides set")

	err = common.SetValues(ctx, map[string]interface{}{
		"max_retries": 2,
	}, "staging")
	if err != nil {
		fatal("failed to set common staging overrides", err)
	}
	step("Common config staging overrides set")

	section("2b. Create the User Service Config")

	userService, err := client.Config().Create(ctx, smplkit.CreateConfigParams{
		Name:        "User Service",
		Key:         strPtr("user_service"),
		Description: strPtr("Configuration for the user microservice."),
		Items: map[string]interface{}{
			"database": map[string]interface{}{
				"host":      "localhost",
				"port":      5432,
				"name":      "users_dev",
				"pool_size": 5,
			},
			"cache_ttl_seconds":            300,
			"enable_signup":                true,
			"pagination_default_page_size": 50,
		},
	})
	if err != nil {
		fatal("failed to create user_service config", err)
	}
	step(fmt.Sprintf("Created user_service config: id=%s", userService.ID))

	err = userService.SetValues(ctx, map[string]interface{}{
		"database": map[string]interface{}{
			"host":      "prod-users-rds.internal.acme.dev",
			"name":      "users_prod",
			"pool_size": 20,
		},
		"cache_ttl_seconds": 600,
	}, "production")
	if err != nil {
		fatal("failed to set user_service production overrides", err)
	}
	step("User service production overrides set")

	err = userService.SetValue(ctx, "enable_signup", false, "production")
	if err != nil {
		fatal("failed to set enable_signup in production", err)
	}
	step("Disabled signup in production")

	section("2c. Create the Auth Module Config (child of User Service)")

	authModule, err := client.Config().Create(ctx, smplkit.CreateConfigParams{
		Name:        "Auth Module",
		Key:         strPtr("auth_module"),
		Description: strPtr("Authentication module within the user service."),
		Parent:      &userService.ID,
		Items: map[string]interface{}{
			"session_ttl_minutes": 60,
			"mfa_enabled":        false,
		},
	})
	if err != nil {
		_ = client.Config().Delete(ctx, userService.ID)
		fatal("failed to create auth_module config", err)
	}
	step(fmt.Sprintf("Created auth_module config: id=%s", authModule.ID))

	err = authModule.SetValues(ctx, map[string]interface{}{
		"session_ttl_minutes": 30,
		"mfa_enabled":         true,
	}, "production")
	if err != nil {
		fatal("failed to set auth_module production overrides", err)
	}
	step("Auth module production overrides set")

	section("2d. List All Configs")

	configs, err := client.Config().List(ctx)
	if err != nil {
		fatal("failed to list configs", err)
	}
	for _, cfg := range configs {
		parent := "(root)"
		if cfg.Parent != nil {
			parent = fmt.Sprintf("(parent: %s)", *cfg.Parent)
		}
		step(fmt.Sprintf("%s %s", cfg.Key, parent))
	}

	// ====================================================================
	// 3. PRESCRIPTIVE PLANE — Connect and read resolved values
	// ====================================================================

	section("3a. Connect and Read Resolved Values")

	err = client.Connect(ctx)
	if err != nil {
		fatal("failed to connect", err)
	}
	step("client.Connect() completed — all configs fetched and cached")

	// Prescriptive access: GetValue(configKey, itemKey)
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

	// ------------------------------------------------------------------
	// 3b. Typed accessors
	// ------------------------------------------------------------------
	section("3b. Typed Accessors")

	appName, _ := client.Config().GetString("user_service", "app_name", "Unknown")
	step(fmt.Sprintf("app_name (string) = %s", appName))

	timeoutMs, _ := client.Config().GetInt("user_service", "request_timeout_ms", 3000)
	step(fmt.Sprintf("request_timeout_ms (number) = %d", timeoutMs))

	signup, _ := client.Config().GetBool("user_service", "enable_signup", true)
	step(fmt.Sprintf("enable_signup (bool) = %v", signup))

	// ------------------------------------------------------------------
	// 3c. Multi-level inheritance
	// ------------------------------------------------------------------
	section("3c. Multi-Level Inheritance (auth_module)")

	sessionTTL, _ := client.Config().GetValue("auth_module", "session_ttl_minutes")
	step(fmt.Sprintf("session_ttl_minutes = %v", sessionTTL))

	mfa, _ := client.Config().GetValue("auth_module", "mfa_enabled")
	step(fmt.Sprintf("mfa_enabled = %v", mfa))

	inheritedApp, _ := client.Config().GetValue("auth_module", "app_name")
	step(fmt.Sprintf("app_name (inherited from common) = %v", inheritedApp))

	// ====================================================================
	// 4. CHANGE LISTENERS AND REFRESH
	// ====================================================================

	section("4a. Change Listeners")

	var changes []*smplkit.ConfigChangeEvent
	client.Config().OnChange(func(evt *smplkit.ConfigChangeEvent) {
		changes = append(changes, evt)
		fmt.Printf("    [CHANGE] %s.%s: %v → %v\n", evt.ConfigKey, evt.ItemKey, evt.OldValue, evt.NewValue)
	})
	step("Global change listener registered")

	var retriesChanges []*smplkit.ConfigChangeEvent
	client.Config().OnChange(func(evt *smplkit.ConfigChangeEvent) {
		retriesChanges = append(retriesChanges, evt)
	}, smplkit.WithConfigKey("common"), smplkit.WithItemKey("max_retries"))
	step("Key-specific listener registered for common.max_retries")

	section("4b. Refresh After Management Change")

	err = common.SetValue(ctx, "max_retries", 7, "production")
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
	// 5. CLEANUP
	// ====================================================================
	section("5. Cleanup")

	if err := client.Config().Delete(ctx, authModule.ID); err != nil {
		fmt.Printf("  Warning: failed to delete auth_module: %v\n", err)
	} else {
		step(fmt.Sprintf("Deleted auth_module (%s)", authModule.ID))
	}

	if err := client.Config().Delete(ctx, userService.ID); err != nil {
		fmt.Printf("  Warning: failed to delete user_service: %v\n", err)
	} else {
		step(fmt.Sprintf("Deleted user_service (%s)", userService.ID))
	}

	err = common.Update(ctx, smplkit.UpdateConfigParams{
		Description:  strPtr(""),
		Items:        map[string]interface{}{},
		Environments: map[string]map[string]interface{}{},
	})
	if err != nil {
		fmt.Printf("  Warning: failed to reset common config: %v\n", err)
	} else {
		step("Common config reset to empty")
	}

	section("ALL DONE")
	fmt.Println("  The Config SDK showcase completed successfully.")
}
