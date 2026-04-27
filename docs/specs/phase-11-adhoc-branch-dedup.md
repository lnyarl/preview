# Phase 11 — Adhoc Preview (repo, branch) Deduplication

## 1. Phase 개요

Admin Test Build(adhoc) 진입점에서 같은 `(repo_full_name, branch)` 조합에 대해 **항상 1건의 active adhoc preview만** 존재하도록 한다. 사용자가 같은 repo+branch 로 Test Build 를 다시 누르면 새 row 를 만들지 않고 기존 row 를 재사용 — active 면 그대로 detail 페이지로 redirect, terminal(`done`/`failed`) 이면 re-queue 후 redirect. webhook 경로(`is_adhoc=false`) 는 영향이 전혀 없다(여전히 `(repo, sha)` 자연키).

끝났을 때의 상태:
- `(repo_full_name, branch)` 가 같은 두 번째 Test Build 가 신규 row 를 생성하지 않는다.
- 기존 adhoc preview 가 active 면 noop, terminal 이면 새 build cycle 로 회수된다.
- webhook 경로의 동작·자연키·UNIQUE 제약은 변하지 않는다.
- 기존 `testBuildSubmit` 의 `!created && prev != nil` reopen 분기는 본 Phase 에서 **삭제**된다 (dedup hit 경로가 동일 기능을 흡수).

## 2. 범위와 비범위

**범위**
- `db/queries/previews.sql` 에 `GetAdhocPreviewByBranch :one` 쿼리 1개 추가.
- sqlc 재생성으로 `internal/db/sqlite/previews.sql.go` 갱신.
- `store.PreviewStore` 인터페이스에 `FindAdhocByBranch(ctx, repoFullName, branch) (*Preview, error)` 메서드 추가 + sqlite 구현체 반영.
- `internal/hub/admin_ui.go` 의 `testBuildSubmit` 핸들러에 dedup 분기 삽입 + 기존 `!created` reopen 분기 **삭제**.
- 모든 fake/test double 에 새 메서드 stub 추가.
- 단위테스트: 새 store 메서드 + `testBuildSubmit` 의 분기들(active noop / terminal re-queue / dedup miss).

**비범위**
- 스키마 변경 (마이그레이션 0건). UNIQUE 제약 추가도 없음 — 애플리케이션 레이어 lookup 만으로 충분.
- webhook 경로 변경. `is_adhoc=false` row 는 dedup 대상이 아니다.
- branch 정규화(대소문자/슬래시) — DB 에 저장된 값 그대로 정확 일치 비교만 수행.
- UI 변경 (Test Build 폼·detail 페이지 그대로).
- sqlc.yaml 변경.
- **폼의 `commit_sha` 입력은 dedup hit 경로에서 무시**된다 (새 sha 반영 없음 — 결정 4 참조).
- **branch 가 다른 동일 sha 충돌(dedup miss + Upsert `!created`)** — 본 Phase 에서는 `slog.Error` + 500 으로 보호하고, 후속 Phase 에서 cross-branch sha 충돌 정책을 별도로 다룬다 (결정 5 참조).

## 3. 설계 결정 및 근거

### 결정 1: 애플리케이션 레이어 lookup (UNIQUE 제약 미사용)
- **결정**: 핸들러에서 SELECT → 분기 → INSERT/UPDATE 한다. DB 레벨 partial UNIQUE index 추가하지 않는다.
- **근거**: (a) 이식성 원칙 — partial UNIQUE 는 SQLite/Postgres 문법 통일이 어렵다. (b) Phase 9 의 `(repo_full_name, commit_sha)` UNIQUE 와의 상호작용 검증 비용을 피한다. (c) Test Build 는 어드민 단발 트리거라 race window 가 좁다.
- **버려진 대안**: partial UNIQUE index `WHERE is_adhoc=1`. → 이식성·검증 비용으로 기각. R-1 누적 시 후속 Phase 에서 도입 가능.
- **되돌릴 때 비용**: 작다. 인터페이스 메서드 추가뿐, 마이그레이션 미발생.

