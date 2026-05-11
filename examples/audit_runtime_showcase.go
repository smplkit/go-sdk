//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Audit.
//
// Covers: event record / list / get, resource_types.list, actions.list.
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

	someResourceID := "showcase-" + randomHex(4)

	// record an event
	now := time.Now().UTC()
	client.Audit().Events().Record(smplkit.CreateEventInput{
		Action:       "invoice.created",
		ResourceType: "invoice",
		ResourceID:   someResourceID,
		OccurredAt:   &now,
		Data: map[string]interface{}{
			"snapshot":   map[string]interface{}{"total_cents": 4900, "currency": "USD"},
			"request_id": "req-abc",
		},
	})
	client.Audit().Events().Flush(2 * time.Second)
	fmt.Printf("Recorded events for invoice %s\n", someResourceID)

	// list events
	page, err := client.Audit().Events().List(ctx, smplkit.ListEventsInput{
		ResourceID: someResourceID,
	})
	fatalIfErr("audit.Events.List", err)
	ids := map[string]bool{}
	for _, e := range page.Events {
		ids[e.ResourceID] = true
	}
	if !ids[someResourceID] {
		fatalIfErr("assertion", fmt.Errorf("expected %s in listed events", someResourceID))
	}
	recordedEventID := page.Events[0].ID
	fmt.Printf("Listed %d event(s) for invoice %s\n", len(page.Events), someResourceID)

	// fetch an event
	event, err := client.Audit().Events().Get(ctx, recordedEventID)
	fatalIfErr("audit.Events.Get", err)
	if event.ID != recordedEventID || event.ResourceID != someResourceID || event.Action != "invoice.created" {
		fatalIfErr("assertion", fmt.Errorf("event fields mismatch: %+v", event))
	}
	fmt.Printf("Fetched event %s: %s\n", event.ID, event.Action)

	// list resource types observed
	resourceTypes, err := client.Audit().ResourceTypes().List(ctx, smplkit.ListResourceTypesInput{})
	fatalIfErr("audit.ResourceTypes.List", err)
	rtIDs := map[string]bool{}
	for _, rt := range resourceTypes.ResourceTypes {
		rtIDs[rt.ID] = true
	}
	if !rtIDs["invoice"] {
		fatalIfErr("assertion", fmt.Errorf("expected invoice in resource types"))
	}
	rtNames := make([]string, 0, len(resourceTypes.ResourceTypes))
	for _, rt := range resourceTypes.ResourceTypes {
		rtNames = append(rtNames, rt.ID)
	}
	fmt.Printf("Observed resource types: %v\n", rtNames)

	// list actions observed
	actions, err := client.Audit().Actions().List(ctx, smplkit.ListActionsInput{})
	fatalIfErr("audit.Actions.List", err)
	aIDs := map[string]bool{}
	for _, a := range actions.Actions {
		aIDs[a.ID] = true
	}
	if !aIDs["invoice.created"] {
		fatalIfErr("assertion", fmt.Errorf("expected invoice.created in actions"))
	}
	aNames := make([]string, 0, len(actions.Actions))
	for _, a := range actions.Actions {
		aNames = append(aNames, a.ID)
	}
	fmt.Printf("Observed actions: %v\n", aNames)

	fmt.Println("Done!")
}

// randomHex returns nBytes worth of random hex chars (length 2*nBytes).
func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	_, err := rand.Read(buf)
	fatalIfErr("rand.Read", err)
	return hex.EncodeToString(buf)
}
