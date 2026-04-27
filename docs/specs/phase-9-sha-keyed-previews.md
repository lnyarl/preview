# Phase 9 — SHA-keyed Previews + is_adhoc 플래그

Status: **DRAFT (rev2 — plan-reviewer 피드백 반영)**
Author: planner
Date: 2026-04-27

---

## 1. Phase 개요

### 1-1. 배경

Phase 0~8 까지 `previews` 테이블의 자연키(natural key)는 `(repo_full_name, pr_number)` 였다. `webhook_handler.handleUpsert` 와 `admin_ui.testBuildSubmit` 두 진입점 모두 이 키를 ON CONFLICT 로 사용해 PR 당 정확히 1 개의 preview row 를 유지했고, `synchronize` 이벤트(같은 PR 에 commit 추가)는 동일 row 의 `commit_sha` 컬럼만 덮어쓰는 식으로 처리했다(`previews.sql.go: UpsertPreview`).

이 모델은 두 가지 한계를 가진다.

1. **커밋 단위 추적 불가** — PR 의 어떤 SHA 가 빌드에 성공했고 어떤 SHA 가 실패했는지를 history 로 보존할 수 없다. `preview_events` 에 status 전이는 남지만 어떤 commit 에 대한 전이인지가 사라진다(commit_sha 가 매번 덮어쓰여지므로).
2. **Admin UI Test Build 의 PR 0 충돌** — `testBuildSubmit` 은 `PrNumber: 0` 으로 Upsert 하는데, 같은 repo 에서 두 번째 Test Build 를 하면 PR 0 키가 이미 점유되어 기존 row 의 sha/branch 만 덮어쓴다. 두 번 빌드한 두 SHA 의 결과가 한 row 에 뒤섞인다(`admin_ui.go:413~458`).

### 1-2. 목표

본 Phase 는 위 한계를 해소하기 위해 자연키를 `(repo_full_name, commit_sha)` 로 이동하고, webhook 으로 자동 생성된 preview 와 사용자가 손으로 만든 preview 를 식별할 `is_adhoc` 플래그를 도입한다.

1. **자연키 변경**: `UNIQUE (repo_full_name, pr_number)` → `UNIQUE (repo_full_name, commit_sha)`. SQLite 의 NULL 처리 규칙(NULL 끼리는 UNIQUE 위반이 아님)을 활용해 `commit_sha = NULL` 이면 항상 신규 row.
2. **is_adhoc 컬럼**: `BOOLEAN NOT NULL DEFAULT 0`. webhook 진입점은 `false`, admin Test Build 진입점은 `true` 로 INSERT.
3. **SHA 지연 resolve**: webhook payload 가 비어있거나 Admin UI 가 SHA 를 비워둔 경우, Agent 가 `git rev-parse` 로 branch tip 을 resolve 한 뒤 첫 STATUS_UPDATE(`building`) 에서 sha 를 hub 에 올린다. Hub 는 `UpdateStatus` 의 `PreviewFields.CommitSha` 를 통해 row 의 sha 를 채운다.
4. **synchronize 이벤트 처리**: 새 commit 이 푸시되면 같은 PR 의 기존 in-flight preview(상태 ∈ {queued, assigned, building, running}) 를 teardown 하고 새 sha 의 신규 row 를 INSERT 한다.
5. **(repo, pr) 의 active row 단일 보장(불변성)**: webhook synchronize 가 도착하면 항상 직전 active row 를 teardown 한 뒤 신규 INSERT 하므로, **`(repo, pr)` 페어당 status ∈ {queued, assigned, building, running} 인 row 는 항상 0 또는 1 개**다. 이 불변성은 §3 결정 4 가 지키는 핵심 약속이며, `FindByHost`(reverse proxy 호스트 → preview 매핑)와 dispatcher 의 단일 active 가정이 이 약속에 의존한다(§9 도 참조).

### 1-3. 비목표

- **PR 별 history 페이지** — Admin UI 에 "PR #123 의 모든 preview 시간순" 을 보여주는 새 페이지는 비범위. 기존 `/admin/previews` 리스트가 SHA 별 row 를 표시하면 충분.
- **Old preview 자동 가비지 컬렉션** — 같은 PR 의 done/failed 상태 row 를 시간 기반으로 정리하는 retention 정책은 본 Phase 비범위. 후속 Phase 에서 다룸.
- **PostgreSQL 동시 마이그레이션** — 본 Phase 는 SQLite 만. PG 에서는 `NULL UNIQUE` 동작이 다르므로(NULL 도 UNIQUE 위반이 아닌 점은 동일하나 인덱스 동작 차이) PG 어댑터 작업 시 별도 검증.
- **`pr_number = 0` 컨벤션 변경** — Admin Test Build 의 `PrNumber: 0` 마커는 그대로. 단 이제는 `is_adhoc = true` 가 진실 소스이므로 prNum=0 검사에 의존한 코드(있다면)를 모두 `is_adhoc` 로 교체.
- **JOB_ASSIGN 의 `commit_sha=""` 처리** — Agent 의 RepoCache.Checkout 은 이미 빈 sha → branch tip resolve 를 지원함(`repocache.go:219~`). 동작 로직 변경 없음. 본 Phase 는 resolve 결과를 hub 에 보고하는 경로만 추가.
- **`ResetAllAssigned` 의 sha-aware 변종** — Hub 재기동 시 `assigned → queued` 일괄 복귀는 그대로(per-row sha 영향 없음).

### 1-4. 성공 기준 요약

- `previews` 테이블에 `is_adhoc INTEGER NOT NULL DEFAULT 0` 컬럼 존재(SQLite 는 BOOLEAN 을 INTEGER 로 저장; F-2).
- `UNIQUE (repo_full_name, pr_number)` 제약 제거, `UNIQUE (repo_full_name, commit_sha)` 제약 추가(F-1).
- 같은 (repo, pr) 에 다른 sha 두 번 webhook → row 2 개 생성(F-7).
- 같은 (repo, sha) 두 번 webhook → row 1 개(idempotent) (F-8).
- 같은 (repo, "") 빈 sha 두 번 → row 2 개(NULL UNIQUE 미발동) (F-9).
- Admin Test Build 두 번 같은 repo 다른 branch → row 2 개, 모두 `is_adhoc=true` (F-10).
- Agent 가 빈 sha 를 resolve 후 building STATUS_UPDATE 로 보고 → row 의 commit_sha 갱신(F-11).
- Webhook synchronize 로 새 sha 도착 시 기존 active row teardown + 새 row queued(F-12).
- `internal/store/store.go` `Preview` struct 에 `IsAdhoc bool` 필드 추가; `UpdateStatus` 호출자가 sha 를 채울 수 있도록 `PreviewFields.CommitSha *string` 필드 추가(F-3, F-4).

---

## 2. In / Out of Scope

### 2-1. In Scope

- **DB 마이그레이션**
  - `internal/db/sqlite/migrations/0006_sha_keyed_previews.up.sql` (신규)
  - `internal/db/sqlite/migrations/0006_sha_keyed_previews.down.sql` (신규)
  - `db/schema/0006_sha_keyed_previews.sql` (신규, sqlc 입력)
- **sqlc 쿼리**
  - `db/queries/previews.sql` 수정: `UpsertPreview` 의 ON CONFLICT 키 + 컬럼 목록 변경, 새 쿼리 `UpdatePreviewSha`/`GetActivePreviewByRepoAndPR`/`GetPreviewByRepoAndSha` 추가
  - `internal/db/sqlite/previews.sql.go` (sqlc 재생성)
- **Store 인터페이스 / 구현**
  - `internal/store/store.go` — `Preview.IsAdhoc bool` + `PreviewFields.CommitSha *string` + 인터페이스에 `GetActiveByRepoAndPR` 메서드(synchronize 처리용)
  - `internal/db/sqlite/preview_store.go` — `Upsert` 의 사전 SELECT 키 교체, IsAdhoc 파라미터 전파, `UpdateStatus` 의 commit_sha COALESCE 갱신, 새 메서드 구현
- **Hub 진입점**
  - `internal/hub/webhook_handler.go`
    - `handleUpsert` 에서 `synchronize` 분기를 분리: 신규 SHA 면 기존 active preview teardown + 신규 INSERT
    - `IsAdhoc=false` 로 Upsert
  - `internal/hub/admin_ui.go`
    - `testBuildSubmit` 에서 `IsAdhoc=true` 로 Upsert
    - sha 미입력 + 동일 row 발견 시 "기존 row reopen" 로직 제거(자연키가 sha 라 새 row 가 보장됨)
- **Hub Status 처리**
  - `internal/hub/status_update.go` (또는 STATUS_UPDATE 라우팅 위치) — `building` STATUS_UPDATE 가 비어있지 않은 `commit_sha` 를 들고 오면 `PreviewFields.CommitSha` 채워 `UpdateStatus` 호출
- **프로토콜**
  - `internal/protocol/messages.go` — `StatusUpdateData.CommitSHA *string` 추가 (Phase 6 PreviewURLs 와 동일한 nullable 패턴)
- **Agent**
  - `internal/agent/runner.go` — `cache.Checkout` 직후 worktree 의 실제 HEAD sha 를 `git rev-parse HEAD` 로 추출하고, `building → running` 전이의 STATUS_UPDATE 또는 별도 `building` STATUS_UPDATE 에 `CommitSHA` 동봉
  - `internal/agent/cmd_runner.go` — `CmdRunner` 인터페이스에 `Output(ctx, name string, args ...string) (string, error)` 메서드 신설(결정 9 참조). 표준 출력 캡쳐가 가능한 새 메서드(`Run` 만으론 stdout 회수 불가)로, 실제 구현체는 `exec.CommandContext(...).Output()` 위임. fake/test 구현체(`internal/agent/cmd_runner_fake.go`, `internal/agent/runner_test.go` 의 fakeCmd, 그 외 cmd 의존 테스트들)에 모두 추가
- **Admin UI 표시**
  - `internal/hub/views/previews.gohtml` — `is_adhoc=true` 행에 "Adhoc" badge
  - `internal/hub/views/preview_detail.gohtml` — Adhoc 라벨 표시
  - JSON `PreviewView` 에 `IsAdhoc bool json:"is_adhoc"` 필드 추가
- **테스트 / fake 호환성** — `Preview.IsAdhoc bool` 필드 추가, `commit_sha` NULLABLE 화에 따른 sqlc `sql.NullString` 래핑(§5-2 참조), `GetActiveByRepoAndPR` 인터페이스 메서드 추가, `PreviewFields.CommitSha *string` 추가에 따라 다음 파일들이 컴파일/회귀 통과해야 함:
  - `internal/db/sqlite/preview_store_test.go` — 자연키 변경 케이스 보강(같은 sha 멱등성, 다른 sha 별 row, NULL sha 다중 row, ErrShaConflict)
  - `internal/hub/webhook_handler_test.go` — synchronize 시 기존 active teardown + 신규 row 검증, decision matrix 4 케이스 (§5-6)
  - `internal/hub/admin_ui_test.go` — Test Build 두 번 다른 sha → row 2 개 + IsAdhoc 플래그 검증
  - `internal/hub/fake_preview_store_test.go`(또는 fakePreviewStore 정의가 있는 파일들 전수) — `IsAdhoc` 필드 보존, `GetActiveByRepoAndPR` 메서드 구현, `UpdateStatus` 의 `fields.CommitSha` 처리(같은 sha 무해 통과 / 다른 sha → ErrShaConflict)
  - `internal/agent/runner_test.go` — fakeCmd 에 `Output` 메서드 추가, `git rev-parse HEAD` 응답 시나리오
  - `internal/agent/cmd_runner_fake.go`(있을 시) — `Output` 메서드 stub
  - `internal/hub/dispatcher_test.go`, `internal/hub/reconciler_test.go` 등 fakePreviewStore 를 쓰는 모든 호출자 — 컴파일 통과만 확인(동작 변경 없음, R-7 참조)

