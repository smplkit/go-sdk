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
	checkoutFlag := client.Flags().Management().NewBooleanFlag("checkout-v2", false,
		smplkit.WithFlagName("Checkout V2"),
		smplkit.WithFlagDescription("Controls rollout of the new checkout experience."),
	)
	if err := checkoutFlag.Save(ctx); err != nil {
		return nil, fmt.Errorf("create checkout-v2: %w", err)
	}
	checkoutFlag.SetEnvironmentEnabled("staging", true)
	checkoutFlag.AddRule(smplkit.NewRule("Enable for enterprise users in US region").
		Environment("staging").
		When("user.plan", "==", "enterprise").
		When("account.region", "==", "us").
		Serve(true).
		Build())
	checkoutFlag.AddRule(smplkit.NewRule("Enable for beta testers").
		Environment("staging").
		When("user.beta_tester", "==", true).
		Serve(true).
		Build())
	checkoutFlag.SetEnvironmentEnabled("production", false)
	checkoutFlag.SetEnvironmentDefault("production", false)
	checkoutFlag.ClearRules("production")
	if err := checkoutFlag.Save(ctx); err != nil {
		return nil, fmt.Errorf("update checkout-v2: %w", err)
	}

	// 2. banner-color — string
	bannerFlag := client.Flags().Management().NewStringFlag("banner-color", "red",
		smplkit.WithFlagName("Banner Color"),
		smplkit.WithFlagDescription("Controls the banner color shown to users."),
		smplkit.WithFlagValues([]smplkit.FlagValue{
			{Name: "Red", Value: "red"},
			{Name: "Green", Value: "green"},
			{Name: "Blue", Value: "blue"},
		}),
	)
	if err := bannerFlag.Save(ctx); err != nil {
		return nil, fmt.Errorf("create banner-color: %w", err)
	}
	bannerFlag.SetEnvironmentEnabled("staging", true)
	bannerFlag.AddRule(smplkit.NewRule("Blue for enterprise users").
		Environment("staging").
		When("user.plan", "==", "enterprise").
		Serve("blue").
		Build())
	bannerFlag.AddRule(smplkit.NewRule("Green for technology companies").
		Environment("staging").
		When("account.industry", "==", "technology").
		Serve("green").
		Build())
	bannerFlag.SetEnvironmentEnabled("production", true)
	bannerFlag.SetEnvironmentDefault("production", "blue")
	bannerFlag.ClearRules("production")
	if err := bannerFlag.Save(ctx); err != nil {
		return nil, fmt.Errorf("update banner-color: %w", err)
	}

	// 3. max-retries — numeric (unconstrained)
	retryFlag := client.Flags().Management().NewNumberFlag("max-retries", 3,
		smplkit.WithFlagName("Max Retries"),
		smplkit.WithFlagDescription("Maximum number of API retries before failing."),
	)
	if err := retryFlag.Save(ctx); err != nil {
		return nil, fmt.Errorf("create max-retries: %w", err)
	}
	retryFlag.SetEnvironmentEnabled("staging", true)
	retryFlag.AddRule(smplkit.NewRule("High retries for large accounts").
		Environment("staging").
		When("account.employee_count", ">", 100).
		Serve(5).
		Build())
	retryFlag.SetEnvironmentEnabled("production", true)
	retryFlag.ClearRules("production")
	if err := retryFlag.Save(ctx); err != nil {
		return nil, fmt.Errorf("update max-retries: %w", err)
	}

	return []*smplkit.Flag{checkoutFlag, bannerFlag, retryFlag}, nil
}

// teardownDemoFlags deletes the demo flags and any auto-created context types.
func teardownDemoFlags(ctx context.Context, client *smplkit.Client, flags []*smplkit.Flag) {
	for _, f := range flags {
		_ = client.Flags().Management().Delete(ctx, f.ID)
	}
	if cts, err := client.Flags().Management().ListContextTypes(ctx); err == nil {
		for _, ct := range cts {
			_ = client.Flags().Management().DeleteContextType(ctx, ct.ID)
		}
	}
}
