//go:build ignore

// Demonstrates the smplkit management SDK for Smpl Flags.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//
// Usage:
//
//	make flags_management_showcase
package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func main() {
	ctx := context.Background()

	// create the client (management-only — zero side effects)
	mgmt, err := smplkit.NewManagementClient(smplkit.ManagementConfig{})
	fatalIfErr("create management client", err)
	defer mgmt.Close()

	setupFlagsManagementShowcase(ctx, mgmt)

	// create a boolean flag
	checkoutFlag := mgmt.Flags().NewBooleanFlag("checkout-v2", false,
		smplkit.WithFlagDescription("Controls rollout of the new checkout experience."),
	)
	fatalIfErr("save checkout-v2", checkoutFlag.Save(ctx))
	fmt.Printf("Created flag: %s\n", checkoutFlag.ID)

	// create a string flag (constrained)
	bannerFlag := mgmt.Flags().NewStringFlag("banner-color", "red",
		smplkit.WithFlagName("Banner Color"),
		smplkit.WithFlagDescription("Controls the banner color shown to users."),
		smplkit.WithFlagValues([]smplkit.FlagValue{
			{Name: "Red", Value: "red"},
			{Name: "Green", Value: "green"},
			{Name: "Blue", Value: "blue"},
		}),
	)
	fatalIfErr("save banner-color", bannerFlag.Save(ctx))
	fmt.Printf("Created flag: %s\n", bannerFlag.ID)

	// create a numeric flag (unconstrained)
	retryFlag := mgmt.Flags().NewNumberFlag("max-retries", 3,
		smplkit.WithFlagDescription("Maximum number of API retries before failing."),
	)
	fatalIfErr("save max-retries", retryFlag.Save(ctx))
	fmt.Printf("Created flag: %s\n", retryFlag.ID)

	// create a JSON flag (constrained)
	themeFlag := mgmt.Flags().NewJsonFlag("ui-theme", map[string]interface{}{
		"mode":   "light",
		"accent": "#0066cc",
	},
		smplkit.WithFlagDescription("Controls the UI theme configuration."),
		smplkit.WithFlagValues([]smplkit.FlagValue{
			{Name: "Light", Value: map[string]interface{}{"mode": "light", "accent": "#0066cc"}},
			{Name: "Dark", Value: map[string]interface{}{"mode": "dark", "accent": "#66ccff"}},
			{Name: "High Contrast", Value: map[string]interface{}{"mode": "dark", "accent": "#ffffff"}},
		}),
	)
	fatalIfErr("save ui-theme", themeFlag.Save(ctx))
	fmt.Printf("Created flag: %s\n", themeFlag.ID)

	// checkoutFlag (serve true in staging to enterprise US users)
	checkoutFlag.EnableRules("staging")
	fatalIfErr("add rule 1", checkoutFlag.AddRule(
		smplkit.NewRule("Enable for enterprise users in US region").
			Environment("staging").
			When("user.plan", "==", "enterprise").
			When("account.region", "==", "us").
			Serve(true).
			Build(),
	))

	// checkoutFlag (serve true in staging for beta testers)
	fatalIfErr("add rule 2", checkoutFlag.AddRule(
		smplkit.NewRule("Enable for beta testers").
			Environment("staging").
			When("user.beta_tester", "==", true).
			Serve(true).
			Build(),
	))

	// checkoutFlag (disabled rules; serve false in production)
	checkoutFlag.DisableRules("production")
	checkoutFlag.SetDefault(false, "production")
	fatalIfErr("save checkout updates", checkoutFlag.Save(ctx))
	fmt.Printf("Updated flag: %s\n", checkoutFlag.ID)

	// list flags
	flags, err := mgmt.Flags().List(ctx)
	fatalIfErr("list flags", err)
	fmt.Printf("Total flags: %d\n", len(flags))
	for _, f := range flags {
		envs := make([]string, 0, len(f.Environments))
		for k := range f.Environments {
			envs = append(envs, k)
		}
		fmt.Printf("  %s (%s) - default=%v, environments=%v\n", f.ID, f.Type, f.Default, envs)
	}

	// get a flag
	fetched, err := mgmt.Flags().Get(ctx, "checkout-v2")
	fatalIfErr("get checkout-v2", err)
	fmt.Printf("\nFetched by id: %s\n", fetched.ID)
	stagingRules := 0
	if envData, ok := fetched.Environments["staging"].(map[string]interface{}); ok {
		if rules, ok := envData["rules"].([]interface{}); ok {
			stagingRules = len(rules)
		}
	}
	prodEnabled := false
	if envData, ok := fetched.Environments["production"].(map[string]interface{}); ok {
		if e, ok := envData["enabled"].(bool); ok {
			prodEnabled = e
		}
	}
	fmt.Printf("  staging rules: %d\n", stagingRules)
	fmt.Printf("  production enabled: %v\n", prodEnabled)

	// update a flag
	bannerFlag.AddValue("Purple", "purple")
	bannerFlag.Default = "blue"
	desc := "Controls the banner color - updated"
	bannerFlag.Description = &desc
	fatalIfErr("add purple rule", bannerFlag.AddRule(
		smplkit.NewRule("Purple for enterprise users").
			Environment("production").
			When("user.plan", "==", "enterprise").
			Serve("purple").
			Build(),
	))
	fatalIfErr("save banner updates", bannerFlag.Save(ctx))
	fmt.Printf("Updated flag: %s'\n", bannerFlag.ID)

	// delete all the rules of a flag
	checkoutFlag.ClearRules("staging")
	fatalIfErr("save cleared rules", checkoutFlag.Save(ctx))

	// revert production's default value back to the flag default
	checkoutFlag.ClearDefault("production")
	fatalIfErr("save cleared default", checkoutFlag.Save(ctx))
	fmt.Printf("Updated flag: %s'\n", checkoutFlag.ID)

	// clear values (flag becomes unconstrained)
	bannerFlag.ClearValues()
	fatalIfErr("save cleared values", bannerFlag.Save(ctx))
	fmt.Printf("Updated flag: %s'\n", bannerFlag.ID)

	// delete flags
	fatalIfErr("delete checkout-v2", mgmt.Flags().Delete(ctx, "checkout-v2"))
	fatalIfErr("delete banner-color", bannerFlag.Delete(ctx))
	fmt.Println("Deleted flags")

	// cleanup
	cleanupFlagsManagementShowcase(ctx, mgmt)
	fmt.Println("Done!")
}
