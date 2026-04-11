# smplkit SDK Examples

Runnable examples demonstrating the [smplkit Go SDK](https://github.com/smplkit/go-sdk).

> **Note:** These examples require valid smplkit credentials and a live environment — they are not self-contained demos.

## Prerequisites

1. Go 1.21+
2. A valid smplkit API key, provided via one of:
   - `SMPLKIT_API_KEY` environment variable
   - `~/.smplkit` configuration file (see SDK docs)
3. At least one config created in your smplkit account (every account comes with a `common` config by default).

## Flags Showcases

### Management Showcase

Demonstrates flag CRUD via the active record pattern: `NewBooleanFlag` / `NewStringFlag` / `NewNumberFlag` / `NewJsonFlag` → mutate → `Save()`, `AddRule()` → `Save()`, `SetEnvironmentEnabled`, `SetEnvironmentDefault`, `ClearRules`, `Get(key)`, `List()`, `Delete(key)`, `Rule` builder.

```bash
go run examples/flags_management_showcase.go examples/helpers.go
```

### Runtime Showcase

Demonstrates typed flag handles (`BooleanFlag`, `StringFlag`, `NumberFlag`, `JsonFlag`), `SetContextProvider`, `Get(ctx)`, context override, `OnChange` / `OnChangeKey`, `Stats()`, `Register()`, `FlushContexts()`.

```bash
go run examples/flags_runtime_showcase.go examples/flags_runtime_setup.go examples/helpers.go
```

## Config Showcases

### Management Showcase

Demonstrates config CRUD via the active record pattern: `New(key)` → set items/environments → `Save()`, `Get(key)`, `List()`, `Delete(key)`.

```bash
go run examples/config_management_showcase.go examples/helpers.go
```

### Runtime Showcase

Demonstrates `Resolve(ctx, key)`, `ResolveInto(ctx, key, &target)`, `Subscribe(ctx, key)`, `OnChange` with `WithConfigID` / `WithItemKey`, `Refresh()`.

```bash
go run examples/config_runtime_showcase.go examples/config_runtime_setup.go examples/helpers.go
```

## Logging Showcases

### Management Showcase

Demonstrates logger and log group CRUD: `New(key)` → `SetLevel` → `Save()`, `Get(key)`, `List()`, `Delete(key)`, `NewGroup` → `SetLevel` / `SetEnvironmentLevel` → `Save()`, `GetGroup`, `ListGroups`, `DeleteGroup`, group assignment.

```bash
go run examples/logging_management_showcase.go examples/helpers.go
```

### Runtime Showcase

Demonstrates `RegisterLogger`, `Start(ctx)`, `OnChange` / `OnChangeKey`, level resolution behavior.

```bash
go run examples/logging_runtime_showcase.go examples/logging_runtime_setup.go examples/helpers.go
```

## Client Initialization

All examples use:

```go
client, err := smplkit.NewClient("", "production", "showcase-service")
```

The three required parameters — API key, environment, and service — are resolved from environment variables if empty strings are passed.
