//go:build ignore

// Demonstrates the smplkit SDK for Smpl Audit.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//
// Usage:
//
//	make audit_showcase
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// JSON Logic filter — only forward invoice.* event types. Events that don't
// match the filter aren't forwarded (and produce no delivery record).
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

	// create the client
	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-service",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	audit := client.Audit()
	someResourceID := "showcase-" + randomHex(4)

	// ----- Events: record / list / get ----------------------------------

	// record an event
	now := time.Now().UTC()
	fatalIfErr("audit.Events.Record", audit.Events().Record(smplkit.CreateEventInput{
		ActorID:    "billing-bot:42",
		ActorLabel: "finance@example.com",
		ActorType:  "USER",
		Category:   "billing",
		Data: map[string]interface{}{
			"snapshot":   map[string]interface{}{"total_cents": 4900, "currency": "USD"},
			"request_id": "req-abc",
		},
		EventType:    "invoice.created",
		OccurredAt:   &now,
		ResourceID:   someResourceID,
		ResourceType: "invoice",
	}))
	// flush synchronously (or omit to have events flushed asynchronously)
	audit.Events().Flush(2 * time.Second)
	fmt.Printf("Recorded event for invoice %s\n", someResourceID)

	// list events
	page, err := audit.Events().List(ctx, smplkit.ListEventsInput{
		ResourceType: "invoice",
		ResourceID:   someResourceID,
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
	event, err := audit.Events().Get(ctx, recordedEventID)
	fatalIfErr("audit.Events.Get", err)
	if event.ID != recordedEventID ||
		event.ResourceID != someResourceID ||
		event.EventType != "invoice.created" ||
		event.ActorID != "billing-bot:42" ||
		event.ActorLabel != "finance@example.com" ||
		event.Category != "billing" {
		fatalIfErr("assertion", fmt.Errorf("event fields mismatch: %+v", event))
	}
	fmt.Printf("Fetched event %s: %s by %s in %s\n",
		event.ID, event.EventType, event.ActorLabel, event.Environment)

	// ----- Discovery: distinct resource_types / event_types / categories

	resourceTypes, err := audit.ResourceTypes().List(ctx, smplkit.ListResourceTypesInput{})
	fatalIfErr("audit.ResourceTypes.List", err)
	rtIDs := map[string]bool{}
	rtNames := make([]string, 0, len(resourceTypes.ResourceTypes))
	for _, rt := range resourceTypes.ResourceTypes {
		rtIDs[rt.ID] = true
		rtNames = append(rtNames, rt.ID)
	}
	if !rtIDs["invoice"] {
		fatalIfErr("assertion", fmt.Errorf("expected invoice in resource types"))
	}
	fmt.Printf("Observed resource types: %v\n", rtNames)

	eventTypes, err := audit.EventTypes().List(ctx, smplkit.ListEventTypesInput{})
	fatalIfErr("audit.EventTypes.List", err)
	etIDs := map[string]bool{}
	etNames := make([]string, 0, len(eventTypes.EventTypes))
	for _, et := range eventTypes.EventTypes {
		etIDs[et.ID] = true
		etNames = append(etNames, et.ID)
	}
	if !etIDs["invoice.created"] {
		fatalIfErr("assertion", fmt.Errorf("expected invoice.created in event types"))
	}
	fmt.Printf("Observed event types: %v\n", etNames)

	categories, err := audit.Categories().List(ctx, smplkit.ListCategoriesInput{})
	fatalIfErr("audit.Categories.List", err)
	catIDs := map[string]bool{}
	catNames := make([]string, 0, len(categories.Categories))
	for _, c := range categories.Categories {
		catIDs[c.ID] = true
		catNames = append(catNames, c.ID)
	}
	if !catIDs["billing"] {
		fatalIfErr("assertion", fmt.Errorf("expected billing in categories"))
	}
	fmt.Printf("Observed categories: %v\n", catNames)

	// ----- Forwarders: SIEM streaming CRUD ------------------------------

	forwarderID := "showcase-" + randomHex(3)

	// tear-down: never leave the showcase forwarder behind, even on failure
	defer func() {
		err := audit.Forwarders().Delete(ctx, forwarderID)
		var notFound *smplkit.NotFoundError
		if err != nil && !errors.As(err, &notFound) {
			fatalIfErr("audit.Forwarders.Delete (teardown)", err)
		}
	}()

	// create a forwarder (disabled by default)
	forwarder := audit.Forwarders().New(
		forwarderID,
		forwarderID,
		smplkit.ForwarderTypeHTTP,
		smplkit.HttpConfiguration{
			Headers: []smplkit.HttpHeader{{Name: "X-Showcase", Value: "ok"}},
			Method:  smplkit.HttpMethodPost,
			URL:     "https://example.com",
		},
		smplkit.WithForwarderFilter(invoiceFilter),
		smplkit.WithForwarderTransform(smplkit.ForwarderTransformTypeJSONata, siemTransform),
	)
	fatalIfErr("audit.Forwarders.Save", forwarder.Save(ctx))
	fmt.Printf("Created forwarder: %s (id=%s)\n", forwarder.Name, forwarder.ID)

	// list forwarders
	listed, err := audit.Forwarders().List(ctx, smplkit.ListForwardersInput{})
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
	forwarder, err = client.Audit().Forwarders().Get(ctx, forwarderID)
	fatalIfErr("audit.Forwarders.Get", err)
	if forwarder.ID != forwarderID {
		fatalIfErr("assertion", fmt.Errorf("fetched forwarder id mismatch: %s", forwarder.ID))
	}
	fmt.Printf("Fetched forwarder: %s (id=%s)\n", forwarder.Name, forwarder.ID)

	// configure where to forward events in production
	forwarder.SetConfiguration(smplkit.HttpConfiguration{
		Headers: []smplkit.HttpHeader{{Name: "X-Showcase", Value: "ok"}},
		Method:  smplkit.HttpMethodPost,
		URL:     "https://httpbin.org/post",
	}, "production")
	fatalIfErr("audit.Forwarders.Save", forwarder.Save(ctx))
	prod, ok := forwarder.Environments["production"]
	if !ok || prod.Configuration == nil || prod.Configuration.URL != "https://httpbin.org/post" {
		fatalIfErr("assertion", fmt.Errorf("updated forwarder configuration mismatch: %+v", forwarder.Environments))
	}
	fmt.Printf("Updated forwarder: %s\n", forwarder.Name)

	// start forwarding events in production
	forwarder.SetEnabled(true, "production")
	fatalIfErr("audit.Forwarders.Save", forwarder.Save(ctx))
	fmt.Printf("Enabled forwarder %s (id=%s) to start forwarding events in production\n",
		forwarder.Name, forwarder.ID)

	// delete a forwarder
	fatalIfErr("audit.Forwarders.Delete", forwarder.Delete(ctx))
	remaining, err := audit.Forwarders().List(ctx, smplkit.ListForwardersInput{})
	fatalIfErr("audit.Forwarders.List", err)
	for _, f := range remaining.Forwarders {
		if f.ID == forwarderID {
			fatalIfErr("assertion", fmt.Errorf("forwarder %s still present after delete", forwarderID))
		}
	}
	fmt.Printf("Deleted forwarder: %s\n", forwarder.Name)

	fmt.Println("Done!")
}

// randomHex returns nBytes worth of random hex chars (length 2*nBytes).
func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	_, err := rand.Read(buf)
	fatalIfErr("rand.Read", err)
	return hex.EncodeToString(buf)
}
