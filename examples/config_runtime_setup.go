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
		if cfg, err := client.Config().GetByKey(ctx, key); err == nil {
			_ = client.Config().Delete(ctx, cfg.ID)
		}
	}

	// ----------------------------------------------------------------
	// Common config — update with base values + environment overrides
	// ----------------------------------------------------------------
	common, err := client.Config().GetByKey(ctx, "common")
	if err != nil {
		return nil, fmt.Errorf("fetch common config: %w", err)
	}

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
		return nil, fmt.Errorf("update common config: %w", err)
	}

	err = common.SetValues(ctx, map[string]interface{}{
		"max_retries":        5,
		"request_timeout_ms": 10000,
	}, "production")
	if err != nil {
		return nil, fmt.Errorf("set common production overrides: %w", err)
	}

	err = common.SetValues(ctx, map[string]interface{}{
		"max_retries": 2,
	}, "staging")
	if err != nil {
		return nil, fmt.Errorf("set common staging overrides: %w", err)
	}

	// ----------------------------------------------------------------
	// User Service config — new config with env overrides
	// ----------------------------------------------------------------
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
		return nil, fmt.Errorf("create user_service config: %w", err)
	}

	err = userService.SetValues(ctx, map[string]interface{}{
		"database": map[string]interface{}{
			"host":      "prod-users-rds.internal.acme.dev",
			"name":      "users_prod",
			"pool_size": 20,
		},
		"cache_ttl_seconds": 600,
	}, "production")
	if err != nil {
		return nil, fmt.Errorf("set user_service production overrides: %w", err)
	}

	err = userService.SetValue(ctx, "enable_signup", false, "production")
	if err != nil {
		return nil, fmt.Errorf("set enable_signup in production: %w", err)
	}

	// ----------------------------------------------------------------
	// Auth Module config — child of user_service with env overrides
	// ----------------------------------------------------------------
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
		return nil, fmt.Errorf("create auth_module config: %w", err)
	}

	err = authModule.SetValues(ctx, map[string]interface{}{
		"session_ttl_minutes": 30,
		"mfa_enabled":         true,
	}, "production")
	if err != nil {
		return nil, fmt.Errorf("set auth_module production overrides: %w", err)
	}

	return &demoConfigs{
		Common:      common,
		UserService: userService,
		AuthModule:  authModule,
	}, nil
}

// teardownDemoConfigs deletes the demo configs and resets common to empty.
func teardownDemoConfigs(ctx context.Context, client *smplkit.Client, demo *demoConfigs) {
	if err := client.Config().Delete(ctx, demo.AuthModule.ID); err != nil {
		fmt.Printf("  Warning: failed to delete auth_module: %v\n", err)
	}

	if err := client.Config().Delete(ctx, demo.UserService.ID); err != nil {
		fmt.Printf("  Warning: failed to delete user_service: %v\n", err)
	}

	err := demo.Common.Update(ctx, smplkit.UpdateConfigParams{
		Description:  strPtr(""),
		Items:        map[string]interface{}{},
		Environments: map[string]map[string]interface{}{},
	})
	if err != nil {
		fmt.Printf("  Warning: failed to reset common config: %v\n", err)
	}
}
