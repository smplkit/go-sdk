# smplkit Go SDK

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
    "fmt"
    "log"

    smplkit "github.com/smplkit/go-sdk"
)

func main() {
    // Option 1: Explicit API key
    client, err := smplkit.NewClient("sk_api_...")

    // Option 2: Environment variable (SMPLKIT_API_KEY)
    // export SMPLKIT_API_KEY=sk_api_...
    client, err = smplkit.NewClient("")

    // Option 3: Configuration file (~/.smplkit)
    // [default]
    // api_key = sk_api_...
    client, err = smplkit.NewClient("")

    if err != nil {
        log.Fatal(err)
    }

    // Get a config by key
    config, err := client.Config().GetByKey("user_service")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(config.Key)

    // List all configs
    configs, err := client.Config().List()
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(len(configs))

    // Create a config
    newConfig, err := client.Config().Create(smplkit.CreateConfigParams{
        Name:        "My Service",
        Key:         strPtr("my_service"),
        Description: strPtr("Configuration for my service"),
        Values:      map[string]any{"timeout": 30, "retries": 3},
    })
    if err != nil {
        log.Fatal(err)
    }

    // Delete a config
    err = client.Config().Delete(newConfig.ID)
    if err != nil {
        log.Fatal(err)
    }
}

func strPtr(s string) *string { return &s }
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

config, err := client.Config().GetByKey("nonexistent")
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

## Documentation

- [Getting Started](https://docs.smplkit.com/getting-started)
- [Go SDK Guide](https://docs.smplkit.com/sdks/go)
- [API Reference](https://docs.smplkit.com/api)

## License

MIT
