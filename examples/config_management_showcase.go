//go:build ignore

// Demonstrates the smplkit management SDK for Smpl Config.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//
// Usage:
//
//	make config_management_showcase
package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func main() {
	ctx := context.Background()

	// or NewClient for the full one-client surface
	config, err := smplkit.NewConfigClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-service",
	})
	fatalIfErr("create config client", err)

	setupConfigManagementShowcase(ctx, config)

	// create a "parent" configuration that all other configs inherit from
	shared := config.New("showcase-common",
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
	fatalIfErr("save shared", shared.Save(ctx))
	fmt.Printf("Created config: %s\n", shared.ID)

	// create a config (inherits from showcase-common)
	userService := config.New("showcase-user-service",
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
	fatalIfErr("save user_service", userService.Save(ctx))

	// update a config
	userService.SetString("database.host", "prod-users-rds.internal.acme.dev", "production")
	userService.SetString("database.name", "users_prod", "production")
	userService.SetNumber("database.pool_size", 20, "production")
	userService.SetNumber("cache_ttl_seconds", 600, "production")
	userService.SetBoolean("enable_signup", false, "production")
	fatalIfErr("update user_service", userService.Save(ctx))
	fmt.Printf("Updated config: %s\n", userService.ID)

	// list configs
	configs, err := config.List(ctx)
	fatalIfErr("list configs", err)
	for _, cfg := range configs {
		parentInfo := " (root)"
		if cfg.Parent != nil {
			parentInfo = fmt.Sprintf(" (parent: %s)", *cfg.Parent)
		}
		fmt.Printf("  %s%s\n", cfg.ID, parentInfo)
	}

	// get a config
	fetched, err := config.Get(ctx, "showcase-user-service")
	fatalIfErr("get user_service", err)
	desc := ""
	if fetched.Description != nil {
		desc = *fetched.Description
	}
	parent := "(none)"
	if fetched.Parent != nil {
		parent = *fetched.Parent
	}
	itemKeys := make([]string, 0, len(fetched.Items))
	for k := range fetched.Items {
		itemKeys = append(itemKeys, k)
	}
	fmt.Printf("Fetched: id=%s, name=%s\n", fetched.ID, fetched.Name)
	fmt.Printf("  description=%s\n", desc)
	fmt.Printf("  parent=%s\n", parent)
	fmt.Printf("  items: %v\n", itemKeys)

	// delete configs
	fatalIfErr("delete user_service", userService.Delete(ctx))
	fatalIfErr("delete shared", shared.Delete(ctx))
	fmt.Println("Deleted configs")

	// cleanup
	cleanupConfigManagementShowcase(ctx, config)
	fmt.Println("Done!")
}
