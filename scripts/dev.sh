#!/usr/bin/env bash
# scripts/dev.sh — Hub + Agent 한 번에 실행 (로컬 개발용)
#
# 사용법:
#   AGENT_REPO_URL=https://github.com/owner/repo.git bash scripts/dev.sh
#
# 또는 .env 파일에 아래 변수를 설정한 뒤:
#   bash scripts/dev.sh
#
# 필수 변수:
#   AGENT_REPO_URL        — Agent가 클론할 git URL
#
# 선택 변수 (기본값 있음):
#   GITHUB_WEBHOOK_SECRET — Webhook HMAC 키 (기본: dev-secret)
#   ADMIN_PASSWORD        — 대시보드 비밀번호 (기본: admin)
#   HUB_ADDR              — Hub 바인드 주소 (기본: :3000)
#   AGENT_TOKEN           — 재사용할 기존 토큰 (미설정 시 자동 등록)
#   AGENT_LABELS          — 라벨 목록, 쉼표 구분 (예: env=home,arch=amd64)
#   AGENT_ADVERTISE_HOST  — Hub가 컨테이너 접속 시 쓸 IP (기본: 127.0.0.1)
set -euo pipefail

# ── Go 바이너리 탐색 ────────────────────────────────────────────────────────
# bash scripts/dev.sh 으로 실행 시 PATH 가 인터랙티브 셸과 달라 go 를 못 찾을 수 있음.
if ! command -v go >/dev/null 2>&1; then
  for _candidate in \
    "$HOME/go/bin/go" \
    "$HOME/.local/go/bin/go" \
    "/usr/local/go/bin/go" \
    "/c/Program Files/Go/bin/go" \
    "/c/Go/bin/go"; do
    if [ -x "$_candidate" ]; then
      export PATH="$(dirname "$_candidate"):$PATH"
      break
    fi
  done
fi

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: 'go' 명령어를 찾을 수 없습니다."
  echo "  Go 설치: https://go.dev/dl/"
  echo "  또는 터미널에서 직접 실행: AGENT_REPO_URL=... go run ./scripts/dev.go (미지원)"
  echo "  임시 해결책: PATH=\$(go env GOROOT)/bin:\$PATH bash scripts/dev.sh"
  exit 1
fi

echo "==> Using Go: $(command -v go) ($(go version | awk '{print $3}'))"

# ── .env 로드 ──────────────────────────────────────────────────────────────
if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

# ── 기본값 ─────────────────────────────────────────────────────────────────
GITHUB_WEBHOOK_SECRET="${GITHUB_WEBHOOK_SECRET:-dev-secret}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
HUB_ADDR="${HUB_ADDR:-:3000}"
AGENT_ADVERTISE_HOST="${AGENT_ADVERTISE_HOST:-127.0.0.1}"
AGENT_LABELS="${AGENT_LABELS:-}"

PORT="${HUB_ADDR#:}"

if [ -z "${AGENT_REPO_URL:-}" ]; then
  echo "ERROR: AGENT_REPO_URL 이 설정되지 않았습니다."
  echo "  예시: AGENT_REPO_URL=https://github.com/owner/repo.git bash scripts/dev.sh"
  echo "  또는 .env 파일에 AGENT_REPO_URL=... 추가"
  exit 1
fi

# ── 종료 시 정리 ────────────────────────────────────────────────────────────
HUB_PID=""
AGENT_PID=""
cleanup() {
  echo ""
  echo "Stopping Hub and Agent..."
  [ -n "$HUB_PID" ]   && kill "$HUB_PID"   2>/dev/null || true
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null || true
  wait "$HUB_PID" "$AGENT_PID" 2>/dev/null || true
  echo "Done."
}
trap cleanup EXIT INT TERM

# ── 1. 마이그레이션 ─────────────────────────────────────────────────────────
echo "==> Applying migrations..."
go run ./cmd/hub migrate up

# ── 2. Hub 기동 ─────────────────────────────────────────────────────────────
echo "==> Starting Hub on $HUB_ADDR ..."
GITHUB_WEBHOOK_SECRET="$GITHUB_WEBHOOK_SECRET" \
ADMIN_PASSWORD="$ADMIN_PASSWORD" \
HUB_ADDR="$HUB_ADDR" \
go run ./cmd/hub &
HUB_PID=$!

# Hub 준비 대기 (최대 15초)
echo -n "    Waiting for Hub to be ready"
for i in $(seq 30); do
  if curl -sf "http://localhost:${PORT}/health" >/dev/null 2>&1; then
    echo " OK"
    break
  fi
  echo -n "."
  sleep 0.5
  if [ "$i" -eq 30 ]; then
    echo ""
    echo "ERROR: Hub가 15초 안에 시작되지 않았습니다."
    exit 1
  fi
done

# ── 3. Agent 토큰 자동 발급 ─────────────────────────────────────────────────
if [ -z "${AGENT_TOKEN:-}" ]; then
  echo "==> Registering dev-agent..."
  RESP=$(curl -sf \
    -u "admin:${ADMIN_PASSWORD}" \
    -H 'Content-Type: application/json' \
    -X POST "http://localhost:${PORT}/admin/agents" \
    -d '{"name":"dev-agent","labels":{}}' 2>&1) || {
    echo "ERROR: Agent 등록 실패. Hub 응답: $RESP"
    exit 1
  }
  # token 필드 추출 (jq 없이)
  AGENT_TOKEN=$(echo "$RESP" | grep -o '"token":"[^"]*"' | sed 's/"token":"//;s/"//')
  if [ -z "$AGENT_TOKEN" ]; then
    echo "ERROR: 토큰을 가져오지 못했습니다. Hub 응답: $RESP"
    exit 1
  fi
  echo "    Token: $AGENT_TOKEN"
fi

# ── 4. --label 플래그 조립 ─────────────────────────────────────────────────
LABEL_FLAGS=""
if [ -n "$AGENT_LABELS" ]; then
  # "env=home,arch=amd64" → "--label env=home --label arch=amd64"
  IFS=',' read -ra LABELS <<< "$AGENT_LABELS"
  for lbl in "${LABELS[@]}"; do
    LABEL_FLAGS="$LABEL_FLAGS --label $lbl"
  done
fi

# ── 5. Agent 기동 ───────────────────────────────────────────────────────────
echo "==> Starting Agent (repo: $AGENT_REPO_URL) ..."
# shellcheck disable=SC2086
go run ./cmd/agent start \
  --hub-url="ws://localhost:${PORT}/agent/ws" \
  --token="$AGENT_TOKEN" \
  --repo-url="$AGENT_REPO_URL" \
  --advertise-host="$AGENT_ADVERTISE_HOST" \
  $LABEL_FLAGS &
AGENT_PID=$!

# ── 안내 ────────────────────────────────────────────────────────────────────
echo ""
echo "┌─────────────────────────────────────────────┐"
echo "│  Preview 개발 환경 실행 중                  │"
echo "├─────────────────────────────────────────────┤"
printf "│  Hub:    http://localhost:%-18s │\n" "${PORT}"
printf "│  Admin:  http://localhost:%s/admin        │\n" "${PORT}"
printf "│          ID: admin / PW: %-18s │\n" "${ADMIN_PASSWORD}"
echo "│                                             │"
echo "│  Ctrl+C 로 종료                             │"
echo "└─────────────────────────────────────────────┘"
echo ""

wait "$HUB_PID" "$AGENT_PID"
