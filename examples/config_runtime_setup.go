//go:build ignore

package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

var (
	configRuntimeDemoEnvironments = []string{"staging", "production"}
	configRuntimeDemoConfigIDs    = []string{
		"showcase-user-service",
		"showcase-auth-module",
		"showcase-common",
	}
)

func setupConfigRuntimeShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	ensureEnvironments(ctx, mgmt, configRuntimeDemoEnvironments...)
	cleanupConfigRuntimeShowcase(ctx, mgmt)

	shared := mgmt.Config().New("showcase-common",
		smplkit.WithConfigName("Showcase Common"),
		smplkit.WithConfigDescription("Showcase-only shared configuration."),
	)
	shared.SetString("app_name", "Acme SaaS Platform", "")
	shared.SetString("support_email", "support@acme.dev", "")
	shared.SetNumber("max_retries", 3, "")
	shared.SetNumber("request_timeout_ms", 5000, "")
	shared.SetNumber("pagination_default_page_size", 25, "")
	shared.SetNumber("max_retries", 5, "production")
	shared.SetNumber("request_timeout_ms", 10000, "production")
	shared.SetNumber("max_retries", 2, "staging")
	fatalIfErr("save shared", shared.Save(ctx))

	userService := mgmt.Config().New("showcase-user-service",
		smplkit.WithConfigName("Showcase User Service"),
		smplkit.WithConfigDescription("Configuration for the user microservice."),
		smplkit.WithConfigParent(shared.ID),
	)
	userService.SetString("database.host", "localhost", "")
	userService.SetNumber("database.port", 5432, "")
	userService.SetString("database.name", "users_dev", "")
	userService.SetNumber("database.pool_size", 5, "")
	userService.SetNumber("cache_ttl_seconds", 300, "")
	userService.SetBoolean("enable_signup", true, "")
	userService.SetNumber("pagination_default_page_size", 50, "")
	userService.SetString("database.host", "prod-users-rds.internal.acme.dev", "production")
	userService.SetString("database.name", "users_prod", "production")
	userService.SetNumber("database.pool_size", 20, "production")
	userService.SetNumber("cache_ttl_seconds", 600, "production")
	userService.SetBoolean("enable_signup", false, "production")
	fatalIfErr("save user_service", userService.Save(ctx))

	authModule := mgmt.Config().New("showcase-auth-module",
		smplkit.WithConfigName("Showcase Auth Module"),
		smplkit.WithConfigDescription("Authentication module within the user service."),
		smplkit.WithConfigParent(shared.ID),
	)
	authModule.SetNumber("session_ttl_minutes", 60, "")
	authModule.SetBoolean("mfa_enabled", false, "")
	authModule.SetNumber("session_ttl_minutes", 30, "production")
	authModule.SetBoolean("mfa_enabled", true, "production")
	fatalIfErr("save auth_module", authModule.Save(ctx))
}

func cleanupConfigRuntimeShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	for _, id := range configRuntimeDemoConfigIDs {
		if err := mgmt.Config().Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete config "+id, err)
			}
		}
	}
}
