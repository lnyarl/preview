# Phase 10 — Repository-scoped Build Secrets (.env)

Status: **DRAFT (rev 2 — review feedback applied)**
Author: planner
Date: 2026-04-27

---

## 1. Phase 개요

### 1-1. 배경

Phase 6 이후 Agent 의 빌드 경로는 사용자 저장소를 clone 한 worktree 에서 `docker compose up -d` (compose 모드) 또는 `docker build` + `ContainerCreate/Start` (Dockerfile 모드)를 실행한다(`internal/agent/runner.go:283~336`). 그러나 사용자의 `docker-compose.yml` 이 `env_file: .env` 또는 `${SECRET_NAME}` 보간(interpolation)을 사용할 경우, `.env` 파일이 worktree 안에 존재하지 않아 다음 두 시나리오 모두에서 빌드가 실패한다.

1. **compose 모드 — 명시적 env_file**: `services.app.env_file: .env` 가 선언되어 있으면 docker compose 가 시작 시점에 `unable to read .env: no such file` 또는 비슷한 에러로 종료(compose v2 는 환경에 따라 경고만 내고 빈 값을 주입하기도 하지만, 운영자가 의도한 비밀이 비어있는 채로 컨테이너가 떠 디버깅 지옥으로 이어진다).
2. **compose 모드 — `${VAR}` 보간**: `image: ${REGISTRY}/api:latest` 같은 보간이 빈 값으로 치환되어 invalid image 에러 발생.

레포가 GitHub public 일 때 `.env` 가 .gitignore 되는 것이 당연하므로, 이 비밀은 운영자가 어디선가 별도로 주입해야 한다. 현재 Preview 시스템에는 그 채널이 전혀 없다(Phase 4 의 `agents.build_commands` 는 Phase 6 에서 제거됨 — `migrations/0005_phase6_drop_build_config.up.sql`).

### 1-2. 목표

Hub Admin UI 에서 **저장소(`repo_full_name`) 단위로** key=value 환경변수 묶음을 관리하고, 그 묶음이 다음 경로로 흘러가도록 한다.

1. **Admin UI** — `GET/POST /admin/repos/{owner}/{repo}/secrets` 페이지에서 textarea(KEY=VALUE 한 줄씩)로 편집·저장.
2. **Hub DB** — `repo_secrets` 테이블에 (repo_full_name, key, value) tuple 로 저장. 평문(plaintext). 암호화는 본 Phase 비범위(§2-2 / 결정 7).
3. **JOB_ASSIGN** — Hub 가 dispatch 직전에 해당 repo 의 secrets 를 조회해 `JobAssignData.BuildEnv map[string]string` 에 동봉.
4. **Agent** — JOB_ASSIGN 수신 후 worktree checkout 직후·`.preview.yml` 로드 직전에 `.env` 파일을 worktree 루트에 작성. 형식은 `KEY=VALUE\n` 줄 반복(docker compose 가 읽는 표준 dotenv 형식). 파일 권한 0600.

이 흐름의 종료 시 다음이 가능해야 한다.

- 운영자가 `/admin/repos/foo/bar/secrets` 에서 `DATABASE_URL=postgres://...`, `API_KEY=xyz` 두 줄 입력 → 저장.
- foo/bar 의 다음 PR webhook → JOB_ASSIGN 에 BuildEnv 동봉 → Agent worktree 에 `.env` 파일 생성 → `docker compose up -d` 가 두 변수를 읽어 컨테이너에 주입 → 빌드 성공.

### 1-3. 비목표 (이 Phase 가 해결하지 않는 것)

- **암호화 저장 / KMS 통합** — DB 컬럼은 plaintext TEXT. 암호화는 Phase 11+ 후보(결정 7).
- **Agent 별 / preview 별 secret override** — 본 Phase 는 repo 단위만. label 매칭이나 PR 별 override 는 비범위.
- **GitHub Secrets / Vault / Doppler 연동** — 외부 SoT 로부터의 sync 미지원.
- **Secret rotation API** — 운영자가 Admin UI 에서 수정·삭제만. 자동 만료·로테이션 없음.
- **기존 진행 중 빌드의 secret 재주입** — 저장 시점에 진행 중인 build 에는 영향 없음. 다음 JOB_ASSIGN 부터 적용. (Phase 4 결정 11 과 동일 정책.)
- **Webhook payload 자동 추출** — GitHub PR description 에 비밀을 적는 건 명백히 잘못된 패턴. 본 Phase 는 Admin UI 입력만.
- **Postgres 동시 마이그레이션 검증** — SQLite 만. PG 어댑터 작업 시 별도 Phase.
- **`.env` 외 형식(YAML/JSON) 지원** — dotenv 만.
- **Dockerfile 모드의 BuildArgs 주입** — 본 Phase 는 worktree `.env` 파일 작성에 한정. compose 모드는 자연스럽게 활용 가능, Dockerfile 모드는 사용자가 `Dockerfile` 안에서 명시 ARG/ENV 처리해야 함(§5-9).
- **Agent → Hub 의 secret echo / ACK** — 단방향.
- **Audit log** — slog 로만 남기고 별도 테이블 미생성(결정 8).

### 1-4. 성공 기준 (요약)

- `repo_secrets` 테이블 존재(F-1).
- `RepoSecretStore` 인터페이스 + sqlite 구현체로 List/Upsert/Delete 가능(F-3~F-7).
- `/admin/repos` 인덱스, `/admin/repos/{repoFullName}/secrets` 폼이 200 SSR 로 렌더(F-9~F-12).
- 폼 저장 → 303 → 페이지 reload 시 입력 값 보존(F-13~F-16).
- `JobAssignData.BuildEnv` 필드 존재, dispatcher 가 dispatch 직전 hydration(F-19, F-20).
- Agent 가 secrets 동봉된 JOB_ASSIGN 수신 시 worktree 루트에 `.env` 파일 작성(F-22).
- compose 모드 e2e: secret 두 개로 `docker-compose.yml` 의 `${VAR}` 가 정상 보간(F-26).

---

## 2. In / Out of Scope

### 2-1. In Scope

**DB / Schema**
- 마이그레이션 0007 — **두 디렉토리 양쪽에 byte-identical 추가** (결정 0 참조):
  - `db/migrations/0007_repo_secrets.up.sql` / `.down.sql` (신규, sqlc/CLI 의 SoT)
  - `internal/db/sqlite/migrations/0007_repo_secrets.up.sql` / `.down.sql` (신규, `migrations_embed.go` 가 embed 하는 사본)
- `db/queries/repo_secrets.sql` (신규)
- sqlc 재생성 → `internal/db/sqlite/repo_secrets.sql.go` (자동 생성)

**Store 인터페이스 / 구현**
- `internal/store/store.go` — `RepoSecret` 도메인 타입 + `RepoSecretStore` 인터페이스 + 정의 추가
- `internal/db/sqlite/repo_secret_store.go` (신규) — sqlite 구현체

**프로토콜**
- `internal/protocol/messages.go` — `JobAssignData.BuildEnv map[string]string` 필드 추가 (omitempty)

**Hub**
- `internal/hub/dispatcher.go` — `JobAssignFromPreview` 시그니처 변경 (env 인자 1개 추가) 또는 Dispatcher 가 `RepoSecretStore` 의존 추가 후 Claim 직후 hydration. 결정 4 참조.
- `internal/hub/admin_ui.go` — 라우트 등록 + 핸들러 3개 (repos 인덱스, secrets GET, secrets POST)
- `internal/hub/views/repos.gohtml` (신규) — repo 목록(distinct repo_full_name from previews + repo_secrets union)
- `internal/hub/views/repo_secrets.gohtml` (신규) — KEY=VALUE textarea 폼

**Agent**
- `internal/agent/runner.go` — `Handle()` 의 (2) Checkout 직후·(3) `.preview.yml` 로드 직전 사이에 `writeDotenv(worktree, msg.BuildEnv)` 호출. compose/Dockerfile 분기 모두 영향(but Dockerfile 모드의 효과는 §5-9 caveat)
- `internal/agent/dotenv.go` (신규) — `WriteDotenv(path string, env map[string]string) error` 헬퍼 + 단위 테스트

**테스트 / fake 호환성**
- `internal/db/sqlite/repo_secret_store_test.go` (신규)
- `internal/hub/admin_ui_test.go` — 페이지 렌더 + 저장 플로우 단위
- `internal/hub/dispatcher_test.go` — fakeRepoSecretStore 추가 + JobAssignData.BuildEnv 검증
- `internal/hub/webhook_handler_test.go`, `internal/hub/reconciler_test.go` 등 Dispatcher 를 조립하는 테스트의 wiring 보정(컴파일 통과)
- `internal/agent/runner_test.go` — fake worktree 에 `.env` 파일이 작성되는지, msg.BuildEnv == nil 일 때 파일 생성 안 함 확인
- `internal/agent/dotenv_test.go` — escape / 줄바꿈 / 파일 권한 케이스
- Playwright e2e: 폼 입력·저장·reload 보존(F-30), JOB_ASSIGN 송신 시 BuildEnv 캡처(F-31)

### 2-2. Out of Scope

- 컬럼 / 페이로드 암호화 (KMS, age, libsodium 등) — 결정 7 / R-2
- Audit log, secret 변경 history — 결정 8
- 사용자 정의 `.env` 경로 (예: `apps/web/.env`) — 결정 6
- Agent 가 `.env` 외에 `--env-file` 인자 등 다른 주입 채널을 직접 호출하는 변형
- secret reference 문법 (다른 secret 참조)
- secret import/export
- `repos` 와 분리된 별도 도메인 모델(`Repo` 엔티티) — 본 Phase 는 `repo_full_name` 문자열만 키
- pr_number 기반 override
- Multi-tenant ACL (운영자 모두가 모든 repo secret 보고 수정 가능)

---

## 3. 설계 결정 (Design Decisions)

### 결정 0 — 마이그레이션 SoT 와 embed 디렉토리 동시 갱신

- **결정**: 본 Phase 의 마이그레이션 0007 은 **두 디렉토리 모두에 byte-identical 한 사본**으로 추가한다.
  - `db/migrations/0007_repo_secrets.up.sql` / `.down.sql` — sqlc 와 외부 `migrate` CLI 가 보는 SoT.
  - `internal/db/sqlite/migrations/0007_repo_secrets.up.sql` / `.down.sql` — `internal/db/sqlite/migrations_embed.go:16` 의 `//go:embed migrations/*.sql` 가 임베드하는 사본. 런타임 (`hub`/`agent` 바이너리) 은 이쪽만 본다.
