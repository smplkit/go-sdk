//go:build ignore

// Demonstrates the smplkit runtime SDK for Smpl Logging.
//
// Prerequisites:
//   - go get github.com/smplkit/go-sdk/v3
//   - go get github.com/smplkit/go-sdk/logging/adapters/slog
//   - A valid smplkit API key, provided via one of:
//   - SMPLKIT_API_KEY environment variable
//   - ~/.smplkit configuration file (see SDK docs)
//
// Usage:
//
//	make logging_runtime_showcase
package main

import (
	"context"
	"fmt"
	"log/slog"

	smplkit "github.com/smplkit/go-sdk/v3"
	slogadapter "github.com/smplkit/go-sdk/logging/adapters/slog"
)

func main() {
	ctx := context.Background()

	client, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "showcase-service",
	})
	fatalIfErr("create client", err)
	defer client.Close()

	// Wire the slog adapter into client.Logging() and replace the global
	// slog default. After InstallDefault, every package that calls
	// slog.Info / slog.Warn / slog.Error / slog.Debug routes through the
	// SDK's level-controlled wrapper — including logs from third-party
	// libraries that use slog.Default().
	adapter := slogadapter.New()
	adapter.InstallDefault()
	client.Logging().RegisterAdapter(adapter)

	fatalIfErr("install logging", client.Logging().Install(ctx))
	fmt.Println("All loggers are now controlled by smplkit")

	// These calls go through the wrapped TextHandler installed above.
	// Whether they reach stderr is decided by the platform-resolved
	// level for this service; the SDK pushes that level onto the
	// wrapped handler and re-applies it whenever the platform changes.
	slog.Info("info from slog.Default")
	slog.Debug("debug from slog.Default (filtered unless level is DEBUG)")

	// Force-pull post-Install platform changes without waiting on the
	// WebSocket. Useful for short-lived scripts.
	fatalIfErr("refresh logging", client.Logging().Refresh(ctx))
}
