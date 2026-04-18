//go:build ignore

// Logging Runtime Showcase
//
// Demonstrates the smplkit Go SDK's logging runtime API:
//   - RegisterLogger for explicit logger registration
//   - Start() to initialize the runtime
//   - OnChange / OnChangeKey for change listeners
//   - Level resolution behavior
//
// Prerequisites:
//   - Go 1.21+
//   - SMPLKIT_API_KEY set (or ~/.smplkit config file)
//
// Usage:
//
//	go run examples/logging_runtime_showcase.go examples/logging_runtime_setup.go examples/helpers.go
package main

import (
	"context"
	"fmt"
	"time"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	ctx := context.Background()

	// ── Section 1: SDK Initialization ─────────────────────────────────
	section("1. SDK Initialization")

	step("Creating smplkit client")
	client, err := smplkit.NewClient(smplkit.Config{Environment: "production", Service: "showcase-service"})
	if err != nil {
		fatal("NewClient failed", err)
	}
	defer client.Close()
	fmt.Println("  Client created successfully")

	// ── Section 2: Setup Demo Loggers ─────────────────────────────────
	section("2. Setup Demo Loggers")

	step("Creating demo loggers and groups")
	loggers, groups := setupDemoLoggers(ctx, client)
	defer teardownDemoLoggers(ctx, client, loggers, groups)
	fmt.Printf("  Created %d loggers and %d groups\n", len(loggers), len(groups))

	logging := client.Logging()

	// ── Section 3: Register Loggers ──────────────────────────────────
	section("3. Register Loggers")

	step("Registering application loggers")
	logging.RegisterLogger("com.acme.payments", smplkit.LogLevelInfo)
	logging.RegisterLogger("com.acme.auth", smplkit.LogLevelDebug)
	logging.RegisterLogger("com.acme.db", smplkit.LogLevelWarn)
	fmt.Println("  Registered 3 loggers for smplkit management")

	step("Registering logger with non-standard name (auto-normalized)")
	logging.RegisterLogger("com/acme:cache", smplkit.LogLevelInfo)
	fmt.Printf("  Normalized: %q → %q\n", "com/acme:cache", smplkit.NormalizeLoggerName("com/acme:cache"))

	// ── Section 4: Start Runtime ─────────────────────────────────────
	section("4. Start Runtime")

	step("Starting logging runtime (fetches definitions, opens WebSocket)")
	if err := logging.Start(ctx); err != nil {
		fatal("Failed to start logging runtime", err)
	}
	fmt.Println("  Runtime started successfully")

	step("Calling Start again (idempotent)")
	if err := logging.Start(ctx); err != nil {
		fatal("Second Start failed", err)
	}
	fmt.Println("  Second Start was a no-op (idempotent)")

	// ── Section 5: Change Listeners ──────────────────────────────────
	section("5. Change Listeners")

	step("Registering global change listener")
	var globalEvents []string
	logging.OnChange(func(e *smplkit.LoggerChangeEvent) {
		levelStr := "<deleted>"
		if e.Level != nil {
			levelStr = string(*e.Level)
		}
		globalEvents = append(globalEvents, fmt.Sprintf("%s→%s", e.ID, levelStr))
	})
	fmt.Println("  Global listener registered")

	step("Registering key-scoped listener for com.acme.payments")
	var keyEvents []string
	logging.OnChangeKey("com.acme.payments", func(e *smplkit.LoggerChangeEvent) {
		levelStr := "<deleted>"
		if e.Level != nil {
			levelStr = string(*e.Level)
		}
		keyEvents = append(keyEvents, fmt.Sprintf("%s→%s", e.ID, levelStr))
	})
	fmt.Println("  Key-scoped listener registered for com.acme.payments")

	// ── Section 6: Trigger a Change ──────────────────────────────────
	section("6. Trigger a Change (via management API)")

	step("Updating payments logger level to ERROR")
	paymentsLogger, err := logging.Management().Get(ctx, "com.acme.payments")
	if err != nil {
		fatal("Failed to get payments logger", err)
	}
	paymentsLogger.SetLevel(smplkit.LogLevelError)
	if err := paymentsLogger.Save(ctx); err != nil {
		fatal("Failed to update payments logger", err)
	}
	fmt.Printf("  Updated: id=%s, level=%s\n", paymentsLogger.ID, *paymentsLogger.Level)

	// Wait briefly for WebSocket event propagation.
	time.Sleep(2 * time.Second)
	fmt.Printf("  Global events captured: %v\n", globalEvents)
	fmt.Printf("  Key events captured: %v\n", keyEvents)

	// ── Summary ──────────────────────────────────────────────────────
	section("Logging Runtime Showcase Complete")
	fmt.Println("  Features exercised:")
	fmt.Println("  [x] RegisterLogger — explicit logger registration")
	fmt.Println("  [x] NormalizeLoggerName — name normalization")
	fmt.Println("  [x] Start(ctx) — runtime initialization (idempotent)")
	fmt.Println("  [x] OnChange — global change listener")
	fmt.Println("  [x] OnChangeKey — key-scoped change listener")
	fmt.Println("  [x] Management mutation to trigger events")
}
