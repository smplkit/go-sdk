.PHONY: test lint build generate

test:
	go test -race -coverprofile=coverage.out ./...

lint:
	golangci-lint run

build:
	go build ./...

generate:
	./scripts/generate.sh
