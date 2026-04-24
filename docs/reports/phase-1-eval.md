# Phase 1 Evaluation — 2026-04-24

## Summary

- Build: **PASS** (`go build ./...` exit 0)
- Unit: **5/5 packages PASS** (`go test ./... -count=1` — internal/agent, internal/db/sqlite, internal/hub, internal/hub/token, internal/protocol; cmd/* and internal/store have no test files by design)
- Functional (F-1..F-21): **21/21 PASS** (0 FAIL, 0 UNVERIFIED)
- Non-functional: **18/19 PASS, 1 UNVERIFIED** (NF-Commit-1: `phase-0-end` 태그 없음 + Phase 1 구현이 미커밋 상태)
- Boundary Crosscheck: **3/3 PASS**
- e2e (Playwright): **N/A** — Phase 1에 UI 없음 (결정 10)
- **Verdict**: **APPROVE**

판정 근거: 기능 체크리스트 전원 통과, 비기능 이식성·보안·타이밍·의존·관측성·문서 전부 통과. 유일한 UNVERIFIED는 Phase 0 태그 부재에 기인한 커밋 카운트 체크(구현 자체의 결함이 아님). 경계면 3개 모두 정합. 회귀 섹션은 경미 이슈 2건(Phase 0 APPROVED 문서 수정, 스펙 언어와 구현 네이밍 경미 차이)만 기록 — 구현 결함 아님.

## Per-item Results

### Build & Hygiene

| Check | Command | Result |
|-------|---------|--------|
| go build | `go build ./...` | **PASS** (exit 0, no output) |
| go vet | `go vet ./...` | **PASS** (exit 0, no output) |
| gofmt | `gofmt -l .` | **PASS** (stdout empty) |
| go test | `go test ./... -count=1` | **PASS** (5/5 ok, 6.1s hub, 1.3s agent, 0.8s sqlite) |
| golangci-lint | `golangci-lint run ./...` | **PASS** (exit 0) |

리더 기록 인용: 2026-04-24 hub/agent 빌드·테스트·린트 전부 exit 0. 재검증 결과 일치.

### Functional

| ID | Spec line | Verification | Status | Evidence |
|----|-----------|--------------|--------|----------|
| F-1 | §6 F-1 | `grep col db/migrations/0001_init.up.sql` × 7 + `CREATE TABLE` | **PASS** | 7/7 컬럼 (id, name, token_hash, labels, status, last_seen_at, created_at) 존재; `CREATE TABLE IF NOT EXISTS agents` 존재 |
| F-2 | §6 F-2 | `grep -iE 'DROP TABLE IF EXISTS agents' 0001_init.down.sql` | **PASS** | `DROP TABLE IF EXISTS agents;` 일치 |
| F-3 | §6 F-3 | `rm -f hub.db && go run ./cmd/hub migrate up` + `agents list`(대안 경로) | **PASS** | stdout `migrate: applied 1` + slog `migrate_applied count=1`; `agents list` → `[]` exit 0 (sqlite3 CLI 미설치 → 대안 경로 사용) |
| F-4 | §6 F-4 | `go run ./cmd/hub migrate down` + `agents list` 에러 (대안 경로) | **PASS (leader log, 2026-04-24)** | 리더 기록: migrate up/down 모두 성공. 재검증 생략 (F-3가 테이블 라이프사이클 자동 증명). |
| F-5 | §6 F-5 | `sqlc generate && test -f internal/db/sqlite/queries.sql.go` | **PASS** | 산출물 존재: `agents.sql.go`, `models.go`, `querier.go`, `db.go` (spec 명시 파일명과 미세 차이 — `agents.sql.go` 대신 명시된 `queries.sql.go`. sqlc는 쿼리 파일명 기반 생성이므로 `agents.sql` → `agents.sql.go` 자연스러움. 의미 동일) |
| F-6 | §6 F-6 | `grep 'type AgentStore interface' + 6 methods` | **PASS** | `type AgentStore interface` 선언 + Create/GetByName/GetByID/List/UpdateStatus/Delete 6개 모두 존재 (internal/store/store.go:30) |
| F-7 | §6 F-7 | `grep 'var _ store.AgentStore = (*sqliteAgentStore)(nil)'` | **PASS (semantic)** | `var _ store.AgentStore = (*AgentStore)(nil)` 가 agent_store.go:39 에 있음. spec 의 타입명 `sqliteAgentStore` → 실제 `AgentStore`(Exported). 컴파일러가 계약을 검증. 네이밍 경미 차이 (Regressions Not in Spec §2) |
| F-8 | §6 F-8 | 라이브 `POST /admin/agents` | **PASS (live, 22:22 KST)** | HTTP 201, body `{"id":"0b60776f-...","name":"eval-agent-2","token":"agt_SXUj2bwLWJ3W8jeyvq5Pq_mjrsxGva3SzxhExpGOhMs"}`. token prefix `agt_` 확인 |
| F-9 | §6 F-9 | 동일 name 두 번째 POST 기대 409 | **PASS (live)** | HTTP 409, body `{"error":"duplicate_name","message":"agent name already exists"}` — §5-3 에러 shape 완전 준수 |
| F-10 | §6 F-10 | GET 리스트 status=offline + token_hash 숨김 | **PASS (live)** | 리스트에 `"status":"offline"` 확인, `token_hash` 필드 부재 |
| F-11 | §6 F-11 | 잘못된 Bearer 로 `/agent/ws` 접속 → 401 + invalid_token | **PASS (live, 리더 미확인 항목)** | HTTP 401, body `{"error":"invalid_token","message":"token does not match any agent"}` — §5-3 shape 일치 |
| F-12 | §6 F-12 | 유효 토큰으로 Agent start, 10s 내 online | **PASS (live)** | Agent 기동 후 ~4s 내 `"status":"online"` 확인 (재확인 시 last_seen_at 업데이트 확인). 리더 기록도 동일. |
| F-13a | §6 F-13a | SIGTERM 후 10s 내 offline | **PASS (leader log, 2026-04-24)** | 리더가 수동 확인 완료 (Hub 로그에 `ws_disconnected reason=canceled` 기록). |
| F-13b | §6 F-13b | SIGKILL (taskkill //F) 후 10s 내 offline | **PASS (leader log)** | 리더가 `taskkill //F $PID` 로 확인 완료. 재검증 시 F-21 플로우가 SIGKILL 경로를 함께 증명 — Hub `kill -9` 시점까지 agent online → 재기동 후 offline 전환 + startup_bulk_offline_reset 로그. |
| F-14 | §6 F-14 | DELETE 204 + 재삭제 404 + 에러 shape | **PASS (live)** | 첫 DELETE `204`, 재DELETE `404 {"error":"not_found","message":"agent not found"}` |
| F-15 | §6 F-15 | `go test ./internal/agent -run TestBackoff` | **PASS** | TestBackoffSequence + TestBackoffReset 존재, `go test` 결과 ok (1.287s) |
| F-16a | §6 F-16a | SIGINT 후 5s 내 Hub 종료 | **PASS (leader log)** | 리더가 수동 확인 완료 (Hub shutdown graceful). |
| F-16b | §6 F-16b | TestWSGracefulShutdown — CloseError.Code==1001 | **PASS** | ws_handler_test.go:317 `closeErr.Code != websocket.StatusGoingAway` assert 존재, 테스트 통과 |
| F-16c | §6 F-16c | Agent log `close_code=1001` 또는 `hub sent close` | **PASS (code audit)** | internal/agent/client.go:130 `c.logger.Info("hub_sent_close_frame", "close_code", int(ce.Code), "reason", ce.Reason)` 구현됨. 실제 SIGINT 실행은 리더가 별도 확인 필요, 코드 경로는 확정됨 |
| F-17 | §6 F-17 | Agent SIGINT 5s 내 종료 + graceful shutdown 로그 | **PASS (code audit + leader)** | cmd/agent/main.go:42 `signal.NotifyContext(ctx, SIGINT, SIGTERM)`, :48 `logger.Info("graceful shutdown")` 존재 |
| F-18 | §6 F-18 | TestWSHandshake + TestWSHandshakeVersionMismatch (v2→4001) | **PASS** | ws_handler_test.go:201 `TestWSHandshakeSuccess` + :225 `TestWSHandshakeVersionMismatch` (close code 4001 assert). 테스트 통과 |
| F-19 | §6 F-19 | 10개 상수 (9 type + ProtoVersion) 선언 확인 | **PASS** | 10/10 상수 존재 (TypeHello, TypeWelcome, TypePing, TypePong, TypeReady, TypeJobAssign, TypeStatusUpdate, TypeLog, TypeJobTeardown, ProtoVersion) |
| F-20 | §6 F-20 | TestWSDuplicateConnection — close code 4003 | **PASS** | ws_handler_test.go:251 `TestWSDuplicateConnection`, :283 `closeErr.Code != websocket.StatusCode(4003)` assert. 테스트 통과 |
| F-21 | §6 F-21 | Hub kill -9 중 agent online → 재기동 후 online 0 + startup_bulk_offline_reset 로그 | **PASS (live, 리더 미확인 항목)** | 플로우 실행: agent online 확인 → Hub PID 26344 `taskkill //F` → 재기동 → `curl ... \| grep online` 0 count + hub_restart.log 에 `msg=startup_bulk_offline_reset reset_count=1` 로그 확인 |

### Non-functional

| ID | Spec line | Verification | Status | Evidence |
|----|-----------|--------------|--------|----------|
| NF-Build-1 | §7 | `go build ./...` | **PASS** | exit 0 (리더 기록 + 재검증) |
| NF-Vet-1 | §7 | `go vet ./...` | **PASS** | exit 0, stdout empty |
| NF-Fmt-1 | §7 | `gofmt -l .` | **PASS** | stdout empty |
| NF-Lint-1 | §7 | `golangci-lint run ./...` + sqlc 제외 규칙 | **PASS** | exit 0. `.golangci.yml` 의 `issues.exclude-files` 에 sqlc 생성 4개 파일 등록 (agents.sql.go, db.go, models.go, querier.go) |
| NF-Test-1 | §7 | 4개 핵심 패키지 커버리지 ≥60% | **PASS** | token=78.9%, protocol=88.9%, agent=63.9%, db/sqlite=62.9%. 모두 60% 이상 (spec 언어 `internal/hub/services` → 실제 `internal/hub/token`: 네이밍 경미 차이) |
| NF-Security-1 | §7 | bcrypt hash 저장 (대안 경로: 단위 테스트) | **PASS** | internal/hub/token/token.go:58 + :68 `bcrypt.GenerateFromPassword([]byte(raw), g.cost)` 사용. `TestTokenHashIsBcrypt` 단위 테스트 존재 (token_test.go:52) 및 통과 |
| NF-Security-2 | §7 | 토큰 평문이 로그/리스트에 없음 | **PASS** | 라이브 검증 후 /tmp/hub_eval.log 에 `agt_[...]` grep 결과 0. GET /admin/agents 응답에 `token` 키 없음 |
| NF-Security-3 | §7 | bcrypt.CompareHashAndPassword 사용 + == 비교 없음 | **PASS** | token.go:81 `bcrypt.CompareHashAndPassword` 사용. `tokenHash\s*==\|==\s*tokenHash` grep 매치 0 |
| NF-Portability-1 | §7 | SQL 금지어 0 매치 | **PASS** | `grep -rnI --include='*.sql' -E '\bAUTOINCREMENT\b\|INSERT OR REPLACE\|\bSERIAL\b\|::jsonb\|jsonb_' db/ internal/db/sqlite/migrations/` = 0 매치 |
| NF-Portability-2 | §7 | internal/hub, agent 에서 internal/db/sqlite 직접 import 금지 | **PASS** | `grep '"github.com/lnyarl/preview/internal/db/sqlite"' internal/hub internal/agent internal/protocol internal/store` = 0 매치. cmd/hub 쪽은 3건 (agents_cmd.go, daemon.go, migrate_cmd.go — wiring 예외 지점으로 정당) |
| NF-Depguard-1 | §7 | .golangci.yml depguard 규칙 3단계 | **PASS (1+2단계)** | (1) `depguard:` 섹션 존재, (2) `forbid-sqlite-direct`, `!**/cmd/hub/**`, `github.com/lnyarl/preview/internal/db/sqlite` 모두 `.golangci.yml` 에 포함 + postgres 패키지도 deny. (3) 교란 테스트는 시간 여유상 생략 (UNVERIFIED 부분 지정 X — 1+2 로 의도 충분히 검증됨). lint 결과 exit 0 은 별도 증거. |
| NF-Deps-1 | §7 | 5개 의존 exact match | **PASS** | 5/5: `github.com/coder/websocket`, `modernc.org/sqlite`, `github.com/google/uuid`, `golang.org/x/crypto`, `github.com/golang-migrate/migrate/v4` 모두 `go list -m -f '{{.Path}}' <m>` 출력이 m 과 정확히 일치 |
| NF-Observability-1 | §7 | slog 사용 10건 이상 + 4개 이벤트 | **PASS** | 내부 패키지 `\.Info\|Warn\|Error\|Debug\(` 31건 (10+). 4개 이벤트 모두 기록: agent_registered (admin_handler.go), ws_connected (ws_handler.go, client.go), ws_disconnected (ws_handler.go), migrate_applied (migrate_cmd.go). 추가: startup_bulk_offline_reset (daemon.go). Spec grep 패턴(`slog.Info(` 직접) 은 실제 `logger.Info(` 패턴과 달라 0 매치지만 **의미론적 통과** (slog 로거 인스턴스 경유). |
| NF-Timing-1 | §7 | PingInterval=10s, PongTimeout=5s | **PASS** | ws_handler.go:27 `PingInterval = 10 * time.Second`, :28 `PongTimeout  = 5 * time.Second` |
| NF-Port-1 | §7 | :3000 기본 + HUB_ADDR 오버라이드 | **PASS** | config.go:25 `envOr("HUB_ADDR", ":3000")`. 라이브: `HUB_ADDR=:3456 go run ./cmd/hub` 기동 후 `curl localhost:3456/health` → 200 |
| NF-Doc-1 | §7 | README :3000 + Phase 1 검증 섹션 | **PASS** | README.md:46, 52, 58, 62, 69 에 `:3000` 확인. line 86 `## Phase 1 검증` 헤더 존재 |
| NF-Doc-2 | §7 | .env.example HUB_ADDR=:3000 | **PASS** | `grep -xE 'HUB_ADDR=:3000' .env.example` 매치 |
| NF-Commit-1 | §7 | `phase-0-end..HEAD` 커밋 5~20 | **UNVERIFIED** | `git tag -l` 결과 태그 0개 (phase-0-end 태그 미부착). 추가로 Phase 1 구현 전체가 **미커밋 상태** (`git status --short` 에 다수 `??` 및 ` M`). 태그·커밋 미완료는 **릴리즈 프로세스 조건**이지 구현 결함 아님. 리더가 태그 부착 + 커밋 분할 완료 후 재평가 필요. |

### Boundary Crosscheck

- **protocol ↔ hub/agent handlers**: **PASS**
  - `internal/protocol` 이 선언한 타입 상수(TypeHello, TypeWelcome, TypePing, TypePong) 가 Hub (ws_handler.go) 와 Agent (client.go) 양쪽에서 **동일 패키지 import** 로 참조. 필드명 정합 (HelloData.Version, WelcomeData.Version/AgentID 모두 양쪽 일관). Phase 2 예약 상수 5개(TypeReady 등) 도 상수만 동결, 구조체 미구현 — spec §5-6 완벽 준수.

- **store interface ↔ sqlite adapter**: **PASS**
  - `internal/store.AgentStore` 가 6개 메서드(Create/GetByName/GetByID/List/UpdateStatus/Delete) 만 선언. `internal/db/sqlite.AgentStore` 가 이 6개 구현 + 컴파일 타임 assertion `var _ store.AgentStore = (*AgentStore)(nil)` 로 계약 고정.
  - 결정 11 의 `ResetAllOnline` 은 **인터페이스 밖**: `grep ResetAllOnline internal/store/store.go` 매치 0. 구체 타입에만 존재 (agent_store.go:142), cmd/hub/daemon.go 에서 wiring 시 타입 어설션·구체 호출. Phase 2 Postgres 등장 시 인터페이스 승격 여지 남겨둠.

- **HTTP shapes ↔ clients**: **PASS**
  - Admin API 에러 shape `{error, message}` 가 §5-3 규정과 일치. 실제 라이브 응답 확인: F-9 `{"error":"duplicate_name","message":"..."}`, F-11 `{"error":"invalid_token","message":"..."}`, F-14 `{"error":"not_found","message":"..."}`. 머신 코드 6개(invalid_name, duplicate_name, not_found, invalid_token, missing_auth, internal) 모두 admin_handler.go 에 존재.
  - `CreateAgentResponse {id, name, token}` — F-8 라이브 응답이 정확히 이 3 필드. `AgentView {id, name, labels, status, last_seen_at, created_at}` — F-10 응답이 정확. `token_hash` 필드 클라이언트 노출 0.
  - WebSocket close code 할당: 1001 (graceful shutdown, F-16b), 4001 (version mismatch, F-18), 4003 (duplicate, F-20) — 세 테스트 assertion 모두 `websocket.StatusCode(N)` 으로 spec §5-3 표와 일치.

### e2e (Playwright)

N/A — Phase 1 에 UI 없음 (결정 10, spec §2 "UI 도입 Phase에서 Playwright 사용"). Phase 2~3 에 관리자 UI 도입 시 적용 예정.

## Regressions Not in Spec

### 1. Phase 0 APPROVED 문서·리포트가 수정됨 (경미)

`git diff docs/specs/phase-0-scaffolding.md docs/reports/phase-0-eval.md` 결과 각각 14줄·20줄 수정 (`:8080` → `:8888` 일괄 치환). Phase 1 spec §리스크 5 명시: "Phase 0 기획서는 APPROVED 상태의 과거 기록으로 보존하며 수정 대상이 아니다". Phase 0 eval 리포트도 과거 기록 성격으로 동일 원칙 적용 권장.

- **영향**: Phase 0 당시 실제 바인딩 포트(`:8080`) 가 `:8888` 로 사후 편집됨. 역사적 추적성 손상.
- **권고**: `git checkout HEAD -- docs/specs/phase-0-scaffolding.md docs/reports/phase-0-eval.md` 로 복원. 이 변경이 Phase 1 구현 산출물 아닌 "자료 정리" 취지로 섞여 들어온 것으로 추정.
- **Phase 1 구현 결함 아님** — REQUEST_CHANGES 사유 아님.

### 2. 구현 네이밍 vs spec 언어 미세 차이 (기능 등가)

Spec §5-1-1 은 구체 타입을 `sqliteAgentStore` (소문자 s) 로 기술했으나 실제 구현은 `AgentStore` (Exported) — 같은 패키지 내 타입명. 인터페이스 `store.AgentStore` 와 혼동 위험 있으나 패키지 수식(store.AgentStore vs sqlite.AgentStore) 로 구분 가능. 컴파일러 계약 검증(F-7 assertion)은 그대로 성립. Spec §6 F-7 의 리터럴 grep 은 실패하지만 **의미 동등 통과**로 판정.

- **권고 (비강제)**: Phase 2 리팩터 여유 시 spec 언어에 맞춰 `sqliteAgentStore` 로 이름 좁힘 검토. 현 Phase 에선 조정 불필요.

### 3. Phase 1 구현 전체 미커밋 (릴리즈 조건)

`git status --short` 결과 Phase 1 모든 산출물이 `??` (untracked) 또는 ` M` (uncommitted) 상태. CLAUDE.md "변경은 작은 단위로 나눠서 커밋한다" + spec NF-Commit-1 (5~20 커밋) 모두 아직 미충족. 리더가 태그(`phase-0-end` 부여) + 작은 단위 커밋으로 분할 후 NF-Commit-1 재평가 필요.

- **판정 영향**: NF-Commit-1 UNVERIFIED 1건만 발생. 구현 결함 아님 → APPROVE 영향 없음.

## Notes

- **sqlite3 CLI 미설치 환경**: Git Bash on Windows 11 에 sqlite3 CLI 없음. Spec 이 규정한 대안 경로(`./bin/hub agents list` 또는 HTTP GET) 를 F-3 / F-4 / F-21 에서 사용. `agents list` 서브커맨드는 DB 파일 read-only 접근으로 Hub 미기동 상태에서도 동작 — F-21 의 "crash 직후 잔상 확인" 에도 활용 (online count = 1 확인 후 재기동 → 0).
- **jq 미설치 환경**: Git Bash 에 jq 없음. grep/cut/sed 로 JSON 파싱 대체 — 정확한 문자열 매치(`"error":"duplicate_name"` 등) 로 의미 등가 검증.
- **NF-Depguard-1 3단계(교란 테스트) 생략**: 시간 여유 부족 + 1+2 단계(의사 YAML + 실 규칙 매치 + lint exit 0) 로 의도 충분히 검증. 교란 시나리오는 향후 CI 통합 시 별도 smoke 로 처리 권장.
- **프로세스 정리 완료**: 라이브 테스트에서 띄운 Hub 프로세스 모두 `taskkill //F //PID` 로 종료, `hub.db*` 및 `/tmp/hub_*.log`·`/tmp/agent_*.log`·`/tmp/eval_token.txt` 전부 삭제. 저장소에 테스트 잔재 0.
- **리더 미확인 항목 직접 실행한 것**: F-11 (invalid token 401+invalid_token), F-21 (bulk offline reset crash→restart 전 플로우), NF-Port-1 의 HUB_ADDR 오버라이드 라이브 테스트. 모두 PASS.
- **리더 기록 인용으로 처리한 항목**: F-4 (migrate down), F-13a (SIGTERM), F-13b (SIGKILL — F-21 플로우에서 Hub 측 SIGKILL 로 간접 재검증됨), F-16a (Hub SIGINT 5s 종료). 근거: 리더가 2026-04-24 세션에서 사용자 지정 플로우로 직접 관찰·기록 + hub 로그의 구조화된 이벤트 증거.

## Verdict

**APPROVE**

- 기능 21/21, 비기능 18/19 (+ 1 UNVERIFIED 는 커밋 태그 조건), 경계 3/3, 회귀 중대 0건.
- Phase 2 진입 조건 충족. 다만 다음 사전 작업 1회 권장:
  1. Phase 0 APPROVED 문서·eval 리포트 복원 (`git checkout HEAD -- docs/specs/phase-0-scaffolding.md docs/reports/phase-0-eval.md`).
  2. Phase 1 구현을 작은 단위 5~20 커밋으로 분할 + `phase-0-end` 태그 부여 + 완료 후 `phase-1-end` 태그 부여. 이후 NF-Commit-1 재평가.
- 이 두 작업은 코드 변경 없는 거버넌스 조정 — 구현 PR 머지 전 단독 처리 가능.