### 결정 2: lookup 키는 `(repo_full_name, branch)` + `is_adhoc=1`
- **결정**: SELECT 조건은 `repo_full_name=? AND branch=? AND is_adhoc=1`. `created_at DESC LIMIT 1`.
- **근거**: webhook row 와 분리 + 같은 branch 의 과거 done/failed 누적 row 중 최신 1건만 본다. 상태 무관 lookup 후 분기에서 상태로 나눈다.
- **버려진 대안**: lookup 자체에 `status IN active` 필터. → terminal 재사용 케이스를 놓치므로 기각.
- **되돌릴 때 비용**: 쿼리 1개 제거.

### 결정 3: 분기 — active = noop redirect, terminal = re-queue
- **결정**: `active = {queued, assigned, building, running, teardown}`, `terminal = {done, failed}`.
  - active → DB 변경 없이 `/admin/previews/{prev.ID}` redirect.
  - terminal → 6 필드 명시 reset + `UpdateStatus(prev.Status → queued)` + `triggerDispatch` + redirect.
- **근거**:
  - active 일 때 강제로 status 를 흔들면 진행 중 build 와 race. 사용자 요구사항도 "이미 돌고 있으면 그대로".
  - **teardown 도 active 그룹**: Phase 6 이후 adhoc preview 가 webhook 외 경로로도 teardown 에 진입할 수 있다 (예: admin teardowns 버튼, JOB_TEARDOWN 처리 중). 사용자가 의도한 "재시작" 이 아닌 "정리 중" 이라 강제 re-queue 는 부적절 → noop 으로 안전망 처리.
  - terminal 분기의 6 필드 reset 셋은 `previewRebuild` 와 동일 → 동작 일관성 유지.
- **버려진 대안**: terminal 시 row 삭제 후 신규 INSERT → preview_events 이력 손실 + 결정 11(상태 단일 진입점) 위배.
- **되돌릴 때 비용**: 핸들러 if 블록 1개 제거.

### 결정 4: re-queue 시 `CommitSha` 는 손대지 않는다
- **결정**: re-queue 시 `PreviewFields.CommitSha = nil` 로 둔다 — store 의 nil 가드가 SQL 갱신 자체를 건너뛰므로 기존 sha 가 자연 보존되고, `previewRebuild` 와 정책 일관.
- **근거**: 핸들러에서 `fields.CommitSha = nil` 로 전달하면 sqlite store 의 nil 가드(`if fields.CommitSha != nil`)가 SQL 의 `commit_sha` 컬럼 갱신 절을 아예 만들지 않는다. **`nil` 전달이 단일 진실 소스**이며, NULLABLE 컬럼에 대한 SQL 레벨 COALESCE 보호는 별개 메커니즘(보조망)일 뿐이다. 폼의 `commit_sha` 입력은 dedup hit 경로에서 의미가 없으며 (§2 비범위), Agent 가 같은 sha 로 재빌드한다.
- **NULL 가드 경계**: NULLABLE 컬럼(예: `CommitSha`)만 nil 전달 시 SQL 갱신을 건너뛴다. **NOT NULL 컬럼(예: `PreviewURLs`, `ContainerID`, `AgentHost`, `AgentPort`, `ErrorMessage`, `AssignedAgentID`)은 nil 가드가 없거나 의미가 다르므로** F-9 의 6 필드 reset 처럼 명시적으로 `ptr("")`/`ptr(0)` 을 넘겨야 한다.
- **버려진 대안**: `commit_sha = ?` 무조건 덮어쓰기로 SQL 변경 → Phase 9 `ErrShaConflict` 의미와 충돌, SQL 갈라치기 비용 큼.
- **되돌릴 때 비용**: 0 (하지 않음).

### 결정 5: dedup miss 분기에서만 Upsert 호출 + 기존 reopen 분기 삭제
- **결정**: dedup hit (`err == nil`) → Upsert 미호출. dedup miss (`ErrNotFound`) → 기존 Upsert 호출 진행. 기존 `testBuildSubmit` 의 `!created && prev != nil && status ∈ {done, failed}` reopen 분기는 **본 Phase 에서 삭제**한다.
- **근거**: dedup hit 경로가 reopen 의 책임을 흡수한다. dedup miss 에서 `!created` 는 다음 두 케이스로 분해된다:
  1. **sha 비어있음** → Upsert 의 ON CONFLICT 미발동 → `!created` 도달 불가 (정상).
  2. **sha 가 채워졌고 `(repo, sha)` 충돌** → `!created` 가 발생할 수 있다. 이 중 *같은 sha 가 다른 branch 로 들어와 webhook 또는 다른 PR 의 row 와 충돌하는 cross-branch sha 충돌* 케이스는 정상 시나리오로 존재한다 (예: 같은 commit 이 두 PR 에 cherry-pick).
