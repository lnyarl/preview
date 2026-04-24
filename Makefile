# Preview -- development task runner.

.PHONY: run-hub run-agent build fmt vet lint sqlc migrate-up migrate-down migrate-version test

## Run the Hub daemon (default port :3000).
run-hub:
	go run ./cmd/hub

## Run the Agent. HUB_URL and HUB_TOKEN must be set in the environment.
run-agent:
	go run ./cmd/agent start --hub-url "$$HUB_URL" --token "$$HUB_TOKEN"

## Build both binaries into ./bin.
build:
	go build -o bin/hub ./cmd/hub
	go build -o bin/agent ./cmd/agent

## Format every Go file in place.
fmt:
	go fmt ./...

## Static checks via the toolchain.
vet:
	go vet ./...

## Run golangci-lint (must be installed locally -- see README).
lint:
	golangci-lint run ./...

## Run tests.
test:
	go test ./...

## Regenerate sqlc code.
sqlc:
	sqlc generate

## Apply pending migrations (embedded; invokes the Hub subcommand).
migrate-up:
	go run ./cmd/hub migrate up

## Roll back the most recent migration.
migrate-down:
	go run ./cmd/hub migrate down

## Show current migration version.
migrate-version:
	go run ./cmd/hub migrate version