### 2-2. Out of Scope

- **DROP COLUMN pr_number** — pr_number 는 표시·라우팅·로깅용으로 보존. 자연키에서만 빠짐.
- **`assigned_agent_id` FK 정책 변경**
- **Admin UI 의 PR 별 그룹 뷰** (다음 Phase 후보)
- **Webhook 의 다른 이벤트 타입(label, review 등)**
- **Hub 시작 시 schema 자동 마이그레이션 옵션 변경** — 기존 `migrate.Up` 흐름 그대로
- **Postgres 호환성 테스트**
- **`is_adhoc` 의 ID prefix 변경 (예: "adhoc-...")** — UUID 그대로
- **Adhoc preview 의 webhook closed 자동 teardown 가드** — 결정 8 참조. GitHub webhook 이 `PrNumber=0` 인 close payload 를 보낼 일이 없어 dead code 가 되므로 제외. "Adhoc 은 수동 teardown 만 유효" 는 PrNumber=0 컨벤션으로 자연스럽게 보장됨

---

## 3. 설계 결정 및 근거

### 결정 1 — 자연키를 `(repo_full_name, commit_sha)` 로 변경

- **결정**: `UNIQUE (repo_full_name, pr_number)` 제약 제거, `UNIQUE (repo_full_name, commit_sha)` 추가.
- **근거**: 
  - PR 별 commit history 보존이 목적. PR 단위 슬롯에서는 동일 PR 의 두 번째 commit 이 첫 commit 의 결과를 덮어써 history 가 사라진다.
  - SHA 는 글로벌 유일에 가까워 자연키 후보로 안정적.
  - `is_adhoc=true` (PrNumber=0) 의 경우에도 다른 SHA 를 입력하면 별도 row 가 생긴다.
- **버려진 대안**:
  - **(repo, pr, sha) 3-튜플 키** — pr=0 일 때도 sha 만 있으면 충돌 회피 가능. 그러나 pr 컬럼이 키에 들어가면 webhook synchronize 시 같은 PR 의 새 sha 가 다른 슬롯이 되어 의도와 일치하지만, "Adhoc 빌드와 webhook 빌드가 같은 SHA 를 가진 경우 별도 row 가 됨" — 같은 commit 두 번 빌드 가능성을 막지 못함. 단순성 면에서 (repo, sha) 가 우월.
  - **(repo, sha) 만 키 + PR 정보는 메타데이터** — Adopted.
- **되돌릴 때 비용**: 마이그레이션 0006 의 down 으로 이전 키 복원 가능. 단 이 시점까지 누적된 "같은 (repo, pr) 에 다중 sha" 데이터가 있으면 down 마이그레이션이 UNIQUE 위반으로 실패. down 스크립트에 "각 (repo, pr) 그룹에서 가장 최근 row 만 보존" 로직 포함 필요(§5-3).

### 결정 2 — `is_adhoc INTEGER NOT NULL DEFAULT 0` (BOOLEAN 등가)

- **결정**: SQLite 는 BOOLEAN 을 INTEGER 로 저장(0/1). 컬럼 타입 `INTEGER NOT NULL DEFAULT 0`. Go 측은 `bool`.
- **근거**: 
  - SQLite/Postgres 양쪽 모두 INTEGER 0/1 저장 가능 (이식성 원칙). Postgres 는 BOOLEAN 타입이 별도지만 sqlc 는 둘 다 Go bool 로 매핑 가능.
  - DEFAULT 0 으로 기존 row 자동 false (webhook 이 만든 것으로 가정하는 보수적 처리).
- **버려진 대안**:
  - **`source TEXT NOT NULL DEFAULT 'webhook'`** — `webhook | adhoc | api` 등 확장 여지. 그러나 현재 두 종류뿐이고 enum 검증 로직이 추가로 필요. 단순 boolean 으로 충분.
- **되돌릴 때 비용**: 컬럼 DROP 마이그레이션. SQLite 는 ALTER TABLE DROP COLUMN 이 3.35+ 에서 지원되므로 단순.

### 결정 3 — sha=NULL 또는 sha="" 의 UNIQUE 회피 처리

- **결정**: SQLite 의 UNIQUE 제약은 NULL 값을 모두 distinct 로 취급한다(여러 NULL 이 공존 가능). 따라서 sha 가 비어있을 때는 컬럼 값을 **NULL** 로 INSERT 하고, 비어있지 않을 때만 실제 SHA 문자열을 INSERT.
  - 현재 schema 에서 `commit_sha` 는 `TEXT NOT NULL DEFAULT ''`. Phase 9 마이그레이션에서 **NULLABLE 로 변경** (`TEXT NULL`).
  - Go 측 `Preview.CommitSha string` 은 그대로 유지(빈 문자열 = NULL 로 매핑). preview_store 의 변환 레이어에서 `if p.CommitSha == "" { sql.NullString{Valid:false} }` 패턴.
  - **sqlc 매핑**: `commit_sha` 가 NULLABLE 가 되면 sqlc 가 자동으로 `sql.NullString` 으로 생성한다. Go 도메인 `Preview.CommitSha string` 은 빈 문자열("")을 그대로 쓰므로, `internal/db/sqlite/preview_store.go` 의 모든 변환 지점(읽기·쓰기 양방향)에서 NullString ↔ string 매핑 헬퍼를 1 쌍 둔다(§5-2 의 헬퍼 정의 참조). `sqlc.yaml` 에는 별도 nullable override 를 추가하지 않는다(생성 코드의 NullString 을 그대로 받고 store 변환 레이어에서 흡수). 이유: override 를 쓰면 sqlc 결과를 그대로 `*string` 으로 노출해야 하는데, 도메인 시그니처를 nil-가능 포인터로 바꾸면 호출자(handler·view) 영향 범위가 커진다. 변환 레이어 1 곳에 격리하는 편이 비용이 작다.
- **근거**:
  - NULL UNIQUE 제외는 SQLite 와 Postgres 모두에서 동일 동작(SQL 표준).
  - "빈 문자열 sha 도 row 분리" 는 webhook payload 가 부분적으로 비어있는 경우(예: 일부 이벤트 페이로드 변형, 수동 trigger)에 대한 안전망. resolve 후 `UpdateStatus` 가 sha 를 채워 row 가 자기 SHA 로 식별 가능하게 됨.
- **버려진 대안**:
  - **`commit_sha TEXT NOT NULL DEFAULT ''` 유지 + 빈 문자열을 SHA 자리표시자로 취급** — UNIQUE 제약이 빈 문자열에서 발동해 두 번째 빈 sha INSERT 가 실패. 의도와 정반대.
  - **partial unique index** (`CREATE UNIQUE INDEX ... WHERE commit_sha != ''`) — SQLite/Postgres 둘 다 지원하지만 마이그레이션·sqlc 흐름이 복잡. 컬럼 NULLABLE 화로 동등 효과.
  - **sqlc.yaml override 로 `*string` 직접 노출** — 도메인·테스트·테스트 fake 까지 nil-포인터 세상으로 끌고 가야 함. store 레이어 흡수가 변경 범위 작음.
- **되돌릴 때 비용**: down 마이그레이션에서 `UPDATE previews SET commit_sha='' WHERE commit_sha IS NULL` + 컬럼 NOT NULL 복원. 두 단계.

### 결정 4 — `synchronize` 이벤트는 "기존 active teardown + 신규 row INSERT"

- **결정**: webhook `synchronize` 액션이 도착하고 새 commit_sha 가 같은 (repo, pr) 의 기존 active(상태 ∈ queued|assigned|building|running) row 와 다르면:
  1. 기존 active row 에 `UpdateStatus(*→teardown, message="superseded_by_sha=<new>")` 호출 + JOB_TEARDOWN 송신
  2. 새 sha 로 신규 INSERT (status='queued', is_adhoc=false)
- **근거**:
  - 새 commit 이 푸시되면 이전 commit 에 대한 preview 는 이미 stale. 디스크/포트 자원 보존을 위해 teardown.
  - history 는 done/failed/teardown 로 남는 row 들에 보존 (정리는 이후 retention phase).
- **버려진 대안**:
  - **기존 active row 를 그대로 두고 새 row 만 INSERT** — 같은 PR 에 active container 2 개 → 자원 낭비, traefik 라우팅 충돌(`pr-{n}.preview.<base>` 가 어느 sha 를 가리킬지 모호). 폐기.
  - **synchronize 시 같은 row 의 sha 만 갱신 (기존 동작 유지)** — 본 Phase 의 목적과 모순.
- **되돌릴 때 비용**: webhook handler 의 분기 제거. 사용자 데이터 영향 없음.

### 결정 5 — Agent 의 sha 보고 경로: STATUS_UPDATE 를 두 번 송신 (즉시 building + Checkout 후 sha 동봉 building)

- **결정**: Agent `Runner.Handle` 의 building STATUS_UPDATE 송신 흐름을 다음과 같이 한다.
  1. **첫 번째 building (즉시)** — JOB_ASSIGN 수신 직후, Checkout 시작 전에 sha 없이 `STATUS_UPDATE{status:"building"}` 송신. status 를 `assigned → building` 으로 즉시 전이시킨다.
  2. **Checkout 수행** — `cache.Checkout` 호출(수십 초 소요 가능).
  3. **두 번째 building (sha 포함)** — Checkout 직후 worktree 의 `git rev-parse HEAD` 실행, 실제 SHA 를 `CommitSHA *string` 필드에 담아 두 번째 `STATUS_UPDATE{status:"building", CommitSHA:&sha}` 송신. `building → building` 자기 전이 + sha 만 채움.
- **근거**:
  - **reconciler race 회피가 핵심**: 만약 building 송신을 Checkout 직후로만 미루면, status 가 수십 초 동안 `assigned` 로 남는다. Hub 의 reconciler `staleAssigned` 회수 로직(`internal/hub/reconciler.go`)은 N 초 이상 assigned 인 row 를 회수 대상으로 삼으므로, 정상 동작 중인 build 가 회수당하는 race 가 발생한다(이전 단일-송신 안에서는 `building` 이 즉시 보내지므로 이 race 가 없었음 — 회귀가 된다). 두 번 보내는 비용보다 race 회피가 우월.
  - **sha 가시성**: 두 번째 메시지가 `/admin/previews` 의 sha 컬럼을 즉시 채워, 빌드 진행 중에도 운영자가 어떤 SHA 가 build 중인지 확인 가능.
  - **idempotent**: status 전이 단일 진입점(`UpdateStatus`)이 `from→to` 가 같은 자기-루프 전이를 허용해야 함(`""→building` 또는 `building→building`). 현재 코드는 `from=""`(any) 시 단순 SET 이므로 호환. message 는 두 번째 송신 시 비어두어 첫 번째 message 를 덮어쓰지 않게 한다(`UpdateStatus` 의 message 인자 빈 문자열은 store 단에서 무변경 처리 — 이는 §5-3 의 SQL 단계가 아니라 store 레이어 가드로 강제, NF-3 참조).