- **근거**: 현재 두 디렉토리에 0001~0006 이 모두 존재하며 각각 byte-identical 임이 phase-2-step1-eval.md §"Embedded migration vs file migration parity" 에서 확인됨. 한쪽만 업데이트하면 (a) 임베드만 업데이트 → 외부 `migrate` CLI 와 schema diff, (b) 외부만 업데이트 → 바이너리 자가 마이그레이션이 0007 을 모름. 둘 다 운영 사고.
- **버려진 대안**:
  - 한쪽 디렉토리로 통일 — 장기적으로 옳지만 본 Phase 범위를 넘는 인프라 변경. 본 Phase 는 기존 컨벤션을 유지.
  - `migrations_embed.go` 가 `db/migrations` 를 embed 하도록 변경 — 모듈 경계(`internal/db/sqlite` 가 위 디렉토리를 본다) 가 깨짐. 별도 Phase 로 분리.
- **되돌림 비용**: 파일 4개 (양쪽 up/down) 동시 삭제. CI 에 byte-identical 검증 grep 1줄을 NF-Portability-4 로 추가해 회귀 방지(§7).

### 결정 1 — secret 의 자연키는 `(repo_full_name, key)`

- **결정**: 새 테이블 `repo_secrets (repo_full_name TEXT, key TEXT, value TEXT, updated_at TEXT, PRIMARY KEY (repo_full_name, key))`.
- **근거**: repo 단위 묶음이 사용 단위(JOB_ASSIGN 1건 = repo 1개의 secrets 전체). 묶음 자체에 ID 가 필요한 작업(개별 reference, audit) 은 비범위.
- **버려진 대안**:
  - 단일 row 에 묶음을 JSON 으로 저장 (`(repo_full_name TEXT PK, env_json TEXT)`) — 한 key 만 수정 시 race(read-modify-write) 가 필요. 현재 단일 hub 프로세스 + SQLite 단일 writer 환경에서는 무해하지만, 키 단위 INSERT/DELETE 가 더 범용적. 또 textarea 전체 submit 이라는 UI 패턴과는 row-set diff 가 자연스럽지 않다 — 두 패턴 모두 가능하므로 단순한 row-per-key 채택.
  - `(repo_full_name, key)` PK 대신 별도 `id TEXT PK` + `UNIQUE (repo_full_name, key)` — 추가 ID 가 어디서도 쓰이지 않음. PK 직접 사용이 단순.
- **되돌림 비용**: 마이그레이션 0007 down 으로 테이블 DROP. row 데이터 손실은 plaintext 라 운영자가 다시 입력 가능(외부 SoT 가 없기 때문에 본질적으로 사용자 책임).

### 결정 2 — UI 입력 단위는 textarea 전체 submit (key 단위 PATCH 아님)

- **결정**: `POST /admin/repos/{repoFullName}/secrets` 가 textarea raw text 한 덩어리를 받아, 서버에서 KEY=VALUE 라인 파싱 → 기존 row 와 diff → INSERT/UPDATE/DELETE 트랜잭션으로 일괄 적용.
- **근거**: 운영자 멘탈 모델은 "한 화면, 한 저장". key 단위 add/remove 인라인 UI 는 SSR 한정에서는 JS 없이 구현이 까다롭고, 실수로 한 키만 빠뜨리는 사고가 잦다.
- **버려진 대안**:
  - 키 단위 add/delete 버튼 행 — JS 없이는 행 마다 form 분리 필요 → 페이지가 복잡해지고 partial save 로 inconsistent state 가능.
  - `htmx`/Alpine.js 도입 — 이식성·외부의존 최소화 원칙 위반.
- **되돌림 비용**: 추후 PATCH 타입 API 추가 시 핸들러 추가만 하면 됨(테이블·인터페이스는 호환).

### 결정 3 — KEY 의 정합 검사는 dotenv 호환 정규식만

- **결정**: 허용되는 KEY 정규식 `^[A-Za-z_][A-Za-z0-9_]*$`. 위반 라인은 422 가 아니라 **에러 메시지와 함께 같은 페이지 200 재렌더**(textarea 입력 보존). VALUE 측 정규식 검증은 없음(빈 문자열 OK, 임의 UTF-8 OK, 단 라인 끝의 `\n` `\r` 제거 후 저장). VALUE 의 trim/escape/split 정책은 결정 3a/3b/3c 참조.
- **근거**: docker compose 가 읽을 dotenv 의 KEY 규칙과 동일. 관대한 VALUE 는 base64·JSON·URL 등 운영자가 흔히 넣는 값을 모두 허용하기 위함.
- **버려진 대안**:
  - 422 + JSON 응답 — SSR 동선과 어긋남.
  - VALUE 도 정규식 검증 — false positive 위험(예: 공백 포함된 base64 패딩 `xxx==`).
- **되돌림 비용**: 정규식 완화/강화는 검증 함수 1곳 수정.

### 결정 3a — VALUE / KEY 의 trim 및 빈/등호없는 줄 처리 정책

- **결정**: textarea 라인 단위 파서는 다음 순서로 동작한다.
  1. 라인 끝의 `\r` `\n` 제거 (CRLF 호환).
  2. **라인 전체** 의 선·후행 공백/탭 제거 (`strings.TrimSpace`). trim 후 빈 라인은 무시.
  3. trim 후 첫 글자가 `#` → 코멘트로 취급, 무시.
  4. `=` 가 한 번도 등장하지 않는 라인 → **검증 실패** (ValidationError: `line N: missing '=' separator`). 220 재렌더, DB 미변경.
  5. `=` 가 첫 글자에 오는 라인 (예: `=foo`) → KEY 가 빈 문자열이므로 **검증 실패** (KEY 정규식 위반과 동일 경로).
  6. **첫 `=` 위치로 split** → KEY 와 VALUE 분리 (결정 3c).
  7. **KEY 만** 추가로 trim (양쪽 공백/탭 제거). 정규식 검증 적용.
  8. **VALUE 는 trim 하지 않는다**. 즉 `KEY= value ` 의 VALUE 는 ` value ` (공백 보존). 운영자가 실수로 공백을 포함시키면 그대로 저장됨 — 문서/페이지 안내 문구로 경고(F-12d).
  9. `KEY=` (등호 뒤 공백·내용 없음, 즉 trim 안 한 VALUE 가 빈 문자열) → 합법. value="" 로 저장. F-18 의 "KEY=" 케이스가 이 경로.
- **근거**: 운영자가 KEY 옆에 실수로 공백을 넣는 빈도는 매우 높음(YAML 키-값 습관) → KEY trim 은 안전. 반면 VALUE 의 공백은 base64 padding (`==`), URL fragment, JSON literal `{ "k": "v" }` 등 의도적 공백 케이스가 흔하므로 trim 하지 않는다. 빈 VALUE 는 dotenv 규약상 허용이며 docker compose 는 빈 문자열을 그대로 주입(`KEY=` 와 `KEY` 미정의는 다름).
- **버려진 대안**:
  - VALUE 양쪽도 trim — base64/JSON 손상 위험 (운영자가 의도 vs 실수를 구분할 방법이 서버 단에 없음).
  - `KEY` (등호 없음) 를 빈 문자열 VALUE 로 처리 — 운영자가 `KEY=value` 의 `=` 를 빠뜨린 오타를 silent 통과시킴. 명시 에러가 안전.
  - inline 공백 (`KEY = value`) 의 `=` 양쪽 trim — 결정 3c 의 "첫 `=` split" 규약과 충돌 가능. 규약을 단순하게 유지.
- **되돌림 비용**: 파서 함수 1곳 수정.

### 결정 3b — VALUE 의 escape 정책 (`#`, `\n` literal)

