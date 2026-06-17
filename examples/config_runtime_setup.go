//go:build ignore

// Setup and simulation helpers for config_runtime_showcase.go.
package main

import (
	"context"
	"errors"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// Complete, dependency-ordered list of every config the config showcases
// create. Children are listed before the shared "showcase-common" parent so
// cleanup never trips the "config referenced as parent" conflict — even when a
// prior run crashed mid-way and left a sibling showcase's child orphaned.
var configRuntimeDemoConfigIDs = []string{
	"showcase-billing",      // child of showcase-common (runtime showcase)
	"showcase-user-service", // child of showcase-common (management showcase)
	"showcase-database",     // root (runtime showcase)
	"showcase-common",       // shared parent — must be deleted last
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
