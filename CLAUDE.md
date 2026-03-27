# smplkit Go SDK

## Repository structure

Two-layer architecture:
- `internal/generated/` — Auto-generated client types from OpenAPI specs. Do not edit manually.
- Root package (`*.go` excluding `internal/`) — Hand-crafted SDK wrapper. This is the public API.

## Regenerating clients

```bash
make generate
```

This regenerates ALL clients from ALL specs in `openapi/`. Do NOT edit files under `internal/generated/` manually — they will be overwritten on next generation.

## Commits

Commit directly to main with conventional commit messages. No branches or PRs.
Exception: automated regeneration PRs from source repos use `regen/` branches by design.

## Testing

```bash
go test -race -coverprofile=coverage.out ./...
```

Target 90%+ coverage on the SDK wrapper layer. Generated code coverage is not enforced.

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