- **결정**:
  - `#` 은 VALUE 안에서 **코멘트로 해석하지 않는다**. 즉 `KEY=value#tag` 는 VALUE 가 `value#tag` 7글자. 라인 시작의 `#` 만 코멘트(결정 3a 단계 3).
  - VALUE 안의 `\n` literal (운영자가 textarea 에 직접 친 두 글자 `\` + `n`) 은 그대로 저장한다 (변환 없음). 즉 운영자가 `KEY=foo\nbar` 입력 → DB 저장 value `foo\nbar` 5글자 → `.env` 파일에 `KEY=foo\nbar\n` 로 직렬화. docker compose 는 이를 literal `\n` 으로 컨테이너에 전달 (개행 변환 안 함).
  - VALUE 안에 실제 개행 문자 (`\n`, 0x0A) 는 textarea 에서 입력 불가능 (textarea 의 enter 는 라인 분리로 해석되어 그 라인이 KEY 로 새로 시작). 예외적으로 `JSON.stringify` 등 외부에서 paste 된 \n 이 VALUE 안에 들어오면 결정 10 의 escape 가 작동하여 literal `\n` 두 글자로 직렬화.
- **근거**:
  - dotenv "사양" 은 파서마다 다르고 (`docker compose`, `joho/godotenv`, `python-dotenv` 등) `#` inline-comment 처리가 비호환. **본 시스템은 가장 보수적 동작 — `#` 을 보존** — 으로 통일하여 VALUE 안에 hash 가 들어가는 access token, color hex, URL fragment, markdown header 등을 안전하게 다룬다.
  - `\n` literal 보존은 운영자가 명시 입력한 두 글자를 변형하지 않기 위함. 원치 않는 동작은 결정 10 의 escape 가 받아준다.
- **버려진 대안**:
  - inline `#` 부터 줄 끝까지 코멘트로 잘라냄 (joho/godotenv default) — base64 의 `#` 또는 hex color `#abcdef` 손상.
  - `\n` literal 을 실제 개행으로 unescape — multi-line value 를 만들면 dotenv 파서가 깨짐 (결정 10 의 한계와 직결).
- **되돌림 비용**: 파서 함수 1곳 + `WriteDotenv` escape 함수 1곳.

### 결정 3c — `=` split 규칙 (첫 `=` 만 분리, VALUE 안의 `=` 보존)

- **결정**: 라인 안에서 **가장 왼쪽의 `=` 1개만 KEY/VALUE 구분자**로 사용. 이후 등장하는 모든 `=` 는 VALUE 의 일부.
  - 예: `KEY=a=b=c` → KEY=`KEY`, VALUE=`a=b=c`.
  - 예: `JWT=eyJhbGc...==` (base64 padding) → KEY=`JWT`, VALUE=`eyJhbGc...==`.
  - 예: `URL=https://x.com/?q=1&r=2` → KEY=`URL`, VALUE=`https://x.com/?q=1&r=2`.
- **근거**: dotenv 의 보편 규약. base64 padding (`==`), JWT, query string 등 VALUE 안에 `=` 가 들어가는 케이스가 매우 흔함. `strings.SplitN(line, "=", 2)` 1줄로 구현.
- **버려진 대안**:
  - 모든 `=` 에서 split → VALUE 가 `=` 를 못 가짐. base64/JWT 사용 불가 → 본 Phase 의 핵심 케이스(`API_KEY`, `DATABASE_URL`) 를 깬다.
  - VALUE 안의 `=` 에러 처리 → 운영자에게 인위적 제약, 문서화·예외 처리 비용.
- **되돌림 비용**: split 호출 1곳.

### 결정 4 — JOB_ASSIGN hydration 은 **Dispatcher 측에서**, store 의존을 직접 받음

- **결정**: `Dispatcher` 구조체에 `RepoSecrets store.RepoSecretStore` 필드 추가. `OnReady` 에서 `Claim` 성공 직후 `RepoSecrets.List(ctx, claimed.RepoFullName)` 호출 → map[string]string 빌드 → `JobAssignFromPreview(*claimed, env)` 로 전달. List 에러는 빌드 차단이 아니라 **WARN 로그 + 빈 env 전달**(R-1).
- **근거**: secret 조회는 dispatch path 의 일부이지 JobAssignData DTO 빌더의 책임이 아님. `JobAssignFromPreview` 를 store 의존으로 만들면 wire-only 변환 함수의 단순성을 잃는다.
- **버려진 대안**:
  - `JobAssignFromPreview(p store.Preview, secretStore store.RepoSecretStore)` — DTO 변환에 IO 부수효과(query) 가 들어가 테스트가 복잡해짐.
  - WS 전송 측(`WSJobSender.SendJobAssign`) 에서 hydrate — Sender 는 wire-write 책임만 가지는 게 단순. layered dependency 는 dispatcher → store 만 단방향 유지.
- **되돌림 비용**: hydrate 위치는 한 함수만 옮기면 됨.

### 결정 5 — Agent 의 `.env` 작성 시점은 Checkout 직후, **로드 전**

- **결정**: `runner.Handle()` 의 (2) Checkout 직후·(3) `LoadPreviewConfig` 직전에 `dotenv.WriteDotenv(filepath.Join(worktree, ".env"), msg.BuildEnv)` 호출. msg.BuildEnv 가 nil 또는 len 0 이면 파일 작성 자체를 skip(.env 가 worktree 에 git-tracked 되어 있는 경우 그것을 보존).
- **근거**:
  - `.preview.yml` 자체가 `${VAR}` 보간을 쓸 가능성은 낮지만, compose v2 의 `--env-file` 자동 탐지가 compose up 시점에 일어나므로, 그 전에 파일이 존재해야 함.
  - msg.BuildEnv 가 비었을 때 worktree 의 기존 `.env` 를 덮지 않는 정책은 "Hub 에 secret 입력이 없는 새 repo 도 기존처럼 동작" 을 보장.
- **버려진 대안**:
  - msg.BuildEnv 가 nil 이어도 빈 `.env` 작성 → 기존 .env 가 git tracked 인 repo 를 깨뜨림.
  - Compose 모드만 작성, Dockerfile 모드 skip — 분기 로직 추가 + Dockerfile 모드도 사용자가 `RUN cp .env /app/` 같은 패턴을 쓸 수 있어 의도적 일관 적용.
- **되돌림 비용**: 호출 1줄 제거.

### 결정 6 — `.env` 의 위치는 **worktree 루트 고정**, 사용자 정의 경로 미지원

- **결정**: 항상 `<worktree>/.env`. `apps/web/.env` 같은 모노레포 내부 경로는 본 Phase 비범위.
- **근거**: docker compose 의 default `--env-file` 위치가 compose 파일 디렉토리이며, 본 시스템은 `cfg.ComposeFile` 이 worktree 루트 기준 경로(`docker-compose.yml` 또는 `cfg.ComposeFile`)이므로 일치한다. 사용자 정의 경로는 `cfg.ComposeFile` 디렉토리 추적 + path 정규화 + escape(`..`) 차단 등 추가 작업이 필요.
- **버려진 대안**:
  - `.preview.yml` 에 `env_file_target: apps/web/.env` 필드 추가 — `.preview.yml` 변경은 별도 Phase 로 분리.
  - 모든 서비스 디렉토리에 `.env` 복사 — over-aggressive, 운영자 의도 추측.
- **되돌림 비용**: `.preview.yml` 스키마 확장 + 경로 정규화로 후속 Phase 처리.

### 결정 7 — 저장은 plaintext, 암호화는 비범위(명시 표기)

- **결정**: `repo_secrets.value` 는 TEXT 컬럼에 평문 저장. 페이로드도 평문 JSON. 단 본 결정과 그 리스크를 코드 주석·`/admin/repos/{...}/secrets` 페이지 상단 주의 문구·README 모두에 명시. 마이그레이션 파일 헤더에도 `-- WARNING: stored as plaintext (Phase 10 limitation, see docs/specs/phase-10).` 추가.
- **근거**:
  - 현재 시스템 어디에도 암호화 인프라(KMS, key derivation) 가 없다. plaintext 시작 → 암호화는 후속 Phase 에서 in-place 마이그레이션이 가능(컬럼은 같고 read/write path 만 transparent decrypt 추가).
  - 셀프호스팅 사용자가 자기 머신을 신뢰한다는 Preview 의 신뢰 모델(Phase 4 결정 6 과 동일).
- **버려진 대안**:
  - `crypto/aes` GCM + 환경변수 마스터키 — 키 회전·DR·키 분실 정책이 모두 새 의존. 본 Phase 범위 폭증.
- **되돌림 비용**: 후속 암호화 Phase 에서 컬럼 추가 없이 transparent encryption 으로 이행 가능. 현재는 `/admin` 접근 제어와 OS 파일 권한이 유일한 방어선임을 명시(NF-Security-1).

### 결정 8 — Audit log 별도 테이블 미생성

- **결정**: 변경은 `slog` 로만 기록(`info admin_ui_repo_secret_save repo=foo/bar n_keys=3 added=2 updated=0 removed=1`). 별도 audit 테이블 없음.
- **근거**: 변경 빈도가 낮고, history 가 필요하면 슬라이스를 누가 언제 본 지의 audit 까지 함께 가야 의미 있는데, 그건 본 Phase 비범위.
- **버려진 대안**:
  - `repo_secret_events` 테이블 + 모든 mutate 에 INSERT — diff 계산 + dual-write 트랜잭션 부담.
- **되돌림 비용**: 후속 Phase 에서 trigger 또는 store 래퍼로 추가 가능.

### 결정 9 — Dispatcher hydration 실패 정책: WARN + 빈 env 로 진행

- **결정**: `RepoSecrets.List` 가 ErrNotFound 가 아닌 다른 에러를 반환하면 `slog.Warn("dispatcher_secret_hydrate_failed", ...)` + 빈 map 으로 dispatch 계속.
- **근거**: secret hydrate 실패가 dispatch 자체를 중단시키면, secret 이 필요 없는 repo 까지 영향. 빌드는 시도하고 정말 secret 누락이면 docker compose 가 자체 에러로 fail → `STATUS_UPDATE failed` 경로 → 운영자가 Admin UI 로 확인 가능.
- **버려진 대안**:
  - 실패 시 dispatch 중단 + queued 유지 → 다음 READY 사이클에 재시도 — secret 이 영구히 비어있으면 무한 retry. retry 횟수 카운터 등 부수 시스템 필요.
- **되돌림 비용**: 정책 변경은 한 함수.

### 결정 10 — `WriteDotenv` 의 escape 정책: 줄바꿈 1개로 줄 분리, VALUE 안의 줄바꿈은 `\n` literal 로 변환

- **결정**: `dotenv.WriteDotenv` 는 각 (k,v) 를 `k=v\n` 으로 직렬화한다. **value 안에 `\n` 이 있으면 literal `\\n` 으로 escape**(즉 파일에는 `\` 다음 `n` 두 글자). docker compose v2 의 `--env-file` 는 multi-line value 를 지원하지 않으므로(quoted multiline 도 plain dotenv 에서는 안전하지 않음), 한 줄 → 한 변수의 단순 규약으로 통일. 이로 인한 손상 가능성은 NF-Security-DotenvLoss-1 로 명시.
- **근거**:
  - dotenv 사양 자체가 단순. 복잡한 quoting 규칙은 compose 와 다른 파서(docker run --env-file, 다른 dotenv 라이브러리) 사이 비호환의 주범.
  - 운영자가 multi-line(예: PEM private key) 를 넣으려면 base64 인코딩을 권장(페이지 안내 문구 §5-3). 본 Phase 가 그 문제를 해결하지 않음을 명시.
- **버려진 대안**:
  - quoted form `KEY="...\n..."` — compose 가 일부 케이스에서 안 풀어줌.
- **되돌림 비용**: escape 함수 1곳.

### 결정 11 — `JobAssignData.BuildEnv` 는 omitempty

- **결정**: 와이어 페이로드에서 `BuildEnv` 가 빈 map 이면 JSON 키 자체 생략. 레거시 Agent(Phase 6/7/8/9) 는 모르는 키를 무시하므로 호환.
- **근거**: 메시지 크기 절약 + 명시적 "secret 없음" 시나리오에서 Hub 측 의도가 그대로 와이어에 보임.
- **버려진 대안**:
  - 항상 `{}` 송신 — 가시성 손해는 없지만 wire size 가 약간 늘고 호환성 검증 케이스 1개 늘어남.
- **되돌림 비용**: `omitempty` 토글.

### 결정 12 — 표준 SQL: `INSERT ... ON CONFLICT(repo_full_name, key) DO UPDATE` (UPSERT)

- **결정**: Upsert 1개 쿼리(`-- name: UpsertRepoSecret :exec`)로 set 단위 입력. 기존에 있던 (k) 가 새 입력에 없으면 별도 `DeleteRepoSecret` 으로 삭제. diff 는 핸들러 레벨에서 계산.
- **근거**: SQLite 3.35+ / Postgres 양쪽이 `ON CONFLICT(...) DO UPDATE` 를 지원. `INSERT OR REPLACE` 같은 SQLite 전용 문법 회피(이식성 원칙).
- **버려진 대안**:
  - DELETE-ALL + INSERT-ALL — 단순하지만 변경 없는 row 의 `updated_at` 까지 갱신되어 audit log slog 에서 false positive.
- **되돌림 비용**: 쿼리 1개 교체.

### 결정 13 — `repo_full_name` 의 URL 인코딩

- **결정**: Admin UI 라우트에서 `repo_full_name` 은 URL path segment 두 개로 분리. 즉 `/admin/repos/{owner}/{repo}/secrets`. 핸들러는 `path.Join(owner, repo)` 로 재조합. 이로써 `owner/repo` 안의 슬래시 인코딩 문제를 회피.
- **근거**: GitHub repo full name 은 항상 정확히 `owner/repo` (슬래시 1개). path segment 두 개 매핑이 자연스러움.
- **버려진 대안**:
  - `/admin/repos/{repoFullName}` 단일 segment + URL encode — `%2F` escape 가 net/http mux 에서 정규화되어 매칭 실패 가능.
- **되돌림 비용**: 라우트 패턴 1줄.

### 결정 14 — `/admin/repos` 인덱스의 데이터 소스: previews + repo_secrets 의 union

- **결정**: 인덱스 페이지는 `previews.repo_full_name` distinct + `repo_secrets.repo_full_name` distinct 의 합집합을 정렬해 표시. 각 행에 "secrets: N keys" 카운트 + 링크.
  - **previews 측 distinct 취득 방식**: `PreviewStore` 에 신규 메서드 `ListRepos(ctx) ([]string, error)` 를 추가하고 SQL `SELECT DISTINCT repo_full_name FROM previews ORDER BY repo_full_name` 으로 구현. Go 측 슬라이스 dedup 이 아니라 **DB 측 DISTINCT** 를 SoT 로 둔다(미래에 previews 행 수가 늘 때 메모리 폭주 방지).
  - **합집합 처리**: 핸들러가 `previews.ListRepos()` 와 `repoSecrets.ListRepos()` 두 슬라이스를 받아 Go 의 `map[string]struct{}` 로 dedup → 정렬 → repoRow 빌드. SQL UNION 은 도메인 경계(두 store 가 같은 DB 를 가정해야 함)를 깨므로 사용하지 않음.
- **근거**: webhook 이 한 번도 안 들어온 repo 라도 운영자가 사전에 secret 을 등록할 수 있어야 함(예: 새 repo 도입 직전 준비). 즉 secret 의 존재가 repo 등록의 첫 진입점일 수 있음. 두 store 가 동일 DB 인스턴스를 공유한다는 가정을 설계에 박아넣지 않기 위해 union 은 application layer.
- **버려진 대안**:
  - `PreviewStore.ListAll` 호출 후 Go 측 distinct — previews 행이 N 개이면 N 행을 메모리로 끌어옴. 본 Phase 는 새 메서드 1개를 추가해 DB 측에서 끝내는 비용을 선택.
  - SQL `UNION` 한 쿼리로 합치는 sqlc — 두 테이블이 같은 DB 인 가정 + sqlc 설정에 cross-table 쿼리 추가. store 인터페이스 분리 원칙 위반.
  - previews 만 — 신규 repo 사전 등록 불가능.
  - 별도 `repos` 도메인 테이블 도입 — 본 Phase 비범위(§2-2).
- **되돌림 비용**: 쿼리 1개 + Store 메서드 1개. previews-only 폴백은 핸들러 분기 1줄.

### 결정 15 — `repo_full_name` 의 case 정규화: **저장·조회·라우팅 시점에 lowercase 강제**

- **결정**: `repo_full_name` 은 모든 진입점에서 **저장 직전 `strings.ToLower` 정규화** 를 적용하고, DB 에는 항상 lowercase 만 존재한다.
  - 적용 지점:
    1. webhook handler (`internal/hub/webhook_handler.go`) 가 GitHub payload 의 `full_name` 을 preview row 로 upsert 하는 자리 — **이미 적용되어 있는지 grep 확인 필요**, 미적용이면 본 Phase 가 명시적으로 수정 (R-8).
    2. Admin UI 의 `repoSecretsGet` / `repoSecretsPost` 핸들러가 path segment `{owner}/{repo}` 를 받아 `repo_full_name = strings.ToLower(owner+"/"+repo)` 로 정규화한 뒤 store 에 전달.
    3. Dispatcher hydration 의 `RepoSecrets.List(ctx, claimed.RepoFullName)` 호출 시 `claimed.RepoFullName` 은 이미 webhook 단에서 lowercase 로 들어왔으므로 추가 변환 불요. (단 방어적으로 `strings.ToLower` 한 번 더 적용 — 비용 무시 가능.)
  - 사용자 화면 표시: lowercase 그대로 표시. GitHub 의 화면상 case (예: `Foo/Bar`) 와 다를 수 있다는 안내 문구를 페이지 상단에 1줄 추가(F-12e).
- **근거**: GitHub 자체가 owner/repo 의 매칭에 case-insensitive (예: `git clone https://github.com/Foo/Bar` 와 `.../foo/bar` 둘 다 동작). 저장된 secret 과 webhook 페이로드의 case 가 어긋나 매칭 실패하면 운영자는 "왜 secret 이 안 들어가지?" 의 silent failure 에 빠진다. **저장 시 normalize** 는 한 번에 끝나며 비교 시 정규화보다 단순하고, 인덱스 조회가 빠르다 (`WHERE repo_full_name = ?` 가 B-tree exact match 로 작동).
- **버려진 대안**:
  - **그대로 저장 + 비교 시 case-insensitive (`COLLATE NOCASE` 또는 `LOWER(col) = LOWER(?)`)** — SQLite 의 `COLLATE NOCASE` 는 ASCII 만 지원. Postgres 는 `CITEXT` 또는 `LOWER()` 로 다른 메커니즘 — 이식성 원칙 위반. `LOWER(col) = LOWER(?)` 는 인덱스 미사용 (functional index 추가 필요 → 마이그레이션 복잡도 증가).
  - 정규화 안 함, 운영자에게 일관된 case 로 입력하도록 안내 — silent failure 의 운영 비용 큼.
- **되돌림 비용**: `strings.ToLower` 호출 3~5곳 제거 + 데이터 마이그레이션 (`UPDATE repo_secrets SET repo_full_name = LOWER(repo_full_name)` 등). 본 Phase 가 plaintext 기간이므로 운영자가 재입력해도 큰 부담 없음.

---

## 4. 아키텍처 / 구조

### 4-1. 디렉토리 트리 (이 Phase 후 추가/변경)

```
db/queries/
  repo_secrets.sql                                [신규]
internal/db/sqlite/
  migrations/
    0007_repo_secrets.up.sql                      [신규]
    0007_repo_secrets.down.sql                    [신규]
  repo_secrets.sql.go                             [신규, sqlc 자동생성]
  repo_secret_store.go                            [신규]
  repo_secret_store_test.go                       [신규]
internal/store/
  store.go                                        [수정] RepoSecret + RepoSecretStore 추가
internal/protocol/
  messages.go                                     [수정] JobAssignData.BuildEnv
internal/hub/
  dispatcher.go                                   [수정] RepoSecrets 의존 + hydration
  dispatcher_test.go                              [수정] fakeRepoSecretStore + hydration 케이스
  admin_ui.go                                     [수정] 라우트 등록 + 핸들러 3개
  admin_ui_test.go                                [수정] secrets 페이지 케이스
  views/
    repos.gohtml                                  [신규] /admin/repos 인덱스
    repo_secrets.gohtml                           [신규] /admin/repos/{owner}/{repo}/secrets
    layout.gohtml                                 [수정] 메뉴에 "Repos" 링크
internal/agent/
  dotenv.go                                       [신규] WriteDotenv 헬퍼
  dotenv_test.go                                  [신규]
  runner.go                                       [수정] Checkout 직후 .env 작성
  runner_test.go                                  [수정] BuildEnv 케이스
cmd/hub/main.go                                   [수정] AdminUIHandler / Dispatcher wiring 에 RepoSecretStore 주입
db/migrations/
  0007_repo_secrets.up.sql                        [신규, sqlc/CLI SoT]
  0007_repo_secrets.down.sql                      [신규]
db/queries/previews.sql                           [수정] ListPreviewRepos 쿼리 추가 (결정 14)
internal/db/sqlite/previews.sql.go                [수정, sqlc 자동생성]
tests/fixtures/compose-with-env/
  docker-compose.yml                              [신규, e2e fixture — F-26]
  Dockerfile                                      [신규, 또는 alpine image 직접 사용]
  README.md                                       [신규, fixture 사용 설명]
docs/specs/phase-10-repo-build-secrets.md         [본 문서]
```

### 4-2. 모듈 의존 관계 (단방향)

```
cmd/hub  ─┬─> internal/hub       ─> internal/store ─> internal/db/sqlite
         └─> internal/agent (X — agent 는 cmd/agent 만)

internal/hub/dispatcher.go ─> internal/store.RepoSecretStore
internal/hub/admin_ui.go   ─> internal/store.RepoSecretStore
internal/agent/runner.go   ─> internal/agent/dotenv.go
internal/agent             ─> internal/protocol (BuildEnv 읽기)
```

`internal/agent` 는 `internal/store` / `internal/db/sqlite` 를 import 하지 않는다(단방향 보장, NF-Layer-1).

### 4-3. 시퀀스: Webhook → Dispatch → Agent 빌드 (BuildEnv 흐름)

```
GitHub webhook
   │
   ▼
WebhookHandler.handleUpsert
   │ Upsert preview row (queued)
   ▼
Dispatcher.OnReady (next READY from agent)
   │ ListQueuedForCandidates / LabelsMatch / Claim
   │
   ├──> RepoSecrets.List(ctx, claimed.RepoFullName)
   │     ├ ok    → env := map[k]v
   │     └ err   → slog.Warn + env := nil  (Decision 9)
   │
   ▼
Sender.SendJobAssign(ctx, agentID, claimed, env)
   │
   ▼
WSJobSender.SendJobAssign  (변경: env 전달받아 envelope 빌드)
   │ NewEnvelope(JOB_ASSIGN, JobAssignFromPreview(p, env))
   ▼
[wire: TEXT WS frame]
   ▼
Agent.client.go readLoop  → Runner.Handle(msg)
   │
   ▼
Handle()
   ├ (1) STATUS_UPDATE building (sha=nil)
   ├ (2) cache.Ensure / Checkout
   ├ (2.5) ★ dotenv.WriteDotenv(worktree+/.env, msg.BuildEnv) ★   [신규]
   ├ (2b) STATUS_UPDATE building (sha resolved)
   ├ (3) LoadPreviewConfig
   ├ (4) detectBuildFiles
   ├ (5) handleCompose / handleDockerfile
   │       └ docker compose up -d  → reads ./.env automatically
   ├ (6) waitRouters
   └ (7) STATUS_UPDATE running
```

### 4-4. 상태 다이어그램: secret 페이지의 폼 처리

```
[GET /admin/repos/{owner}/{repo}/secrets]
  ├ list rows from store
  ├ render textarea with "KEY=VALUE\n..." (sorted by key)
  └ 200 OK

[POST /admin/repos/{owner}/{repo}/secrets]
  ├ parse form "raw"
  ├ split lines, trim, drop empty/comment(#)
  ├ for each line:
  │   ├ split first '=' → key, value
  │   ├ validate key regex
  │   └ on fail → re-render 200 with error + raw preserved
  ├ compute diff vs current rows
  │   ├ to_upsert = lines not in current OR value differs
  │   └ to_delete = current keys not in lines
  ├ tx: UpsertRepoSecret * N + DeleteRepoSecret * M
  └ 303 → /admin/repos/{owner}/{repo}/secrets?msg=saved
```

---

## 5. 인터페이스 계약

### 5-1. DB 스키마 (마이그레이션 0007)

| 테이블 | 컬럼 | 타입 | 제약 | 비고 |
|---|---|---|---|---|
| `repo_secrets` | `repo_full_name` | TEXT | NOT NULL | GitHub `owner/repo` |
| `repo_secrets` | `key` | TEXT | NOT NULL | dotenv KEY 정규식 만족 |
| `repo_secrets` | `value` | TEXT | NOT NULL DEFAULT '' | plaintext (결정 7) |
| `repo_secrets` | `updated_at` | TEXT | NOT NULL | RFC3339 UTC |
| `repo_secrets` | (PK) | — | `PRIMARY KEY (repo_full_name, key)` | 결정 1 |

`up.sql` (전문 예시):

```sql
-- WARNING: stored as plaintext (Phase 10 limitation, see docs/specs/phase-10-repo-build-secrets.md §3 결정 7).
CREATE TABLE repo_secrets (
    repo_full_name TEXT NOT NULL,
    key            TEXT NOT NULL,
    value          TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (repo_full_name, key)
);
CREATE INDEX idx_repo_secrets_repo ON repo_secrets(repo_full_name);
```

`down.sql`:

```sql
DROP INDEX IF EXISTS idx_repo_secrets_repo;
DROP TABLE IF EXISTS repo_secrets;
```

> SQLite 3.35+ + Postgres 양쪽 호환. **보조 인덱스 결정**: 복합 PK `(repo_full_name, key)` 의 자동 인덱스가 `WHERE repo_full_name = ?` 의 prefix 매칭에 사용 가능하므로 별도 인덱스가 **이론적으로는 불필요**하다. 그러나 본 Phase 는 다음 이유로 **명시 인덱스 `idx_repo_secrets_repo` 를 추가**한다.
>
> 1. SQLite query planner 가 복합 PK 의 prefix scan 을 사용하는 것이 보장되지만, EXPLAIN 결과를 명시적으로 단일 인덱스로 본 명세에 못 박는다(F-1b).
> 2. Postgres 어댑터로 이식할 때 query planner 가 다를 가능성 — 명시 인덱스가 안전.
> 3. `ListRepoSecretRepos` 의 `SELECT DISTINCT repo_full_name` 도 인덱스-only scan 으로 끝남.
>
> 비용은 INSERT 시 인덱스 1개 추가 유지(secret 변경 빈도 매우 낮음 → 무시 가능). 만약 향후 디스크 절약이 중요해지면 인덱스 제거 후 PK prefix 만 의존 가능 — 마이그레이션 1줄로 회수.

### 5-2. sqlc 쿼리 (`db/queries/repo_secrets.sql`)

```sql
-- name: ListRepoSecrets :many
SELECT repo_full_name, key, value, updated_at
FROM repo_secrets
WHERE repo_full_name = ?
ORDER BY key;

-- name: ListRepoSecretRepos :many
SELECT DISTINCT repo_full_name
FROM repo_secrets
ORDER BY repo_full_name;

-- name: UpsertRepoSecret :exec
INSERT INTO repo_secrets (repo_full_name, key, value, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(repo_full_name, key) DO UPDATE SET
    value      = excluded.value,
    updated_at = excluded.updated_at;

-- name: DeleteRepoSecret :exec
DELETE FROM repo_secrets WHERE repo_full_name = ? AND key = ?;

-- name: DeleteAllRepoSecretsFor :exec
DELETE FROM repo_secrets WHERE repo_full_name = ?;
```

### 5-3. Store 인터페이스 (`internal/store/store.go`)

```go
// RepoSecret 은 repo_secrets 한 행의 도메인 표현.
// Value 는 plaintext (Phase 10 결정 7). 본 구조체는 절대 마샬링되어 와이어로
// 흘러가지 않으며, JOB_ASSIGN 의 BuildEnv 는 map[string]string 으로 별도 빌드된다.
type RepoSecret struct {
    RepoFullName string
    Key          string
    Value        string
    UpdatedAt    time.Time
}

// RepoSecretStore 는 repo_secrets 테이블에 대한 이식성 있는 저장소 인터페이스.
// 단일 진입점:
//   - List: 한 repo 의 모든 secret 행 (key 정렬)
//   - Upsert: 1 행 INSERT 또는 value 갱신
//   - Delete: 1 행 삭제
//   - DeleteAllFor: repo 단위 일괄 삭제 (현재 Admin UI 에선 미사용, 추후 repo 삭제 동선용)
//   - ListRepos: distinct repo_full_name (인덱스 페이지용)
type RepoSecretStore interface {
    List(ctx context.Context, repoFullName string) ([]RepoSecret, error)
    Upsert(ctx context.Context, s RepoSecret) error
    Delete(ctx context.Context, repoFullName, key string) error
    DeleteAllFor(ctx context.Context, repoFullName string) error
    ListRepos(ctx context.Context) ([]string, error)
}
```

또한 `PreviewStore` 인터페이스에 다음 메서드 1개를 추가한다 (결정 14).

```go
type PreviewStore interface {
    // ... 기존 메서드 ...

    // ListRepos 는 previews 테이블의 distinct repo_full_name 을 정렬 반환한다.
    // /admin/repos 인덱스 페이지가 repo_secrets 와 union 하기 위한 진입점.
    ListRepos(ctx context.Context) ([]string, error)
}
```

대응 sqlc 쿼리 추가 (`db/queries/previews.sql`):

```sql
-- name: ListPreviewRepos :many
SELECT DISTINCT repo_full_name
FROM previews
ORDER BY repo_full_name;
```

### 5-4. 프로토콜 변경

`internal/protocol/messages.go`:

```go
type JobAssignData struct {
    PreviewID    string            `json:"preview_id"`
    RepoFullName string            `json:"repo_full_name"`
    RepoURL      string            `json:"repo_url"`
    CommitSHA    string            `json:"commit_sha"`
    Branch       string            `json:"branch,omitempty"`
    Labels       []string          `json:"labels,omitempty"`
    BuildEnv     map[string]string `json:"build_env,omitempty"` // Phase 10
}
```

JSON 예시 (secret 있음):

```json
{
  "type": "JOB_ASSIGN",
  "data": {
    "preview_id": "9f1e...",
    "repo_full_name": "foo/bar",
    "repo_url": "https://github.com/foo/bar.git",
    "commit_sha": "abc123",
    "branch": "feat/x",
    "labels": ["env=home"],
    "build_env": {
      "DATABASE_URL": "postgres://app:pw@db:5432/app",
      "API_KEY": "sk-..."
    }
  }
}
```

JSON 예시 (secret 없음 — omitempty):

```json
{
  "type": "JOB_ASSIGN",
  "data": {
    "preview_id": "9f1e...",
    "repo_full_name": "foo/bar",
    "repo_url": "https://github.com/foo/bar.git",
    "commit_sha": "abc123"
  }
}
```

ProtoVersion 은 `v1` 그대로(추가 필드는 호환). 레거시 Agent(Phase 9 이하) 는 unknown JSON key 를 무시하므로 안전.

### 5-5. 함수 시그니처

| 대상 | 시그니처 | 비고 |
|---|---|---|
| `internal/agent/dotenv.go` | `func WriteDotenv(path string, env map[string]string) error` | env 가 nil/len0 → no-op 반환 nil. 파일 권한 0600. |
| `internal/agent/dotenv.go` | `func escapeDotenvValue(v string) string` | 줄바꿈을 literal `\\n` 으로 escape (결정 10) |
| `internal/hub/dispatcher.go` | `func JobAssignFromPreview(p store.Preview, env map[string]string) protocol.JobAssignData` | 기존 시그니처 1인자 → 2인자. nil env → BuildEnv 미설정. **호출처 grep 결과 단일** (`internal/hub/ws_registry.go:113`). 본 Phase 가 그 한 줄을 수정 + dispatcher 가 그 호출 직전 hydrate 한 env 를 인자로 흘려보내거나, dispatcher 가 자체 호출로 대체. 결정 4 의 채택안은 후자(WSJobSender 의 시그니처는 그대로 유지하지 않고 env 전달을 받도록 변경 — 5-5 의 다음 행 참조). |
| `internal/hub/ws_registry.go` | `func (s *WSJobSender) SendJobAssign(ctx, agentID, p store.Preview, env map[string]string) error` | 시그니처에 env 인자 추가. 내부에서 `JobAssignFromPreview(p, env)` 호출. dispatcher 가 hydrate 한 env 를 wire 로 옮기는 통로. |
| `internal/hub/dispatcher.go` | `Dispatcher` 구조체에 `RepoSecrets store.RepoSecretStore` 필드 추가 | 결정 4 |
| `internal/hub/dispatcher.go` | `func NewDispatcher(as, ps, rs, sender, logger)` | 시그니처에 `rs store.RepoSecretStore` 추가. nil 허용 → hydrate skip. |
| `internal/hub/admin_ui.go` | `(h *AdminUIHandler) reposIndex(w, r)` | GET `/admin/repos` |
| `internal/hub/admin_ui.go` | `(h *AdminUIHandler) repoSecretsGet(w, r)` | GET `/admin/repos/{owner}/{repo}/secrets` |
| `internal/hub/admin_ui.go` | `(h *AdminUIHandler) repoSecretsPost(w, r)` | POST `/admin/repos/{owner}/{repo}/secrets` |
| `internal/hub/admin_ui.go` | `AdminUIHandler.RepoSecrets store.RepoSecretStore` 필드 추가 | wiring 에서 cmd/hub/main.go 가 주입 |

### 5-6. HTTP 엔드포인트

| 메서드 | 경로 | 핸들러 | 응답 | 비고 |
|---|---|---|---|---|
| GET | `/admin/repos` | `reposIndex` | 200 SSR | repo 인덱스(결정 14) |
| GET | `/admin/repos/{owner}/{repo}/secrets` | `repoSecretsGet` | 200 SSR / 404 | 폼 + textarea |
| POST | `/admin/repos/{owner}/{repo}/secrets` | `repoSecretsPost` | 303 → `?msg=saved` / 200 (validation 실패) | 결정 2 / 결정 3 |

> 405 (잘못된 method) 처리는 net/http mux 패턴 매칭으로 자동(POST 라우트만 등록되었으면 GET 은 405 가 아니라 404). 기존 admin_ui 의 `agentDelete` 와 동일 컨벤션.

### 5-7. Admin UI 페이지 레이아웃

`/admin/repos`:

```
+--------------------------------------------------------------+
|  Preview Admin                  [agents] [previews] [repos]  |
|  hgroup                                                       |
|   <h1>Repositories</h1>                                       |
|   <h2>Build secrets per repo</h2>                             |
+--------------------------------------------------------------+
|  Table                                                        |
|   ┌────────────────────┬────────────┬───────────────────────┐ |
|   │ Repo               │ Secrets    │ Action                │ |
|   ├────────────────────┼────────────┼───────────────────────┤ |
|   │ foo/bar            │ 3 keys     │ [Edit secrets]        │ |
|   │ org/web-app        │ 0 keys     │ [Edit secrets]        │ |
|   └────────────────────┴────────────┴───────────────────────┘ |
+--------------------------------------------------------------+
```

`/admin/repos/{owner}/{repo}/secrets`:

```
+--------------------------------------------------------------+
|  Preview Admin                                                |
|  hgroup                                                       |
|   <h1>Secrets: foo/bar</h1>                                   |
|   <h2>Stored as plaintext (see Phase 10 docs)</h2>            |
+--------------------------------------------------------------+
|  <form method=POST action=/admin/repos/foo/bar/secrets>       |
|   <label>KEY=VALUE per line. Lines starting with #            |
|     are comments and stripped.                                 |
|     KEY must match ^[A-Za-z_][A-Za-z0-9_]*$.                  |
|     Multi-line values are not supported (see docs).</label>   |
|   <textarea name=raw rows=12 placeholder="DB_URL=postgres..." |
|   >DB_URL=postgres://app:pw@db:5432/app                       |
|API_KEY=sk-xxxxxxxxxxxx                                        |
|FEATURE_FLAG=on</textarea>                                     |
|   <button type=submit>Save</button>                           |
|  </form>                                                      |
|                                                               |
|  {{if .SavedFlash}}<article role=alert>Saved.                 |
|   {{.AddedN}} added, {{.UpdatedN}} updated,                   |
|   {{.RemovedN}} removed.</article>{{end}}                     |
|  {{if .ValidationError}}<article role=alert>                  |
|   {{.ValidationError}}</article>{{end}}                       |
+--------------------------------------------------------------+
|  Notes                                                        |
|   - Empty value (KEY=) is allowed and saved as empty string.  |
|   - Removing all lines and saving deletes all secrets.        |
|   - Changes apply to the **next** JOB_ASSIGN.                 |
+--------------------------------------------------------------+
```

### 5-8. ViewModel

```go
type reposIndexView struct {
    Title string
    Rows  []repoRow
    Error string
}
type repoRow struct {
    RepoFullName  string
    SecretCount   int
    EditURLPath   string // "/admin/repos/foo/bar/secrets"
}

type repoSecretsView struct {
    Title           string
    RepoFullName    string
    OwnerSegment    string
    RepoSegment     string
    RawText         string // textarea 표시 내용 (저장된 값에서 재구성)
    SavedFlash      bool
    AddedN          int
    UpdatedN        int
    RemovedN        int
    ValidationError string
    Error           string
}
```

### 5-9. Dockerfile 모드 caveat

본 Phase 의 `.env` 파일 작성은 **worktree 루트** 에 단일 파일을 만든다. 그 효과는 빌드 모드에 따라 다음과 같다.

- **compose 모드**: `docker compose up` 이 동일 디렉토리의 `.env` 를 자동 인식 → `${VAR}` 보간 + `env_file: .env` 의 양쪽이 동작. **이번 Phase 의 주 사용 케이스**.
- **Dockerfile 모드**: `docker build` 가 `.env` 를 자동으로 ARG/ENV 로 주입하지 **않는다**. 사용자가 `Dockerfile` 안에서 `COPY .env /app/.env` 후 런타임에 dotenv 라이브러리로 읽거나, build args 로 명시 전달해야 한다. 본 Phase 는 그 안내 문구를 `repo_secrets.gohtml` 에 적시(F-12c)하며, Dockerfile 모드에서 자동 ENV 주입은 비범위(§2-2). Hub 의 wiring/Dispatcher 동작은 빌드 모드를 알지 못하며 양쪽에 동일하게 BuildEnv 를 보낸다(simpler is better).

---

## 6. 기능 요구사항 체크리스트 (F-*)

### 6-1. DB / Store

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-1 | 마이그레이션 0007 up 실행 후 `repo_secrets` 테이블이 존재한다(컬럼 4개 + PK 복합) | `PRAGMA table_info(repo_secrets)` |
| F-1b | `idx_repo_secrets_repo` 인덱스 존재 + `EXPLAIN QUERY PLAN SELECT ... WHERE repo_full_name = ?` 가 인덱스 사용 표시 | `PRAGMA index_list(repo_secrets)` + `EXPLAIN QUERY PLAN` |
| F-1c | `db/migrations/0007_*.sql` 와 `internal/db/sqlite/migrations/0007_*.sql` 가 byte-identical (결정 0) | `diff db/migrations/0007_repo_secrets.up.sql internal/db/sqlite/migrations/0007_repo_secrets.up.sql` 빈 출력 + down 동일 |
| F-2 | 마이그레이션 0007 down 실행 후 테이블이 사라진다 | up→down→`PRAGMA table_info` 빈 결과 |
| F-3 | `RepoSecretStore.Upsert` 가 신규 row 를 INSERT 한다 | sqlite 단위 테스트: Upsert 후 List 결과 1행 |
| F-4 | 같은 (repo, key) 로 Upsert 두 번 → 1행 + value 갱신 + updated_at 갱신 | 단위 테스트 (시간 차) |
| F-5 | `Delete(repo, key)` 가 단일 row 삭제 | 단위 |
| F-6 | `List(repo)` 가 key ASC 정렬 + 다른 repo 의 row 미포함 | 단위 |
| F-7 | `RepoSecretStore.ListRepos()` 가 distinct repo_full_name 정렬 반환 | 단위 |
| F-7b | `PreviewStore.ListRepos()` 가 distinct repo_full_name 정렬 반환 (결정 14) | 단위 |
| F-8 | `DeleteAllFor(repo)` 가 같은 repo 의 모든 row 삭제, 다른 repo 보존 | 단위 |
| F-8b | `Upsert` / `List` 호출 시 store 가 입력 `repo_full_name` 을 lowercase 정규화 후 처리 (결정 15) — 즉 `RepoSecret{RepoFullName: "Foo/Bar"}` 입력 후 `List("foo/bar")` 가 같은 row 반환 | 단위 |

### 6-2. Hub Admin UI — 페이지 렌더

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-9 | `GET /admin/repos` 가 200 + 메뉴에 "Repos" 활성 표시 | Playwright snapshot |
| F-10 | 인덱스에 previews + repo_secrets 의 union repo 목록이 정렬되어 표시 | 시드 후 Playwright table 검증 |
| F-11 | 각 행에 secret 카운트(`{n} keys`) + Edit 링크 | 동일 |
| F-12 | `GET /admin/repos/{owner}/{repo}/secrets` 가 200 + textarea 안에 저장된 KEY=VALUE 렌더(key 정렬) | sqlite seed 후 Playwright |
| F-12b | 같은 페이지에 plaintext 경고 문구가 표시된다 | DOM grep |
| F-12c | Dockerfile 모드 caveat 안내 문구가 표시된다(§5-9) | DOM grep |
| F-12d | "VALUE 의 선·후행 공백은 그대로 저장됩니다" 안내 문구 표시 (결정 3a) | DOM grep |
| F-12e | "case-insensitive 정규화로 lowercase 저장" 안내 문구 표시 (결정 15) | DOM grep |
| F-13 | 존재하지 않는 segment 조합(`/admin/repos/foo/bar/secrets` + DB 비어있음)도 200(빈 textarea) — 사전 등록 동선 | Playwright |

### 6-3. Hub Admin UI — 폼 저장

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-14 | POST 폼 제출 후 303 → `?msg=saved` | HTTP 응답 검사 |
| F-15 | 저장 후 reload 시 textarea 에 입력 값(line 정렬 후) 보존 | Playwright |
| F-16 | 저장 시 diff 가 정확하게 적용된다 (added=2 / updated=1 / removed=1) — 시드+POST→DB SELECT 비교 | 통합 |
| F-17 | KEY 정규식 위반 라인 포함 → 200 재렌더 + ValidationError 표시 + DB 미변경 | Playwright + SELECT |
| F-17b | `=` 가 없는 라인 포함 → 200 재렌더 + ValidationError (`line N: missing '=' separator`) + DB 미변경 (결정 3a 단계 4) | Playwright + SELECT |
| F-17c | `=foo` (KEY 가 빈 문자열) 라인 포함 → 200 재렌더 + ValidationError + DB 미변경 (결정 3a 단계 5) | Playwright + SELECT |
| F-17d | `KEY=a=b=c` 입력 → DB 의 VALUE 가 `a=b=c` 로 저장 (결정 3c, 첫 `=` 만 split) | 통합 SELECT |
| F-17e | `KEY=eyJhbGc...==` (base64 padding) → VALUE 가 `eyJhbGc...==` 로 보존 (결정 3c) | 통합 SELECT |
| F-17f | `KEY=value#tag` → VALUE 가 `value#tag` 로 저장 (결정 3b, inline `#` 보존) | 통합 SELECT |
| F-17g | 라인 시작 `# comment` 는 코멘트로 무시 (결정 3a 단계 3) | 통합 |
| F-17h | `  KEY  =value` 입력 → KEY 가 `KEY` 로 trim 후 저장 (결정 3a 단계 7) | 통합 SELECT |
| F-17i | `KEY= value ` 입력 → VALUE 가 ` value ` 로 trim 안 됨 보존 (결정 3a 단계 8) | 통합 SELECT |
| F-17j | `KEY=` 입력 → 합법, value="" 저장 (결정 3a 단계 9) | 통합 SELECT |
| F-17k | `KEY=foo\nbar` (literal `\` + `n`) 입력 → DB VALUE 5글자 그대로 + `.env` 직렬화 시도 동일 (결정 3b) | 통합 SELECT + dotenv 단위 |
| F-18 | textarea 빈 문자열 제출 → 같은 repo 의 모든 row 삭제 후 303 (`removed=N`) | 통합 |
| F-18b | `# comment` 라인은 무시되어 저장되지 않는다 | 통합 |
| F-18c | path segment `Foo/Bar` 로 POST → DB 에는 lowercase `foo/bar` 로 저장 (결정 15) | 통합 SELECT |

### 6-4. 프로토콜 / Dispatcher hydration

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-19 | `JobAssignData.BuildEnv` 가 wire round-trip 정상 동작(있을 때 / nil 일 때 omitempty) | 단위 |
| F-20 | Dispatcher 가 Claim 직후 `RepoSecrets.List(repo)` 호출, 결과를 `JobAssignFromPreview` 에 전달 | dispatcher_test 의 fakeRepoSecretStore + 호출 카운트 |
| F-21 | hydration 실패(Store 가 임의 에러 반환) 시 WARN 로그 + 빈 BuildEnv 로 dispatch 계속 | dispatcher_test |
| F-21b | RepoSecrets == nil 로 NewDispatcher 한 경우 hydration 자체 skip + 정상 dispatch | dispatcher_test |

### 6-5. Agent runtime

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-22 | `WriteDotenv` 가 worktree 루트에 `.env` 파일을 작성한다(권한 0600) | 단위 (`os.Stat.Mode().Perm()`) |
| F-23 | env len=0 또는 nil 일 때 파일 미생성(기존 파일 보존) | 단위(파일 사전 작성 후 호출 → 내용 동일) |
| F-24 | value 안의 `\n` 이 literal `\\n` 으로 escape 되어 한 줄에 직렬화 | 단위 byte-by-byte |
| F-25 | Runner.Handle 이 Checkout 직후·LoadPreviewConfig 직전에 WriteDotenv 1회 호출 | runner_test 의 fake cmd + 파일 확인 |

### 6-6. End-to-end (Playwright + 실제 docker compose)

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-26 | **fixture 자체는 본 Phase 산출물** — `tests/fixtures/compose-with-env/` 디렉토리(신규)에 `docker-compose.yml` (env_file 없이 `${GREETING}` 만 image tag/command 에 보간하지 않고, 대신 `environment: GREETING=${GREETING}` 로 컨테이너 환경 주입) + 최소 `Dockerfile` (또는 `image: alpine` + `command: sh -c 'echo $GREETING'`) 1세트를 포함. e2e 시나리오: `repo_url=file://...` 또는 mock webhook 으로 시뮬 → preview 빌드 → `docker logs` 또는 컨테이너 응답에 운영자 입력 `GREETING` 값 포함 확인 | Playwright + `docker logs` grep. fixture 디렉토리 트리는 §4-1 에 추가됨 |
| F-27 | secret 미입력 repo 의 빌드는 정상 동작(.env 없음, 기존 동작 회귀 없음) | Playwright |
| F-28 | secret 입력 후 변경하지 않으면 다음 빌드도 같은 .env 가 작성됨 | 두 번째 webhook 시뮬 + worktree 내용 |
| F-29 | secret 변경 → 변경 후 새 webhook → 새 .env 내용 반영 | 동일 |
| F-30 | Playwright 폼 입력→저장→reload 보존 e2e | e2e |
| F-31 | Playwright 시나리오: 폼 저장 + fake Agent 가 JOB_ASSIGN 의 BuildEnv 를 캡처해 일치 확인 | e2e + Agent stub |

---

## 7. 비기능 요구사항 체크리스트 (NF-*)

| ID | 항목 | 검증 방법 |
|---|---|---|
| NF-Build-1 | `go build ./...` 통과 | CI |
| NF-Vet-1 | `go vet ./...` clean | CI |
| NF-Test-1 | `go test ./... -race` green | CI |
| NF-Lint-1 | `golangci-lint run` clean | CI |
| NF-Dep-1 | go.mod 외부 의존성 추가 0건 | `go mod tidy` diff |
| NF-Layer-1 | `internal/agent` 가 `internal/store` 또는 `internal/db/*` 를 import 하지 않는다 | Grep 도구로 pattern `"github.com/lnyarl/preview/internal/(store\|db)"` path `internal/agent/` 검색 → 결과 0. (Windows·Unix 공통: bash `grep -rE` 또는 PowerShell `Get-ChildItem -Recurse internal\agent | Select-String '\"github.com/lnyarl/preview/internal/(store\|db)\"'` 도 동등.) |
| NF-Layer-2 | `internal/hub` 가 `internal/db/sqlite/*.sql.go` (sqlc 생성 코드) 를 직접 import 하지 않는다 — `internal/store` 인터페이스만 | grep |
| NF-File-1 | 신규 파일은 모두 책임 주석 헤더(3~5줄) 보유 | grep `// 이 파일의 책임:` |
| NF-File-2 | 신규 파일 어떤 것도 300 줄을 넘지 않는다 | `wc -l` |
| NF-Slog-1 | 신규 slog 키 컨벤션: `dispatcher_secret_*`, `admin_ui_repo_secrets_*`, `agent_dotenv_*` | grep |
| NF-Portability-1 | 마이그레이션·sqlc 쿼리에 SQLite 전용 문법 0건 (`AUTOINCREMENT`, `INSERT OR REPLACE`, `::jsonb`) | Grep 도구로 pattern `AUTOINCREMENT\|INSERT OR REPLACE\|::jsonb`, paths `db/queries` + `db/migrations` + `internal/db/sqlite/migrations` → 결과 0 |
| NF-Portability-2 | `RepoSecretStore` 인터페이스가 `internal/store` 에만 정의 | grep |
| NF-Portability-3 | sqlc 생성 코드는 `internal/db/sqlite/repo_secrets.sql.go` 하위에만 존재 | 디렉토리 검사 |
| NF-Portability-4 | `db/migrations/0007_*.sql` 와 `internal/db/sqlite/migrations/0007_*.sql` 가 byte-identical (결정 0) | `diff db/migrations/0007_repo_secrets.up.sql internal/db/sqlite/migrations/0007_repo_secrets.up.sql` 빈 출력 + down 동일. CI hook 등록 권장. |
| NF-Security-1 | `repo_secrets.value` 가 plaintext 임이 마이그레이션 파일·페이지·README·도메인 주석 4곳에 명시된다 | grep `Phase 10` 또는 `plaintext` |
| NF-Security-2 | `WriteDotenv` 가 작성하는 파일 모드 0600 (group/other 읽기 금지) | F-22 와 동일하나 NF 측면 — `os.Stat` |
| NF-Security-3 | Admin UI 가 secret value 를 textarea 에 표시하기 전 HTML escape 한다(`<script>` 등 inject 차단) | `html/template` 자동 escape 의 변환 결과 확인 + 단위 테스트 |
| NF-Security-DotenvLoss-1 | multi-line value 를 입력해 저장한 뒤 `.env` 에 escape 된 형태로 작성됨이 검증되며, 그 손상 가능성이 페이지 안내·README 에 명시된다 | F-24 + grep 안내 문구 |
| NF-Security-Env-1 | JOB_ASSIGN 의 BuildEnv 는 hub→agent WS frame(평문)으로 흐르며, WS 가 TLS 종단(reverse proxy 또는 hub 자체 https) 에 의존한다는 사실이 README 에 1줄 명시 | grep README |
| NF-Wire-1 | `JobAssignData` 에 unknown field 가 들어와도 Agent 가 무시(레거시 Hub 호환) | 와이어 단위 round-trip 테스트 |
| NF-Wire-2 | `BuildEnv` 가 빈 map 일 때 wire JSON 에 키가 생략된다(omitempty) | 단위 marshal 테스트 |
| NF-Regression-1 | Phase 9 의 SHA-keyed preview 동선(F-7~F-12) 회귀 통과 | 기존 e2e 재실행 |
| NF-Regression-2 | Phase 4/6 의 agent_detail / 빌드 흐름 회귀 통과 | 기존 e2e 재실행 |
| NF-Regression-3 | webhook handler 진입점에서 `repo_full_name` 이 lowercase 정규화되어 previews 행에 저장됨 (R-8) | 단위: mixed case payload 입력 → SELECT 결과 lowercase |
| NF-Doc-1 | README 의 "Build secrets" 섹션 신설 + 예시 1개 | grep |

---

## 8. 리스크와 완화책

### 리스크 R-1: Hub→Agent WS frame 평문 송신으로 secret 누출

- **원인**: BuildEnv 가 평문 JSON 으로 WS 위에 흐른다.
- **영향**: WS 가 평문 HTTP 위에 있을 경우 secret 노출 → 운영 환경 보안 침해.
- **완화책**:
  1. 본 Phase 는 운영자가 hub 앞에 reverse proxy(TLS 종단) 또는 hub 의 자체 https 를 통해 wss:// 를 사용한다는 것을 README 에 명시(NF-Security-Env-1).
  2. `cmd/hub` 시작 로그에 "WARNING: serving plain HTTP — JOB_ASSIGN payload contains build secrets" 경고 1줄(NF-Security-Env-1 보강).
- **트리거 지표**: README 와 시작 로그에 경고 문구가 누락되면 완화 실패. plaintext HTTP 운영 노출이 보안 사고로 보고되면 R-1 의 완화 실패로 판정.

### 리스크 R-2: DB 평문 저장 → 디스크 도난·백업 유출 시 즉시 노출

- **원인**: `repo_secrets.value` plaintext.
- **영향**: hub.db 파일 단일 노출이 모든 repo secret 의 노출과 동치.
- **완화책**:
  1. 결정 7 의 명시 표기 — 운영자가 위험을 인지한 상태에서 사용.
  2. 후속 Phase(envelope encryption) 를 본 기획서 §10 에 명시.
  3. hub.db 파일 권한 0600 으로 OS 단 보호 (기존 modernc.org/sqlite 동작 — 별도 검증).
- **트리거 지표**: 운영자에게 "Phase 11 암호화" 일정이 합의되지 않으면 본 Phase 산출이 SoT 가 되어 위험 누적. R-2 의 완화 실패는 Phase 11 의 우선순위 강제 조건.

### 리스크 R-3: textarea 전체 submit 로 secret 우발 삭제

- **원인**: 결정 2 의 set replace 시맨틱. 운영자가 한 줄 지우고 저장하면 그 키가 영구 삭제.
- **영향**: 빌드 실패가 다음 webhook 에서야 발견.
- **완화책**:
  1. POST 처리 시 diff 결과(added/updated/removed)를 SavedFlash 에 명시(F-16).
  2. 모든 row 가 삭제되는 경우 명시 경고(향후 confirm dialog 후보 — 본 Phase 는 alert 로만).
  3. slog 에 변경 카운트 기록(NF-Slog-1).
- **트리거 지표**: SavedFlash 미표시 또는 slog 누락 시 완화 실패.

### 리스크 R-4: Dispatcher hydration 으로 dispatch latency 증가

- **원인**: Claim 직후 추가 SQL 1쿼리.
- **영향**: 다수 preview queue 와 짧은 PING 주기에서 dispatcher 가 secret 쿼리 비용을 누적.
- **완화책**:
  1. `repo_secrets.repo_full_name` 단일 컬럼 인덱스(§5-1) 로 단일 쿼리 비용 O(log N).
  2. 한 OnReady 에 한 번만 호출(claimed 1건당 1 쿼리). `ListQueuedForCandidates` 에는 미호출.
  3. 미래 Phase 에서 in-memory cache + invalidate-on-save 가능(본 Phase 는 명시 비범위).
- **트리거 지표**: dispatcher_test 의 hydrate 호출 횟수 == claimed 건수. 이 비율 위반은 코드 결함.

### 리스크 R-5: `.env` 파일이 git tracked 인 worktree 를 본 Phase 가 덮어쓸 가능성

- **원인**: 결정 5 의 "BuildEnv 비어있으면 skip" 정책이 있지만, 운영자가 1개라도 secret 을 등록하면 기존 git tracked `.env` 가 덮어써진다.
- **영향**: 사용자가 의도한 default `.env` 의 일부 키가 사라지고 hub 측 secret 만 남음.
- **완화책**:
  1. 페이지 안내 문구에 "이 secret 은 worktree 의 `.env` 를 통째로 대체합니다" 명시.
  2. 후속 Phase 후보: merge 정책(union) 또는 사용자 정의 path. 본 Phase 비범위.
- **트리거 지표**: 사용자 보고("내 .env 의 어떤 키가 사라졌어요").

### 리스크 R-6: 마이그레이션 0007 적용 후 기존 fake stores 가 컴파일 깨짐

- **원인**: `internal/store.RepoSecretStore` 인터페이스 추가는 직접적으론 기존 fake 를 깨지 않는다. 그러나 두 시그니처 변경이 호출처에 영향:
  - `JobAssignFromPreview(p store.Preview)` → `(p store.Preview, env map[string]string)` — **호출처 grep 결과 1곳뿐**: `internal/hub/ws_registry.go:113`. 본 Phase 가 그 한 줄을 수정하면 끝(fan-out 작음).
  - `WSJobSender.SendJobAssign(ctx, agentID, p)` → `(ctx, agentID, p, env)` — 호출처는 dispatcher 내부 1곳 + 테스트에서 fakeSender 정의가 있는 모든 파일. dispatcher_test.go, reconciler_test.go, webhook_handler_test.go, ws_sync_test.go, server_shutdown_test.go 등에서 fakeSender 의 메서드 시그니처 보정 필요.
  - `NewDispatcher(as, ps, sender, logger)` → `(as, ps, rs, sender, logger)` — 위와 동일한 테스트 파일 set + `cmd/hub/main.go`.
- **완화책**:
  1. `NewDispatcher` 신규 시그니처에서 `rs` 가 nil 허용(F-21b).
  2. `WSJobSender.SendJobAssign` 의 env 인자도 nil 허용 → `JobAssignFromPreview(p, nil)` → BuildEnv omitempty.
  3. 기존 테스트는 nil 전달로 컴파일 통과만 확인. 기능 검증은 dispatcher_test 신규 케이스에서 수행.
  4. `cmd/hub/main.go` 는 실제 sqlite 구현체 주입.
- **트리거 지표**: `go build ./...` 또는 `go test ./...` 실패.

### 리스크 R-7: Dockerfile 모드에서 secret 입력해도 무성 실패 (worktree `.env` 만 생기고 컨테이너 ENV 미주입)

- **원인**: 결정 5 의 정책상 빌드 모드와 무관하게 worktree 루트에 `.env` 가 작성된다. 그러나 Dockerfile 모드의 `docker build` 는 `.env` 를 자동으로 ARG/ENV 로 주입하지 않으며 (compose 모드와 다름, §5-9), `ContainerCreate/Start` 도 마찬가지다. 운영자는 Admin UI 에서 secret 을 입력했고 페이지에는 "Saved" 가 표시되며 worktree 에 `.env` 도 생기지만, 컨테이너 안에서는 환경변수가 없는 silent failure 가 발생한다.
- **영향**: 운영자가 secret 누락 원인을 추적하느라 시간 낭비. Dockerfile 모드 사용자가 본 Phase 의 가치를 못 받는다고 오인.
- **완화책**:
  1. `repo_secrets.gohtml` 페이지 상단에 caveat 명시 (F-12c) — "Dockerfile 모드의 컨테이너에는 자동 주입되지 않습니다. `Dockerfile` 안에서 `COPY .env /app/.env` + 런타임 dotenv 로딩, 또는 build args 로 명시 전달이 필요합니다." 1문단.
  2. README "Build secrets" 섹션의 동일 caveat (NF-Doc-1).
  3. 향후 Phase: Dockerfile 모드일 때 `ContainerCreate` 의 `Env` 슬라이스에 BuildEnv 를 동봉하는 옵션 (본 Phase 비범위, §2-2). 본 Phase 에서는 안내 문구가 유일한 방어선.
- **트리거 지표**: 운영자 보고("secret 등록했는데 컨테이너에서 안 보여요" — Dockerfile 모드) 발생 시 안내 문구 누락 또는 미숙지로 판정. F-12c 의 DOM grep 이 통과하면 1차 방어.

### 리스크 R-8: webhook handler 가 GitHub payload 의 case 를 그대로 저장 → 결정 15 의 lowercase 가정 깨짐

- **원인**: 결정 15 는 "DB 의 `repo_full_name` 은 항상 lowercase" 를 가정한다. 그러나 webhook handler 가 GitHub payload 의 `repository.full_name` (GitHub 자체는 owner 표시명을 보존하므로 mixed case 가능) 을 그대로 previews 행에 저장하고 있다면, dispatcher hydration 시 `claimed.RepoFullName` 이 mixed case 로 들어와 `RepoSecrets.List(ctx, "Foo/Bar")` → store 가 lowercase 정규화하더라도 통과. 단 `previews.repo_full_name` 자체가 mixed case 면 `/admin/repos` 인덱스의 union 결과에 `Foo/Bar` 와 `foo/bar` 두 행이 별도 표시되는 모순 발생.
- **완화책**:
  1. 본 Phase 가 `internal/hub/webhook_handler.go` 의 `repo_full_name` 처리 자리를 grep 으로 확인 → 미정규화면 `strings.ToLower` 추가 + previews 의 기존 mixed-case 데이터에 대한 1회 마이그레이션 SQL `UPDATE previews SET repo_full_name = LOWER(repo_full_name)` 를 0007 의 up.sql 에 포함 (테이블 생성 SQL 과 동일 트랜잭션).
  2. `PreviewStore.Upsert` 등 도메인 진입점에도 동일 정규화 적용 (방어적).
  3. 검증: F-18c 가 secret 측 정규화 검증, 추가로 NF-Regression-3 으로 webhook 측 정규화 검증 추가.
- **트리거 지표**: `/admin/repos` 인덱스에 같은 repo 가 case 만 다르게 두 번 표시되면 완화 실패.

---

## 9. 다음 Phase 연결점

본 Phase 의 산출물이 후속 Phase 에서 어떻게 활용되거나 확장될 수 있는지:

1. **Phase 11 (가칭) — Build Secrets 암호화**:
   - `repo_secrets.value` 를 envelope encryption(예: master key + per-row data key) 으로 in-place 마이그레이션.
   - `RepoSecretStore.Upsert/List` 의 인터페이스는 그대로, 구현체에서 transparent encrypt/decrypt.
   - 마스터키 주입은 `cmd/hub` 의 환경변수 또는 KMS adapter.
   - 본 Phase 의 §3 결정 7, R-2 가 직접 가리키는 후속.

2. **Phase 12 (가칭) — Per-PR Secret Override / 사용자 정의 path**:
   - `repo_secrets` 와 직교하는 `preview_secrets` (preview_id PK) 또는 `repo_secrets` 에 `pr_number INTEGER NULL` 컬럼 추가.
   - `.preview.yml` 에 `env_files: [...]` 필드 도입(결정 6 의 후속).

3. **Phase 13 (가칭) — Audit log**:
   - 결정 8 후속. `repo_secret_events` 테이블 + Upsert/Delete 시 같은 트랜잭션으로 INSERT.

4. **본 Phase 가 다음 Phase 에 남기는 TODO**:
   - 마이그레이션 파일 헤더에 plaintext 경고 (결정 7) — 후속 암호화 Phase 의 마이그레이션이 본 컬럼을 변환해야 함.
   - README 의 "Build secrets" 섹션에 "Phase 11 에서 암호화 예정" 플레이스홀더 1줄.
   - `dispatcher.RepoSecrets nil-safe` 동작 (F-21b) 은 후속 Phase 에서도 유지(테스트가 모두 nil 주입을 사용).

5. **dispatcher 의 in-memory cache** (본 Phase 비범위, R-4 완화 후속):
   - `Dispatcher` 가 `RepoSecretStore` 위에 sync.Map cache 를 두고 Admin UI 의 POST 핸들러가 invalidate 트리거. 본 Phase 는 단일 hub + 낮은 dispatch 빈도 가정으로 미적용.

---

## 10. 미해결/확인 사항 (Open Questions)

1. **`.env` 파일이 worktree 의 `.gitignore` 와 충돌**: 사용자 repo 의 `.gitignore` 에 `.env` 가 있어도 우리는 worktree 에 직접 작성하므로 무영향(git 명령을 거치지 않음). 단 `git status` 시 untracked 로 표기되며 사용자가 수동으로 worktree 를 들여다보면 보임. **확인 필요**: 본 Phase 동작이 기대치인지.
2. **Hub 가 단일 인스턴스 가정의 한계**: 다중 hub 인스턴스 환경에서 Admin UI POST 와 dispatcher hydrate 가 다른 노드에서 일어나면 SQLite 의 `BEGIN IMMEDIATE` race 가 발생. 현재 hub 는 단일 인스턴스 가정이므로 본 Phase 는 영향 없음 — **확인 필요**: 향후 멀티-hub 시 본 Phase 의 row-level UPSERT 가 자연스럽게 정합성을 보장한다(PK 충돌 → DO UPDATE).
3. **JOB_ASSIGN 의 `BuildEnv` 와 `Labels` 의 의미 충돌 없음**: Labels 는 라우팅 매칭, BuildEnv 는 빌드 시 주입. 두 값이 한 메시지에 공존해도 Agent 측에서 분리 처리 — **확인만**.
4. **Dispatcher 가 secret 을 못 찾는 경우와 빈 map 의 의미 차이**: 본 Phase 는 둘 다 "BuildEnv 미주입" 으로 동일 처리. 후속 Phase 에서 "secret 필요한데 없음" 을 명시 에러로 분기하려면 별도 marker 필요 — **합의 필요**.
5. **`repo_secrets` 와 `previews.repo_full_name` 의 FK 관계**: 본 Phase 는 FK 미설정. 인덱스 페이지의 union 동작이 단순 distinct 합집합으로 구현. **확인만**.

> 이전 초안의 Open Question 1 (case 정규화) 은 결정 15 로 격상되었음.

---

## 리뷰 이력

- 2026-04-27 — planner: 초안 작성.
- 2026-04-27 — planner: rev 2 — plan-reviewer 1차 피드백 11항목 반영.
  - **반드시(1~7)**:
    1. 결정 0 신설 + §2-1·§4-1·F-1c·NF-Portability-4 에 양 디렉토리 byte-identical 정책 명시.
    2. 결정 3a 신설 (trim/빈줄/등호없는줄/`KEY=` 정책) + F-17b/c/h/i/j/F-12d.
    3. 결정 3b 신설 (`#` 보존, `\n` literal 보존) + F-17f/k.
    4. 결정 3c 신설 (첫 `=` 만 split) + F-17d/e.
    5. F-26 fixture 를 본 Phase 산출물로 격상 + §4-1 에 `tests/fixtures/compose-with-env/` 추가.
    6. 결정 14 확장: previews 측은 `PreviewStore.ListRepos` 신규 메서드 + DB DISTINCT, 합집합은 application layer.
    7. 결정 15 신설 (lowercase 정규화, 저장 시점 강제) + F-8b/F-12e/F-18c/NF-Regression-3.
  - **가능하면(8~11)**:
    8. R-6 확장: `JobAssignFromPreview` 호출처 grep 결과 (1곳, ws_registry.go:113) 명시.
    9. NF-Layer-1 의 grep 명령을 Grep 도구 + PowerShell 동등 명령으로 표기.
    10. §5-1 노트에 보조 인덱스의 결정 근거 격상 (PK prefix 만으로 가능하나 명시 인덱스 채택).
    11. R-7 신설 (Dockerfile 모드 silent failure) + F-12c 안내 문구로 완화.
