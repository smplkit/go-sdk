//go:build ignore

package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk"
)

// setupDemoFlags creates and configures three demo flags for the runtime showcase.
// Returns [checkoutFlag, bannerFlag, retryFlag].
func setupDemoFlags(ctx context.Context, client *smplkit.Client) ([]*smplkit.Flag, error) {
	// 1. checkout-v2 — boolean
	checkoutFlag, err := client.Flags().Create(ctx, smplkit.CreateFlagParams{
		Key:         "checkout-v2",
		Name:        "Checkout V2",
		Type:        smplkit.FlagTypeBoolean,
		Default:     false,
		Description: strPtr("Controls rollout of the new checkout experience."),
	})
	if err != nil {
		return nil, fmt.Errorf("create checkout-v2: %w", err)
	}
	err = checkoutFlag.Update(ctx, smplkit.UpdateFlagParams{
		Environments: map[string]interface{}{
			"staging": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					smplkit.NewRule("Enable for enterprise users in US region").
						When("user.plan", "==", "enterprise").
						When("account.region", "==", "us").
						Serve(true).
						Build(),
					smplkit.NewRule("Enable for beta testers").
						When("user.beta_tester", "==", true).
						Serve(true).
						Build(),
				},
			},
			"production": map[string]interface{}{
				"enabled": false,
				"default": false,
				"rules":   []interface{}{},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("update checkout-v2: %w", err)
	}

	// 2. banner-color — string
	bannerFlag, err := client.Flags().Create(ctx, smplkit.CreateFlagParams{
		Key:         "banner-color",
		Name:        "Banner Color",
		Type:        smplkit.FlagTypeString,
		Default:     "red",
		Description: strPtr("Controls the banner color shown to users."),
		Values: []smplkit.FlagValue{
			{Name: "Red", Value: "red"},
			{Name: "Green", Value: "green"},
			{Name: "Blue", Value: "blue"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create banner-color: %w", err)
	}
	err = bannerFlag.Update(ctx, smplkit.UpdateFlagParams{
		Environments: map[string]interface{}{
			"staging": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					smplkit.NewRule("Blue for enterprise users").
						When("user.plan", "==", "enterprise").
						Serve("blue").
						Build(),
					smplkit.NewRule("Green for technology companies").
						When("account.industry", "==", "technology").
						Serve("green").
						Build(),
				},
			},
			"production": map[string]interface{}{
				"enabled": true,
				"default": "blue",
				"rules":   []interface{}{},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("update banner-color: %w", err)
	}

	// 3. max-retries — numeric
	retryFlag, err := client.Flags().Create(ctx, smplkit.CreateFlagParams{
		Key:         "max-retries",
		Name:        "Max Retries",
		Type:        smplkit.FlagTypeNumeric,
		Default:     3,
		Description: strPtr("Maximum number of API retries before failing."),
		Values: []smplkit.FlagValue{
			{Name: "Low (1)", Value: 1},
			{Name: "Standard (3)", Value: 3},
			{Name: "High (5)", Value: 5},
			{Name: "Aggressive (10)", Value: 10},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create max-retries: %w", err)
	}
	err = retryFlag.Update(ctx, smplkit.UpdateFlagParams{
		Environments: map[string]interface{}{
			"staging": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					smplkit.NewRule("High retries for large accounts").
						When("account.employee_count", ">", 100).
						Serve(5).
						Build(),
				},
			},
			"production": map[string]interface{}{
				"enabled": true,
				"rules":   []interface{}{},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("update max-retries: %w", err)
	}

	return []*smplkit.Flag{checkoutFlag, bannerFlag, retryFlag}, nil
}

// teardownDemoFlags deletes the demo flags and any auto-created context types.
func teardownDemoFlags(ctx context.Context, client *smplkit.Client, flags []*smplkit.Flag) {
	for _, f := range flags {
		_ = client.Flags().Delete(ctx, f.ID)
	}
	if cts, err := client.Flags().ListContextTypes(ctx); err == nil {
		for _, ct := range cts {
			_ = client.Flags().DeleteContextType(ctx, ct.ID)
		}
	}
}