- **본 Phase 의 처리**: 이 케이스는 본 Phase 의 **비범위로 선언**한다 (§2 참조). `slog.Error("admin_ui_test_build_unexpected_existing", ...)` + HTTP 500 으로 보호하고, 후속 Phase 에서 cross-branch sha 충돌 정책(예: branch 별 별도 row 허용 / 사용자에게 충돌 안내 페이지 등)을 정식 설계한다.
- **버려진 대안**: hit/miss 무관 항상 Upsert + 사후 dedup → DB 라운드트립 낭비, 신규 row 가 잠시 생겼다 사라지는 부수효과 검증 비용. / 기존 reopen 분기 유지 → 본 Phase 에서 dedup hit 경로가 책임을 흡수하므로 중복 책임.
- **되돌릴 때 비용**: 핸들러 분기 단순화 — reopen 코드 복구.

## 4. 아키텍처/구조

### 변경 디렉토리 트리 (변경 파일만)

```
db/queries/previews.sql                          (+8 lines: GetAdhocPreviewByBranch)
internal/db/sqlite/previews.sql.go               (sqlc regen)
internal/store/store.go                          (PreviewStore +1 메서드)
internal/db/sqlite/preview_store.go              (FindAdhocByBranch 구현)
internal/hub/admin_ui.go                         (testBuildSubmit dedup 분기 + 기존 reopen 분기 삭제)
internal/hub/admin_ui_test.go                    (3 분기 케이스)
internal/db/sqlite/preview_store_test.go         (FindAdhocByBranch 단위테스트)
internal/hub/{coverage,dispatcher,reconciler,webhook_handler,ws_sync}_test.go  (fake stub)
```

### testBuildSubmit 시퀀스 (의사코드)

```
ParseForm + repoFullNameFromURL  // 기존 그대로

(1) prev, err := PreviewStore.FindAdhocByBranch(ctx, repoFullName, branch)

(2) err == nil  → dedup HIT
    if prev.Status ∈ active (= {queued, assigned, building, running, teardown}, §3 결정 3):
        h.Logger.Info("test_build_dedup_active", ...)
        redirect /admin/previews/{prev.ID}; return                       // F-8, F-11
    if prev.Status ∈ {done, failed}:
        fields := PreviewFields{6 ptrs reset, see §5 / F-9}              // CommitSha 는 nil — store nil 가드가 SQL 갱신 skip (결정 4)
        err := UpdateStatus(prev.ID, prev.Status → "queued",
                            "re-queued by test build", now, fields)
        if errors.Is(err, ErrStaleState):
            // 다른 경로(예: 동시 다른 핸들러)가 먼저 상태를 바꾼 케이스.
            // 본 Phase 의 새 정책: ErrStaleState 는 다른 경로가 이미 처리한 것으로 보고
            // 그대로 redirect (ws_handler.go 의 ErrStaleState 무시 패턴 준용).
            // dispatcher 트리거 생략 — 다른 경로가 이미 처리했으므로 그 경로에서 triggerDispatch 책임. (F-6)
            h.Logger.Info("test_build_dedup_requeued", ...)
            redirect /admin/previews/{prev.ID}; return
        if err != nil: 500; return
        h.Logger.Info("test_build_dedup_requeued", ...)
        triggerDispatch(ctx)
        redirect /admin/previews/{prev.ID}; return                       // F-9, F-11

(3) errors.Is(err, ErrNotFound)  → dedup MISS
    created, _, err := PreviewStore.Upsert(ctx, p)
    if err != nil: 500
    if !created:
        // §3 결정 5 / §2 비범위: cross-branch sha 충돌 (같은 sha 가 다른 branch/PR 로 진입)이
        // 포함된 케이스. 본 Phase 는 비범위로 선언하고 slog.Error + 500 으로 보호한다.
        h.Logger.Error("admin_ui_test_build_unexpected_existing", ...); 500; return
    h.Logger.Info("test_build_triggered", ...)                           // 기존 로그 — dedup hit 시 도달 안 함 (NF-2)
    triggerDispatch(ctx)
    redirect /admin/previews/{p.ID}

(4) err != nil && !ErrNotFound  → 500
```

