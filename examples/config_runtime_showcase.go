//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Config.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
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

	smplkit "github.com/smplkit/go-sdk/v3"
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

	// get a config as a live proxy (rule 10 — every read goes through
	// the resolved cache, WS updates land automatically)
	userSvc, err := client.Config().Get(ctx, "showcase-user-service")
	fatalIfErr("get user_service", err)
	fmt.Printf("Total resolved keys: %d\n", userSvc.Len())
	host, _ := userSvc.Get("database.host")
	fmt.Printf("database.host = %v\n", host)
	maxRetries, _ := userSvc.Get("max_retries")
	fmt.Printf("max_retries = %v\n", maxRetries)
	cacheTTL, _ := userSvc.Get("cache_ttl_seconds")
	fmt.Printf("cache_ttl_seconds = %v\n", cacheTTL)
	pageSize, _ := userSvc.Get("pagination_default_page_size")
	fmt.Printf("pagination_default_page_size = %v\n", pageSize)
	signup, _ := userSvc.Get("enable_signup")
	fmt.Printf("enable_signup = %v\n", signup)
	nope, _ := userSvc.Get("nonexistent_key")
	fmt.Printf("nonexistent_key = %v\n", nope)

	// production overrides resolve through the inheritance chain
	if host != "prod-users-rds.internal.acme.dev" {
		fatalIfErr("database.host", fmt.Errorf("got %v", host))
	}

	var anyChanges, retriesChanges int64

	// global listener — fires when ANY config item changes
	client.Config().OnChange(func(event *smplkit.ConfigChangeEvent) {
		atomic.AddInt64(&anyChanges, 1)
		fmt.Printf("    [CHANGE] %s.%s: %v -> %v\n",
			event.ConfigID, event.ItemKey, event.OldValue, event.NewValue)
	})

	// item-scoped listener via the live-proxy handle
	commonProxy, err := client.Config().Get(ctx, "showcase-common")
	fatalIfErr("get common", err)
	commonProxy.OnChangeKey("max_retries", func(event *smplkit.ConfigChangeEvent) {
		atomic.AddInt64(&retriesChanges, 1)
	})

	// simulate someone making a change to trigger listeners
	updateMaxRetries(ctx, client, 7)

	// wait a moment for the event to be delivered
	time.Sleep(200 * time.Millisecond)

	// userSvc always reflects the latest values — same proxy, same id,
	// no re-fetch needed
	updatedRetries, _ := commonProxy.Get("max_retries")
	fmt.Printf("max_retries after update = %v\n", updatedRetries)
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
