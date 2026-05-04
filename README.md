# smplkit Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/smplkit/go-sdk/v3.svg)](https://pkg.go.dev/github.com/smplkit/go-sdk/v3) [![Build](https://github.com/smplkit/go-sdk/actions/workflows/ci-cd.yml/badge.svg)](https://github.com/smplkit/go-sdk/actions) [![Coverage](https://codecov.io/gh/smplkit/go-sdk/branch/main/graph/badge.svg)](https://codecov.io/gh/smplkit/go-sdk) [![License](https://img.shields.io/github/license/smplkit/go-sdk)](LICENSE) [![Docs](https://img.shields.io/badge/docs-docs.smplkit.com-blue)](https://docs.smplkit.com)

The official Go SDK for [smplkit](https://www.smplkit.com) — simple application infrastructure that just works.

## Installation

```bash
go get github.com/smplkit/go-sdk/v3
```

## Requirements

- Go 1.24+

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"

    smplkit "github.com/smplkit/go-sdk/v3"
)

func main() {
    ctx := context.Background()

    // APIKey, Environment, Service may also come from SMPLKIT_* env vars
    // or ~/.smplkit; explicit Config fields take precedence.
    client, err := smplkit.NewClient(smplkit.Config{
        APIKey:      "sk_api_...",
        Environment: "production",
        Service:     "my-service",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    // ── Runtime: resolve config values ──────────────────────────────────
    // Get returns a LiveConfig proxy; reads always reflect the latest
    // values pushed by the WebSocket.
    cfg, err := client.Config().Get(ctx, "user_service")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(cfg.Value()["timeout"])

    // Or unmarshal directly into a typed struct.
    type ServiceConfig struct {
        Timeout int `json:"timeout"`
        Retries int `json:"retries"`
    }
    var sc ServiceConfig
    if err := client.Config().GetInto(ctx, "user_service", &sc); err != nil {
        log.Fatal(err)
    }
    fmt.Println(sc.Timeout)

    // ── Management: CRUD operations ──────────────────────────────────────
    mgmt := client.Manage().Config()

    configs, err := mgmt.List(ctx)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(len(configs))

    fetched, err := mgmt.Get(ctx, "user_service")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(fetched.ID)

    newConfig := mgmt.New("my_service", smplkit.WithConfigName("My Service"))
    newConfig.SetNumber("timeout", 30, "")
    newConfig.SetNumber("retries", 3, "")
    if err := newConfig.Save(ctx); err != nil {
        log.Fatal(err)
    }

    if err := mgmt.Delete(ctx, "my_service"); err != nil {
        log.Fatal(err)
    }
}
```

`client.Manage()` exposes the eight management namespaces (`Config()`, `Flags()`, `Loggers()`, `LogGroups()`, `Contexts()`, `ContextTypes()`, `Environments()`, `AccountSettings()`). For setup scripts and CI jobs that don't need the runtime, construct a management-only client with no side effects via `smplkit.NewManagementClient(smplkit.ManagementConfig{...})`.

## Configuration

All settings are resolved from four sources, in order of precedence:

1. **Constructor arguments** — explicit `Config` fields, highest priority.
2. **Environment variables** — e.g. `SMPLKIT_API_KEY`, `SMPLKIT_ENVIRONMENT`.
3. **Configuration file** (`~/.smplkit`) — INI-format with profile support.
4. **Defaults** — built-in SDK defaults.

### Configuration File

The `~/.smplkit` file supports a `[common]` section (applied to all profiles) and named profiles:

```ini
[common]
environment = production
service = my-app

[default]
api_key = sk_api_abc123

[local]
base_domain = localhost
scheme = http
api_key = sk_api_local_xyz
environment = development
debug = true
```

### Constructor Examples

```go
// Use a named profile
client, err := smplkit.NewClient(smplkit.Config{Profile: "local"})

// Or configure explicitly
client, err := smplkit.NewClient(smplkit.Config{
    APIKey:      "sk_api_...",
    Environment: "production",
    Service:     "my-service",
})
```

For the complete configuration reference, see the [Configuration Guide](https://docs.smplkit.com/getting-started/configuration).

## Error Handling

All SDK errors extend `*smplkit.Error` and support `errors.Is()` / `errors.As()`:

```go
import "errors"

cfg, err := client.Manage().Config().Get(ctx, "nonexistent")
if err != nil {
    var notFound *smplkit.NotFoundError
    if errors.As(err, &notFound) {
        fmt.Println("Not found:", notFound.Base.Message)
    } else {
        fmt.Println("Error:", err)
    }
}
```

| Error                | Cause                          |
|----------------------|--------------------------------|
| `NotFoundError`      | HTTP 404 — resource not found  |
| `ConflictError`      | HTTP 409 — conflict            |
| `ValidationError`    | HTTP 422 — validation error    |
| `TimeoutError`       | Request timed out              |
| `ConnectionError`    | Network connectivity issue     |
| `Error`              | Any other SDK error            |

`Smpl`-prefixed aliases (`SmplError`, `SmplNotFoundError`, etc.) exist for cross-SDK familiarity but the unprefixed names are canonical.

## Feature Flags

Full management + runtime client with real-time WebSocket updates and a typed-handle evaluation API.

### Management API

```go
ctx := context.Background()
mgmt := client.Manage().Flags()

// Create a flag using typed factories
flag := mgmt.NewBooleanFlag("checkout-v2", false,
    smplkit.WithFlagName("Checkout V2"),
    smplkit.WithFlagDescription("Controls rollout of the new checkout experience."),
)
if err := flag.Save(ctx); err != nil {
    log.Fatal(err)
}

// Per-environment defaults and rules — env="" targets the base default,
// non-empty scopes to that environment.
flag.SetDefault(true, "staging")
if err := flag.AddRule(smplkit.NewRule("Enable for enterprise").
    Environment("staging").
    When("user.plan", "==", "enterprise").
    Serve(true).
    Build()); err != nil {
    log.Fatal(err)
}
if err := flag.Save(ctx); err != nil {
    log.Fatal(err)
}

// List / get / delete
allFlags, _ := mgmt.List(ctx)
fetched, _ := mgmt.Get(ctx, "checkout-v2")
_ = allFlags
_ = fetched
_ = mgmt.Delete(ctx, "checkout-v2")
```

### Runtime Evaluation

```go
flags := client.Flags()

// Typed flag handles (no Connect step — runtime initializes lazily on
// first Get and opens the live-updates WebSocket in the background).
checkout := flags.BooleanFlag("checkout-v2", false)
banner   := flags.StringFlag("banner-color", "red")
retries  := flags.NumberFlag("max-retries", 3)

// Ambient context: a provider runs on every evaluation that doesn't
// pass an explicit context.
flags.SetContextProvider(func(ctx context.Context) []smplkit.Context {
    return []smplkit.Context{
        smplkit.NewContext("user", "user-42", map[string]interface{}{
            "plan": "enterprise",
        }),
    }
})

// Evaluate — uses provider context, caches results.
isV2 := checkout.Get(ctx)            // true (rule matched)
color := banner.Get(ctx)             // "blue"
maxR := retries.Get(ctx)             // 5
_ = color
_ = maxR

// Per-call context override.
basicUser := smplkit.NewContext("user", "u-1", map[string]interface{}{"plan": "free"})
isV2 = checkout.Get(ctx, basicUser)  // false

// Live-update listeners.
flags.OnChange(func(evt *smplkit.FlagChangeEvent) {
    fmt.Println("flag changed:", evt.ID)
})
checkout.OnChange(func(evt *smplkit.FlagChangeEvent) {
    fmt.Println("checkout-v2 specifically changed")
})

// Manual re-fetch (bypasses the WebSocket — useful in short-lived scripts).
if err := flags.Refresh(ctx); err != nil {
    log.Fatal(err)
}

// Cache stats.
stats := flags.Stats()
fmt.Printf("hits=%d misses=%d\n", stats.CacheHits, stats.CacheMisses)

// Cleanup is handled by client.Close(); call flags.Disconnect(ctx) to
// stop the runtime sub-client without tearing down the rest.
```

If you need on-change listeners to receive events for writes that happen immediately after construction (e.g. in showcases or tests), call `client.WaitUntilReady(ctx, 0)` once after `NewClient` to block until the WebSocket subscription has been registered server-side.

### Flag Types

| Constant            | Value       |
|---------------------|-------------|
| `FlagTypeBoolean`   | `"BOOLEAN"` |
| `FlagTypeString`    | `"STRING"`  |
| `FlagTypeNumeric`   | `"NUMERIC"` |
| `FlagTypeJSON`      | `"JSON"`    |

## Logging

Centrally manage log levels per service+environment from the smplkit platform; the SDK pushes resolved levels onto whichever logging framework you wrap.

### Adapters

The SDK ships two adapters as separate Go modules so you only pay for the framework you use:

- `github.com/smplkit/go-sdk/logging/adapters/slog` — Go's standard `log/slog`.
- `github.com/smplkit/go-sdk/logging/adapters/zap` — `go.uber.org/zap`.

```bash
go get github.com/smplkit/go-sdk/logging/adapters/slog
```

### Runtime: slog

```go
import (
    "log/slog"

    smplkit "github.com/smplkit/go-sdk/v3"
    slogadapter "github.com/smplkit/go-sdk/logging/adapters/slog"
)

adapter := slogadapter.New()

// Wrap and replace the global slog default. Every package that uses
// slog.Info / slog.Warn / slog.Error / slog.Debug now routes through
// the SDK's level-controlled wrapper. Use WrapHandler explicitly if
// you want to keep your own slog.NewJSONHandler / custom destination.
adapter.InstallDefault()

client.Logging().RegisterAdapter(adapter)
if err := client.Logging().Install(ctx); err != nil {
    log.Fatal(err)
}

// Force a re-fetch of managed levels without waiting for the WebSocket.
if err := client.Logging().Refresh(ctx); err != nil {
    log.Fatal(err)
}

// Listen for level changes from the platform.
client.Logging().OnChange(func(evt *smplkit.LoggerChangeEvent) {
    fmt.Println("logger changed:", evt.ID, "level:", evt.Level, "source:", evt.Source)
})
```

> Why `InstallDefault` replaces (rather than wraps) the existing default: Go's `log/slog` has no global registry of loggers, so the SDK can only manage handlers it sits in front of. Wrapping `slog.Default()`'s pre-existing handler causes a recursion deadlock through `log.Default()`; installing a fresh `TextHandler` to stderr avoids the cycle. To attach smplkit control to a non-default handler, use `adapter.WrapHandler(yourHandler)` and call `slog.SetDefault` yourself.

### Runtime: zap

```go
import (
    "go.uber.org/zap"
    "go.uber.org/zap/zapcore"

    zapadapter "github.com/smplkit/go-sdk/logging/adapters/zap"
)

adapter := zapadapter.New()
core := adapter.WrapCore(zapcore.NewCore(
    zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
    zapcore.AddSync(os.Stderr),
    zapcore.DebugLevel,
))
logger := zap.New(core)

client.Logging().RegisterAdapter(adapter)
_ = client.Logging().Install(ctx)
logger.Info("hello from zap")
```

### Management API

```go
mgmt := client.Manage().Loggers()

// Create or update a logger entry on the platform. PUT is upsert.
logger := mgmt.New("acme.app")
logger.SetLevel(smplkit.LogLevelInfo, "")          // base level
logger.SetLevel(smplkit.LogLevelDebug, "staging")  // env override
if err := logger.Save(ctx); err != nil {
    log.Fatal(err)
}

// Read paths.
fetched, _ := mgmt.Get(ctx, "acme.app")
all, _    := mgmt.List(ctx)
_ = fetched
_ = all

// Force the discovery buffer to send any pending registrations now
// (the runtime drains it on a 5s ticker by default).
if err := mgmt.Flush(ctx); err != nil {
    log.Fatal(err)
}

// Log groups have their own namespace; see client.Manage().LogGroups().
groups := client.Manage().LogGroups()
group := groups.New("infra", smplkit.WithLogGroupName("Infra"))
group.SetLevel(smplkit.LogLevelWarn)
if err := group.Save(ctx); err != nil {
    log.Fatal(err)
}
```

### Log Levels

| Constant            | Value      |
|---------------------|------------|
| `LogLevelTrace`     | `"TRACE"`  |
| `LogLevelDebug`     | `"DEBUG"`  |
| `LogLevelInfo`      | `"INFO"`   |
| `LogLevelWarn`      | `"WARN"`   |
| `LogLevelError`     | `"ERROR"`  |
| `LogLevelFatal`     | `"FATAL"`  |
| `LogLevelSilent`    | `"SILENT"` |

## Debug Logging

Set `SMPLKIT_DEBUG=1` to enable verbose diagnostic output to stderr. This is useful for troubleshooting real-time level changes, WebSocket connectivity, and SDK initialization. Debug output bypasses the managed logging framework and writes directly to stderr.

```bash
SMPLKIT_DEBUG=1 ./my-app
```

Accepted values: `1`, `true`, `yes` (case-insensitive). Any other value (or unset) disables debug output.

## Documentation

- [Getting Started](https://docs.smplkit.com/getting-started)
- [Go SDK Guide](https://docs.smplkit.com/sdks/go)
- [API Reference](https://docs.smplkit.com/api)

## License

MIT
