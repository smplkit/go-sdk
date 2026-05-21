//go:build ignore

// Setup, simulation, and cleanup helpers for config_runtime_showcase.go.
//
// The runtime showcase is intentionally runtime-only — declarations,
// typed getters, change listeners. In a real deployment the configs
// would either already exist (admin-curated) or be created by the
// SDK's discovery on first run. Here we pre-create them through the
// management API so the showcase can also demonstrate a live admin
// override end-to-end in a single process.
package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

var configRuntimeDemoConfigIDs = []string{"showcase-billing", "showcase-common"}

func setupConfigRuntimeShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	cleanupConfigRuntimeShowcase(ctx, mgmt)

	common := mgmt.Config().New("showcase-common",
		smplkit.WithConfigDescription("Shared defaults for showcase services."),
	)
	common.SetString("app.name", "Acme SaaS", "")
	common.SetString("support.email", "support@acme.dev", "")
	fatalIfErr("save common", common.Save(ctx))

	billing := mgmt.Config().New("showcase-billing",
		smplkit.WithConfigDescription("Plan-limit configuration for billing."),
		smplkit.WithConfigParent(common.ID),
	)
	billing.SetNumber("plan.max_seats", 5, "")
	billing.SetNumber("plan.trial_days", 14, "")
	billing.SetString("plan.tier", "free", "")
	fatalIfErr("save billing", billing.Save(ctx))
}

func simulateAdminOverride(ctx context.Context, mgmt *smplkit.ManagementClient) {
	billing, err := mgmt.Config().Get(ctx, "showcase-billing")
	fatalIfErr("get billing", err)
	billing.SetNumber("plan.max_seats", 25, "production")
	fatalIfErr("save billing", billing.Save(ctx))
}

func cleanupConfigRuntimeShowcase(ctx context.Context, mgmt *smplkit.ManagementClient) {
	for _, id := range configRuntimeDemoConfigIDs {
		if err := mgmt.Config().Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete config "+id, err)
			}
		}
	}
}
