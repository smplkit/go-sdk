//go:build ignore

// Setup and simulation helpers for config_runtime_showcase.go.
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

func simulateAdminOverride(ctx context.Context, config *smplkit.ConfigClient) {
	// Push pending runtime-side registrations through so the lookup below
	// can find the freshly-declared config.
	fatalIfErr("flush registrations", config.Flush(ctx))
	billing, err := config.Get(ctx, "showcase-billing")
	fatalIfErr("get billing", err)
	billing.SetNumber("plan.max_seats", 25, "production")
	fatalIfErr("save billing", billing.Save(ctx))
}

func cleanupConfigRuntimeShowcase(ctx context.Context, config *smplkit.ConfigClient) {
	for _, id := range configRuntimeDemoConfigIDs {
		if err := config.Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete config "+id, err)
			}
		}
	}
}
