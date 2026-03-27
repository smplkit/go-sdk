# smplkit Go SDK

The official Go client for the [smplkit](https://docs.smplkit.com) platform.

## Requirements

Go 1.21 or later.

## Installation

```bash
go get github.com/smplkit/go-sdk
```

## Authentication

Create a client with your API key (prefixed `sk_api_`):

```go
client := smplkit.NewClient("sk_api_your_key_here")
```

The key is sent as a Bearer token on every request. Never log or expose your API key.

## Quick Start

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	smplkit "github.com/smplkit/go-sdk"
)

func main() {
	client := smplkit.NewClient("sk_api_your_key_here")

	ctx := context.Background()

	// List all configs
	configs, err := client.Config().List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, cfg := range configs {
		fmt.Printf("%s: %s\n", cfg.Key, cfg.Name)
	}

	// Get a config by key
	cfg, err := client.Config().GetByKey(ctx, "my-service")
	if err != nil {
		var notFound *smplkit.SmplNotFoundError
		if errors.As(err, &notFound) {
			fmt.Println("Config not found")
			return
		}
		log.Fatal(err)
	}
	fmt.Printf("Config: %s (values: %v)\n", cfg.Name, cfg.Values)

	// Create a config
	key := "new-service"
	newCfg, err := client.Config().Create(ctx, smplkit.CreateConfigParams{
		Name:   "New Service",
		Key:    &key,
		Values: map[string]interface{}{"log_level": "info"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Created: %s\n", newCfg.ID)

	// Delete a config
	if err := client.Config().Delete(ctx, newCfg.ID); err != nil {
		log.Fatal(err)
	}
}
```

## Configuration

Use functional options to customise the client:

```go
client := smplkit.NewClient("sk_api_...",
	smplkit.WithBaseURL("https://custom.example.com"),
	smplkit.WithTimeout(10 * time.Second),
	smplkit.WithHTTPClient(myHTTPClient),
)
```

## Error Handling

All SDK errors embed `SmplError` and support `errors.Is()` / `errors.As()`:

| Error Type             | HTTP Status | Description                          |
|------------------------|-------------|--------------------------------------|
| `SmplNotFoundError`    | 404         | Resource does not exist              |
| `SmplConflictError`    | 409         | Operation conflicts with state       |
| `SmplValidationError`  | 422         | Server rejected the request          |
| `SmplConnectionError`  | ---         | Network request failed               |
| `SmplTimeoutError`     | ---         | Request exceeded timeout             |

```go
cfg, err := client.Config().GetByKey(ctx, "missing")
if err != nil {
	var notFound *smplkit.SmplNotFoundError
	if errors.As(err, &notFound) {
		// handle 404
	}
	var base *smplkit.SmplError
	if errors.As(err, &base) {
		fmt.Println(base.StatusCode, base.ResponseBody)
	}
}
```

## Documentation

Full documentation is available at [docs.smplkit.com](https://docs.smplkit.com).

## Contributing

This project is in its initial development phase. Contributions are not currently accepted, but feel free to open issues for bugs or feature requests.

## License

MIT --- see [LICENSE](LICENSE).
