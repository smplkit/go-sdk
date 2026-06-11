//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Config.
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

// Example Go configuration structs to showcase how "code-first"
// configuration management works.

// App holds the application display name.
type App struct {
	Name string `json:"name"`
}

// Support holds the customer support contact.
type Support struct {
	Email string `json:"email"`
}

// Plan holds plan-limit configuration.
type Plan struct {
	MaxSeats  int    `json:"max_seats"`
	TrialDays int    `json:"trial_days"`
	Tier      string `json:"tier"`
}

// Common holds shared defaults for showcase services.
type Common struct {
	App     App     `json:"app"`
	Support Support `json:"support"`
}

// Billing holds plan-limit configuration for billing — inherits from Common.
type Billing struct {
	App     App     `json:"app"`
	Support Support `json:"support"`
	Plan    Plan    `json:"plan"`
}

func main() {
	ctx := context.Background()

	// or NewClient for synchronous use
	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-service",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	cleanupConfigRuntimeShowcase(ctx, client.Config())

	// bind Go structs
	common := &Common{
		App:     App{Name: "Acme SaaS"},
		Support: Support{Email: "support@acme.dev"},
	}
	fatalIfErr("bind common", client.Config().Bind(ctx, "showcase-common", common))

	billing := &Billing{
		App:     App{Name: "Acme SaaS"},
		Support: Support{Email: "support@acme.dev"},
		Plan:    Plan{MaxSeats: 5, TrialDays: 14, Tier: "free"},
	}
	fatalIfErr("bind billing", client.Config().Bind(ctx, "showcase-billing", billing,
		smplkit.WithBindParent(common),
	))

	fmt.Printf("common.app.name = %s\n", common.App.Name)
	fmt.Printf("billing.app.name = %s  # inherited from common\n", billing.App.Name)
	fmt.Printf("billing.plan.max_seats = %d\n", billing.Plan.MaxSeats)

	// add listeners if desired
	var changes int64
	client.Config().OnChange(func(event *smplkit.ConfigChangeEvent) {
		atomic.AddInt64(&changes, 1)
		fmt.Printf("    [CHANGE] %s.%s: %v -> %v\n",
			event.ConfigID, event.ItemKey, event.OldValue, event.NewValue)
	}, smplkit.WithConfigID("showcase-billing"), smplkit.WithItemKey("plan.max_seats"))

	fatalIfErr("wait until ready", client.WaitUntilReady(ctx, 0))

	// simulate someone making a change in smplkit console
	simulateAdminOverride(ctx, client.Config())
	time.Sleep(1500 * time.Millisecond)

	// observe changes are automatically reflected in bound models
	fmt.Printf("billing.plan.max_seats after override = %d\n", billing.Plan.MaxSeats)
	if billing.Plan.MaxSeats != 25 {
		fatalIfErr("billing.Plan.MaxSeats", fmt.Errorf("expected 25, got %d", billing.Plan.MaxSeats))
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
	fmt.Printf("db['primary']['host'] = %v\n", primary["host"])
	fmt.Printf("db['pool_size'] = %v\n", db["pool_size"])
	if primary["host"] != "db.acme.example" {
		fatalIfErr("db.primary.host", fmt.Errorf("expected db.acme.example, got %v", primary["host"]))
	}
	if db["pool_size"] != 10 {
		fatalIfErr("db.pool_size", fmt.Errorf("expected 10, got %v", db["pool_size"]))
	}

	// or read live values via Subscribe(id)
	commonView, err := client.Config().Subscribe(ctx, "showcase-common")
	fatalIfErr("subscribe common", err)
	fmt.Println("showcase-common (via subscribe):")
	for k, v := range commonView.Value() {
		fmt.Printf("    %s = %v\n", k, v)
	}
	appName, _ := commonView.Get("app.name")
	if appName != "Acme SaaS" {
		fatalIfErr("commonView app.name", fmt.Errorf("expected Acme SaaS, got %v", appName))
	}

	// or skip the model/map and just fetch specific keys directly
	slowQueryMs := client.Config().GetValueOr(ctx, "showcase-database", "slow_query_threshold_ms", 500)
	fmt.Printf("showcase-database.slow_query_threshold_ms = %v  # default used (key absent)\n", slowQueryMs)
	if slowQueryMs != 500 {
		fatalIfErr("slowQueryMs", fmt.Errorf("expected 500, got %v", slowQueryMs))
	}

	cleanupConfigRuntimeShowcase(ctx, client.Config())
	fmt.Println("Done!")
}
