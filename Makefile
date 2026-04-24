# Preview — development task runner.
#
# Phase 0 ships thin wrappers; Phase 1+ adds real schema, queries, and migrations.
# Targets marked "(Phase 1+)" are declared here so the interface is stable, but
# they require artefacts or tools that do not yet exist in Phase 0.

.PHONY: run-hub run-agent build fmt vet lint sqlc migrate-up migrate-down test

## Run the Hub entry point (Phase 0: "Hello Hub" at :8080).
run-hub:
	go run ./cmd/hub

## Run the Agent entry point (Phase 0: logs "Hello Agent" and exits).
run-agent:
	go run ./cmd/agent

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

## Run golangci-lint (must be installed locally — see README).
lint:
	golangci-lint run ./...

## Run unit tests.
test:
	go test ./...

## Regenerate sqlc code (Phase 1+: requires db/queries/*.sql).
sqlc:
	sqlc generate

## Apply pending migrations (Phase 1+: requires golang-migrate and DATABASE_URL).
migrate-up:
	migrate -path db/migrations -database "$$DATABASE_URL" up

## Roll back the most recent migration (Phase 1+).
migrate-down:
	migrate -path db/migrations -database "$$DATABASE_URL" down 1
