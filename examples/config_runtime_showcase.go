//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Config.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//   - The smplkit Config service running and reachable
//
// Usage:
//
//	make config_runtime_showcase
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	ctx := context.Background()

	// create the client (runtime + management on the same client)
	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-service",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	setupConfigRuntimeShowcase(ctx, client.Manage())

	// get a config as a plain dict
	userSvc, err := client.Config().Get(ctx, "showcase-user-service")
	fatalIfErr("get user_service", err)
	fmt.Printf("Total resolved keys: %d\n", len(userSvc))
	fmt.Printf("database.host = %v\n", userSvc["database.host"])
	fmt.Printf("max_retries = %v\n", userSvc["max_retries"])
	fmt.Printf("cache_ttl_seconds = %v\n", userSvc["cache_ttl_seconds"])
	fmt.Printf("pagination_default_page_size = %v\n", userSvc["pagination_default_page_size"])
	fmt.Printf("enable_signup = %v\n", userSvc["enable_signup"])
	fmt.Printf("nonexistent_key = %v\n", userSvc["nonexistent_key"])

	// production overrides resolve through the inheritance chain
	if userSvc["database.host"] != "prod-users-rds.internal.acme.dev" {
		fatalIfErr("database.host", fmt.Errorf("got %v", userSvc["database.host"]))
	}

	var anyChanges, retriesChanges int64

	// global listener — fires when ANY config item changes
	client.Config().OnChange(func(event *smplkit.ConfigChangeEvent) {
		atomic.AddInt64(&anyChanges, 1)
		fmt.Printf("    [CHANGE] %s.%s: %v -> %v\n",
			event.ConfigID, event.ItemKey, event.OldValue, event.NewValue)
	})

	// item-scoped listener via the live-proxy handle
	commonProxy, err := client.Config().Subscribe(ctx, "showcase-common")
	fatalIfErr("subscribe common", err)
	commonProxy.OnChangeKey("max_retries", func(event *smplkit.ConfigChangeEvent) {
		atomic.AddInt64(&retriesChanges, 1)
	})

	// simulate someone making a change to trigger listeners
	updateMaxRetries(ctx, client, 7)

	// wait a moment for the event to be delivered
	time.Sleep(200 * time.Millisecond)

	// userSvc always reflects the latest values via re-read
	updated, err := client.Config().Get(ctx, "showcase-user-service")
	fatalIfErr("re-get user_service", err)
	fmt.Printf("max_retries after update = %v\n", updated["max_retries"])
	fmt.Printf("Global changes received: %d\n", atomic.LoadInt64(&anyChanges))
	fmt.Printf("Retries-specific changes received: %d\n", atomic.LoadInt64(&retriesChanges))

	cleanupConfigRuntimeShowcase(ctx, client.Manage())
	fmt.Println("Done!")
}

func updateMaxRetries(ctx context.Context, client *smplkit.Client, maxRetries int) {
	common, err := client.Manage().Config().Get(ctx, "showcase-common")
	fatalIfErr("get common", err)
	common.SetNumber("max_retries", float64(maxRetries), "production")
	fatalIfErr("save common", common.Save(ctx))
}
