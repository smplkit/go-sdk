.PHONY: install test lint build generate

install:
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	go mod download

test:
	go test -race -coverprofile=coverage.out ./...

lint:
	golangci-lint run

build:
	go build ./...

generate:
	./scripts/generate.sh
