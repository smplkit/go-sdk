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

func main() {
	ctx := context.Background()

	// create the client
	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-billing",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	setupConfigRuntimeShowcase(ctx, client.Manage())

	// Block until the live-updates WebSocket subscription is registered
	// server-side. Without this, writes fired immediately afterward can
	// race the broadcast of their own change events.
	fatalIfErr("wait until ready", client.WaitUntilReady(ctx, 0))

	// declare a common/shared configuration
	common, err := client.Config().GetOrCreate(ctx, "showcase-common",
		smplkit.WithConfigDescription("Shared defaults for showcase services."),
	)
	fatalIfErr("get_or_create common", err)

	// declare a configuration that inherits from some parent
	billing, err := client.Config().GetOrCreate(ctx, "showcase-billing",
		smplkit.WithConfigParent(common.ID()),
		smplkit.WithConfigDescription("Plan-limit configuration discovered from code."),
	)
	fatalIfErr("get_or_create billing", err)

	// get a configured value
	appName := common.GetString("app.name", "Acme SaaS")
	supportEmail := common.GetString("support.email", "support@acme.dev")
	maxSeats := billing.GetInt("plan.max_seats", 5, smplkit.WithItemDescription("Maximum seats per organization."))
	trialDays := billing.GetInt("plan.trial_days", 14)
	tier := billing.GetString("plan.tier", "free")

	fmt.Printf("app.name = %s\n", appName)
	fmt.Printf("support.email = %s\n", supportEmail)
	fmt.Printf("plan.max_seats = %d\n", maxSeats)
	fmt.Printf("plan.trial_days = %d\n", trialDays)
	fmt.Printf("plan.tier = %s\n", tier)

	// listen for changes
	var changes int64
	billing.OnChangeKey("plan.max_seats", func(event *smplkit.ConfigChangeEvent) {
		atomic.AddInt64(&changes, 1)
		fmt.Printf("    [CHANGE] %s.%s: %v -> %v\n",
			event.ConfigID, event.ItemKey, event.OldValue, event.NewValue)
	})

	// simulate someone overriding a value in the console
	simulateAdminOverride(ctx, client.Manage())

	// wait for the WebSocket push to deliver the change, then refetch
	time.Sleep(1500 * time.Millisecond)

	// get the latest value
	updatedSeats := billing.GetInt("plan.max_seats", 5)
	fmt.Printf("plan.max_seats after override = %d\n", updatedSeats)
	if updatedSeats != 25 {
		fatalIfErr("plan.max_seats", fmt.Errorf("expected 25, got %d", updatedSeats))
	}
	if atomic.LoadInt64(&changes) < 1 {
		fatalIfErr("changes", fmt.Errorf("expected at least one change event"))
	}

	cleanupConfigRuntimeShowcase(ctx, client.Manage())
	fmt.Println("Done!")
}