- **버려진 대안**:
  - **building 송신을 Checkout 직후로만 옮김 (단일 송신)** — Checkout 동안 status='assigned' 로 남아 reconciler staleAssigned 회수 대상이 된다(R-8). 회귀 위험으로 폐기.
  - **별도 신규 메시지 타입(`COMMIT_RESOLVED` 등)** — wire 복잡도 증가, 호환성 부담.
  - **JOB_ASSIGN 을 양방향(ack)으로 확장** — WS reply 패턴 도입 비용.
  - **Agent → Hub HTTP 호출** — 채널 분리 복잡.
- **되돌릴 때 비용**: 두 번째 building 송신만 제거하면 됨. nullable `CommitSHA` 는 호환성 영향 0.

### 결정 6 — `commit_sha` 컬럼은 한 번 채워지면 동일 SHA 로만 갱신 허용 (재할당 금지)

- **결정**: `UpdateStatus` 가 `PreviewFields.CommitSha != nil` 일 때, 현재 row 의 commit_sha 가 NULL 이면 신규 sha 로 채움. 이미 non-NULL 이고 새 sha 와 다르면 `ErrShaConflict` (신규 에러) 반환 + WARN 로그.
- **근거**:
  - 자연키가 commit_sha 인 이상, sha 변경은 row 의 정체성을 바꾸는 일. 새 sha 면 새 row 여야 한다.
  - Agent 가 잘못된 sha 를 두 번 보고하는 race 도 방어.
- **버려진 대안**:
  - **무조건 덮어쓰기** — 자연키 위반 발생 가능 (다른 row 의 sha 와 충돌하면 SQL UNIQUE 위반).
  - **무조건 무시** — 잘못된 첫 sha 가 영구 박힘.
- **되돌릴 때 비용**: `ErrShaConflict` 반환 분기 제거. 호출자 영향 적음.

### 결정 7 — `is_adhoc` 의 의미는 "사용자가 hub UI 또는 cmd 로 직접 만든 preview"

- **결정**: 진입점별 IsAdhoc 값:
  - `webhook_handler.handleUpsert` → false (상시)
  - `admin_ui.testBuildSubmit` → true
  - 미래의 hub CLI `cmd/hub previews create` (있다면) → true
- **근거**: webhook 자동화 vs 수동의 구별이 운영자가 즉시 알아야 하는 핵심 정보(예: 라벨 자동 매칭이 안 되는 이유, PR 닫혀도 자동 teardown 안 되는 이유 등).
- **버려진 대안**:
  - **`source` enum** (결정 2 참조) — overkill.
- **되돌릴 때 비용**: 컬럼 DROP. Admin UI 의 badge 제거.

### 결정 8 — (DELETED — Out of Scope 로 이동)

- **상태**: 본 결정은 Phase 9 범위에서 제거되어 §2-2 Out of Scope 로 이동.
- **이유**: GitHub webhook 의 `pull_request` payload 는 항상 `pull_request.number >= 1` 이며 `PrNumber=0` 인 close payload 가 정상 경로로 도달할 수 없다. 즉 `handleClose` 에서 `IsAdhoc=true` 가드를 추가해도 사실상 dead code 가 된다(Adhoc 은 PrNumber=0 으로만 만들어지므로 webhook close 매칭 자체가 불가능). 미래에 "IsAdhoc + 임의 PrNumber" 케이스가 실제로 도입될 때 함께 설계하는 편이 정직.
- **대신 본 Phase 의 입장**: Adhoc 은 webhook close 와 무관하게 "수동 teardown 만 유효". 룰을 코드에 박지 않고 자연스러운 PrNumber=0 컨벤션으로 자동 회피된다. 이로 인해 F-13(이전 체크리스트의 IsAdhoc close 가드 검증)도 §6 에서 제거 → 새 F-13 은 "decision matrix 4 케이스 검증" 으로 재배정.

### 결정 9 — `CmdRunner.Output` 메서드 신설

- **결정**: `internal/agent/cmd_runner.go` 의 `CmdRunner` 인터페이스에 `Output(ctx context.Context, name string, args ...string) (string, error)` 메서드를 추가한다. 실제 구현체는 `exec.CommandContext(ctx, name, args...).Output()` 의 결과(`stdout`)를 문자열로 반환. fake 구현체는 호출 패턴을 기록하고 미리 설정된 응답을 반환한다.
- **근거**:
  - 결정 5 의 `git rev-parse HEAD` 는 stdout(SHA 문자열)을 회수해야 하는데, 기존 `CmdRunner.Run(ctx, ...)` 는 stdout 을 그냥 흘려버려 회수 불가. 새 메서드 1 개 추가가 가장 작은 비용.
  - `os/exec.Cmd.Output()` 의 시맨틱(기본적으로 stderr 는 ExitError 에 캡쳐됨)을 그대로 노출. 호출자는 `*exec.ExitError` 패턴 매칭으로 실패 시 stderr 확인 가능.
- **버려진 대안**:
  - **`Run` 시그니처를 `(stdout, stderr, error)` 반환으로 변경** — 기존 호출자 모두 깨짐. 비호환.
  - **별도 함수 `ExecOutput` 을 인터페이스 밖에 정의 (구체 의존)** — 테스트가 어려워짐(fakeCmd 로 대체 불가).
  - **Agent 내부에서 `os/exec` 직접 호출** — DI 깨지고 단위 테스트 불가.
- **되돌릴 때 비용**: `Output` 메서드 + 호출 1 곳(`runner.go` 의 sha resolve) 제거. fake 구현체에서도 메서드 제거. 의존 0.

---

## 4. 아키텍처 / 구조

### 4-1. 데이터 흐름 (synchronize 케이스)

```
GitHub                Hub.webhook_handler           PreviewStore         Agent
  │                          │                            │                │
  ├─ POST pull_request ─────▶│ HMAC verify, parse        │                │
  │   action=synchronize     │                            │                │
  │   sha=def456             │                            │                │
  │                          │                            │                │
  │                          ├─ GetActiveByRepoAndPR(...)▶│                │
  │                          │◀─ row{sha=abc123, status=building, id=p1}  │
  │                          │                            │                │
  │                          ├─ UpdateStatus(p1, *→teardown, "superseded")▶│
  │                          │                            ├─ event INSERT │
  │                          │                            ├─ UPDATE row   │
  │                          │◀────── ok ─────────────────│                │
  │                          ├─ JOB_TEARDOWN(p1) ──────────────────────────▶│
  │                          │                            │                │
  │                          ├─ Upsert(new row p2, sha=def456, queued) ──▶│
  │                          │                            ├─ event INSERT │
  │                          │                            ├─ INSERT row   │
  │                          │◀────── created=true ───────│                │
  │◀─ 202 {preview_id:p2,    │                            │                │
  │    status:queued}        │                            │                │
```

### 4-2. 데이터 흐름 (sha resolve 케이스 — Adhoc Test Build)

```
Operator             Hub.admin_ui              PreviewStore       Agent
   │                       │                         │              │
   ├─ POST test-build ────▶│                         │              │
   │   sha=""              │                         │              │
   │                       │                         │              │
   │                       ├─ Upsert(IsAdhoc=true,  │              │
   │                       │   CommitSha="" → NULL)▶│              │
   │                       │                         ├─ INSERT row  │
   │                       │◀─ created=true (id=p3)  │              │
   │                       │                         │              │
   │                       ├─ triggerDispatch ──────▶│ (Claim →     │
   │                       │                         │  JOB_ASSIGN) │
   │                       │                         │              │
   │                       │                         │ JOB_ASSIGN──▶│ (a) building#1
   │                       │                         │ (sha="")     │     sha=nil
   │                       │◀── STATUS_UPDATE building, sha=nil ─────┤  ← 즉시 송신
   │                       │   (assigned→building 즉시 전이; reconciler race 회피)
   │                       │                         │              │ (b) Checkout
   │                       │                         │              │     (수십 초)
   │                       │                         │              │ (c) git
   │                       │                         │              │     rev-parse
   │                       │                         │              │     HEAD
   │                       │                         │              │ → "ff00aa"
   │                       │                         │              │
   │                       │◀── STATUS_UPDATE building#2, sha=ff00aa ─┤  ← 두 번째 송신
   │                       │   (building→building 자기-루프; sha 만 채움)
   │                       ├─ UpdateStatus(p3,       │              │
   │                       │   building→building,    │              │
   │                       │   fields.CommitSha=&ff00aa)▶           │
   │                       │                         ├─ tx BEGIN    │
   │                       │                         ├─ SELECT cur  │
   │                       │                         │  (sha NULL → │
   │                       │                         │   통과)      │
   │                       │                         ├─ UPDATE      │
   │                       │                         │  COALESCE(   │
   │                       │                         │   NULL,      │
   │                       │                         │   "ff00aa")  │
   │                       │                         ├─ event INSERT│
   │                       │                         ├─ tx COMMIT   │
   │                       │◀── ok ──────────────────│              │
```

### 4-3. 디렉토리 변경

```
db/
  queries/previews.sql                    [수정]
  schema/0006_sha_keyed_previews.sql      [신규]
internal/db/sqlite/
  migrations/
    0006_sha_keyed_previews.up.sql        [신규]
    0006_sha_keyed_previews.down.sql      [신규]
  previews.sql.go                         [재생성]
  preview_store.go                        [수정]
  preview_store_test.go                   [수정 + 테스트 추가]
internal/store/
  store.go                                [수정 — Preview/PreviewFields/Interface]
internal/protocol/
  messages.go                             [수정 — StatusUpdateData.CommitSHA 추가]
internal/hub/
  webhook_handler.go                      [수정 — synchronize 분기 + IsAdhoc=false]
  admin_ui.go                             [수정 — IsAdhoc=true + reopen 로직 제거]
  status_update.go                        [수정 — sha 보고 처리]
  views/previews.gohtml                   [수정 — Adhoc badge]
  views/preview_detail.gohtml             [수정 — Adhoc 라벨]
  webhook_handler_test.go                 [수정 + 테스트 추가]
  admin_ui_test.go                        [수정 + 테스트 추가]
internal/agent/
  runner.go                               [수정 — rev-parse HEAD + sha 보고]
  runner_test.go                          [수정 + 테스트 추가]
```

### 4-4. 모듈 의존 관계

기존 단방향 의존(cmd → hub/agent → store → db)을 그대로 유지. 신규 의존 0.

---

## 5. 인터페이스 계약

### 5-1. Schema (Phase 9 후)

