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

The SDK supports Go 1.21+. CI runs tests against Go 1.21, 1.22, and 1.23.

## Conventions

- `context.Context` is the first parameter on every method that does I/O.
- Return `(T, error)` tuples; never panic.
- Custom error types support `errors.Is()` / `errors.As()`.
- Functional options pattern for client configuration.
- Unexported fields, exported methods.
- GoDoc comments on all public types and methods.
- Table-driven tests with testify.

## Package Naming

- **Module path:** `github.com/smplkit/go-sdk`
- **Import:** `import "github.com/smplkit/go-sdk"` then use as `smplkit.NewClient(...)`
- **Install:** `go get github.com/smplkit/go-sdk`
