.PHONY: install test lint build generate \
	config_runtime_showcase config_management_showcase \
	flags_runtime_showcase flags_management_showcase \
	logging_runtime_showcase logging_management_showcase

install:
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
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

config_runtime_showcase:
	go run examples/config_runtime_showcase.go examples/config_runtime_setup.go examples/helpers.go

config_management_showcase:
	go run examples/config_management_showcase.go examples/helpers.go

flags_runtime_showcase:
	go run examples/flags_runtime_showcase.go examples/flags_runtime_setup.go examples/helpers.go

flags_management_showcase:
	go run examples/flags_management_showcase.go examples/flags_runtime_setup.go examples/helpers.go

logging_runtime_showcase:
	go run examples/logging_runtime_showcase.go examples/logging_runtime_setup.go examples/helpers.go

logging_management_showcase:
	go run examples/logging_management_showcase.go examples/helpers.go