```sql
CREATE TABLE previews_new (
    id                 TEXT PRIMARY KEY NOT NULL,
    repo_full_name     TEXT NOT NULL,
    pr_number          INTEGER NOT NULL,
    commit_sha         TEXT,                      -- NULLABLE (was NOT NULL DEFAULT '')
    branch             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'queued',
    assigned_agent_id  TEXT,
    container_id       TEXT,
    agent_host         TEXT,
    agent_port         INTEGER,
    labels             TEXT NOT NULL DEFAULT '{}',
    error_message      TEXT,
    repo_clone_url     TEXT NOT NULL DEFAULT '',
    preview_urls       TEXT NOT NULL DEFAULT '',
    is_adhoc           INTEGER NOT NULL DEFAULT 0,  -- 신규
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE (repo_full_name, commit_sha),            -- 변경
    FOREIGN KEY (assigned_agent_id) REFERENCES agents(id) ON DELETE SET NULL
);
CREATE INDEX idx_previews_status        ON previews_new(status);
CREATE INDEX idx_previews_repo_pr       ON previews_new(repo_full_name, pr_number);
CREATE INDEX idx_previews_assigned      ON previews_new(assigned_agent_id, status);
-- 주의: idx_previews_repo_sha 는 명시 생성하지 않는다. UNIQUE(repo_full_name, commit_sha)
-- 제약이 자동으로 동일 컬럼 순서의 인덱스(sqlite_autoindex_previews_*)를 만들어주므로
-- 중복 생성은 디스크 낭비 + 쓰기 비용 2배가 된다. NF-5 의 O(log n) 보장은 자동 인덱스로 충족.
```

**FK / preview_events 보존 보장**: 본 마이그레이션은 §5-5 에서 `DROP TABLE previews; ALTER TABLE previews_new RENAME TO previews;` 패턴을 쓴다. `preview_events.preview_id` 가 `previews(id)` 에 FK CASCADE 로 묶여있다면 DROP 시점에 events 도 함께 삭제될 수 있다. 따라서 `PRAGMA foreign_keys = OFF;` 를 마이그레이션 전체 구간에 걸어 CASCADE 를 무력화하고, 신규 테이블 RENAME 후 `PRAGMA foreign_keys = ON;` 으로 복귀시킨다. 이 무력화 동안 `preview_events.preview_id` 의 값은 보존되고, RENAME 후 새 `previews(id)` 가 동일 ID 공간을 그대로 점유하므로 FK 정합성도 유지된다(검증: F-17). 이 약속은 §5-5 SQL 의 인라인 주석으로도 명문화한다.

### 5-2. Store 인터페이스 변경

```go
type Preview struct {
    ID              string
    RepoFullName    string
    PrNumber        int
    CommitSha       string      // 빈 문자열 = NULL (마이그레이션 후 nullable)
    Branch          string
    Status          string
    AssignedAgentID *string
    ContainerID     *string
    AgentHost       *string
    AgentPort       *int
    RepoCloneURL    string
    PreviewURLs     string
    Labels          []string
    ErrorMessage    *string
    IsAdhoc         bool        // 신규 (Phase 9)
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type PreviewFields struct {
    ContainerID     *string
    AgentHost       *string
    AgentPort       *int
    PreviewURLs     *string
    ErrorMessage    *string
    AssignedAgentID *string
    CommitSha       *string  // 신규 (Phase 9): nil=무변경, &"abc"=NULL→"abc" 채움 시도
}

type PreviewStore interface {
    // 기존 메서드 시그니처 유지. Upsert 의 의미만 변경(자연키 (repo, sha))
    Upsert(ctx context.Context, p Preview) (created bool, prev *Preview, err error)
    GetByID(ctx context.Context, id string) (*Preview, error)
    FindByHost(ctx context.Context, repoFullName string, prNumber int) (*Preview, error)
    ListQueuedForCandidates(ctx context.Context) ([]Preview, error)
    Claim(ctx context.Context, candidateIDs []string, agentID string, now time.Time) (*Preview, error)
    UpdateStatus(ctx context.Context, id string, fromStatus, toStatus, message string, now time.Time, fields PreviewFields) error
    ListRunningByAgent(ctx context.Context, agentID string) ([]Preview, error)
    ListStaleAssigned(ctx context.Context, staleAfter time.Time) ([]Preview, error)
    ListByAgent(ctx context.Context, agentID string, statuses []string) ([]Preview, error)
    ListAll(ctx context.Context) ([]Preview, error)
    ListPreviewEvents(ctx context.Context, previewID string, limit, offset int) ([]PreviewEvent, error)

    // 신규 (Phase 9): synchronize 시 같은 (repo, pr) 의 active row 찾기
    // 상태 ∈ {queued, assigned, building, running} 중 가장 최근 1건 반환.
    // 없으면 ErrNotFound.
    GetActiveByRepoAndPR(ctx context.Context, repoFullName string, prNumber int) (*Preview, error)
}

// 신규 에러
var ErrShaConflict = errors.New("preview commit_sha already set to a different value")
```

**sqlc NullString ↔ string 변환 헬퍼** (`internal/db/sqlite/preview_store.go` 내부):

```go
// nullStringFromCommitSha: 도메인의 빈 문자열을 NULL 로, 비어있지 않으면 valid 로 매핑.
func nullStringFromCommitSha(s string) sql.NullString {
    if s == "" { return sql.NullString{Valid: false} }
    return sql.NullString{String: s, Valid: true}
}

// commitShaFromNullString: NULL 을 빈 문자열로, valid 면 그대로 반환.
func commitShaFromNullString(ns sql.NullString) string {
    if !ns.Valid { return "" }
    return ns.String
}
```

이 두 헬퍼는 `Upsert` / `previewRowToDomain` / `UpdateStatus` 의 fields.CommitSha 빌드 / `GetActiveByRepoAndPR` / `GetPreviewByRepoAndSha` 등 sqlc 가 `sql.NullString` 을 노출하는 모든 경계면에서 양방향으로 적용한다. 새 코드에서는 직접 NullString 리터럴을 쓰지 말고 반드시 헬퍼를 통하도록 한다(코드 리뷰 항목).

### 5-3. SQL 쿼리 변경 (`db/queries/previews.sql`)

```sql
-- name: UpsertPreview :one
INSERT INTO previews (
  id, repo_full_name, pr_number, commit_sha, branch,
  status, labels, repo_clone_url, is_adhoc, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?)
ON CONFLICT(repo_full_name, commit_sha) DO UPDATE SET
  pr_number      = EXCLUDED.pr_number,
  branch         = EXCLUDED.branch,
  labels         = EXCLUDED.labels,
  repo_clone_url = EXCLUDED.repo_clone_url,
  updated_at     = EXCLUDED.updated_at
  -- is_adhoc 은 conflict 에서 갱신 안 함 (첫 INSERT 의 진실 보존)
  -- status 도 conflict 에서 갱신 안 함 (단일 진입점 원칙 — UpdateStatus/Claim 만 변경)
RETURNING *;

-- name: UpdatePreviewStatusFields :execrows
UPDATE previews
SET status = ?,
    updated_at = ?,
    container_id      = COALESCE(?, container_id),
    agent_host        = COALESCE(?, agent_host),
    agent_port        = COALESCE(?, agent_port),
    preview_urls      = COALESCE(?, preview_urls),
    error_message     = COALESCE(?, error_message),
    assigned_agent_id = COALESCE(?, assigned_agent_id),
    commit_sha        = COALESCE(commit_sha, ?)        -- 신규: NULL 일 때만 채움
WHERE id = ?;

-- name: GetActivePreviewByRepoAndPR :one
SELECT * FROM previews
WHERE repo_full_name = ? AND pr_number = ?
  AND status IN ('queued','assigned','building','running')
ORDER BY created_at DESC
LIMIT 1;

-- name: GetPreviewByRepoAndSha :one
SELECT * FROM previews
WHERE repo_full_name = ? AND commit_sha = ?;
```

#### Adhoc(NULL sha) Upsert 분기 — `if !created` 가 도달 가능한 조건

ON CONFLICT 절은 UNIQUE(repo_full_name, commit_sha) 가 발동해야 작동한다. 그런데 SQL 표준상 NULL UNIQUE 는 발동하지 않는다(여러 NULL 공존). 따라서:

- **Adhoc Test Build with `commit_sha=NULL`** — 매 호출마다 신규 INSERT. ON CONFLICT 절 자체가 절대 발동 안 함. `created` 는 항상 `true`.
- **Adhoc Test Build with `commit_sha="abc"` (사용자가 sha 명시)** — 같은 (repo, "abc") 가 이미 존재하면 ON CONFLICT 발동. 즉 `if !created` 분기는 **sha 가 명시된 두 번째 Test Build (또는 같은 sha 의 webhook 과 충돌)** 에서만 도달한다.
- **Webhook with `commit_sha="abc"`** — 같은 (repo, "abc") 존재 시 ON CONFLICT 발동. 두 번째 webhook 호출 동일 payload(idempotent) 또는 같은 commit 이 다른 PR/branch 로도 푸시된 케이스.
- **Webhook with `commit_sha=""` (payload 비어있음)** — Adhoc NULL 과 동일. 매 호출 신규 INSERT.

**`if !created` 분기에서의 reopen 정책**: 기존 row 의 `status ∈ {done, failed, teardown}` 이면 reopen 의도(같은 sha 재시도 트리거)이므로 `UpdateStatus(*→queued, message="reopened_by_<entry>")` 호출. `status ∈ {queued, assigned, building, running}` 이면 이미 active 이므로 추가 액션 없이 기존 row 의 ID 만 사용(idempotent). 결정: **"같은 sha 로 두 번 Test Build → 첫 번째가 done 이면 두 번째는 reopen, 첫 번째가 still active 면 두 번째는 noop"**. 의사코드는 §5-7 참조.

#### ErrShaConflict 검사 — 트랜잭션 내 사전 SELECT 강제

결정 6 의 ErrShaConflict 검사는 store 레이어에서 수행한다. **반드시 같은 BeginTx 안에서 실행**:

```go
// internal/db/sqlite/preview_store.go: UpdateStatus 의사 흐름
tx, err := s.db.BeginTx(ctx, nil)
defer tx.Rollback()

q := s.queries.WithTx(tx)
if fields.CommitSha != nil {
    cur, err := q.GetPreviewByID(ctx, id)              // 같은 tx 안 SELECT
    if err != nil { return err }
    if cur.CommitSha.Valid && cur.CommitSha.String != *fields.CommitSha {
        return store.ErrShaConflict                    // 다른 값 → 거부
    }
    // cur.CommitSha.Valid==false 이거나 같은 값이면 통과 → COALESCE 가 NULL 채움 또는 same-set
}

_, err = q.UpdatePreviewStatusFields(ctx, params)      // 같은 tx
_, err = q.InsertPreviewEvent(ctx, eventParams)        // 같은 tx
return tx.Commit()
```

트랜잭션 외부 SELECT 면 SELECT~UPDATE 사이에 다른 STATUS_UPDATE 가 sha 를 채워 race 가 발생한다. 본 흐름은 NF-3 (트랜잭션 무결성)의 일부로 강제.

### 5-4. STATUS_UPDATE 메시지

```go
type StatusUpdateData struct {
    PreviewID    string            `json:"preview_id"`
    Status       string            `json:"status"`
    Message      string            `json:"message,omitempty"`
    ContainerID  *string           `json:"container_id,omitempty"`
    AgentHost    *string           `json:"agent_host,omitempty"`
    AgentPort    *int              `json:"agent_port,omitempty"`
    ErrorMessage *string           `json:"error_message,omitempty"`
    PreviewURLs  map[string]string `json:"preview_urls,omitempty"`
    CommitSHA    *string           `json:"commit_sha,omitempty"` // 신규 Phase 9
}
```

