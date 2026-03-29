// Config Showcase — end-to-end walkthrough of the Smpl Config Go SDK.
//
// Demonstrates the full SDK surface:
//   - Client initialization
//   - Management-plane CRUD: create, update, list, delete
//   - Environment-specific overrides (SetValues, SetValue)
//   - Multi-level inheritance
//   - Runtime value resolution: Connect, Get, typed accessors, Exists
//   - Real-time updates via WebSocket and change listeners
//   - Manual refresh and cache diagnostics
//   - Environment comparison
//   - Cleanup
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
	"time"

	smplkit "github.com/smplkit/go-sdk"
)

func section(title string) {
	fmt.Printf("\n%s\n", "════════════════════════════════════════════════════════════════")
	fmt.Printf("  %s\n", title)
	fmt.Printf("%s\n\n", "════════════════════════════════════════════════════════════════")
}

func step(description string) {
	fmt.Printf("  → %s\n", description)
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
	os.Exit(1)
}

func strPtr(s string) *string { return &s }

func main() {
	ctx := context.Background()

	apiKey := os.Getenv("SMPLKIT_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: Set the SMPLKIT_API_KEY environment variable before running.")
		fmt.Fprintln(os.Stderr, `  export SMPLKIT_API_KEY="sk_api_..."`)
		os.Exit(1)
	}

	// ====================================================================
	// 1. SDK INITIALIZATION
	// ====================================================================
	section("1. SDK Initialization")

	// You can also omit the API key entirely — the SDK will resolve it from
	// the SMPLKIT_API_KEY environment variable or ~/.smplkit config file.
	// See the SDK README for details.
	client, err := smplkit.NewClient(apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	step("smplkit.Client initialized")

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

	// ------------------------------------------------------------------
	// 2a. Update the built-in common config
	// ------------------------------------------------------------------
	section("2a. Update the Common Config")

	// Every account has a 'common' config at provisioning.
	common, err := client.Config().GetByKey(ctx, "common")
	if err != nil {
		fatal("failed to fetch common config", err)
	}
	step(fmt.Sprintf("Fetched common config: id=%s, key=%q", common.ID, common.Key))

	// Set base values — these apply to all environments by default.
	err = common.Update(ctx, smplkit.UpdateConfigParams{
		Description: strPtr("Organization-wide shared configuration"),
		Values: map[string]interface{}{
			"app_name":                       "Acme SaaS Platform",
			"support_email":                  "support@acme.dev",
			"max_retries":                    3,
			"request_timeout_ms":             5000,
			"pagination_default_page_size":   25,
			"credentials": map[string]interface{}{
				"oauth_provider": "https://auth.acme.dev",
				"client_id":      "acme_default_client",
				"scopes":         []interface{}{"read"},
			},
			"feature_flags": map[string]interface{}{
				"provider":                 "smplkit",
				"endpoint":                 "https://flags.smplkit.com",
				"refresh_interval_seconds": 30,
			},
		},
	})
	if err != nil {
		fatal("failed to update common config", err)
	}
	step("Common config base values set")

	// Production overrides flow to every config that inherits from common.
	err = common.SetValues(ctx, map[string]interface{}{
		"max_retries":        5,
		"request_timeout_ms": 10000,
		"credentials": map[string]interface{}{
			"scopes": []interface{}{"read", "write", "admin"},
		},
	}, "production")
	if err != nil {
		fatal("failed to set common production overrides", err)
	}
	step("Common config production overrides set")

	err = common.SetValues(ctx, map[string]interface{}{
		"max_retries": 2,
		"credentials": map[string]interface{}{
			"scopes": []interface{}{"read", "write"},
		},
	}, "staging")
	if err != nil {
		fatal("failed to set common staging overrides", err)
	}
	step("Common config staging overrides set")

	// ------------------------------------------------------------------
	// 2b. Create user_service config
	// ------------------------------------------------------------------
	section("2b. Create the User Service Config")

	userService, err := client.Config().Create(ctx, smplkit.CreateConfigParams{
		Name:        "User Service",
		Key:         strPtr("user_service"),
		Description: strPtr("Configuration for the user microservice and its dependencies."),
		Values: map[string]interface{}{
			"database": map[string]interface{}{
				"host":      "localhost",
				"port":      5432,
				"name":      "users_dev",
				"pool_size": 5,
				"ssl_mode":  "prefer",
			},
			"cache_ttl_seconds":            300,
			"enable_signup":                true,
			"allowed_email_domains":        []interface{}{"acme.dev", "acme.com"},
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
			"ssl_mode":  "require",
		},
		"cache_ttl_seconds": 600,
	}, "production")
	if err != nil {
		fatal("failed to set user_service production overrides", err)
	}
	step("User service production overrides set")

	err = userService.SetValues(ctx, map[string]interface{}{
		"database": map[string]interface{}{
			"host":      "staging-users-rds.internal.acme.dev",
			"name":      "users_staging",
			"pool_size": 10,
		},
	}, "staging")
	if err != nil {
		fatal("failed to set user_service staging overrides", err)
	}
	step("User service staging overrides set")

	err = userService.SetValue(ctx, "enable_signup", false, "production")
	if err != nil {
		fatal("failed to set enable_signup in production", err)
	}
	step("User service: enable_signup=false in production")

	// ------------------------------------------------------------------
	// 2c. Create auth_module config (child of user_service)
	// ------------------------------------------------------------------
	section("2c. Create the Auth Module Config")

	authModule, err := client.Config().Create(ctx, smplkit.CreateConfigParams{
		Name:        "Auth Module",
		Key:         strPtr("auth_module"),
		Description: strPtr("Authentication and authorization module config."),
		Parent:      &userService.ID,
		Values: map[string]interface{}{
			"token_expiry_minutes": 60,
			"algorithm":            "RS256",
			"issuer":               "acme-auth",
			"session_ttl_minutes":  15,
			"mfa_enabled":          false,
		},
	})
	if err != nil {
		// Clean up user_service before exiting.
		_ = client.Config().Delete(ctx, userService.ID)
		fatal("failed to create auth_module config", err)
	}
	step(fmt.Sprintf("Created auth_module config: id=%s, parent=%s", authModule.ID, *authModule.Parent))

	err = authModule.SetValues(ctx, map[string]interface{}{
		"session_ttl_minutes": 30,
		"mfa_enabled":         true,
	}, "production")
	if err != nil {
		_, _ = client.Config().List(ctx) // noop — just to illustrate cleanup pattern
		_ = client.Config().Delete(ctx, authModule.ID)
		_ = client.Config().Delete(ctx, userService.ID)
		fatal("failed to set auth_module production overrides", err)
	}
	step("Auth module production overrides set")

	// ------------------------------------------------------------------
	// 2d. List all configs
	// ------------------------------------------------------------------
	section("2d. List All Configs")

	configs, err := client.Config().List(ctx)
	if err != nil {
		fatal("failed to list configs", err)
	}
	step(fmt.Sprintf("Total configs: %d", len(configs)))
	for _, cfg := range configs {
		parent := "<none>"
		if cfg.Parent != nil {
			parent = *cfg.Parent
		}
		fmt.Printf("    %-20s  id=%-40s  parent=%s\n", cfg.Key, cfg.ID, parent)
	}

	// ====================================================================
	// 3. RUNTIME PLANE — Resolve configuration
	// ====================================================================
	section("3. Runtime Plane — Resolve Configuration")

	// ------------------------------------------------------------------
	// 3a. Connect to user_service in production
	// ------------------------------------------------------------------
	section("3a. Connect to user_service (production)")

	runtime, err := userService.Connect(ctx, "production")
	if err != nil {
		fatal("failed to connect to user_service runtime", err)
	}
	defer runtime.Close()
	step("Runtime connected; cache populated from parent chain (user_service → common)")

	// ------------------------------------------------------------------
	// 3b. Read resolved values
	// ------------------------------------------------------------------
	section("3b. Read Resolved Values")

	// Generic Get with default.
	maxRetries := runtime.GetInt("max_retries", 0)
	step(fmt.Sprintf("max_retries = %d  (production override from common)", maxRetries))

	timeout := runtime.GetInt("request_timeout_ms", 0)
	step(fmt.Sprintf("request_timeout_ms = %d", timeout))

	appName := runtime.GetString("app_name")
	step(fmt.Sprintf("app_name = %q  (inherited from common)", appName))

	enableSignup := runtime.GetBool("enable_signup")
	step(fmt.Sprintf("enable_signup = %v  (user_service production override)", enableSignup))

	cacheTTL := runtime.GetInt("cache_ttl_seconds")
	step(fmt.Sprintf("cache_ttl_seconds = %d  (user_service production override)", cacheTTL))

	// Exists check.
	step(fmt.Sprintf("Exists('database') = %v", runtime.Exists("database")))
	step(fmt.Sprintf("Exists('nonexistent_key') = %v", runtime.Exists("nonexistent_key")))

	// Get with default for missing key.
	notThere := runtime.Get("missing_key", "default_value")
	step(fmt.Sprintf("Get('missing_key', 'default_value') = %q", notThere))

	// ------------------------------------------------------------------
	// 3c. Verify local caching (Stats)
	// ------------------------------------------------------------------
	section("3c. Verify Local Caching (Stats)")

	stats := runtime.Stats()
	step(fmt.Sprintf("Network fetches so far: %d", stats.FetchCount))
	step(fmt.Sprintf("Last fetch at: %s", stats.LastFetchAt.Format(time.RFC3339)))

	// Read many values — none should trigger a network fetch.
	for i := 0; i < 100; i++ {
		runtime.Get("max_retries")
		runtime.Get("database")
		runtime.Get("app_name")
	}
	statsAfter := runtime.Stats()
	step(fmt.Sprintf("Network fetches after 300 reads: %d", statsAfter.FetchCount))
	if statsAfter.FetchCount != stats.FetchCount {
		fatal("SDK made unexpected network calls", fmt.Errorf(
			"before=%d after=%d", stats.FetchCount, statsAfter.FetchCount))
	}
	step("PASSED — all reads served from local cache")

	// ------------------------------------------------------------------
	// 3d. Get all resolved values
	// ------------------------------------------------------------------
	section("3d. Get Full Resolved Configuration")

	allValues := runtime.GetAll()
	step(fmt.Sprintf("Total resolved keys: %d", len(allValues)))
	for _, k := range sortedKeys(allValues) {
		fmt.Printf("    %s = %v\n", k, allValues[k])
	}

	// ------------------------------------------------------------------
	// 3e. Multi-level inheritance — connect to auth_module in production
	// ------------------------------------------------------------------
	section("3e. Multi-Level Inheritance (auth_module)")

	authRuntime, err := authModule.Connect(ctx, "production")
	if err != nil {
		fatal("failed to connect to auth_module runtime", err)
	}

	sessionTTL := authRuntime.GetInt("session_ttl_minutes")
	step(fmt.Sprintf("session_ttl_minutes = %d  (auth_module production override)", sessionTTL))

	mfaEnabled := authRuntime.GetBool("mfa_enabled")
	step(fmt.Sprintf("mfa_enabled = %v  (auth_module production override)", mfaEnabled))

	dbVal := authRuntime.Get("database")
	step(fmt.Sprintf("database (inherited from user_service) = %v", dbVal))

	appNameInherited := authRuntime.GetString("app_name")
	step(fmt.Sprintf("app_name (inherited from common) = %q", appNameInherited))

	authRuntime.Close()
	step("auth_runtime closed")

	// ====================================================================
	// 4. REAL-TIME UPDATES — WebSocket-driven cache invalidation
	// ====================================================================
	section("4. Real-Time Updates via WebSocket")

	// ------------------------------------------------------------------
	// 4a. Register change listeners
	// ------------------------------------------------------------------
	var changesReceived []map[string]interface{}
	runtime.OnChange(func(evt *smplkit.ConfigChangeEvent) {
		changesReceived = append(changesReceived, map[string]interface{}{
			"key":       evt.Key,
			"old_value": evt.OldValue,
			"new_value": evt.NewValue,
			"source":    evt.Source,
		})
		fmt.Printf("    [CHANGE] %s: %v → %v\n", evt.Key, evt.OldValue, evt.NewValue)
	})
	step("Global change listener registered")

	var retryChanges []*smplkit.ConfigChangeEvent
	runtime.OnChange(func(evt *smplkit.ConfigChangeEvent) {
		retryChanges = append(retryChanges, evt)
	}, "max_retries")
	step("Key-specific listener registered for 'max_retries'")

	// ------------------------------------------------------------------
	// 4b. Simulate a config change via the management API
	// ------------------------------------------------------------------
	step("Updating max_retries on common (production) via management API...")

	err = common.SetValue(ctx, "max_retries", 7, "production")
	if err != nil {
		fatal("failed to update max_retries", err)
	}

	// Give the WebSocket a moment to deliver the update.
	time.Sleep(2 * time.Second)

	newRetries := runtime.GetInt("max_retries")
	step(fmt.Sprintf("max_retries after live update = %d (expected 7 if WS delivered)", newRetries))
	step(fmt.Sprintf("Changes received by global listener: %d", len(changesReceived)))
	step(fmt.Sprintf("Retry-specific changes received: %d", len(retryChanges)))

	// ------------------------------------------------------------------
	// 4c. Connection lifecycle
	// ------------------------------------------------------------------
	section("4c. WebSocket Connection Lifecycle")

	wsStatus := runtime.ConnectionStatus()
	step(fmt.Sprintf("WebSocket status: %s", wsStatus))

	err = runtime.Refresh()
	if err != nil {
		fatal("manual refresh failed", err)
	}
	step("Manual refresh completed")
	step(fmt.Sprintf("max_retries after manual refresh = %d", runtime.GetInt("max_retries")))

	// ====================================================================
	// 5. ENVIRONMENT COMPARISON
	// ====================================================================
	section("5. Environment Comparison")

	for _, env := range []string{"development", "staging", "production"} {
		envRuntime, envErr := userService.Connect(ctx, env)
		if envErr != nil {
			step(fmt.Sprintf("[%-12s] failed to connect: %v", env, envErr))
			continue
		}
		dbHost := "<none>"
		if db, ok := envRuntime.Get("database").(map[string]interface{}); ok {
			if h, ok := db["host"].(string); ok {
				dbHost = h
			}
		}
		retries := envRuntime.GetInt("max_retries")
		step(fmt.Sprintf("[%-12s] db.host=%-50s  retries=%d", env, dbHost, retries))
		envRuntime.Close()
	}

	// ====================================================================
	// 6. CLEANUP
	// ====================================================================
	section("6. Cleanup")

	// Close the production runtime.
	runtime.Close()
	step("Production runtime closed")

	// Delete in dependency order (children first).
	if err := client.Config().Delete(ctx, authModule.ID); err != nil {
		fmt.Printf("  Warning: failed to delete auth_module: %v\n", err)
	} else {
		step(fmt.Sprintf("Deleted auth_module (id=%s)", authModule.ID))
	}

	if err := client.Config().Delete(ctx, userService.ID); err != nil {
		fmt.Printf("  Warning: failed to delete user_service: %v\n", err)
	} else {
		step(fmt.Sprintf("Deleted user_service (id=%s)", userService.ID))
	}

	// Reset common to empty state.
	err = common.Update(ctx, smplkit.UpdateConfigParams{
		Description:  strPtr(""),
		Values:       map[string]interface{}{},
		Environments: map[string]map[string]interface{}{},
	})
	if err != nil {
		fmt.Printf("  Warning: failed to reset common config: %v\n", err)
	} else {
		step("Common config reset to empty")
	}

	// ====================================================================
	// DONE
	// ====================================================================
	section("ALL DONE")
	fmt.Println("  The Config SDK showcase completed successfully.")
	fmt.Println("  If you got here, Smpl Config is ready to ship.")
	fmt.Println()
	fmt.Println("Features exercised:")
	fmt.Println("  [x] Client initialization")
	fmt.Println("  [x] Update config with base values and environment overrides")
	fmt.Println("  [x] SetValues (base and per-environment)")
	fmt.Println("  [x] SetValue (single key)")
	fmt.Println("  [x] Create config with values")
	fmt.Println("  [x] Create config with parent (inheritance)")
	fmt.Println("  [x] List all configs")
	fmt.Println("  [x] Connect to config (runtime plane)")
	fmt.Println("  [x] Typed accessors: GetString, GetInt, GetBool")
	fmt.Println("  [x] Exists, Get with default")
	fmt.Println("  [x] Stats (local caching verification)")
	fmt.Println("  [x] GetAll (full resolved config)")
	fmt.Println("  [x] Multi-level inheritance")
	fmt.Println("  [x] OnChange listeners (global and key-specific)")
	fmt.Println("  [x] Real-time WebSocket updates")
	fmt.Println("  [x] ConnectionStatus, Refresh")
	fmt.Println("  [x] Environment comparison")
	fmt.Println("  [x] Delete configs")
}

// sortedKeys returns map keys in sorted order (for deterministic output).
func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort (small maps).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
