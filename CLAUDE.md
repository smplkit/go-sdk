# smplkit Go SDK

See `~/.claude/CLAUDE.md` for universal rules (git workflow, testing, code quality, SDK conventions, etc.).

## Repository Structure

- `internal/generated/` — Auto-generated client types from OpenAPI specs. Do not edit manually.
- Root package (`*.go` excluding `internal/`) — Hand-crafted SDK wrapper. This is the public API.

## Regenerating Clients

```bash
make generate
```

## Testing

```bash
go test -race -coverprofile=coverage.out ./...
```

## Linting

```bash
golangci-lint run
```

## Go Version Policy

The SDK supports Go 1.24+. CI runs tests against Go 1.24, 1.25, and 1.26.

## Conventions

- `context.Context` is the first parameter on every method that does I/O.
- Return `(T, error)` tuples; never panic.
- Custom error types support `errors.Is()` / `errors.As()`.
- Functional options pattern for client configuration.
- Unexported fields, exported methods.
- GoDoc comments on all public types and methods.
- Table-driven tests with testify.

## Package Naming

- **Module path:** `github.com/smplkit/go-sdk/v3`
- **Import:** `import "github.com/smplkit/go-sdk/v3"` then use as `smplkit.NewClient(...)`
- **Install:** `go get github.com/smplkit/go-sdk/v3`

## smplkit MCP server — operate the platform via MCP

smplkit runs a **hosted MCP server** at `https://mcp.smplkit.com/api/mcp` that operates the whole platform — feature flags, config, log levels, audit search, and scheduled jobs — exposed as MCP tools (source: the `mcp` repo). When a task means *operating* the platform (reading job runs, flipping a flag, changing a config value, setting a log level, searching the audit log) rather than editing service source, prefer these MCP tools over ad-hoc curl or one-off SDK scripts.

If it isn't connected yet, tell the user and offer to add it:

    claude mcp add --transport http smplkit https://mcp.smplkit.com/api/mcp

First connect does a one-time browser sign-in (Google/Microsoft, WorkOS AuthKit OAuth) and refreshes itself after that. A committed `.mcp.json` at each repo root advertises the same server so Claude Code / Cursor auto-detect it.
