//go:build ignore

// Management Showcase — end-to-end walkthrough of the smplkit management API.
//
// Demonstrates client.Management():
//   - Environments: list, create (AD_HOC), update in-place, delete
//   - ContextTypes: create, list, mutate attributes, update, delete
//   - Contexts: register (buffered + flush), list, get, delete
//   - AccountSettings: read, mutate, save
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk
//   - SMPLKIT_API_KEY set (or ~/.smplkit config file)
//   - The smplkit app service running and reachable
//
// Usage:
//
//	go run examples/management_showcase.go examples/helpers.go
package main

import (
	"context"
	"fmt"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	ctx := context.Background()

	// ====================================================================
	// 1. SDK INITIALIZATION
	// ====================================================================
	section("1. SDK Initialization")

	client, err := smplkit.NewClient(smplkit.Config{Environment: "production", Service: "management-showcase"})
	if err != nil {
		fatal("NewClient failed", err)
	}
	defer client.Close()
	step("smplkit.Client initialized")

	mgmt := client.Management()

	// Best-effort cleanup of any leftovers from previous runs.
	step("Cleaning up leftovers from previous runs")
	_ = mgmt.Environments().Delete(ctx, "showcase_adhoc")
	for _, id := range []string{"user", "account", "device"} {
		_ = mgmt.ContextTypes().Delete(ctx, id)
	}
	fmt.Println("  Cleanup complete")

	// ====================================================================
	// 2a. LIST BUILT-IN ENVIRONMENTS
	// ====================================================================
	section("2a. List built-in environments")

	envs, err := mgmt.Environments().List(ctx)
	if err != nil {
		fatal("List environments failed", err)
	}
	for _, e := range envs {
		fmt.Printf("  id=%q name=%q classification=%q\n", e.ID, e.Name, e.Classification)
	}

	// ====================================================================
	// 2b. CREATE AN AD_HOC ENVIRONMENT
	// ====================================================================
	section("2b. Create an AD_HOC environment")

	color := "#8b5cf6"
	preview := mgmt.Environments().New("showcase_adhoc", "Showcase Ad-Hoc",
		smplkit.WithEnvironmentColor(color),
		smplkit.WithEnvironmentClassification(smplkit.EnvironmentClassificationAdHoc),
	)
	if err := preview.Save(ctx); err != nil {
		fatal("Create environment failed", err)
	}
	step(fmt.Sprintf("Created: id=%q classification=%q", preview.ID, preview.Classification))

	// ====================================================================
	// 2c. UPDATE AN ENVIRONMENT IN PLACE
	// ====================================================================
	section("2c. Update an environment in place")

	prod, err := mgmt.Environments().Get(ctx, "production")
	if err != nil {
		fatal("Get environment failed", err)
	}
	newColor := "#ef4444"
	prod.Color = &newColor
	if err := prod.Save(ctx); err != nil {
		fatal("Update environment failed", err)
	}
	step(fmt.Sprintf("Updated: id=%q color=%q", prod.ID, *prod.Color))

	// ====================================================================
	// 3a. CREATE CONTEXT TYPES
	// ====================================================================
	section("3a. Create context types")

	userCT := mgmt.ContextTypes().New("user", smplkit.WithContextTypeName("User"))
	for _, attr := range []string{"plan", "region", "beta_tester", "signup_date", "account_age_days"} {
		userCT.AddAttribute(attr)
	}
	if err := userCT.Save(ctx); err != nil {
		fatal("Create user context type failed", err)
	}
	step(fmt.Sprintf("user: attributes=%v", attributeKeys(userCT.Attributes)))

	accountCT := mgmt.ContextTypes().New("account", smplkit.WithContextTypeName("Account"))
	for _, attr := range []string{"tier", "industry", "region", "employee_count", "annual_revenue"} {
		accountCT.AddAttribute(attr)
	}
	if err := accountCT.Save(ctx); err != nil {
		fatal("Create account context type failed", err)
	}

	deviceCT := mgmt.ContextTypes().New("device", smplkit.WithContextTypeName("Device"))
	for _, attr := range []string{"os", "version", "type"} {
		deviceCT.AddAttribute(attr)
	}
	if err := deviceCT.Save(ctx); err != nil {
		fatal("Create device context type failed", err)
	}
	step("account, device created")

	// ====================================================================
	// 3b. LIST + MUTATE AN EXISTING CONTEXT TYPE
	// ====================================================================
	section("3b. List + mutate an existing context type")

	ctList, err := mgmt.ContextTypes().List(ctx)
	if err != nil {
		fatal("List context types failed", err)
	}
	for _, ct := range ctList {
		fmt.Printf("  id=%q name=%q\n", ct.ID, ct.Name)
	}

	existing, err := mgmt.ContextTypes().Get(ctx, "user")
	if err != nil {
		fatal("Get context type failed", err)
	}
	existing.AddAttribute("lifetime_value")
	existing.RemoveAttribute("account_age_days")
	if err := existing.Save(ctx); err != nil {
		fatal("Update context type failed", err)
	}
	step(fmt.Sprintf("user attributes now: %v", attributeKeys(existing.Attributes)))

	// ====================================================================
	// 4a. REGISTER CONTEXTS (immediate flush)
	// ====================================================================
	section("4a. Register contexts (immediate flush)")

	err = mgmt.Contexts().Register(ctx,
		[]smplkit.Context{
			smplkit.NewContext("user", "usr_a1b2c3", map[string]interface{}{"plan": "free", "region": "us"}),
			smplkit.NewContext("user", "usr_d4e5f6", map[string]interface{}{"plan": "enterprise", "region": "eu"}),
			smplkit.NewContext("account", "acct_acme_inc", map[string]interface{}{"tier": "enterprise", "industry": "retail"}),
		},
		smplkit.WithContextFlush(),
	)
	if err != nil {
		fatal("Register contexts failed", err)
	}
	step("3 contexts registered + flushed")

	// ====================================================================
	// 4b. LIST CONTEXTS BY TYPE
	// ====================================================================
	section("4b. List contexts of a single type")

	userContexts, err := mgmt.Contexts().List(ctx, "user")
	if err != nil {
		fatal("List contexts failed", err)
	}
	for _, c := range userContexts {
		fmt.Printf("  type=%q key=%q attributes=%v\n", c.ContextType, c.Key, c.Attributes)
	}

	// ====================================================================
	// 4c. GET + DELETE BY COMPOSITE ID (or by (type, key))
	// ====================================================================
	section("4c. Get + delete by composite id (or by (type, key))")

	one, err := mgmt.Contexts().Get(ctx, "user:usr_a1b2c3")
	if err != nil {
		fatal("Get context failed", err)
	}
	step(fmt.Sprintf("got: %s", one.CompositeID()))

	same, err := mgmt.Contexts().Get(ctx, "user", "usr_a1b2c3")
	if err != nil {
		fatal("Get context by (type, key) failed", err)
	}
	step(fmt.Sprintf("got via (type, key): %s", same.CompositeID()))

	if err := mgmt.Contexts().Delete(ctx, "user:usr_a1b2c3"); err != nil {
		fatal("Delete context failed", err)
	}
	step("deleted user:usr_a1b2c3")

	// ====================================================================
	// 5a. READ ACCOUNT SETTINGS
	// ====================================================================
	section("5a. Read account settings")

	settings, err := mgmt.AccountSettings().Get(ctx)
	if err != nil {
		fatal("Get account settings failed", err)
	}
	step(fmt.Sprintf("environment_order=%v", settings.EnvironmentOrder()))
	step(fmt.Sprintf("raw=%v", settings.Raw))

	// ====================================================================
	// 5b. MUTATE + SAVE (ACTIVE RECORD)
	// ====================================================================
	section("5b. Mutate + save (active record)")

	settings.SetEnvironmentOrder([]string{"production", "staging", "development"})
	if err := settings.Save(ctx); err != nil {
		fatal("Save account settings failed", err)
	}
	step(fmt.Sprintf("saved: environment_order=%v", settings.EnvironmentOrder()))

	// ====================================================================
	// 6. CLEANUP
	// ====================================================================
	section("6. Cleanup")

	userCtxs, err := mgmt.Contexts().List(ctx, "user")
	if err != nil {
		fatal("List user contexts failed", err)
	}
	for _, c := range userCtxs {
		if err := mgmt.Contexts().Delete(ctx, c.ContextType, c.Key); err != nil {
			fmt.Printf("  Warning: failed to delete %s: %v\n", c.CompositeID(), err)
		}
	}
	acctCtxs, err := mgmt.Contexts().List(ctx, "account")
	if err != nil {
		fatal("List account contexts failed", err)
	}
	for _, c := range acctCtxs {
		if err := mgmt.Contexts().Delete(ctx, c.ContextType, c.Key); err != nil {
			fmt.Printf("  Warning: failed to delete %s: %v\n", c.CompositeID(), err)
		}
	}

	for _, id := range []string{"user", "account", "device"} {
		if err := mgmt.ContextTypes().Delete(ctx, id); err != nil {
			fmt.Printf("  Warning: failed to delete context type %s: %v\n", id, err)
		} else {
			fmt.Printf("  Deleted context type: %s\n", id)
		}
	}

	if err := mgmt.Environments().Delete(ctx, "showcase_adhoc"); err != nil {
		fmt.Printf("  Warning: failed to delete showcase_adhoc: %v\n", err)
	} else {
		fmt.Println("  Deleted environment: showcase_adhoc")
	}

	// ====================================================================
	// SUMMARY
	// ====================================================================
	section("Management Showcase Complete")
	fmt.Println("  Features exercised:")
	fmt.Println("  [x] Environments().New() + Save() — create AD_HOC environment")
	fmt.Println("  [x] Environments().List() / Get() — retrieve environments")
	fmt.Println("  [x] Environment.Save() on existing — update in-place")
	fmt.Println("  [x] Environments().Delete() — cleanup")
	fmt.Println("  [x] ContextTypes().New() + Save() — create with attributes")
	fmt.Println("  [x] ContextTypes().List() / Get() — retrieve types")
	fmt.Println("  [x] AddAttribute / RemoveAttribute + Save() — mutate")
	fmt.Println("  [x] ContextTypes().Delete() — cleanup")
	fmt.Println("  [x] Contexts().Register(..., WithContextFlush()) — immediate register")
	fmt.Println("  [x] Contexts().List(type) — list by context type")
	fmt.Println("  [x] Contexts().Get(composite) / Get(type, key) — dual overload")
	fmt.Println("  [x] Contexts().Delete(composite) — cleanup")
	fmt.Println("  [x] AccountSettings().Get() — read settings")
	fmt.Println("  [x] SetEnvironmentOrder + Save() — update settings")
}

// attributeKeys returns the sorted attribute keys from a context type attribute map.
func attributeKeys(attrs map[string]map[string]interface{}) []string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	return keys
}
