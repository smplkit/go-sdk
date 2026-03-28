# smplkit SDK Examples

Runnable examples demonstrating the [smplkit Go SDK](https://github.com/smplkit/go-sdk).

> **Note:** These examples require valid smplkit credentials and a live environment — they are not self-contained demos.

## Prerequisites

1. Go 1.21 or later.

2. Install the SDK:

   ```bash
   go get github.com/smplkit/go-sdk
   ```

3. A valid smplkit API key (create one in the [smplkit console](https://www.smplkit.com)).
4. At least one config created in your smplkit account (every account comes with a `common` config by default).

## Config Showcase

**File:** [`config_showcase.go`](config_showcase.go)

An end-to-end walkthrough of the Smpl Config SDK covering:

- **Client initialization** — `smplkit.NewClient(apiKey)`
- **Management-plane CRUD** — create, list, and delete configs
- **Config inheritance** — parent/child relationships (`user_service` → `common`, `auth_module` → `user_service`)
- **Fetch by key** — `GetByKey("common")`

> Several SDK features exercised by the [Python showcase](https://github.com/smplkit/python-sdk/blob/main/examples/config_showcase.py) are not yet implemented in the Go SDK and are marked as skipped in the output. These include: update/set values, environment overrides, runtime value resolution, real-time WebSocket updates, and typed accessors.

### Running

```bash
export SMPLKIT_API_KEY="sk_api_..."
go run examples/config_showcase.go
```

The script creates temporary configs, exercises available SDK features, then cleans up after itself.
