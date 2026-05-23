//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Config.
//
// Headline pattern: declare configurations as Go structs (or maps),
// Bind them to a config id, then use the same struct pointer (or map)
// directly — its fields stay in sync with the server via the SDK's
// in-memory cache and WebSocket push.
//
// Also demonstrates three lower-friction patterns:
//   - Bind with a map[string]interface{} (no struct required)
//   - Get(ctx, id) for dict-like lookup of an entire config
//   - GetValueOr(ctx, id, key, default) for one-shot value reads with fallback
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
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

// Common holds shared defaults for showcase services.
type Common struct {
	AppName      string `json:"app_name"`
	SupportEmail string `json:"support_email"`
}

// Billing holds plan-limit configuration for billing.
type Billing struct {
	MaxSeats  int    `json:"max_seats"`
	TrialDays int    `json:"trial_days"`
	Tier      string `json:"tier"`
}

func main() {
	ctx := context.Background()

	// create the client
	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-billing",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	cleanupConfigRuntimeShowcase(ctx, client.Manage())

	// Block until the live-updates WebSocket subscription is registered
	// server-side. Without this, writes fired immediately afterward can
	// race the broadcast of their own change events.
	fatalIfErr("wait until ready", client.WaitUntilReady(ctx, 0))

	// bind Go structs — Bind mutates them in place via WebSocket dispatch
	common := &Common{AppName: "Acme SaaS", SupportEmail: "support@acme.dev"}
	fatalIfErr("bind common", client.Config().Bind(ctx, "showcase-common", common))

	billing := &Billing{MaxSeats: 5, TrialDays: 14, Tier: "free"}
	fatalIfErr("bind billing", client.Config().Bind(ctx, "showcase-billing", billing,
		smplkit.WithBindParent(common),
	))

	fmt.Printf("common.AppName = %s\n", common.AppName)
	fmt.Printf("billing.MaxSeats = %d\n", billing.MaxSeats)
	fmt.Printf("billing.Tier = %s\n", billing.Tier)

	// add listeners if desired
	var changes int64
	client.Config().OnChange(func(event *smplkit.ConfigChangeEvent) {
		atomic.AddInt64(&changes, 1)
		fmt.Printf("    [CHANGE] %s.%s: %v -> %v\n",
			event.ConfigID, event.ItemKey, event.OldValue, event.NewValue)
	}, smplkit.WithConfigID("showcase-billing"), smplkit.WithItemKey("max_seats"))

	// simulate someone making a change in smplkit console
	simulateAdminOverride(ctx, client.Manage())
	time.Sleep(1500 * time.Millisecond)

	// observe changes are automatically reflected in the bound struct
	fmt.Printf("billing.MaxSeats after override = %d\n", billing.MaxSeats)
	if billing.MaxSeats != 25 {
		fatalIfErr("billing.MaxSeats", fmt.Errorf("expected 25, got %d", billing.MaxSeats))
	}
	if atomic.LoadInt64(&changes) < 1 {
		fatalIfErr("changes", fmt.Errorf("expected at least one change event"))
	}

	// you can also bind plain-old maps
	db := map[string]interface{}{
		"primary": map[string]interface{}{
			"host": "db.acme.example",
			"port": 5432,
		},
		"pool_size":            10,
		"statement_timeout_ms": 30000,
	}
	fatalIfErr("bind db", client.Config().Bind(ctx, "showcase-database", db))
	primary := db["primary"].(map[string]interface{})
	fmt.Printf("db.primary.host = %v\n", primary["host"])
	fmt.Printf("db.pool_size = %v\n", db["pool_size"])
	if primary["host"] != "db.acme.example" {
		fatalIfErr("db.primary.host", fmt.Errorf("expected db.acme.example, got %v", primary["host"]))
	}

	// or get a config by ID (raises NotFoundError if not found; pass a
	// default if you want a fallback)
	commonView, err := client.Config().Get(ctx, "showcase-common")
	fatalIfErr("get common", err)
	fmt.Println("showcase-common (via Get):")
	for k, v := range commonView.Value() {
		fmt.Printf("    %s = %v\n", k, v)
	}
	if commonView.Value()["app_name"] != "Acme SaaS" {
		fatalIfErr("commonView app_name", fmt.Errorf("expected Acme SaaS, got %v", commonView.Value()["app_name"]))
	}

	// or skip the struct/map and just fetch specific keys directly
	slowQueryMs := client.Config().GetValueOr(ctx, "showcase-database", "slow_query_threshold_ms", 500)
	fmt.Printf("showcase-database.slow_query_threshold_ms = %v  // default used; now registered\n", slowQueryMs)
	if slowQueryMs != 500 {
		fatalIfErr("slowQueryMs", fmt.Errorf("expected 500, got %v", slowQueryMs))
	}

	cleanupConfigRuntimeShowcase(ctx, client.Manage())
	fmt.Println("Done!")
}
