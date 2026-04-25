# Preview -- development task runner.
# 사용법: make <target> [v=버전]

.PHONY: dev up down logs ps push tag build fmt vet lint test sqlc \
        migrate-up migrate-down migrate-version run-hub run-agent

# ── 개발 환경 (Docker) ────────────────────────────────────────────────────────

## Hub + Agent를 Docker로 시작 (백그라운드).
dev:
	docker compose up -d --build

## docker compose up (빌드 없이, 빠름).
up:
	docker compose up -d

## 중지 (볼륨 유지).
down:
	docker compose down

## 실시간 로그 스트림.
logs:
	docker compose logs -f

## 컨테이너 상태 확인.
ps:
	docker compose ps

# ── 릴리즈 ────────────────────────────────────────────────────────────────────

## 현재 브랜치를 origin에 푸시.
push:
	git push

## 태그 생성 + 푸시 → GitHub Actions 릴리즈 빌드 트리거.
## 사용법: make tag v=1.0.0
tag:
	@test -n "$(v)" || (echo "버전을 지정하세요: make tag v=1.0.0" && exit 1)
	git tag v$(v)
	git push origin v$(v)
	@echo "✓ v$(v) 태그 푸시 완료 — GitHub Actions 릴리즈 빌드가 시작됩니다."
	@echo "  https://github.com/lnyarl/preview/actions"

# ── 코드 품질 ─────────────────────────────────────────────────────────────────

## 전체 테스트.
test:
	go test ./... -count=1

## 빌드 검사.
build:
	go build -o bin/hub ./cmd/hub
	go build -o bin/agent ./cmd/agent

## 포맷.
fmt:
	go fmt ./...

## go vet.
vet:
	go vet ./...

## golangci-lint (설치 필요: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest).
lint:
	golangci-lint run ./...

# ── DB / sqlc ─────────────────────────────────────────────────────────────────

## sqlc 코드 재생성.
sqlc:
	sqlc generate

## 마이그레이션 적용.
migrate-up:
	go run ./cmd/hub migrate up

## 마이그레이션 롤백.
migrate-down:
	go run ./cmd/hub migrate down

## 현재 마이그레이션 버전.
migrate-version:
	go run ./cmd/hub migrate version

# ── 로컬 직접 실행 ────────────────────────────────────────────────────────────

## Hub 직접 실행 (Docker 없이).
run-hub:
	go run ./cmd/hub

## Agent 직접 실행. HUB_URL, HUB_TOKEN 환경변수 필요.
run-agent:
	go run ./cmd/agent start --hub-url "$$HUB_URL" --token "$$HUB_TOKEN"
