//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Audit.
//
// Covers: event record / list / get, plus the SIEM forwarders surface
// (create / list / delete + the test_forwarder/Execute proxy + a
// DoNotForward event flow).
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//   - The Pro tier is required for the forwarders portion. The
//     showcase gracefully skips those steps on a 402 (free / standard
//     tier) so it stays runnable in any environment.
//
// Usage:
//
//	make audit_runtime_showcase
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func main() {
	ctx := context.Background()

	// create the client
	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-service",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	// record an event
	someResourceID := "showcase-" + randomHex(4)
	now := time.Now().UTC()
	fatalIfErr("audit.Record invoice.created", client.Audit().Events().Record(smplkit.CreateEventInput{
		Action:       "invoice.created",
		ResourceType: "invoice",
		ResourceID:   someResourceID,
		OccurredAt:   &now,
		Data: map[string]interface{}{
			"snapshot":   map[string]interface{}{"total_cents": 4900, "currency": "USD"},
			"request_id": "req-abc",
		},
	}))

	// force the event to be posted (normally happens automatically, in the
	// background, but we want to force it to be written now for this demo)
	client.Audit().Events().Flush(2 * time.Second)

	// list events
	page, err := client.Audit().Events().List(ctx, smplkit.ListEventsInput{
		ResourceType: "invoice",
		ResourceID:   someResourceID,
		PageSize:     10,
	})
	fatalIfErr("audit.List", err)

	fmt.Printf("Found %d events for %s:\n", len(page.Events), someResourceID)
	for _, ev := range page.Events {
		fmt.Printf("  %s  id=%s  actor=%s\n", ev.Action, ev.ID, ev.ActorType)
	}

	if len(page.Events) != 1 {
		fatalIfErr("expect 1 event", fmt.Errorf("Expected 1 event, got %d", len(page.Events)))
	}

	// fetch an event by ID
	first, err := client.Audit().Events().Get(ctx, page.Events[0].ID)
	fatalIfErr("audit.Get", err)
	fmt.Printf("Round-tripped: %s at %s\n", first.Action, first.OccurredAt.Format(time.RFC3339))

	// Forwarders (Pro tier — gracefully skip on 402)
	fwdName := "showcase-" + randomHex(3)
	fwd, err := client.Audit().Forwarders().Create(ctx, smplkit.CreateForwarderInput{
		Name:          fwdName,
		ForwarderType: "HTTP",
		Enabled:       true,
		HTTP: smplkit.ForwarderHttp{
			Method:        "POST",
			URL:           "https://httpbin.org/post",
			Headers:       []smplkit.HttpHeader{{Name: "X-Showcase", Value: "ok"}},
			SuccessStatus: "2xx",
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), "status=402") {
			fmt.Println("Skipping forwarder showcase — account is not Pro tier")
			fmt.Println("Done!")
			return
		}
		fatalIfErr("audit.Forwarders.Create", err)
	}
	fmt.Printf("Created forwarder: %s\n", fwd.Slug)

	defer func() {
		if delErr := client.Audit().Forwarders().Delete(ctx, fwd.ID); delErr != nil {
			fmt.Printf("warning: failed to delete forwarder %s: %v\n", fwd.Slug, delErr)
		} else {
			fmt.Printf("Deleted forwarder: %s\n", fwd.Slug)
		}
	}()

	// DoNotForward event
	fatalIfErr("audit.Record DoNotForward", client.Audit().Events().Record(smplkit.CreateEventInput{
		Action:       "invoice.created",
		ResourceType: "invoice",
		ResourceID:   someResourceID + "-skipped",
		DoNotForward: true,
	}))
	client.Audit().Events().Flush(2 * time.Second)

	// Test the destination via the proxy
	body := `{"hello":"world"}`
	timeout := 5000
	test, err := client.Audit().Functions().TestForwarder().Actions().Execute(ctx, smplkit.TestForwarderInput{
		URL:           "https://httpbin.org/post",
		Body:          &body,
		SuccessStatus: "2xx",
		TimeoutMs:     &timeout,
	})
	fatalIfErr("audit.Functions.TestForwarder", err)
	statusStr := "<nil>"
	if test.ResponseStatus != nil {
		statusStr = fmt.Sprintf("%d", *test.ResponseStatus)
	}
	fmt.Printf("test_forwarder: succeeded=%t status=%s\n", test.Succeeded, statusStr)

	listed, err := client.Audit().Forwarders().List(ctx, smplkit.ListForwardersInput{PageSize: 5})
	fatalIfErr("audit.Forwarders.List", err)
	fmt.Printf("Account has %d active forwarders\n", len(listed.Forwarders))

	fmt.Println("Done!")
}

// randomHex returns nBytes worth of random hex chars (length 2*nBytes).
func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	_, err := rand.Read(buf)
	fatalIfErr("rand.Read", err)
	return hex.EncodeToString(buf)
}
