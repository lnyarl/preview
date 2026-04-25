# Preview 사용 설명서

## 1. 준비

### 요구사항

- **Go 1.22+** — `go version`으로 확인
- **Docker** — Hub가 아닌 **Agent 머신**에 필요
- **git** — Agent 머신에 필요 (bare clone + worktree)

### 빌드

```bash
# DB 마이그레이션 (hub.db 생성)
go run ./cmd/hub migrate up

# 바이너리 빌드 (선택 사항)
go build -o bin/hub ./cmd/hub
go build -o bin/agent ./cmd/agent
```

---

## 2. Hub 실행

```bash
GITHUB_WEBHOOK_SECRET=비밀키 \
ADMIN_PASSWORD=관리자비밀번호 \
go run ./cmd/hub
```

Hub가 `:3000`에서 시작됩니다.

### 주요 환경변수

| 변수 | 기본값 | 설명 |
|---|---|---|
| `GITHUB_WEBHOOK_SECRET` | **(필수)** | GitHub Webhook HMAC 서명 검증 키 |
| `ADMIN_PASSWORD` | 빈 값 (인증 없음) | `/admin/*` Basic Auth 비밀번호. 빈 값이면 로컬 개발 모드 (경고 로그 출력). |
| `HUB_ADDR` | `:3000` | 바인드 주소 |
| `DATABASE_URL` | `sqlite://./hub.db` | DB 경로 |
| `PREVIEW_BASE_DOMAIN` | `localhost` | 리버스 프록시 호스트 매칭 base (pr-N.preview.**여기**) |
| `PREVIEW_REPO_URL` | (빈 값) | Agent에 전달할 git URL. 빈 값이면 webhook의 repo full name을 그대로 사용. |
| `RECONCILE_INTERVAL` | `1m` | Reconciler 주기 |
| `STALE_ASSIGNED_AFTER` | `5m` | assigned 상태가 이 시간 이상 지속되면 queued로 복귀 |

`.env` 파일이 있으면 자동으로 로드됩니다 (없어도 동작합니다).  
`.env.example`을 복사해서 값을 채우세요:

```bash
cp .env.example .env
# .env 편집: GITHUB_WEBHOOK_SECRET, ADMIN_PASSWORD 설정
```

### 관리자 대시보드

Hub 실행 후 브라우저에서 `http://localhost:3000/admin` 접속  
→ 사용자: `admin` / 비밀번호: `ADMIN_PASSWORD` 값

---

## 3. Agent 등록 및 실행

### 3-1. Agent 등록 (대시보드)

1. `http://localhost:3000/admin/agents` 접속
2. **Add Agent** 폼에서 이름과 라벨 입력 후 Submit
3. 생성된 **토큰을 즉시 복사** — 페이지를 벗어나면 다시 볼 수 없음

### 3-1b. Agent 등록 (JSON API)

```bash
curl -u admin:관리자비밀번호 \
  -H 'Content-Type: application/json' \
  -X POST http://localhost:3000/admin/agents \
  -d '{"name":"my-agent","labels":{"env":"home"}}'
# 응답 예시: {"id":"...","name":"my-agent","token":"agt_XXXXX"}
# token 필드를 저장해 두세요.
```

### 3-2. Agent 실행

Agent를 실행할 머신(Docker가 설치된 곳)에서:

```bash
go run ./cmd/agent start \
  --hub-url=ws://HUB주소:3000/agent/ws \
  --token=agt_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX \
  --repo-url=https://github.com/owner/repo.git \
  --label env=home
```

#### Private 리포지토리 인증

`--repo-url`이 Private 리포이면 URL에 토큰을 포함합니다:

```bash
--repo-url=https://사용자명:ghp_토큰값@github.com/owner/repo.git
```

**GitHub PAT 발급 방법 (Fine-grained, 최소 권한):**

1. GitHub → Settings → Developer settings → **Fine-grained tokens** → Generate new token
2. **Repository access**: Only select repositories → 해당 리포 선택
3. **Repository permissions → Contents**: `Read-only`
4. 나머지는 모두 No access

> Classic PAT를 사용한다면 `repo` 스코프 하나면 됩니다.

