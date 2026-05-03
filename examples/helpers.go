//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// fatalIfErr prints err and exits with status 1.
func fatalIfErr(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", msg, err)
		os.Exit(1)
	}
}

// ensureEnvironments ensures the named environments exist on the server.
// It never deletes environments — environment lifecycle is the customer's
// responsibility, not the showcase's (rule 15).
func ensureEnvironments(ctx context.Context, mgmt *smplkit.ManagementClient, ids ...string) {
	existing, err := mgmt.Environments().List(ctx)
	fatalIfErr("list environments", err)
	have := map[string]bool{}
	for _, e := range existing {
		have[e.ID] = true
	}
	for _, id := range ids {
		if have[id] {
			continue
		}
		env := mgmt.Environments().New(id, titleCase(id))
		fatalIfErr(fmt.Sprintf("create environment %q", id), env.Save(ctx))
	}
}

// titleCase returns id with the first letter uppercased — good enough for
// the demo names (showcase environments are always lowercase ids).
func titleCase(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}
