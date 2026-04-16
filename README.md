# smplkit Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/smplkit/go-sdk.svg)](https://pkg.go.dev/github.com/smplkit/go-sdk) [![Build](https://github.com/smplkit/go-sdk/actions/workflows/ci-cd.yml/badge.svg)](https://github.com/smplkit/go-sdk/actions) [![Coverage](https://codecov.io/gh/smplkit/go-sdk/branch/main/graph/badge.svg)](https://codecov.io/gh/smplkit/go-sdk) [![License](https://img.shields.io/github/license/smplkit/go-sdk)](LICENSE) [![Docs](https://img.shields.io/badge/docs-docs.smplkit.com-blue)](https://docs.smplkit.com)

The official Go SDK for [smplkit](https://www.smplkit.com) — simple application infrastructure that just works.

## Installation

```bash
go get github.com/smplkit/go-sdk
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

    smplkit "github.com/smplkit/go-sdk"
)

func main() {
    ctx := context.Background()

    // API key resolved from SMPLKIT_API_KEY env var or ~/.smplkit config file.
    // Pass explicitly as the first argument to override:
    //   smplkit.NewClient("sk_api_...", "production", "my-service")
    client, err := smplkit.NewClient("", "production", "my-service")
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    // ── Runtime: resolve config values ──────────────────────────────────
    // Returns the merged map for the current environment.
    values, err := client.Config().Get(ctx, "user_service")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(values["timeout"])

    // Or unmarshal directly into a typed struct.
    type ServiceConfig struct {
        Timeout int    `json:"timeout"`
        Retries int    `json:"retries"`
    }
    var cfg ServiceConfig
    if err := client.Config().GetInto(ctx, "user_service", &cfg); err != nil {
        log.Fatal(err)
    }
    fmt.Println(cfg.Timeout)

    // ── Management: CRUD operations ──────────────────────────────────────
    mgmt := client.Config().Management()

    configs, err := mgmt.List(ctx)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(len(configs))

    raw, err := mgmt.Get(ctx, "user_service")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(raw.ID)

    newConfig := mgmt.New("my_service", smplkit.WithConfigName("My Service"))
    newConfig.Items = map[string]interface{}{"timeout": 30, "retries": 3}
    if err := newConfig.Save(ctx); err != nil {
        log.Fatal(err)
    }

    if err := mgmt.Delete(ctx, "my_service"); err != nil {
        log.Fatal(err)
    }
}
```

## Configuration

The API key is resolved using the following priority:

1. **Explicit argument:** Pass `apiKey` to `NewClient()`.
2. **Environment variable:** Set `SMPLKIT_API_KEY`.
3. **Configuration file:** Add `api_key` under `[default]` in `~/.smplkit`:

```ini
# ~/.smplkit

[default]
api_key = sk_api_your_key_here
```

If none of these are set, `NewClient` returns a `SmplError` listing all three methods.

```go
client, err := smplkit.NewClient("sk_api_...",
    smplkit.WithTimeout(30 * time.Second),   // default
    smplkit.WithHTTPClient(customHTTPClient),
)
```

## Error Handling

All SDK errors extend `SmplError` and support `errors.Is()` / `errors.As()`:

```go
import "errors"

config, err := client.Config().Management().Get(ctx, "nonexistent")
if err != nil {
    var notFound *smplkit.SmplNotFoundError
    if errors.As(err, &notFound) {
        fmt.Println("Not found:", notFound.Message)
    } else {
        fmt.Println("Error:", err)
    }
}
```

| Error                  | Cause                        |
|------------------------|------------------------------|
| `SmplNotFoundError`    | HTTP 404 — resource not found |
| `SmplConflictError`    | HTTP 409 — conflict           |
| `SmplValidationError`  | HTTP 422 — validation error   |
| `SmplTimeoutError`     | Request timed out             |
| `SmplConnectionError`  | Network connectivity issue    |
| `SmplError`            | Any other SDK error           |

## Feature Flags

The SDK includes a full-featured feature flags client with management API, prescriptive runtime evaluation, and real-time updates.

### Management API

```go
ctx := context.Background()
mgmt := client.Flags().Management()

// Create a flag using typed factories
flag := mgmt.NewBooleanFlag("checkout-v2", false,
    smplkit.WithFlagName("Checkout V2"),
    smplkit.WithFlagDescription("Controls rollout of the new checkout experience."),
)
if err := flag.Save(ctx); err != nil {
    log.Fatal(err)
}

// Configure environments and rules, then save again
flag.SetEnvironmentEnabled("staging", true)
flag.AddRule(smplkit.NewRule("Enable for enterprise").
    Environment("staging").
    When("user.plan", "==", "enterprise").
    Serve(true).
    Build())
if err := flag.Save(ctx); err != nil {
    log.Fatal(err)
}

// List, get, delete
allFlags, _ := mgmt.List(ctx)
fetched, _ := mgmt.Get(ctx, "checkout-v2")
_ = allFlags
_ = fetched
err := mgmt.Delete(ctx, "checkout-v2")
```

### Runtime Evaluation

```go
// Define typed flag handles
checkout := flags.BoolFlag("checkout-v2", false)
banner   := flags.StringFlag("banner-color", "red")
retries  := flags.NumberFlag("max-retries", 3)

// Register a context provider
flags.SetContextProvider(func(ctx context.Context) []smplkit.Context {
    return []smplkit.Context{
        smplkit.NewContext("user", "user-42", map[string]interface{}{
            "plan": "enterprise",
        }),
    }
})

// Connect to an environment
err := flags.Connect(ctx, "staging")

// Evaluate — uses provider context, caches results
isV2 := checkout.Get(ctx)            // true (rule matched)
color := banner.Get(ctx)             // "blue"

// Explicit context override
basicUser := smplkit.NewContext("user", "u-1", map[string]interface{}{"plan": "free"})
isV2 = checkout.Get(ctx, basicUser)  // false

// Change listeners
flags.OnChange(func(evt *smplkit.FlagChangeEvent) {
    fmt.Println("flag changed:", evt.Key)
})

// Cache stats
stats := flags.Stats()
fmt.Printf("hits=%d misses=%d\n", stats.CacheHits, stats.CacheMisses)

// Cleanup
flags.Disconnect(ctx)
```

### Flag Types

| Constant              | Value       |
|-----------------------|-------------|
| `FlagTypeBoolean`     | `"BOOLEAN"` |
| `FlagTypeString`      | `"STRING"`  |
| `FlagTypeNumeric`     | `"NUMERIC"` |
| `FlagTypeJSON`        | `"JSON"`    |

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
