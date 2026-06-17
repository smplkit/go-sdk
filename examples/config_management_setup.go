//go:build ignore

// Setup / cleanup helpers for config_management_showcase.go.
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
var configMgmtDemoConfigIDs = []string{
	"showcase-billing",      // child of showcase-common (runtime showcase)
	"showcase-user-service", // child of showcase-common (management showcase)
	"showcase-database",     // root (runtime showcase)
	"showcase-common",       // shared parent — must be deleted last
}

func setupConfigManagementShowcase(ctx context.Context, config *smplkit.ConfigClient) {
	cleanupConfigManagementShowcase(ctx, config)
}

func cleanupConfigManagementShowcase(ctx context.Context, config *smplkit.ConfigClient) {
	for _, id := range configMgmtDemoConfigIDs {
		if err := config.Delete(ctx, id); err != nil {
			var nf *smplkit.NotFoundError
			if !errors.As(err, &nf) {
				fatalIfErr("delete config "+id, err)
			}
		}
	}
}