## 5. 인터페이스 계약

### 새 SQL 쿼리

```sql
-- name: GetAdhocPreviewByBranch :one
SELECT * FROM previews
WHERE repo_full_name = ?
  AND branch = ?
  AND is_adhoc = 1
ORDER BY created_at DESC
LIMIT 1;
```

| 항목 | 값 |
|---|---|
| sqlc kind | `:one` |
| 매칭 0건 | `sql.ErrNoRows` (스토어가 `store.ErrNotFound` 로 변환) |
| 정렬 | `created_at DESC` (최근 1건) |
| 표준 SQL | 예. `is_adhoc=1` 은 INTEGER 0/1 표현 (Phase 9 와 동일) |

### 새 인터페이스 메서드

```go
// FindAdhocByBranch 는 같은 (repo_full_name, branch) 의 가장 최근 adhoc preview 1건을
// 반환한다. 상태 무관. 없으면 ErrNotFound.
// Phase 11: Admin Test Build 의 (repo, branch) 단위 dedup 진입점.
FindAdhocByBranch(ctx context.Context, repoFullName, branch string) (*Preview, error)
```

| 입력 | repoFullName: trim 된 "owner/repo". branch: trim 된 폼 입력값 |
|---|---|
| 반환 | ErrNotFound 외에는 항상 valid `*Preview` (nil 아님). labels 디코드 실패 등 내부 에러는 `fmt.Errorf` 로 wrap 반환 |
| 부수효과 | 없음 (read-only) |

## 6. 기능 요구사항 체크리스트

