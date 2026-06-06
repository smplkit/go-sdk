//go:build ignore

// Demonstrates the smplkit management SDK for Smpl Audit.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//
// Usage:
//
//	make audit_management_showcase
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// JSON Logic filter — only forward invoice.* event types.
// Events that don't match the filter aren't forwarded (and produce no delivery record).
// See https://jsonlogic.com for the full operator reference.
var invoiceFilter = map[string]interface{}{
	"in": []interface{}{"invoice.", map[string]interface{}{"var": "event_type"}},
}

// JSONata template — reshape the event payload before POSTing to the
// destination. This example flattens the event into a compact SIEM-style
// record. See https://jsonata.org for the full language reference.
const siemTransform = `
{
    "event": event_type,
    "subject": resource_type & ":" & resource_id,
    "ts": occurred_at,
    "actor": actor_label
}
`

func main() {
	ctx := context.Background()

	// create the management client
	manage, err := smplkit.NewManagementClient(smplkit.ManagementConfig{})
	fatalIfErr("create management client", err)

	forwarderName := "showcase-" + randomHexMgmt(3)

	// create a new forwarder, enabled in the production environment.
	//
	// Enablement is per-environment (ADR-055): a forwarder delivers in an
	// environment only when that environment has an entry in the
	// Environments map with Enabled=true. The base `enabled` field is
	// read-only and always false. Every referenced environment must exist
	// and be managed for the account.
	forwarder := manage.Audit().Forwarders().New(
		forwarderName,
		forwarderName,
		smplkit.ForwarderTypeHTTP,
		smplkit.HttpConfiguration{
			Method:  smplkit.HttpMethodPost,
			URL:     "https://httpbin.org/post",
			Headers: []smplkit.HttpHeader{{Name: "X-Showcase", Value: "ok"}},
		},
		smplkit.WithForwarderEnvironments(map[string]smplkit.ForwarderEnvironment{
			"production": {Enabled: true},
		}),
		smplkit.WithForwarderFilter(invoiceFilter),
		smplkit.WithForwarderTransform(smplkit.ForwarderTransformTypeJSONata, siemTransform),
	)
	fatalIfErr("audit.Forwarders.Save", forwarder.Save(ctx))
	fmt.Printf("Created forwarder: %s (id=%s)\n", forwarder.Name, forwarder.ID)

	// list forwarders
	listed, err := manage.Audit().Forwarders().List(ctx, smplkit.ListForwardersInput{})
	fatalIfErr("audit.Forwarders.List", err)
	found := false
	for _, f := range listed.Forwarders {
		if f.ID == forwarder.ID {
			found = true
			break
		}
	}
	if !found {
		fatalIfErr("assertion", fmt.Errorf("forwarder %s not found in list", forwarder.ID))
	}
	fmt.Printf("Account has %d forwarder(s)\n", len(listed.Forwarders))

	// get a forwarder
	fetched, err := manage.Audit().Forwarders().Get(ctx, forwarder.ID)
	fatalIfErr("audit.Forwarders.Get", err)
	prod, prodEnabled := fetched.Environments["production"]
	if fetched.ID != forwarder.ID || !prodEnabled || !prod.Enabled {
		fatalIfErr("assertion", fmt.Errorf("fetched forwarder fields mismatch: %+v", fetched))
	}
	fmt.Printf("Fetched forwarder: %s (production enabled=%t)\n", fetched.Name, prod.Enabled)

	// update a forwarder — disable delivery in production.
	//
	// Header values are returned redacted on reads, so re-supply the real
	// values before saving; this showcase clears the override (none set
	// here) and toggles the production entry off.
	fetched.Configuration.Headers = []smplkit.HttpHeader{{Name: "X-Showcase", Value: "ok"}}
	fetched.Environments["production"] = smplkit.ForwarderEnvironment{Enabled: false}
	fatalIfErr("audit.Forwarders.Save", fetched.Save(ctx))
	if fetched.Environments["production"].Enabled {
		fatalIfErr("assertion", fmt.Errorf("updated forwarder fields mismatch: %+v", fetched))
	}
	fmt.Printf("Disabled forwarder in production: %s\n", fetched.Name)

	// delete a forwarder
	fatalIfErr("audit.Forwarders.Delete", fetched.Delete(ctx))
	remaining, err := manage.Audit().Forwarders().List(ctx, smplkit.ListForwardersInput{})
	fatalIfErr("audit.Forwarders.List", err)
	for _, f := range remaining.Forwarders {
		if f.ID == fetched.ID {
			fatalIfErr("assertion", fmt.Errorf("forwarder %s still present after delete", fetched.ID))
		}
	}
	fmt.Printf("Deleted forwarder: %s\n", fetched.Name)

	fmt.Println("Done!")
}

func randomHexMgmt(nBytes int) string {
	buf := make([]byte, nBytes)
	_, err := rand.Read(buf)
	fatalIfErr("rand.Read", err)
	return hex.EncodeToString(buf)
}
