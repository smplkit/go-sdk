.PHONY: install test lint build generate \
	config_runtime_showcase config_management_showcase \
	flags_runtime_showcase flags_management_showcase \
	logging_runtime_showcase logging_management_showcase \
	audit_showcase \
	jobs_showcase \
	showcases

install:
	# Pinned: oapi-codegen v2.8.0 raised its floor to Go 1.25.0, which breaks
	# CI (setup-go reads our go.mod → Go 1.24.3, GOTOOLCHAIN=local can't
	# auto-upgrade). v2.7.2 is the last release compatible with our Go directive
	# and was `@latest` until v2.8.0 shipped, so this restores last-known-good.
	# Revisit when we intentionally raise the module's Go version.
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.2
	go mod download

test:
	go test -race -coverprofile=coverage.out ./...
	cd logging/adapters/slog && go test -race ./...
	cd logging/adapters/zap && go test -race ./...

lint:
	golangci-lint run

build:
	go build ./...

generate:
	./scripts/generate.sh

# ── Six showcases (rule 15 of the cross-SDK overhaul) ──────────────────────

config_management_showcase:
	go run examples/config_management_showcase.go \
		examples/config_management_setup.go \
		examples/helpers.go

config_runtime_showcase:
	go run examples/config_runtime_showcase.go \
		examples/config_runtime_setup.go \
		examples/helpers.go

flags_management_showcase:
	go run examples/flags_management_showcase.go \
		examples/flags_management_setup.go \
		examples/helpers.go

flags_runtime_showcase:
	go run examples/flags_runtime_showcase.go \
		examples/flags_runtime_setup.go \
		examples/helpers.go

logging_management_showcase:
	go run examples/logging_management_showcase.go \
		examples/logging_management_setup.go \
		examples/helpers.go

logging_runtime_showcase:
	go run examples/logging_runtime_showcase.go \
		examples/helpers.go

audit_showcase:
	go run examples/audit_showcase.go \
		examples/helpers.go

jobs_showcase:
	go run examples/jobs_showcase.go \
		examples/jobs_setup.go \
		examples/helpers.go

showcases: config_management_showcase config_runtime_showcase \
	flags_management_showcase flags_runtime_showcase \
	logging_management_showcase logging_runtime_showcase \
	audit_showcase \
	jobs_showcase
