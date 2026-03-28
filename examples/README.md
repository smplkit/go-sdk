# smplkit SDK Examples

Runnable examples demonstrating the [smplkit Go SDK](https://github.com/smplkit/go-sdk).

> **Note:** These examples require valid smplkit credentials and a live environment — they are not self-contained demos.

## Prerequisites

1. Go 1.24+
2. A valid smplkit API key (create one in the [smplkit console](https://app.smplkit.com)).
3. At least one config created in your smplkit account (every account comes with a `common` config by default).

## Config Showcase

**File:** [`config_showcase.go`](config_showcase.go)

An end-to-end walkthrough of the Smpl Config SDK covering:

- **Client initialization** — `smplkit.NewClient(apiKey)`
- **Management-plane CRUD** — create, update, list, get by key, and delete configs
- **Environment overrides** — `SetValues()` and `SetValue()` for per-environment configuration
- **Multi-level inheritance** — child → parent → common hierarchy setup
- **Runtime value resolution** — `Connect()`, `Get()`, typed accessors (`GetString`, `GetInt`, `GetBool`)
- **Real-time updates** — WebSocket-driven cache invalidation with change listeners
- **Manual refresh and cache diagnostics** — `Refresh()`, `Stats()`

### Running

```bash
export SMPLKIT_API_KEY="sk_api_..."
go run examples/config_showcase.go
```

The script creates temporary configs, exercises all SDK features, then cleans up after itself.