### 5-5. 마이그레이션 SQL (SQLite 테이블 재생성 패턴)

SQLite 는 `DROP CONSTRAINT` / `ALTER COLUMN` 미지원이므로 **테이블 재생성**이 표준.

```sql
-- 0006_sha_keyed_previews.up.sql
--
-- 핵심 보장:
--   1) preview_events 테이블 데이터 전량 보존
--      - preview_events.preview_id 는 previews(id) 에 FK CASCADE 일 수 있음.
--      - 따라서 PRAGMA foreign_keys = OFF 로 마이그레이션 구간 동안 CASCADE 무력화.
--      - DROP/RENAME 후에도 previews(id) 의 ID 공간이 그대로 유지되므로 events 의 FK 정합성도 유지.
--   2) commit_sha=='' → NULL 변환 (NULL UNIQUE 미발동 활용)
--   3) is_adhoc DEFAULT 0 (기존 row 는 모두 webhook-origin 가정)
--
PRAGMA foreign_keys = OFF;

CREATE TABLE previews_new (
    -- 위 §5-1 정의 그대로 (UNIQUE(repo_full_name, commit_sha) 포함)
);

INSERT INTO previews_new
  (id, repo_full_name, pr_number, commit_sha, branch, status,
   assigned_agent_id, container_id, agent_host, agent_port,
   labels, error_message, repo_clone_url, preview_urls,
   is_adhoc, created_at, updated_at)
SELECT
  id, repo_full_name, pr_number,
  CASE WHEN commit_sha = '' THEN NULL ELSE commit_sha END,  -- 빈 문자열 → NULL
  branch, status, assigned_agent_id, container_id, agent_host, agent_port,
  labels, error_message, repo_clone_url, preview_urls,
  0,                                                          -- is_adhoc default
  created_at, updated_at
FROM previews;

-- DROP/RENAME — preview_events 는 FK OFF 동안 보존됨.
-- RENAME 후 events 의 preview_id 가 가리키는 ID 들은 동일 ID 공간에서 발견 가능.
DROP TABLE previews;
ALTER TABLE previews_new RENAME TO previews;

CREATE INDEX idx_previews_status   ON previews(status);
CREATE INDEX idx_previews_repo_pr  ON previews(repo_full_name, pr_number);
CREATE INDEX idx_previews_assigned ON previews(assigned_agent_id, status);
-- idx_previews_repo_sha 는 UNIQUE 제약이 자동 인덱스를 생성하므로 명시 생성 안 함

PRAGMA foreign_keys = ON;

-- 검증(선택, 마이그레이션 후 운영자 sanity-check):
--   SELECT COUNT(*) FROM preview_events;                              -- 변환 전과 동일해야 함
--   SELECT COUNT(*) FROM preview_events e
--     LEFT JOIN previews p ON p.id = e.preview_id
--     WHERE p.id IS NULL;                                             -- 0 이어야 함 (정합)
```

```sql
-- 0006_sha_keyed_previews.down.sql
PRAGMA foreign_keys = OFF;

-- (repo, pr) 그룹에서 가장 최근 row 만 보존 (UNIQUE 충돌 방지, 결정 1)
DELETE FROM previews
WHERE id NOT IN (
  SELECT id FROM (
    SELECT id, ROW_NUMBER() OVER (
      PARTITION BY repo_full_name, pr_number ORDER BY created_at DESC
    ) AS rn
    FROM previews
  )
  WHERE rn = 1
);

CREATE TABLE previews_old (
    id                 TEXT PRIMARY KEY NOT NULL,
    repo_full_name     TEXT NOT NULL,
    pr_number          INTEGER NOT NULL,
    commit_sha         TEXT NOT NULL DEFAULT '',
    branch             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'queued',
    assigned_agent_id  TEXT,
    container_id       TEXT,
    agent_host         TEXT,
    agent_port         INTEGER,
    labels             TEXT NOT NULL DEFAULT '{}',
    error_message      TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    repo_clone_url     TEXT NOT NULL DEFAULT '',
    preview_urls       TEXT NOT NULL DEFAULT '',
    UNIQUE (repo_full_name, pr_number),
    FOREIGN KEY (assigned_agent_id) REFERENCES agents(id) ON DELETE SET NULL
);

INSERT INTO previews_old
SELECT id, repo_full_name, pr_number,
  COALESCE(commit_sha, ''), branch, status,
  assigned_agent_id, container_id, agent_host, agent_port,
  labels, error_message, created_at, updated_at,
  repo_clone_url, preview_urls
FROM previews;

DROP TABLE previews;
ALTER TABLE previews_old RENAME TO previews;

CREATE INDEX idx_previews_status   ON previews(status);
CREATE INDEX idx_previews_repo_pr  ON previews(repo_full_name, pr_number);
CREATE INDEX idx_previews_assigned ON previews(assigned_agent_id, status);

PRAGMA foreign_keys = ON;
```

### 5-6. Webhook handler 분기 (개정)

#### Synchronize 처리 — Decision Matrix (4 케이스)

`synchronize` 액션 도착 시, `(repo, pr)` 의 기존 active row 유무 × 새 sha 의 기존 sha 와 일치 여부에 따라 4 케이스가 존재한다. 본 Phase 가 채택하는 동작:

| # | 기존 active row | 새 sha vs 기존 sha | 동작 |
|---|---|---|---|
| A | 존재 | 같음 | **Upsert idempotent** — ON CONFLICT 절이 (repo, sha) 매치로 같은 row 의 branch/labels/repo_clone_url/updated_at 만 갱신. 추가 액션 없음. teardown 없음. 신규 INSERT 없음. |
| B | 존재 | 다름 | **기존 row teardown + 신규 INSERT** — `UpdateStatus(existing.ID, *→teardown, "superseded_by_sha=<new>")` + JOB_TEARDOWN 송신. 그 후 신규 row INSERT (status=queued, message="created_after_supersede_of=<old_preview_id>"). |
| C | 없음 (done/failed 만) | 같은 sha 의 done/failed row 존재 | **Upsert ON CONFLICT 발동 → reopen** — `if !created && prev.Status in {done,failed}` 분기에서 `UpdateStatus(prev.ID, *→queued, "reopened_by_synchronize")` 호출. 결과적으로 같은 row 가 재활용되며 신규 row 는 만들어지지 않음. (운영 시나리오: 빌드 실패 후 같은 commit 재푸시 — 흔치 않지만 가능) |
| D | 없음 | 새 sha (DB 에 처음 보는 sha) | **신규 INSERT** — Upsert created=true. teardown 없음(active 없음). |

→ 이 matrix 는 §6 의 F-12 / 신규 F-12a / F-12b / F-12c 로 검증한다.

#### 의사코드

```go
func (h *WebhookHandler) handleUpsert(ctx context.Context, p *github.PullRequestPayload, now time.Time) {
    repo   := p.Repository.FullName
    prNum  := int(p.PullRequest.Number)
    newSha := p.PullRequest.Head.SHA              // 명시 — 지적 16
    branch := p.PullRequest.Head.Ref
    cloneURL := p.Repository.CloneURL
    labels := extractLabels(p.PullRequest.Labels)

    // === Case B: synchronize + 기존 active 다른 sha → teardown ===
    var supersededID string
    if p.Action == "synchronize" {
        existing, err := h.Store.GetActiveByRepoAndPR(ctx, repo, prNum)
        if err == nil && existing != nil && existing.CommitSha != newSha {
            _ = h.Store.UpdateStatus(ctx, existing.ID, "", "teardown",
                "superseded_by_sha="+newSha, now, store.PreviewFields{})
            if h.TeardownSender != nil && existing.AssignedAgentID != nil {
                _ = h.TeardownSender.SendTeardown(ctx, *existing.AssignedAgentID, existing.ID)
            }
            supersededID = existing.ID
        }
        // 기존 active 같은 sha (Case A) → fall through, Upsert 가 idempotent 처리
        // 기존 active 없음 (Case C/D) → fall through
    }

    // === Upsert ===
    preview := store.Preview{
        ID:           uuid.NewString(),
        RepoFullName: repo,
        PrNumber:     prNum,
        CommitSha:    newSha,                     // 빈 문자열 가능 → store 가 NULL 매핑
        Branch:       branch,
        Labels:       labels,
        RepoCloneURL: cloneURL,
        IsAdhoc:      false,
        CreatedAt:    now,
        UpdatedAt:    now,
    }
    created, prev, err := h.Store.Upsert(ctx, preview)
    if err != nil { /* http 5xx, return */ }

    finalID := preview.ID
    if !created {
        // Case A 또는 Case C 도달 — prev 가 기존 row
        finalID = prev.ID
        // Case C: 기존이 done/failed → reopen
        if prev.Status == "done" || prev.Status == "failed" {
            _ = h.Store.UpdateStatus(ctx, prev.ID, "", "queued",
                "reopened_by_"+p.Action, now, store.PreviewFields{})
        }
        // 기존이 active (queued/assigned/building/running) → 추가 액션 없음 (Case A)
    } else if supersededID != "" {
        // Case B 의 신규 INSERT 직후 — 새 row 의 첫 event message 에 supersede 정보 남김
        _ = h.Store.UpdateStatus(ctx, finalID, "queued", "queued",
            "created_after_supersede_of="+supersededID, now, store.PreviewFields{})
        // 자기-루프 전이지만 message 만 기록 — preview_events 에 추적 가능 행 추가
    }

    // dispatcher 트리거 등 후속 흐름은 기존과 동일
}
```

> **`opened` / `reopened` 흐름**: synchronize 의 Case B 분기(active teardown)는 적용하지 않는다. `opened` 는 통상 같은 PR 의 기존 active 가 존재할 수 없으므로 Case D 가 일반적이고, 만약 같은 sha 가 이미 있으면 Case A. `reopened` 는 같은 (repo, sha) 의 done/failed row 가 일반적이므로 Case C (Upsert reopen). 두 케이스 모두 위 Upsert 로직만으로 자연스럽게 처리됨.

> **신규 row 의 supersede 추적 message** (지적 13 결정): Case B 의 신규 INSERT 이후 즉시 `queued→queued` 자기-루프 `UpdateStatus` 호출로 preview_events 에 `"created_after_supersede_of=<old_id>"` 메시지를 남긴다. Upsert 자체는 status_message 인자를 받지 않는 INSERT 이므로, 이 회차의 self-loop UpdateStatus 가 기록 책임을 진다. 비용은 추가 SQL 1 회 (INSERT 1 + UPDATE 1) — 운영 timeline 의 가독성 가치를 우선.

### 5-7. Admin UI testBuildSubmit (개정)

