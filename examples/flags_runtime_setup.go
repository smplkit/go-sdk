//go:build ignore

package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

var flagsRuntimeDemoFlagIDs = []string{"checkout-v2", "banner-color", "max-retries"}

func setupFlagsRuntimeShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	cleanupFlagsRuntimeShowcase(ctx, mgmt)

	checkout := mgmt.Flags().NewBooleanFlag("checkout-v2", false,
		smplkit.WithFlagDescription("Controls rollout of the new checkout experience."),
	)
	checkout.EnableRules("production")
	fatalIfErr("rule us+enterprise", checkout.AddRule(
		smplkit.NewRule("Enable for enterprise users in US region").
			Environment("production").
			When("user.plan", "==", "enterprise").
			When("account.region", "==", "us").
			Serve(true).Build(),
	))
	fatalIfErr("rule beta", checkout.AddRule(
		smplkit.NewRule("Enable for beta testers").
			Environment("production").
			When("user.beta_tester", "==", true).
			Serve(true).Build(),
	))
	fatalIfErr("save checkout-v2", checkout.Save(ctx))

	banner := mgmt.Flags().NewStringFlag("banner-color", "red",
		smplkit.WithFlagName("Banner Color"),
		smplkit.WithFlagDescription("Controls the banner color shown to users."),
		smplkit.WithFlagValues([]smplkit.FlagValue{
			{Name: "Red", Value: "red"},
			{Name: "Green", Value: "green"},
			{Name: "Blue", Value: "blue"},
		}),
	)
	banner.EnableRules("production")
	fatalIfErr("rule banner enterprise", banner.AddRule(
		smplkit.NewRule("Blue for enterprise users").
			Environment("production").
			When("user.plan", "==", "enterprise").
			Serve("blue").Build(),
	))
	fatalIfErr("rule banner technology", banner.AddRule(
		smplkit.NewRule("Green for technology companies").
			Environment("production").
			When("account.industry", "==", "technology").
			Serve("green").Build(),
	))
	fatalIfErr("save banner-color", banner.Save(ctx))

	retries := mgmt.Flags().NewNumberFlag("max-retries", 3,
		smplkit.WithFlagDescription("Maximum number of API retries before failing."),
	)
	retries.EnableRules("production")
	fatalIfErr("rule retries large", retries.AddRule(
		smplkit.NewRule("High retries for large accounts").
			Environment("production").
			When("account.employee_count", ">", 100).
			Serve(5).Build(),
	))
	fatalIfErr("save max-retries", retries.Save(ctx))
}

func cleanupFlagsRuntimeShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	for _, id := range flagsRuntimeDemoFlagIDs {
		if err := mgmt.Flags().Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete flag "+id, err)
			}
		}
	}
}
