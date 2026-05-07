//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Audit.
//
// Audit is a fire-and-forget event-recording surface. Create enqueues
// the event onto an in-memory bounded buffer and returns immediately;
// the buffer worker retries with exponential backoff on transient
// failures and drops oldest under back-pressure (ADR-047 §2.6).
// Reads (Get, List) are synchronous on the wire.
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

	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-service",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	// unique resource id so we can find back exactly the events this
	// showcase wrote, regardless of what other history exists.
	resourceID := "showcase-" + randomHex(4)

	// 1) fire-and-forget Create — returns nil immediately. The actual
	//    POST happens on the buffer worker. Customer events must NOT
	//    use a ResourceType beginning with "smpl." — that namespace is
	//    reserved for smplkit-emitted events; the server returns 403.
	now := time.Now().UTC()
	fatalIfErr("audit.Create invoice.created", client.Audit().Events().Create(smplkit.CreateEventInput{
		Action:       "invoice.created",
		ResourceType: "invoice",
		ResourceID:   resourceID,
		OccurredAt:   &now,
		Snapshot:     map[string]interface{}{"total_cents": 4900, "currency": "USD"},
		Data:         map[string]interface{}{"request_id": "req-abc"},
	}))

	// 2) caller-supplied idempotency key — replaying with the same key
	//    returns the original event (server dedupes on
	//    account_id + idempotency_key).
	idempotencyKey := "showcase-" + randomHex(16)
	for i := 0; i < 2; i++ {
		fatalIfErr("audit.Create invoice.updated", client.Audit().Events().Create(smplkit.CreateEventInput{
			Action:         "invoice.updated",
			ResourceType:   "invoice",
			ResourceID:     resourceID,
			Snapshot:       map[string]interface{}{"total_cents": 5400},
			IdempotencyKey: idempotencyKey,
		}))
	}

	// 3) Flush — block until the in-memory buffer drains so that the
	//    events we just wrote are durable before we read them.
	client.Audit().Events().Flush(5 * time.Second)

	// 4) List — server-side filters per ADR-047 §4. Cursor pagination
	//    via PageSize / PageAfter; page.NextCursor is non-empty when
	//    more pages exist.
	page, err := client.Audit().Events().List(ctx, smplkit.ListEventsInput{
		ResourceType: "invoice",
		ResourceID:   resourceID,
		PageSize:     10,
	})
	fatalIfErr("audit.List", err)

	fmt.Printf("Found %d events for %s:\n", len(page.Events), resourceID)
	for _, ev := range page.Events {
		fmt.Printf("  %s  id=%s  actor=%s\n", ev.Action, ev.ID, ev.ActorType)
	}

	// idempotency dedupe check — 3 creates (1 distinct + 2 with the
	// same idempotency key) so we expect exactly 2 events.
	if len(page.Events) != 2 {
		fatalIfErr("idempotency check", fmt.Errorf("expected 2 events, got %d", len(page.Events)))
	}

	// 5) Get — read a single event by id.
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