```go
func (h *AdminUIHandler) testBuildSubmit(w http.ResponseWriter, r *http.Request) {
    // ... 입력 파싱 (repoFullName, branch, commitSha, repoCloneURL) ...
    now := h.Clock.Now()

    p := store.Preview{
        ID:           uuid.NewString(),
        RepoFullName: repoFullName,
        PrNumber:     0,
        CommitSha:    commitSha,         // 빈 문자열이면 store 가 NULL 매핑
        Branch:       branch,
        RepoCloneURL: repoCloneURL,
        Labels:       []string{},
        IsAdhoc:      true,              // 신규 (Phase 9)
        CreatedAt:    now,
        UpdatedAt:    now,
    }
    created, prev, err := h.PreviewStore.Upsert(r.Context(), p)
    if err != nil {
        h.renderError(w, http.StatusInternalServerError, err)
        return
    }

    // `if !created` 도달 조건 — §5-3 Adhoc 분기 설명 참조:
    //   commit_sha = NULL  → ON CONFLICT 절 미발동, 항상 created=true → 이 분기 도달 불가
    //   commit_sha = "abc" 명시 + 같은 (repo, "abc") 가 이미 존재 → ON CONFLICT 발동 → 이 분기 도달
    previewID := p.ID
    if !created {
        previewID = prev.ID
        // 정책: 기존 row 의 status 에 따라 분기
        //   - done / failed                → 같은 sha 재시도 → reopen
        //   - queued / assigned / building / running → 이미 active → noop (의도: "같은 sha 로 두 번
        //     Test Build 했을 때 첫 번째가 done 이면 두 번째는 reopen, 첫 번째가 still active 면
        //     두 번째는 그냥 첫 번째에 합류")
        if prev.Status == "done" || prev.Status == "failed" {
            _ = h.PreviewStore.UpdateStatus(r.Context(), prev.ID, "", "queued",
                "reopened_by_test_build", now, store.PreviewFields{})
        }
        // active 면 추가 액션 없음 — 호출자에게 기존 ID 반환 (운영자가 동일 빌드 link 보게 됨)
    }

    h.triggerDispatch(r.Context())
    h.redirectToPreviewDetail(w, r, previewID)
}
```

이 의사코드는 §5-3 의 Adhoc UpsertPreview SQL 분기 결정과 1:1 대응한다. 컴파일 가능한 수준의 구체화로 plan-reviewer 지적 8 해소.

### 5-8. Status update 핸들러 sha 보고

`internal/hub/status_update.go` (또는 STATUS_UPDATE 라우팅 위치) 에 다음 분기 추가:

```go
// previewUrlsJSON: map[string]string → *string (JSON 직렬화) 헬퍼
//   - 이미 Phase 6 에서 status_update.go 또는 인접 파일에 도입된 헬퍼를 재사용한다.
//     없다면 본 Phase 에서 같은 파일에 신설(아래 시그니처).
//   - 시그니처: func previewUrlsJSON(m map[string]string) *string
//     m == nil 또는 len(m)==0 → nil 반환 (PreviewFields 의 의미: 무변경)
//     그 외 → JSON 직렬화한 문자열의 포인터 반환
//
fields := store.PreviewFields{
    ContainerID:  msg.ContainerID,
    AgentHost:    msg.AgentHost,
    AgentPort:    msg.AgentPort,
    PreviewURLs:  previewUrlsJSON(msg.PreviewURLs),
    ErrorMessage: msg.ErrorMessage,
    CommitSha:    msg.CommitSHA,   // 신규 (이미 *string nullable 포인터)
}
err := store.UpdateStatus(ctx, msg.PreviewID, "", msg.Status, msg.Message, now, fields)
if errors.Is(err, store.ErrShaConflict) {
    // nil-check 가드 — msg.CommitSHA 가 nil 인데 ErrShaConflict 가 도달하는 케이스는
    // 논리상 발생할 수 없지만 (store 가 fields.CommitSha != nil 일 때만 검사 진입),
    // 방어적 코드로 nil 역참조 panic 차단.
    if msg.CommitSHA != nil {
        logger.Warn("preview_sha_conflict",
            "preview_id", msg.PreviewID,
            "agent_sha", *msg.CommitSHA)
    } else {
        logger.Warn("preview_sha_conflict_nil",
            "preview_id", msg.PreviewID,
            "note", "ErrShaConflict returned with nil CommitSHA — store contract bug?")
    }
    // 응답 OK (status 자체는 적용 시도). row 의 sha 는 보존.
}
```

### 5-9. Agent runner.go sha resolve (결정 5 — 두 번 송신)

결정 5 의 채택대로, building STATUS_UPDATE 를 두 번 보낸다. 첫 번째는 즉시(reconciler staleAssigned race 회피), 두 번째는 Checkout 직후 sha 동봉.

```go
// === 1) 첫 번째 building (즉시) — assigned → building 으로 즉시 전이 ===
//   reconciler 의 staleAssigned 회수 대상에서 빠져야 하므로 Checkout 전에 보냄.
_ = r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
    PreviewID: pid,
    Status:    "building",
    Message:   "checkout_started",
    CommitSHA: nil,                 // 아직 모름
})

// === 2) Checkout (수십 초 가능) ===
worktree, err := r.cache.Checkout(ctx, msg.RepoURL, pid, msg.CommitSHA, msg.Branch)
if err != nil {
    // 실패 → failed STATUS_UPDATE 후 종료
    return
}

// === 3) HEAD sha resolve ===
resolvedSha := ""
if out, gerr := r.cmd.Output(ctx, "git", "-C", worktree, "rev-parse", "HEAD"); gerr == nil {
    resolvedSha = strings.TrimSpace(out)
} else {
    // NF-8: resolve 실패해도 빌드 중단 안 함. resolvedSha="" 유지 → hub row sha 는 NULL 유지.
    slog.Warn("rev_parse_head_failed", "preview_id", pid, "err", gerr)
}

// === 4) 두 번째 building — sha 동봉 ===
// 정책: 항상 보고(idempotent). msg.CommitSHA 가 이미 비어있지 않더라도 worktree 의 실제 HEAD 가 진실.
//   - 이유 1: agent 가 받은 sha 와 worktree HEAD 가 다를 수 있는 엣지 케이스(branch 가 push 사이
//     움직였을 때 등) 에서 worktree 가 빌드 진실에 해당.
//   - 이유 2: hub UpdateStatus 의 commit_sha COALESCE 는 NULL 일 때만 채우므로, 이미 채워져 있으면
//     같은 sha 라면 NF-3 의 ErrShaConflict 검사를 통과(같은 값 → 무해)하고, 다른 sha 면 정확한
//     충돌 시그널을 남긴다. "skip if non-empty" 최적화는 진단 정보를 손실시키므로 채택하지 않는다.
var shaPtr *string
if resolvedSha != "" {
    shaPtr = &resolvedSha
}
_ = r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
    PreviewID: pid,
    Status:    "building",        // 자기-루프 전이
    Message:   "",                // 빈 message → store 에서 무변경 처리 (첫 message 보존)
    CommitSHA: shaPtr,
})

// === 5) 이후 docker build / docker run / running STATUS_UPDATE 흐름은 기존과 동일 ===
```

**Send 정책 요약**:
- 첫 번째 building: 항상, sha=nil
- 두 번째 building: 항상(idempotent), `resolvedSha != ""` 면 sha 동봉. 빈 문자열이면 nil 동봉(=무변경).
- "이미 sha 가 있으면 skip" 최적화는 채택 안 함 — 진단 정보(불일치 검출) 가치가 우월.

### 5-10. Admin UI 표시

- `previews.gohtml` 의 각 row 에 `{{if .IsAdhoc}}<span class="badge">Adhoc</span>{{end}}`
- `preview_detail.gohtml` 에 `<dt>Source</dt><dd>{{if .Preview.IsAdhoc}}Adhoc (manual){{else}}Webhook{{end}}</dd>`
- JSON `PreviewView.IsAdhoc` 필드 직렬화 (snake_case `is_adhoc`)

### 5-11. 로그·이벤트 노출

- `webhook_handler.handleUpsert` 의 `slog.Info("preview_webhook_processed", ...)` 에 `is_adhoc=false` 키 추가
- `admin_ui.testBuildSubmit` 의 `slog.Info("test_build_triggered", ...)` 에 `is_adhoc=true` 키 추가
- `preview_events.message` 에는 별도로 안 적음(엔티티 태그가 row 자체에 있음)
- synchronize 으로 superseded 된 row 의 teardown event message 는 `"superseded_by_sha=<new>"` 로 명시 — 운영자가 timeline 에서 즉시 인지

---

## 6. 기능 요구사항 체크리스트

각 항목은 (`F-N`) ID 와 검증 방법을 포함한다.

- [ ] **F-1** `previews` 테이블의 UNIQUE 제약이 `(repo_full_name, commit_sha)` 로 변경됨.
  - 검증(이식성 우선, store 인터페이스 레벨): `preview_store_test.go` 에서 같은 (repo, sha) 두 번 Upsert → ListAll 길이 1, 다른 sha 두 번 Upsert → 길이 2. UNIQUE 제약이 (repo, sha) 임을 행동으로 증명. (참고: `sqlite3 hub.db ".schema previews"` 도 동작하지만 SQLite 전용이라 cross-DB 호환 검증이 아님 — 디버깅 용도로만 사용.)
- [ ] **F-2** `is_adhoc INTEGER NOT NULL DEFAULT 0` 컬럼 존재.
  - 검증: `preview_store_test.go` 에서 IsAdhoc 미설정으로 Upsert 후 `GetByID(...).IsAdhoc == false`, IsAdhoc=true 로 Upsert 후 `GetByID(...).IsAdhoc == true`. 컬럼 존재 + DEFAULT 0 동작을 행동으로 증명. (참고: 직접 schema 검사는 SQLite 전용이므로 보조 수단.)
- [ ] **F-3** `Preview.IsAdhoc bool` 필드가 `internal/store/store.go` 에 존재 + sqlite store 가 INTEGER 0/1 ↔ bool 매핑.
  - 검증: `go vet ./...` + 단위 테스트가 IsAdhoc=true 로 INSERT 후 `GetByID` 결과 IsAdhoc=true 확인.
- [ ] **F-4** `PreviewFields.CommitSha *string` 필드 존재 + `UpdateStatus` 가 nil 일 때 무변경, &"<sha>" 일 때 NULL → 채움 로직.
  - 검증: `preview_store_test.go` 에 (1) sha=NULL row 만들고 UpdateStatus(fields.CommitSha=&"abc")  → row.CommitSha=="abc" 인 케이스, (2) 이미 sha="abc" 인 row 에 fields.CommitSha=&"def"  → ErrShaConflict 반환 케이스.
- [ ] **F-5** `Upsert` 의 사전 SELECT 가 `(repo, sha)` 기준 + IsAdhoc 파라미터 전파.
  - 검증: 기존 `preview_store_test.go` 케이스에 IsAdhoc 검증 추가.
- [ ] **F-6** `GetActiveByRepoAndPR` 메서드가 status ∈ {queued, assigned, building, running} 만 매칭.
  - 검증: 단위 테스트: 같은 (repo, pr) 에 status=done row + status=building row → building row 반환. status=done 만 있으면 ErrNotFound.
- [ ] **F-7** 같은 (repo, pr) 에 다른 sha 두 번 webhook → previews row 2 개 생성.
  - 검증: webhook_handler_test 에 시나리오 추가. ListAll 로 길이 2 확인 + 두 row 의 sha 가 다름.
- [ ] **F-8** 같은 (repo, sha) 두 번 webhook → row 1 개 (idempotent).
  - 검증: 같은 시나리오로 ListAll 길이 1 확인.
- [ ] **F-9** sha=NULL 두 번 INSERT → row 2 개.
  - 검증: preview_store_test 에 두 번 Upsert(CommitSha="") 후 ListAll 길이 2.
