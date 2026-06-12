package smplkit

import (
	"os"
	"testing"
)

// TestMain neutralizes the background WebSocket dialer for the whole unit-test
// suite.
//
// The lazy-auto-connect clients open a sharedWebSocket and dial on first live
// use; a unit test that exercises the live surface would otherwise spawn a
// real connection (failing against fake credentials) and leak a reconnect
// goroutine that cross-contaminates other tests. Replacing wsLaunch with a
// no-op keeps every socket fully constructed — listener registration and
// connection status still behave normally — but never starts the
// dial/reconnect loop, so no test reaches the network. The no-op closes
// wsDone so stop() still completes.
//
// The dedicated WebSocket test (ws_test.go) drives run() and connect()
// directly, bypassing this seam, to exercise the real start/connect/reconnect
// machinery against a locally-mocked server.
func TestMain(m *testing.M) {
	wsLaunch = func(ws *sharedWebSocket) { close(ws.wsDone) }
	os.Exit(m.Run())
}
