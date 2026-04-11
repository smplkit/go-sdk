//go:build ignore

package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk"
)

// demoConfigs holds references to configs created by setupDemoConfigs.
type demoConfigs struct {
	Common      *smplkit.Config
	UserService *smplkit.Config
	AuthModule  *smplkit.Config
}

// setupDemoConfigs creates and configures the demo config hierarchy for the
// runtime showcase:
//
//	common          — org-wide shared configuration (updated, not created)
//	user_service    — microservice config with env overrides
//	auth_module     — child of user_service with env overrides
//
// Any leftover configs from a previous run are cleaned up first.
func setupDemoConfigs(ctx context.Context, client *smplkit.Client) (*demoConfigs, error) {
	// Pre-flight: delete any configs left over from a previous run.
	for _, key := range []string{"auth_module", "user_service"} {
		_ = client.Config().Delete(ctx, key)
	}

	// ----------------------------------------------------------------
	// Common config — update with base values + environment overrides
	// ----------------------------------------------------------------
	common, err := client.Config().Get(ctx, "common")
	if err != nil {
		return nil, fmt.Errorf("fetch common config: %w", err)
	}

	common.Description = strPtr("Organization-wide shared configuration")
	common.Items = map[string]interface{}{
		"app_name":                     "Acme SaaS Platform",
		"support_email":                "support@acme.dev",
		"max_retries":                  3,
		"request_timeout_ms":           5000,
		"pagination_default_page_size": 25,
	}
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
		return nil, fmt.Errorf("update common config: %w", err)
	}

	// ----------------------------------------------------------------
	// User Service config — new config with env overrides
	// ----------------------------------------------------------------
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
		return nil, fmt.Errorf("create user_service config: %w", err)
	}

	// ----------------------------------------------------------------
	// Auth Module config — child of user_service with env overrides
	// ----------------------------------------------------------------
	authModule := client.Config().New("auth_module",
		smplkit.WithConfigName("Auth Module"),
		smplkit.WithConfigDescription("Authentication module within the user service."),
		smplkit.WithConfigParent(userService.ID),
		smplkit.WithConfigItems(map[string]interface{}{
			"session_ttl_minutes": 60,
			"mfa_enabled":         false,
		}),
		smplkit.WithConfigEnvironments(map[string]map[string]interface{}{
			"production": {
				"session_ttl_minutes": 30,
				"mfa_enabled":         true,
			},
		}),
	)
	err = authModule.Save(ctx)
	if err != nil {
		_ = client.Config().Delete(ctx, "user_service")
		return nil, fmt.Errorf("create auth_module config: %w", err)
	}

	return &demoConfigs{
		Common:      common,
		UserService: userService,
		AuthModule:  authModule,
	}, nil
}

// teardownDemoConfigs deletes the demo configs and resets common to empty.
func teardownDemoConfigs(ctx context.Context, client *smplkit.Client, demo *demoConfigs) {
	if err := client.Config().Delete(ctx, "auth_module"); err != nil {
		fmt.Printf("  Warning: failed to delete auth_module: %v\n", err)
	}

	if err := client.Config().Delete(ctx, "user_service"); err != nil {
		fmt.Printf("  Warning: failed to delete user_service: %v\n", err)
	}

	demo.Common.Description = strPtr("")
	demo.Common.Items = map[string]interface{}{}
	demo.Common.Environments = map[string]map[string]interface{}{}
	err := demo.Common.Save(ctx)
	if err != nil {
		fmt.Printf("  Warning: failed to reset common config: %v\n", err)
	}
}