| 플래그 | 설명 |
|---|---|
| `--hub-url` | Hub WebSocket 주소 (`ws://` 또는 `wss://`) |
| `--token` | 등록 시 발급받은 Agent 토큰 |
| `--repo-url` | 빌드할 Git 리포지토리 URL |
| `--label key=value` | 라벨 (여러 개 지정 가능: `--label env=home --label arch=amd64`) |
| `--advertise-host` | Hub가 컨테이너 접속 시 사용할 이 머신의 IP (기본: `127.0.0.1`) |
| `--work-dir` | 워크 디렉토리 경로 (기본: `~/.hub-agent`) |
| `--max-jobs` | 동시 처리할 최대 Job 수 (기본: `1`) |
| `--prefetch-interval` | bare clone 사전 fetch 주기 (기본: `5m`, `0`이면 비활성) |

---

## 4. Docker Compose로 한 번에 실행

Hub + Agent를 `docker compose up` 한 번으로 실행합니다.

### 4-1. .env 설정

```bash
cp .env.example .env
```

`.env`에서 아래 두 항목만 필수로 채웁니다:

```bash
GITHUB_WEBHOOK_SECRET=아무_문자열
AGENT_REPO_URL=https://github.com/owner/repo.git
```

Private 리포이면 PAT를 URL에 포함:

```bash
AGENT_REPO_URL=https://사용자명:ghp_토큰값@github.com/owner/repo.git
```

**PAT 권한** (Fine-grained 기준):
- Repository access: 해당 리포만 선택
- Repository permissions → **Contents: Read-only**

### 4-2. 실행

```bash
# 마이그레이션 + Hub + Agent 동시 시작
docker compose up -d

# 로그 확인
docker compose logs -f
```

`docker compose up` 실행 시 내부 순서:
1. `migrate` 서비스 → DB 마이그레이션 자동 실행
2. `hub` 서비스 시작 → `dev-agent` 자동 등록 (`DEV_AGENT_TOKEN` 고정값 사용)
3. `agent` 서비스 시작 → Hub에 자동 연결

### 4-3. 대시보드 접속

`http://localhost:3000/admin`

- 사용자: `admin`
- 비밀번호: `.env`의 `ADMIN_PASSWORD` 값 (기본값 없음 → 미설정 시 인증 없이 접속)

### 4-4. 중지 / 재시작

```bash
docker compose down          # 중지 (DB 볼륨은 유지)
docker compose down -v       # 중지 + 볼륨(DB) 삭제
docker compose restart hub   # Hub만 재시작
docker compose logs agent    # Agent 로그 확인
```

### 4-5. Linux에서 advertise-host

Linux는 `host.docker.internal`이 기본 지원되지 않습니다. `.env`에 추가:

```bash
AGENT_ADVERTISE_HOST=172.17.0.1   # Docker bridge IP (기본)
```

---

## 5. GitHub Webhook 설정

GitHub 리포지토리 → **Settings** → **Webhooks** → **Add webhook**:

| 항목 | 값 |
|---|---|
| Payload URL | `https://Hub주소/webhooks/github` |
| Content type | `application/json` |
| Secret | `GITHUB_WEBHOOK_SECRET` 값 |
| Events | **Let me select individual events** → `Pull requests`만 체크 |

---

## 6. PR 미리보기 전체 흐름

```
PR 열기
  → Hub: preview row 생성 (status=queued)
  → Agent READY 전송 → Hub: JOB_ASSIGN
  → Agent: git fetch → worktree → docker build → docker run
  → Hub: status=running, agent_host:port 기록
  → 브라우저에서 http://pr-{PR번호}.preview.localhost:3000 접속
     (Hub가 컨테이너로 리버스 프록시)

PR 닫기
  → Hub: JOB_TEARDOWN 전송
  → Agent: 컨테이너 stop+rm, worktree 삭제
  → Hub: status=done
```

### 리포지토리 요구사항

Agent가 빌드하는 리포지토리의 **루트에 `Dockerfile`이 있어야** 합니다.  
컨테이너는 포트 80을 노출해야 Hub가 프록시할 수 있습니다.

---

## 7. 관리자 대시보드 기능

| 페이지 | URL | 기능 |
|---|---|---|
| 대시보드 홈 | `/admin` | Agent / Preview 현황 요약 카드 |
| Agent 목록 | `/admin/agents` | Agent 등록, 삭제, 상태/라벨 확인 |
| Preview 목록 | `/admin/previews` | repo / status 필터, 목록 |
| Preview 상세 | `/admin/previews/{id}` | 상태 타임라인, Rebuild 버튼 |

**Rebuild 버튼**: `done` 또는 `failed` 상태의 preview를 `queued`로 되돌려 재빌드를 트리거합니다.

---

## 8. CLI 서브커맨드

### Hub

