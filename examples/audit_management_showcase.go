//go:build ignore

// Demonstrates the smplkit management SDK for Smpl Audit.
//
// Covers: forwarder create / get / list / update / delete.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//   - The Pro tier is required. The showcase gracefully skips on a 402
//     (free / standard tier) so it stays runnable in any environment.
//
// Usage:
//
//	make audit_management_showcase
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// JSON Logic filter — only forward invoice.* actions.
// Events that don't match are recorded as filtered_out deliveries.
// See https://jsonlogic.com for the full operator reference.
var invoiceFilter = map[string]interface{}{
	"in": []interface{}{"invoice.", map[string]interface{}{"var": "action"}},
}

// JSONata template — reshape the event payload before POSTing to the
// destination. See https://jsonata.org for the full language reference.
const siemTransform = `
{
    "event": action,
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

	// create a forwarder
	forwarder, err := manage.Audit().Forwarders().Create(ctx, smplkit.CreateForwarderInput{
		Name:          forwarderName,
		ForwarderType: smplkit.ForwarderTypeHTTP,
		Enabled:       true,
		HTTP: smplkit.ForwarderHttp{
			Method:        "POST",
			URL:           "https://httpbin.org/post",
			Headers:       []smplkit.HttpHeader{{Name: "X-Showcase", Value: "ok"}},
			SuccessStatus: "2xx",
		},
		Filter:    invoiceFilter,
		Transform: siemTransform,
	})
	if err != nil {
		var pre *smplkit.PaymentRequiredError
		if errors.As(err, &pre) {
			fmt.Println("Skipping forwarder showcase — account is not Pro tier")
			fmt.Println("Done!")
			return
		}
		fatalIfErr("audit.Forwarders.Create", err)
	}
	if forwarder.Name != forwarderName || !forwarder.Enabled || forwarder.Filter == nil || forwarder.Transform == nil {
		fatalIfErr("assertion", fmt.Errorf("forwarder fields mismatch: %+v", forwarder))
	}
	fmt.Printf("Created forwarder: %s\n", forwarder.Name)

	defer func() {
		if delErr := manage.Audit().Forwarders().Delete(ctx, forwarder.ID); delErr != nil {
			fmt.Printf("warning: failed to delete forwarder %s: %v\n", forwarder.Name, delErr)
		} else {
			fmt.Printf("Deleted forwarder: %s\n", forwarder.Name)
		}
	}()

	// fetch a forwarder
	fetched, err := manage.Audit().Forwarders().Get(ctx, forwarder.ID)
	fatalIfErr("audit.Forwarders.Get", err)
	if fetched.ID != forwarder.ID || fetched.Name != forwarderName || fetched.Filter == nil || fetched.Transform == nil {
		fatalIfErr("assertion", fmt.Errorf("fetched forwarder fields mismatch: %+v", fetched))
	}
	fmt.Printf("Fetched forwarder: %s\n", fetched.Name)

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

	// update a forwarder
	renamed := forwarder.Name + "-renamed"
	updated, err := manage.Audit().Forwarders().Update(ctx, forwarder.ID, smplkit.UpdateForwarderInput{
		Name:          renamed,
		ForwarderType: forwarder.ForwarderType,
		Enabled:       false,
		HTTP: smplkit.ForwarderHttp{
			Method:        "POST",
			URL:           "https://httpbin.org/post",
			Headers:       []smplkit.HttpHeader{{Name: "X-Showcase", Value: "ok"}},
			SuccessStatus: "2xx",
		},
		Filter:    invoiceFilter,
		Transform: siemTransform,
	})
	fatalIfErr("audit.Forwarders.Update", err)
	if updated.Name != renamed || updated.Enabled {
		fatalIfErr("assertion", fmt.Errorf("updated forwarder fields mismatch: %+v", updated))
	}
	fmt.Printf("Updated forwarder: %s (enabled=%t)\n", updated.Name, updated.Enabled)

	remaining, err := manage.Audit().Forwarders().List(ctx, smplkit.ListForwardersInput{})
	fatalIfErr("audit.Forwarders.List after delete", err)
	_ = remaining

	fmt.Println("Done!")
}

func randomHexMgmt(nBytes int) string {
	buf := make([]byte, nBytes)
	_, err := rand.Read(buf)
	fatalIfErr("rand.Read", err)
	return hex.EncodeToString(buf)
}
