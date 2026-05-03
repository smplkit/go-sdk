//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Flags.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//
// Usage:
//
//	make flags_runtime_showcase
//
// Note: this showcase calls client.Flags().SetContextProvider inline to
// demonstrate context-driven flag evaluation. In a real app (chi, gin,
// echo, etc.), the provider is wired once into middleware — not
// scattered through your handlers.
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	smplkit "github.com/smplkit/go-sdk"
)

var (
	alice = map[string]interface{}{
		"beta_tester": true,
		"email":       "alice.adams@acme.com",
		"first_name":  "Alice",
		"last_name":   "Adams",
		"plan":        "enterprise",
	}
	bob = map[string]interface{}{
		"beta_tester": false,
		"email":       "bob.jones@acme.com",
		"first_name":  "Bob",
		"last_name":   "Jones",
		"plan":        "free",
	}
	largeTechAccount = map[string]interface{}{
		"employee_count": 500,
		"id":             1234,
		"industry":       "technology",
		"region":         "us",
	}
	smallRetailAccount = map[string]interface{}{
		"employee_count": 10,
		"id":             5678,
		"industry":       "retail",
		"region":         "eu",
	}
)

// Create context within which flags will be evaluated.
func createContext(user, account map[string]interface{}) []smplkit.Context {
	return []smplkit.Context{
		smplkit.NewContext("user", user["email"].(string), nil,
			smplkit.WithAttr("beta_tester", user["beta_tester"]),
			smplkit.WithAttr("first_name", user["first_name"]),
			smplkit.WithAttr("last_name", user["last_name"]),
			smplkit.WithAttr("plan", user["plan"]),
		),
		smplkit.NewContext("account", fmt.Sprintf("%v", account["id"]), nil,
			smplkit.WithAttr("industry", account["industry"]),
			smplkit.WithAttr("region", account["region"]),
			smplkit.WithAttr("employee_count", account["employee_count"]),
		),
	}
}

func main() {
	ctx := context.Background()

	// create the client
	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "staging",
		Service:     "showcase-service",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	setupFlagsRuntimeShowcase(ctx, client.Manage())

	// declare flags - default values will be used if the flag does not
	// exist or smplkit is unreachable
	checkoutV2 := client.Flags().BooleanFlag("checkout-v2", false)
	bannerColor := client.Flags().StringFlag("banner-color", "red")
	maxRetries := client.Flags().NumberFlag("max-retries", 3)

	var allChanges, bannerChanges int64

	// global listener — fires when ANY flag definition changes
	client.Flags().OnChange(func(event *smplkit.FlagChangeEvent) {
		atomic.AddInt64(&allChanges, 1)
		fmt.Printf("    Global flag listener: %q updated via %s\n", event.ID, event.Source)
	})

	// flag listener — fires only when a specific flag changes
	client.Flags().OnChangeKey("banner-color", func(event *smplkit.FlagChangeEvent) {
		atomic.AddInt64(&bannerChanges, 1)
		fmt.Println("    banner-color flag changed!")
	})

	// request 1 — Alice from a large tech account
	checkoutResult := checkoutV2.Get(ctx, createContext(alice, largeTechAccount)...)
	fmt.Printf("checkout-v2 = %v\n", checkoutResult)

	bannerResult := bannerColor.Get(ctx, createContext(alice, largeTechAccount)...)
	fmt.Printf("banner-color = %v\n", bannerResult)

	retriesResult := maxRetries.Get(ctx, createContext(alice, largeTechAccount)...)
	fmt.Printf("max-retries = %v\n", retriesResult)

	// request 2 — Bob from a small retail account
	checkoutResult2 := checkoutV2.Get(ctx, createContext(bob, smallRetailAccount)...)
	fmt.Printf("checkout-v2 = %v\n", checkoutResult2)

	bannerResult2 := bannerColor.Get(ctx, createContext(bob, smallRetailAccount)...)
	fmt.Printf("banner-color = %v\n", bannerResult2)

	retriesResult2 := maxRetries.Get(ctx, createContext(bob, smallRetailAccount)...)
	fmt.Printf("max-retries = %v\n", retriesResult2)

	// get a flag's value (explicitly pass context)
	explicitResult := checkoutV2.Get(ctx,
		smplkit.NewContext("user", "john.smith@acme.com", nil,
			smplkit.WithAttr("plan", "free"),
			smplkit.WithAttr("beta_tester", false),
		),
		smplkit.NewContext("account", "1111", nil,
			smplkit.WithAttr("region", "jp"),
		),
	)
	fmt.Printf("checkout-v2 (free, JP) = %v\n", explicitResult)

	// simulate someone making changes to a flag to trigger listeners
	updateRules(ctx, client)

	// wait a moment for the event to be delivered
	time.Sleep(200 * time.Millisecond)

	// verify both listeners fired
	if atomic.LoadInt64(&allChanges) < 1 {
		fatalIfErr("allChanges", fmt.Errorf("expected at least one global change"))
	}
	if atomic.LoadInt64(&bannerChanges) < 1 {
		fatalIfErr("bannerChanges", fmt.Errorf("expected at least one banner change"))
	}

	cleanupFlagsRuntimeShowcase(ctx, client.Manage())
	fmt.Println("Done!")
}

func updateRules(ctx context.Context, client *smplkit.Client) {
	currentBanner, err := client.Manage().Flags().Get(ctx, "banner-color")
	fatalIfErr("get banner-color", err)
	fatalIfErr("add small rule", currentBanner.AddRule(
		smplkit.NewRule("Red for small companies").
			Environment("staging").
			When("account.employee_count", "<", 50).
			Serve("red").Build(),
	))
	fatalIfErr("save banner-color", currentBanner.Save(ctx))
}