- [ ] **F-10** Admin Test Build 두 번 같은 repo 다른 sha → row 2 개, 모두 IsAdhoc=true.
  - 검증: admin_ui_test 에 시나리오 추가. ListAll 두 row 모두 IsAdhoc=true.
- [ ] **F-11** Agent 가 빈 sha 로 JOB_ASSIGN 받고 worktree HEAD 추출 후 building STATUS_UPDATE 에 sha 동봉 → hub 가 row 의 commit_sha 갱신.
  - 검증: runner_test 에 fakeCmd 가 `git -C ... rev-parse HEAD` 호출에 "ff00aa" 응답하도록 설정. SendStatusUpdate 호출 인자의 CommitSHA == "ff00aa" assert.
  - 추가: status_update_test 또는 통합 테스트로 hub 가 fields.CommitSha 를 채워 UpdateStatus 호출하는지 확인.
- [ ] **F-12** Webhook synchronize Decision Matrix Case B — 기존 active row 존재 + 다른 sha → 기존 teardown(status=teardown, message="superseded_by_sha=...") + JOB_TEARDOWN 송신 + 신규 row queued INSERT + 신규 row preview_events 에 "created_after_supersede_of=<old_id>" message 1 건.
  - 검증: webhook_handler_test 에 fakePreviewStore + fakeTeardownSender 로 시나리오 구성. ListAll 길이 2, teardownSender.SendTeardown 호출 1 회, 두 row 의 status (`teardown`, `queued`), 신규 row 의 ListPreviewEvents 에 supersede message 존재.
- [ ] **F-12a** Decision Matrix Case A — 기존 active row 존재 + 같은 sha → Upsert idempotent (teardown 없음, 신규 INSERT 없음).
  - 검증: webhook_handler_test: 같은 sha 로 synchronize 두 번 → ListAll 길이 1, teardownSender.SendTeardown 호출 0 회, status 무변경.
- [ ] **F-12b** Decision Matrix Case C — active row 없음 + 같은 sha 의 done/failed row 존재 → reopen.
  - 검증: webhook_handler_test: 사전에 (repo, sha=X, status=failed) row 준비 → synchronize(sha=X) → ListAll 길이 1 (같은 row), status=queued, preview_events 에 "reopened_by_synchronize" message.
- [ ] **F-12c** Decision Matrix Case D — active 없음 + 새 sha → 신규 INSERT 만, teardown 호출 0 회.
  - 검증: webhook_handler_test: 빈 store → synchronize(sha=Y) → ListAll 길이 1, teardownSender.SendTeardown 호출 0 회.
- [ ] **F-13** (구 결정 8 검증 항목 삭제 — 결정 8 자체가 §2-2 Out of Scope 로 이동)
  - 대신 본 슬롯에 **신규 검증 항목**: "is_adhoc=true 의 webhook close payload 는 GitHub 가 보낼 수 없으므로 본 Phase 에서 별도 가드 코드를 추가하지 않으며, 기존 handleClose 동작이 회귀 없음" 을 명시.
  - 검증: webhook_handler_test 의 기존 handleClose 케이스 전부 변경 없이 통과. 또한 "PrNumber=0 인 close payload 는 정상 경로로 도달 불가" 를 README/주석에 메모(코드 검사가 아닌 설계 메모).
- [ ] **F-14** `PreviewView` JSON 응답에 `"is_adhoc": true|false` 필드 포함.
  - 검증: `curl -H 'Accept: application/json' /admin/previews` 응답 JSON 검사.
- [ ] **F-15** Admin UI HTML 의 previews 리스트에 IsAdhoc=true row 가 "Adhoc" badge 와 함께 렌더.
  - 검증: Playwright e2e: Adhoc test build 트리거 후 `/admin/previews` 페이지에서 badge 텍스트 존재.
- [ ] **F-16** Admin UI preview detail 페이지에 Source 행이 "Adhoc (manual)" 또는 "Webhook" 으로 표시.
  - 검증: Playwright e2e.
- [ ] **F-17** up 마이그레이션 0006 실행 시 **preview_events 데이터가 모두 보존되며 새 previews 와 FK 정합**. down 마이그레이션 실행 시 (repo, pr) 그룹에서 가장 최근 row 만 보존하고 기존 자연키 복원.
  - 검증 (up): 통합 테스트 또는 수동 — pre-up 시점에 `preview_events` row N 개 존재, up 후 `preview_events` 의 row 개수 == N (전량 보존), 그리고 `SELECT COUNT(*) FROM preview_events e LEFT JOIN previews p ON p.id = e.preview_id WHERE p.id IS NULL == 0` (FK orphan 없음).
  - 검증 (down): up → 데이터 INSERT → down → store 인터페이스 레벨로 같은 (repo, pr) 다른 sha 두 번 Upsert 시 두 번째가 UNIQUE 위반(또는 기존 동작대로 update) — 즉 (repo, pr) UNIQUE 가 복원되었음을 행동으로 증명. commit_sha NOT NULL 도 마찬가지로 빈 sha INSERT 시 store 가 ""(empty string) 으로 저장하는지 확인.
- [ ] **F-18** sqlc 생성 코드(`previews.sql.go`)가 새 쿼리/컬럼 반영. `make sqlc` 또는 `sqlc generate` 후 git diff 가 깨끗.
  - 검증: CI 에서 `sqlc generate && git diff --exit-code internal/db/sqlite/`.
- [ ] **F-19** 모든 기존 단위 테스트가 통과(회귀 0). 본 Phase 의 사이드 이펙트(IsAdhoc 필드, GetActiveByRepoAndPR 메서드, PreviewFields.CommitSha, CmdRunner.Output) 가 fake 구현체들에 모두 반영되어 컴파일·테스트 통과.
  - 검증: `go test ./...` 0 fail. 영향 받는 fake 구현 파일 목록은 §2-1 In Scope 의 "테스트 / fake 호환성" 항목 참조.
- [ ] **F-20** 기존 webhook 시나리오(opened, reopened, closed) 의 외부 관찰 동작 변경 없음(동일 (repo, sha) 재오픈은 reopen 로직 유효).
  - 검증: webhook_handler_test 의 기존 케이스가 별도 수정 없이 통과(필드 추가만 적용).
- [ ] **F-21** Agent 가 building STATUS_UPDATE 를 두 번 송신(즉시 sha=nil + Checkout 후 sha 동봉). reconciler 의 staleAssigned 회수 대상에서 빠짐.
  - 검증: runner_test 에서 fakeHub 가 `SendStatusUpdate` 호출 횟수 == 최소 2 회 (building 두 번), 첫 번째는 `CommitSHA == nil`, 두 번째는 `CommitSHA != nil` (resolve 성공 시). 또한 별도 reconciler_test 는 회귀 없음 확인 (회수 대상 0 건).
- [ ] **F-22** ErrShaConflict 검사가 `UpdateStatus` 트랜잭션 내부에서 수행되어, SELECT~UPDATE 사이의 race 가 없음.
  - 검증: preview_store_test 에서 (1) NULL sha row 에 fields.CommitSha=&"abc" → 통과, GetByID 결과 sha="abc". (2) sha="abc" row 에 fields.CommitSha=&"def" → ErrShaConflict + GetByID 결과 sha="abc" 보존. (3) 코드 리뷰: BeginTx ~ Commit 구간 안에 사전 SELECT(`GetPreviewByID`) 가 들어있는지 확인.

---

## 7. 비기능 요구사항 체크리스트

- [ ] **NF-1 (이식성)** 모든 새 SQL 이 SQLite + Postgres 양쪽 표준 문법.
  - 검증: 마이그레이션 SQL 에 SQLite 전용(`AUTOINCREMENT`, `WITHOUT ROWID`)이나 PG 전용(`SERIAL`, `JSONB`) 키워드 없음. ROW_NUMBER() OVER 는 SQLite 3.25+ / PG 표준 — OK.
  - 주의 1: up/down 의 `PRAGMA foreign_keys = OFF/ON` 은 SQLite 전용. PG 어댑터에서는 down 마이그레이션을 별도 작성해야 함(주석으로 명시).
  - 주의 2: **PG 의 `NULL` UNIQUE 동작 미세 차이** — PG 14 까지는 SQLite 와 동일하게 NULL 끼리 distinct (여러 NULL 공존 허용). PG 15+ 부터는 `UNIQUE NULLS NOT DISTINCT` 옵션이 추가되어 명시할 경우 NULL 끼리도 충돌로 본다. **본 Phase 의 PG 어댑터 마이그레이션은 옵션 없는 평문 `UNIQUE (repo_full_name, commit_sha)` 를 쓰며 절대 `NULLS NOT DISTINCT` 를 붙이지 않는다** — 그래야 SQLite 와 동작이 일치(NULL 다중 row 허용). 이 약속을 PG 어댑터 작업 시 별도 ADR 또는 PG 마이그레이션 파일 상단 주석으로 명시.
  - 주의 3: COALESCE / ON CONFLICT DO UPDATE / EXCLUDED / ROW_NUMBER() OVER 는 양쪽 표준이므로 변경 없음.
- [ ] **NF-2 (단일 진입점)** preview.status 변경은 여전히 `UpdateStatus` / `Claim` 만이 수행. Upsert 는 status 미변경.
  - 검증: 코드 리뷰. `UpsertPreview` SQL 의 ON CONFLICT 절에 status 갱신 없음.
- [ ] **NF-3 (트랜잭션 무결성)** sha 갱신과 status 전이가 같은 트랜잭션 내 1회 INSERT preview_events 와 함께 수행.
  - 검증: `preview_store.go: UpdateStatus` 의 BeginTx 범위 안에서 모두 실행됨을 코드 리뷰.
- [ ] **NF-4 (로그)** 모든 신규 진입점에서 `is_adhoc`, `commit_sha`(resolved 결과 포함) 키가 slog 출력에 포함.
  - 검증: 단위 테스트에서 testHandler 로 슬로그 캡쳐 + 키 존재 assert.
- [ ] **NF-5 (성능)** `(repo_full_name, commit_sha)` 컬럼 순서의 인덱스가 존재해 `GetPreviewByRepoAndSha` O(log n).
  - 검증: SQLite 의 경우 UNIQUE 제약이 자동 인덱스 `sqlite_autoindex_previews_<n>` 를 만들어준다. `EXPLAIN QUERY PLAN SELECT ... WHERE repo_full_name=? AND commit_sha=?` 가 `SEARCH ... USING INDEX` 출력. 명시적 `CREATE INDEX idx_previews_repo_sha` 는 중복이므로 만들지 않음(§5-1 참조).
- [ ] **NF-6 (호환성)** 기존 STATUS_UPDATE 송신부(Agent) 는 CommitSHA 누락이 default — 즉시 빌드 가능한 후방 호환성.
  - 검증: protocol round-trip 테스트 + 기존 agent 송신부에서 CommitSHA 미설정 케이스 통과.
- [ ] **NF-7 (관측성)** Adhoc badge 가 운영자가 0.5 초 이내에 인지 가능하도록 별도 색상(예: pico secondary outline) 적용.
  - 검증: Playwright screenshot 비교 또는 수동.
- [ ] **NF-8 (안전성)** sha resolve 실패(`git rev-parse HEAD` 비-zero) 시 Agent 는 빌드를 중단하지 않고 기존 sha("") 그대로 진행, hub row 의 sha 는 NULL 유지.
  - 검증: runner_test 에 cmd.Output 이 에러 반환하는 케이스: SendStatusUpdate 의 CommitSHA == nil + 후속 빌드 단계 진행.
