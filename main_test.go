package smplkit

import (
	"os"
	"testing"
)

// TestMain neutralizes the background event-stream connector for the whole
// unit-test suite.
//
// The lazy-auto-connect clients open a sharedEventStream and connect on first
// live use; a unit test that exercises the live surface would otherwise spawn
// a real connection (failing against fake credentials) and leak a reconnect
// goroutine that cross-contaminates other tests. Replacing eventsLaunch with
// a no-op keeps every stream fully constructed — listener registration and
// connection status still behave normally — but never starts the
// connect/reconnect loop, so no test reaches the network. The no-op closes
// streamDone so stop() still completes.
//
// The dedicated event-stream test (events_test.go) drives run() and connect()
// directly, bypassing this seam, to exercise the real start/connect/reconnect
// machinery against a locally-mocked server.
// realEventsLaunch preserves the production launcher (captured before the
// neutralization below) so the dedicated event-stream tests can restore it
// via withRealEventsLaunch.
var realEventsLaunch = eventsLaunch

func TestMain(m *testing.M) {
	eventsLaunch = func(s *sharedEventStream) { close(s.streamDone) }
	os.Exit(m.Run())
}