- [ ] **F-1**: `GetAdhocPreviewByBranch` 쿼리가 `previews.sql` 에 추가되어 있고, `is_adhoc = 1` 필터를 포함한다. — 검증: `db/queries/previews.sql` grep `GetAdhocPreviewByBranch` 1회 매칭, 본문에 `is_adhoc = 1` 포함.
- [ ] **F-2**: sqlc 재생성 후 `internal/db/sqlite/previews.sql.go` 에 `func (q *Queries) GetAdhocPreviewByBranch(` 함수가 존재한다 (기존 쿼리들이 같은 파일에 생성되어 있음 — `sqlc.yaml` 의 input glob `db/queries/*.sql` 가 자동 포착, **수동 수정 금지**). — 검증: 같은 파일에 패턴 1회 매칭.
- [ ] **F-3**: `store.PreviewStore` 인터페이스에 `FindAdhocByBranch` 시그니처가 추가되어 있고 컴파일된다. — 검증: `go build ./...` 성공 + `var _ store.PreviewStore = (*sqlitestore.PreviewStore)(nil)` 컴파일타임 확인.
- [ ] **F-4**: `sqlitestore.PreviewStore.FindAdhocByBranch` 가 매칭 0건일 때 `store.ErrNotFound` 를 반환한다. — 검증: 단위테스트 `TestFindAdhocByBranch_NotFound`.
- [ ] **F-5**: `FindAdhocByBranch` 가 같은 (repo, branch) 의 webhook row(`is_adhoc=false`) 는 매칭하지 않는다. — 검증: 단위테스트가 `is_adhoc=false` row 를 미리 INSERT 후 호출해 `ErrNotFound` 확인.
- [ ] **F-6**: `testBuildSubmit` 의 re-queue 분기에서 `UpdateStatus` 가 `ErrStaleState` 를 반환하면 500/409 없이 그대로 redirect 한다. **본 Phase 에서 새로 도입하는 정책**: ErrStaleState 시 다른 경로가 이미 상태를 처리한 것으로 보고 무시·redirect 한다 (`internal/hub/ws_handler.go` 의 ErrStaleState 무시 패턴을 준용). 이때 `triggerDispatch` **를 호출하지 않는다** — dispatcher 트리거 책임은 상태를 바꾼 그 경로에 있다. (참고: `internal/hub/admin_ui.go` 의 기존 `previewRebuild` 는 ErrStaleState 별도 분기 없이 모든 err 에 500 을 반환하므로, 본 정책은 `previewRebuild` 와 다르다.) — 검증: fake store 가 `ErrStaleState` 반환하도록 세팅 후 핸들러 호출 → (a) 응답 코드 303 + Location `/admin/previews/{prev.ID}` 확인, (b) fake dispatcher 의 OnReady 호출 카운트 = 0 확인. 단위테스트에서는 fake store 가 `UpdateStatus` 호출 시 `store.ErrStaleState` 를 반환하도록 주입한다(race 시나리오 직접 재현 없이 동일 코드 경로 통과).
- [ ] **F-7**: `testBuildSubmit` 가 dedup miss 인 경우 기존 동작(신규 row INSERT, redirect) 을 그대로 수행한다. 기존 `!created && prev != nil` reopen 분기는 **삭제**되었음을 코드 레벨에서 확인. — 검증: 단위테스트 `TestTestBuildSubmit_NewPreview` 통과 + grep 으로 `reopened_by_test_build` / `prev.Status == "done"` 분기 코드가 사라졌는지 확인.
- [ ] **F-8**: `testBuildSubmit` 가 dedup hit + active 인 경우 신규 row 를 만들지 않고 기존 ID 로 redirect 한다. — 검증: ListAll() 길이 1 유지 + `Location` 헤더 = `/admin/previews/{기존ID}`.
- [ ] **F-9**: `testBuildSubmit` 가 dedup hit + terminal(done/failed) 인 경우 `UpdateStatus(→queued)` 호출 시 `PreviewFields` 의 6 필드가 정확히 다음 값으로 reset 된다 (previewRebuild 코드 참조): `ContainerID=ptr("")`, `AgentHost=ptr("")`, `AgentPort=ptr(0)`, `PreviewURLs=ptr("")`, `ErrorMessage=ptr("")`, `AssignedAgentID=ptr("")`. **`CommitSha` 는 reset 안 함** (nil — 결정 4). — 검증: fake store 가 호출 인자를 기록 → 6 포인터 모두 non-nil & 빈 값, `CommitSha == nil` 확인. (dispatcher 트리거 검증은 F-11 에 일임.)
- [ ] **F-10**: webhook 경로(`is_adhoc=false`) 는 본 Phase 변경에 영향받지 않는다. — 검증: 기존 `TestWebhook*` 통과 + `TestFindAdhocByBranch_IgnoresWebhookRow` 추가.
- [ ] **F-11**: dedup hit 시 dispatcher 트리거가 active 분기에서는 **호출되지 않고**, terminal 분기에서만 호출된다. — 검증: fake dispatcher OnReady 카운트 (active=0, terminal≥1).
- [ ] **F-12**: `FindAdhocByBranch` 가 동일 (repo, branch, adhoc) row 가 2건일 때 `created_at` 더 최신 row 를 반환한다. — 검증: 단위테스트 `TestFindAdhocByBranch_LatestWins`.

## 7. 비기능 요구사항 체크리스트