- [ ] **NF-9 (멱등성)** webhook 재전송(같은 payload) → 추가 row 0, status 변경 0.
  - 검증: webhook_handler_test 에 동일 payload 2 회 호출 후 ListAll 길이 1 + status 동일.

---

## 8. 리스크와 완화책

### R-1. 마이그레이션 중 레이스
- **리스크**: 0006 up 마이그레이션은 DROP TABLE → RENAME 을 포함. Hub 가 동시 가동 중이면 data loss 가능.
- **완화**: 마이그레이션은 기동 시점(`migrate.Up`)에만 실행. 운영자 가이드: 마이그레이션 동안 Hub 다운 권장.

### R-2. 기존 데이터의 commit_sha=='' 다중 row 가 새 UNIQUE 위반
- **리스크**: Phase 8 까지의 데이터에서 webhook 이 비어있는 sha 로 만든 row 들이 같은 repo 에 여러 개 존재할 수 있음(현재 자연키 (repo, pr) 가 다르면 OK 였음). up 마이그레이션의 SELECT 에서 sha='' → NULL 변환 후 INSERT 이므로, 같은 (repo, NULL) 페어가 여러 개여도 NULL UNIQUE 미발동으로 안전.
- **완화**: NULL UNIQUE 동작이 SQLite 표준임을 마이그레이션 주석에 명시. PG 도 동일.

### R-3. SHA conflict 무한 루프
- **리스크**: Agent 가 잘못된 sha 를 보고 → Hub 가 ErrShaConflict 반환 → Agent 가 재시도 → 같은 에러.
- **완화**: ErrShaConflict 는 STATUS_UPDATE 응답으로 전파하지 않음. Hub 측에서 WARN 로그만 남기고 다른 status 갱신은 정상 처리(PreviewFields.CommitSha 만 무시). 결정 6 의 "WARN + 무시" 로직 명시.

### R-4. synchronize 의 동시 도착으로 두 active row 생성
- **리스크**: 네트워크 글리치로 같은 PR 에 빠른 synchronize 두 번 도착 → GetActiveByRepoAndPR 두 번 모두 같은 row 를 보고 둘 다 teardown + 두 신규 row 생성.
- **완화**: 본 Phase 는 single-flight lock 을 도입하지 않음. UNIQUE (repo, sha) 가 두 동일 sha 동시 INSERT 를 막고, 두 다른 sha 동시 INSERT 면 둘 다 의미 있음 (가장 최근이 결국 raceLast 승). 운영 모니터링으로 추적.

### R-5. Adhoc preview 가 webhook closed 와 무관해 영구 잔존
- **리스크**: 결정 8 로 PR closed 가 IsAdhoc 을 건드리지 않음. 운영자가 손으로 정리해야 함.
- **완화**: Admin UI agent_detail 페이지의 "teardowns" 버튼이 이미 존재 (admin_ui.go:478). Adhoc badge 로 운영자가 식별 용이. 자동 retention 은 별도 Phase.

### R-6. Down 마이그레이션 데이터 손실
- **리스크**: `0006.down.sql` 이 (repo, pr) 그룹의 가장 최근 row 만 보존하고 나머지 삭제. 운영자가 의도하지 않게 down 하면 history 손실.
- **완화**: down SQL 상단에 큰 주석으로 경고. README/USAGE 에 "0006 down 은 데이터 손실 동반" 명시.

### R-7. is_adhoc 이 다른 컬럼으로 노출되지 않은 위치 누락
- **리스크**: dispatcher, reconciler 등이 IsAdhoc 을 모르면 라벨 매칭이나 stale 회수에서 Adhoc 을 일반 PR 처럼 취급. 본 Phase 는 의도적으로 동등 취급(라벨 매칭은 IsAdhoc 무관, stale 회수도 무관).
- **완화**: 명시적으로 "IsAdhoc 은 표시 외에는 동작 영향 없음" 을 본 기획서에 명문화 (§7 NF). dispatcher_test/reconciler_test 의 회귀 0 으로 검증. (구 결정 8 의 close 가드도 §2-2 Out of Scope 로 이동 — IsAdhoc 은 정말로 표시만 한다.)

### R-8. building 송신 위치 변경에 따른 reconciler staleAssigned 회수 race
- **리스크**: 만약 building STATUS_UPDATE 송신을 Checkout 직후로만 미루면, 큰 repo 의 Checkout 이 수십 초 걸리는 동안 status 가 `assigned` 로 남아 reconciler 의 staleAssigned 회수 대상이 된다. 정상 build 가 회수당하는 회귀가 발생.
- **완화**: 결정 5 가 채택한 "두 번 송신" 전략으로 회피. 첫 번째 building 을 Checkout 전에 즉시 보내 status 를 `building` 으로 전이시키고, 두 번째 building 에서 sha 만 채운다.
- **검증**: F-21 — runner_test 가 SendStatusUpdate 호출 횟수 == 최소 2 회를 assert. reconciler_test 의 staleAssigned 시나리오에 building 두 번 흐름이 포함되도록 회귀 케이스 보강.

### R-9. synchronize Case A (같은 sha 재webhook) 의 active row 잘못된 reopen
- **리스크**: webhook synchronize 가 같은 sha 로 두 번 도착하는 케이스에서 Upsert 가 ON CONFLICT 발동 → `if !created && prev.Status in {done,failed}` 이 우연히 true 면 active row 를 done 으로 오해해 reopen 로직이 잘못 작동. (현실에서는 같은 sha 가 active 상태이면 done 이 아니므로 발동 안 하지만, 동시성 race 로 잠시 status=done 이 보일 가능성).
- **완화**: 분기 조건은 `prev.Status` 의 즉시 값에만 의존하므로 race window 가 매우 짧다. UpdateStatus 호출 자체는 트랜잭션이라 동일 status 자기-루프 전이는 무해. F-12a 가 idempotency 보장.

---

## 9. 다음 Phase 연결점

### 본 Phase 가 약속하는 불변성 (다음 Phase 가 의존 가능)

- **(repo, pr) 페어당 active row 0 또는 1 개** — §1-2 의 5번 항목, 결정 4, 그리고 webhook synchronize 의 Case B 처리에 의해 보장됨. `FindByHost` (현재 reverse proxy 가 호스트헤더 → preview 매핑에 사용)가 같은 (repo, pr) 의 여러 active row 를 만나는 경우는 발생하지 않으며, 실제로 만나면 **버그**로 간주(F-12 가 회귀 검출). 향후 Phase 의 dispatcher / proxy / UI 코드는 "active 는 최대 1" 가정을 그대로 사용 가능.
- **(repo, sha) 글로벌 유일** — UNIQUE 제약으로 강제. SHA 기반 라우팅 / sha 별 URL 생성이 키 충돌 걱정 없이 가능.
- **commit_sha 는 한 번 채워지면 재할당 금지** — 결정 6 / F-22. row 의 sha 정체성이 영구 고정.
- **is_adhoc 은 첫 INSERT 의 진실 보존** — Upsert ON CONFLICT 절이 is_adhoc 을 갱신하지 않으므로, row 의 출처 식별이 안정적.

### 다음 Phase 후보

- **Phase 10 후보 — Retention & GC**: 같은 PR 의 done/failed/teardown 상태 row 가 누적됨. N 개 초과 또는 M 일 경과 시 자동 삭제. is_adhoc=true 는 별도 정책 (예: 7일).
- **Phase 11 후보 — PR 별 history 페이지**: `/admin/repos/{owner}/{repo}/pulls/{n}` 에 SHA timeline 렌더. 본 Phase 의 (repo, pr) 인덱스 + GetActiveByRepoAndPR 가 기반.
- **Phase 12 후보 — sha-aware Reverse proxy**: 현재 `pr-{n}.preview.<base>` 호스트가 단일 active 만 가리킴. SHA 별 hostname (`sha-{abbr}.pr-{n}.preview.<base>`) 추가 시 본 Phase 의 (repo, sha) 키가 즉시 활용 가능. 위 "active 0 또는 1" 불변성이 호스트헤더 매핑 모호성을 제거.

---

## 10. 변경 이력

- 2026-04-27 — 초안 작성 (Phase 9 / sha-keyed previews + is_adhoc).
- 2026-04-27 (rev2) — plan-reviewer 피드백 18 건 반영:
  - **치명적 1**: §5-1 / §5-5 에 preview_events 보존 + FK 정합 보장 명시, F-17 검증 항목에 events 보존 + orphan 0 확인 추가.
  - **치명적 2**: §5-3 에 Adhoc(NULL sha) 의 ON CONFLICT 미발동 + `if !created` 도달 조건(sha 명시된 두 번째 빌드만) + reopen 정책("done/failed→reopen, active→noop") 명문화.
  - **치명적 3**: 결정 3 + §5-2 에 sqlc `sql.NullString` 매핑과 store 변환 헬퍼 시그니처 명시. sqlc.yaml override 미사용 결정.
  - **치명적 4**: 결정 5 를 "두 번 송신" 으로 변경 + R-8 추가, F-21 신설.
  - **치명적 5**: §5-6 에 Decision Matrix 4 케이스 표 + 의사코드 Case A/B/C/D 명시. F-12 → F-12 / F-12a / F-12b / F-12c 분리.
  - **치명적 6**: §5-3 에 ErrShaConflict 사전 SELECT 가 BeginTx 내부에서 수행됨 명시 + F-22 신설.
  - **치명적 7**: F-1 / F-2 / F-17 검증을 SQLite 전용 명령에서 store 인터페이스 레벨 행동 검증으로 교체.
  - **중요 8**: §5-7 testBuildSubmit 의사코드 컴파일 가능 수준으로 구체화.
  - **중요 9**: §5-8 에 previewUrlsJSON 헬퍼 출처 명시 + msg.CommitSHA nil-check 가드 추가.
  - **중요 10**: §2-1 In Scope 에 `cmd_runner.go`, fake 구현체 추가. 결정 9 신설.
  - **중요 11**: 결정 8 → §2-2 Out of Scope 로 이동. F-13 재배정.
  - **중요 12**: §5-1 에 `idx_previews_repo_sha` 중복 인덱스 제거(자동 인덱스 활용). NF-5 검증도 보강.
  - **중요 13**: §5-6 신규 row 의 첫 message 에 "created_after_supersede_of=<old_id>" 자기-루프 UpdateStatus 호출 결정.
  - **중요 14**: §2-1 In Scope "테스트 / fake 호환성" 섹션에 영향 받는 fake 파일 목록 전수 명시.
  - **중요 15**: NF-1 에 PG 14 vs 15+ 의 NULLS NOT DISTINCT 차이 + 본 Phase 가 평문 UNIQUE 만 사용한다는 약속 명시.
  - **사소 16**: §5-6 의사코드에 `newSha := p.PullRequest.Head.SHA` 명시.
  - **사소 17**: §5-9 에 "항상 보고(idempotent)" 정책 명시 + 진단 정보 보존 근거 첨부.
  - **사소 18**: §1-2 5번 항목 + §9 "본 Phase 가 약속하는 불변성" 섹션에 "(repo, pr) active row 항상 0 또는 1" 불변성 명문화.
