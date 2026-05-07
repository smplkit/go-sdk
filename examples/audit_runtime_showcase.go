//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Audit.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
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
		Snapshot:     map[string]interface{}{"total_cents": 4900, "currency": "USD"},
		Data:         map[string]interface{}{"request_id": "req-abc"},
	}))

	// force the event to be posted (normally happens automatically, in the
	// background, but we want to force it to be written now for this demo)
	client.Audit().Events().Flush(200 * time.Millisecond)

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

	fmt.Println("Done!")
}

// randomHex returns nBytes worth of random hex chars (length 2*nBytes).
func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	_, err := rand.Read(buf)
	fatalIfErr("rand.Read", err)
	return hex.EncodeToString(buf)
}