- [ ] **NF-1 (이식성)**: 새 쿼리는 표준 SQL 만 사용한다. — 검증: `INSERT OR REPLACE`/`AUTOINCREMENT`/jsonb 연산자 grep 없음.
- [ ] **NF-2 (관측성)**: dedup hit 진입 시 `h.Logger.Info("test_build_dedup_active", "repo", ..., "branch", ..., "preview_id", ...)` 또는 `h.Logger.Info("test_build_dedup_requeued", "repo", ..., "branch", ..., "preview_id", ...)` 가 한 줄 출력된다 (handler 의 `*slog.Logger` 필드를 통한 호출 — package-level `slog.Info` 직접 호출 금지). **기존 `test_build_triggered` 로그는 dedup hit 시 출력되지 않는다** (redirect 전에 return 하므로 기존 줄에 도달 안 함). — 검증: `bytes.Buffer` 로그 캡처 후 substring 매칭 + dedup hit 케이스에서 `test_build_triggered` 부재 확인.
- [ ] **NF-3 (레이어링)**: `internal/hub` 가 `internal/db/sqlite` 를 직접 import 하지 않는다. — 검증: `.golangci.yml` depguard + `go vet ./...` 클린.
- [ ] **NF-4 (이력 보존)**: re-queue 분기는 `UpdateStatus` 를 통해 `preview_events` 행을 1개 추가한다. 기존 row 가 삭제되지 않는다. — 검증: `ListPreviewEventsRaw` 카운트 hit 후 +1.
- [ ] **NF-5 (멱등성)**: 같은 (repo, branch) 로 active row 에 두 번 연속 Test Build 를 눌러도 DB row 수·상태 모두 변하지 않는다. — 검증: 핸들러 두 번 호출 후 `ListAll()` 길이·`prev.Status` 동일 확인.
- [ ] **NF-6 (성능)**: `previews` 테이블은 이미 PK + `idx_previews_repo_pr` 보유. is_adhoc=1 row 수가 수십 건 이하라 SQLite planner 의 full scan 도 sub-ms 수준. **추가 인덱스 불필요**. — 검증: 마이그레이션에 새 인덱스 없음 + `EXPLAIN QUERY PLAN SELECT ... WHERE repo_full_name=? AND branch=? AND is_adhoc=1 ORDER BY created_at DESC LIMIT 1` 의 출력이 기존 인덱스를 사용하거나 SCAN 으로 끝나는 것을 확인 (단위테스트에서 raw query).
- [ ] **NF-7 (테스트 더블 일관성)**: 모든 fake `PreviewStore` 구현체에 `FindAdhocByBranch` stub 이 추가되어 컴파일 통과. **대상 파일**: `internal/hub/admin_ui_test.go`, `internal/hub/coverage_test.go`, `internal/hub/dispatcher_test.go` (fake 2개: `dispFakePreviewStore`, `raceFakeStore`), `internal/hub/reconciler_test.go`, `internal/hub/webhook_handler_test.go`, `internal/hub/ws_sync_test.go`. **기본 stub 동작**: `return nil, store.ErrNotFound`. — 검증: `go build ./...` + `go test ./...` 컴파일 단계 성공.

## 8. 리스크와 완화책

- **R-1: race window — 같은 (repo, branch) Test Build 동시 클릭**. lookup 후 Upsert 사이에 다른 트랜잭션 INSERT → row 2건 공존 가능. **dispatcher 가 둘 다 빌드해 사용자 관찰 가능한 이상 동작** (두 detail 페이지, 두 컨테이너). **부수 영향**: 첫 클릭의 redirect 가 끝난 뒤에도 두 번째 row 가 살아있으므로, 다음 dedup 의 lookup target 이 `created_at DESC LIMIT 1` 규칙에 따라 더 최근 row 로 바뀐다 — 사용자가 화면에서 본 preview ID 와 다음 트리거 시점의 ID 가 불일치할 수 있다.
  - 완화: HTTP stateless 구조상 application-layer 에서 완전 방지 불가. 어드민 단발 트리거이므로 발생 빈도 낮음 — 수용. **근본 해결은 후속 Phase 에서 DB 레벨 partial UNIQUE index** (예: `CREATE UNIQUE INDEX ... ON previews(repo_full_name, branch) WHERE is_adhoc=1`) **추가 검토**.

- **R-2: branch 정규화 부재**. `feature/x` vs `Feature/X` 별개 row 로 dedup 실패.
  - 완화: 본 Phase 정확 일치만 보장(§2). 추후 `git check-ref-format` 호환 정규화 도입.

- **R-3: terminal row 의 RepoCloneURL/Branch/Labels 잔여값**. 6 필드만 reset 하므로 사용자가 폼에서 RepoCloneURL 을 변경했다면 새 입력은 무시된다.
  - 완화: 같은 (repo, branch) 의 dedup 이라 변경 가능성 낮음. `previewRebuild` 와 일관 — 기존 값 유지.

## 9. 다음 Phase 연결점

- 본 Phase 의 `FindAdhocByBranch` 는 향후 Admin UI 의 "재빌드"·"Cancel & Restart" 동선에서 lookup primitive 로 재사용 가능.
- adhoc 의 dedup 키가 `(repo, branch)` 로 명문화되었으므로, 후속 Phase 에서 webhook ↔ adhoc 통합 정책의 진입점이 된다. **다만 PR close 시 adhoc 정리는 PR 단위라 branch 동일성만으로 단정할 수 없다** — 추후 별도 lookup (예: `FindAdhocByPR` 또는 `(repo, pr_number)` 매핑) 추가 가능성.
- partial UNIQUE index 도입 시 결정 1 의 "버려진 대안" 으로 돌아와도 lookup 메서드 시그니처는 그대로 유지되고 INSERT 경합만 DB 레벨로 옮겨진다.