```bash
go run ./cmd/hub                            # 데몬 실행
go run ./cmd/hub migrate up                 # 마이그레이션 적용
go run ./cmd/hub migrate down               # 마이그레이션 롤백
go run ./cmd/hub migrate version            # 현재 마이그레이션 버전 확인
go run ./cmd/hub agents list                # Agent 목록 (JSON)
go run ./cmd/hub previews list              # Preview 목록 (JSON)
go run ./cmd/hub previews show <id>         # Preview 단건 조회 (JSON)
go run ./cmd/hub previews seed-stale        # 테스트용 stale assigned preview 생성
```

### 플래그 (데몬 실행 시)

```bash
go run ./cmd/hub --reconcile-interval=2s --stale-assigned-after=3s
```

---

## 9. Graceful Shutdown

### Hub (`Ctrl+C` 또는 `SIGTERM`)

1. `dispatcher.Pause()` — 새로운 Job 배정 중단
2. 진행 중인 HTTP 요청 최대 **30초** 대기
3. 연결된 모든 Agent에 WebSocket close frame (1001) 전송
4. 종료

### Agent (`Ctrl+C` 또는 `SIGTERM`)

1. 새 JOB_ASSIGN 거절 (STATUS_UPDATE `failed` 전송)
2. 진행 중인 docker build 최대 **30초** 대기
3. 실행 중인 컨테이너는 **그대로 유지** — teardown은 PR close 시에만
4. Hub에 WebSocket close frame 전송 후 종료

Agent 재시작 시 기존 컨테이너를 docker label로 자동 감지해 Hub에 `running_previews`로 보고합니다.  
Hub는 이를 DB 상태와 비교해 불일치하는 항목을 자동으로 정리합니다.

---

## 10. 멀티 머신 / 라벨 라우팅

여러 Agent를 다른 라벨로 등록하면 PR의 GitHub Label로 특정 머신에 배포할 수 있습니다.

```bash
# 집 PC Agent
go run ./cmd/agent start ... --label env=home

# 사무실 PC Agent
go run ./cmd/agent start ... --label env=office --label arch=arm64
```

- PR에 GitHub Label `env=home` → 집 PC Agent에서만 빌드
- PR에 GitHub Label 없음 → 가용한 모든 Agent 중 먼저 `READY`를 보낸 Agent에 배정

매칭 규칙: **preview.labels ⊆ agent.labels** (preview가 요구하는 라벨이 agent 라벨의 부분집합이면 매칭)

---

## 11. Reconciliation (자동 복구)

Hub는 1분 주기(기본값)로 다음을 수행합니다:

- `assigned` 상태가 5분 이상 지속된 preview → `queued`로 복귀 (Agent 크래시 대비)
- Agent 재연결 시: DB running이지만 Agent에 없는 preview → `failed`로 표시

수동으로 Reconciler 주기를 단축해 테스트할 수 있습니다:

```bash
go run ./cmd/hub --reconcile-interval=2s --stale-assigned-after=3s
```

---

## 12. 개발 / 테스트

```bash
# 전체 테스트
go test ./... -count=1

# Race detector (Linux/macOS)
go test ./internal/hub -run TestDispatcherClaimRace -race

# 빌드 검사
go build ./...
go vet ./...
```

### 로컬 Webhook 시뮬레이션

```bash
export PORT=3000
export SECRET=test-secret

PAYLOAD='{"action":"opened","pull_request":{"number":1,"head":{"sha":"abc","ref":"feat/x"},"labels":[]},"repository":{"full_name":"owner/repo"}}'
SIG=$(printf '%s' "$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" -hex | sed 's/^.* //')

curl -s -X POST http://localhost:$PORT/webhooks/github \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: pull_request" \
  -H "X-Hub-Signature-256: sha256=$SIG" \
  -d "$PAYLOAD"
```

---

## 13. 프로덕션 체크리스트

- [ ] **`ADMIN_PASSWORD` 설정** — 미설정 시 대시보드 무인증
- [ ] **`GITHUB_WEBHOOK_SECRET` 설정** — 미설정 시 Hub 시작 불가
- [ ] **Hub 앞에 TLS 종료 프록시** (caddy/nginx) — Hub 자체는 HTTP만
- [ ] **`hub.db` 정기 백업** — `cp hub.db hub.db.bak`
- [ ] **Agent의 `--advertise-host`** — Hub와 통신 가능한 IP로 설정
- [ ] **`PREVIEW_BASE_DOMAIN`** — 실제 도메인으로 설정 (예: `example.com`)
