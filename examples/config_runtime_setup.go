//go:build ignore

// Setup and simulation helpers for config_runtime_showcase.go.
//
// The runtime showcase declares its own configs via
// client.Config().Bind, so this helper only handles cleanup and the
// live admin-override simulation that stands in for an operator
// editing values in the smplkit console.
package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

var configRuntimeDemoConfigIDs = []string{
	"showcase-billing",
	"showcase-common",
	"showcase-database",
}

func simulateAdminOverride(ctx context.Context, mgmt *smplkit.ManagementClient) {
	// Real customers never read back through the management API
	// immediately after binding via the runtime client — this is a
	// simulation-only step. Push pending runtime-side registrations
	// through so the lookup below can find the freshly-declared config.
	fatalIfErr("flush registrations", mgmt.Config().Flush(ctx))
	billing, err := mgmt.Config().Get(ctx, "showcase-billing")
	fatalIfErr("get billing", err)
	billing.SetNumber("max_seats", 25, "production")
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
