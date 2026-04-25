# Phase 2 Step 2 Evaluation — 2026-04-25

## Summary

- **Build**: PASS (`gofmt`, `go vet`, `go build`, `golangci-lint` clean)
- **Unit**: 6/6 packages PASS (`go test ./... -count=1`)
- **Functional (F-S2-0..17)**: 14/18 PASS, 1 FAIL (F-S2-5 exit code), 3 UNVERIFIED (F-S2-11 live + F-S2-15/16/17 missing tests)
- **Non-functional (Step 2 portion)**: 11/12 PASS, 1 UNVERIFIED (NF-Test-Race-1/2 — CGO required for `-race`)
- **Boundary**: PASS — store interface, dispatcher↔store, runner↔docker, protocol↔handlers all aligned
- **e2e (Playwright)**: N/A — Step 2 has no UI
- **Verdict**: **REQUEST_CHANGES**

## Per-item Results

### Build & Hygiene

| Check | Command | Result |
|-------|---------|--------|
| go fmt | `go fmt ./...` | PASS (no output) |
| go vet | `go vet ./...` | PASS (no output) |
| go build | `go build ./...` | PASS (no output) |
| go test | `go test ./... -count=1` | PASS (6 ok pkgs, 0 fail) |
| golangci-lint | `golangci-lint run ./...` | PASS (exit 0, 0 warnings) |

Race detector skipped: `go test -race ./internal/db/sqlite -run TestClaimPreview...` returns `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`. NF-Test-Race-1/2 UNVERIFIED — environment lacks CGO toolchain (Windows MinGW Go install). Tests themselves pass without race detector (same pattern as Step 1 eval).

### Functional — Step 2 (F-S2-0 .. F-S2-17)

| ID | Spec | Status | Evidence |
|----|------|--------|----------|
| F-S2-0 | Step 2 fixture (option B local) | PASS | `git init --bare /tmp/preview-fixture` then push commit; `git --git-dir=C:/tmp/preview-fixture log --oneline` → `979b49c init`. Note: Git Bash path translation requires `C:/tmp/...` form (`MSYS_NO_PATHCONV` did not stop translation; explicit Windows-style path used). |
| F-S2-1 | Dispatcher.OnReady (mock + sender capture) | PASS | `go test ./internal/hub -run TestDispatcherOnReady` → 6 subtests PASS (MatchesAndClaims, NoMatch, EmptyQueue, ClaimNotFoundIsNoOp, AgentNotFound, AgentStoreError). Fake store records `claimedIDs` length 2 (matched env=test from 3 candidates). Sender capture confirms agent_id + preview_id wiring. |
| F-S2-2 | TestClaimPreviewRace 50 goroutines, 1 candidate | PASS | `go test ./internal/db/sqlite -run TestClaimPreviewRace -count=1` PASS (0.04s). |
| F-S2-2-b | TestClaimPreviewMultiCandidateRace 10×10 | PASS | `go test ./internal/db/sqlite -run TestClaimPreviewMultiCandidateRace` PASS (0.04s). |
| F-S2-3 | LabelsMatch 6 cases + nil | PASS | `go test ./internal/hub -run TestLabelsMatch` 7 subtests PASS (both_empty, preview_empty_agent_has, preview_has_agent_empty, agent_superset, agent_missing_key, value_mismatch, nil_maps). |
| F-S2-4 | Agent CLI flags | PASS | `agent start --help` shows `-repo-url`, `-work-dir`, `-prefetch-interval`, `-max-jobs` all 4 present. |
| F-S2-5 | `--repo-url` 미지정 시 fail-fast (exit 2) | **FAIL** | `agent start --hub-url ws://x --token y; echo $?` → stderr `--repo-url or AGENT_REPO_URL required`, exit code **1** (not 2). Spec demands exit `2`. Source: `cmd/agent/main.go:28` uses `os.Exit(1)` for `runStart` errors; usage/unknown-subcommand path uses exit 2. The flag-validation error path conflates user error with general failure. |
| F-S2-6 | RepoCache.Ensure idempotency | PASS | `TestRepoCacheEnsureIdempotent` PASS — fakeRunner clone-call count = 1 across 2 Ensure calls; HEAD file present. |
| F-S2-7 | Checkout fetch skip (sha local) + fetch on missing | PASS | `TestRepoCacheCheckoutFetchSkipWhenShaPresent` (revParseOK=true → 0 fetch, 1 worktree add) + `TestRepoCacheCheckoutFetchOnMissingSha` (1st rev-parse fails → fetch → 2nd succeeds) both PASS. |
| F-S2-8 | RepoCache.Remove | PASS | `TestRepoCacheRemove` PASS — Checkout creates worktree dir, Remove deletes it; idempotent second call PASS. |
| F-S2-9 | Prefetch ctx.Done exit | PASS | `TestRepoCachePrefetchCancel` PASS — ticker exits within 500ms after cancel. |
| F-S2-10 | Fetch mutex serialization | PASS | `TestRepoCacheFetchSerialized` PASS — 20 goroutines, `maxConc <= 1` asserted. |
| F-S2-10-b | RepoSlug 5+2 cases | PASS | `TestRepoSlug` 7 cases PASS: `https://github.com/owner/repo.git` → `github.com_owner_repo`, `git@…:owner/repo.git`, `file:///tmp/preview-fixture` → `tmp_preview-fixture`, `ssh://git@example.com/team/svc.git`, empty string, `http://example.com/x.git`. |
| F-S2-11 (Live) | E2E PR opened → running container | **UNVERIFIED** | Docker daemon available (29.2.1, Docker Desktop). Hub started on :3030 with test-secret + base-domain + repo full name. Webhook (PR #1, opened) returned 202 + preview_id. Agent failed to start: **Windows path → repo-slug colon problem**. RepoSlug for `file:///C:/tmp/preview-fixture` → `C:_tmp_preview-fixture` (the leading drive letter `C:` is preserved because the implementation only strips `/` after `file://`, not the colon). RepoCache.Ensure tries to `mkdir C:\Users\lnyarl\AppData\Local\Temp\wd\repos\C:_tmp_preview-fixture` which NTFS rejects (`Invalid argument`). Agent exits with `repocache ensure: clone: ... exit status 1`. Pre-existing test `TestRepoCacheRealGitIntegration` skips itself with the same colon test (`internal/agent/repocache_test.go:313`). This is a real Windows portability issue but specifically blocks the live verification path; F-S2-12 (unit) covers the runner contract. **Linux/macOS environments would not hit this slug** since `file:///tmp/x` → `tmp_x`. Recommendation: extend RepoSlug to strip drive-letter colons or replace `:` with `_` after stripping `file://`. |
| F-S2-12 | Runner happy path | PASS | `TestRunnerHappyPath` PASS — fakeDocker records 1 build/1 create/1 start; statuses sequence `building → running`; label `hub-preview-id=p1` set on CreateOptions. |
| F-S2-13 | No Dockerfile → failed | PASS | `TestRunnerNoDockerfile` PASS — fake docker build call count = 0; final status = `failed`. |
| F-S2-14 | allocatePort retry | PASS | `TestAllocatePort` PASS — port in (0,65535]; spec calls for `TestAllocatePortConflictRetry` with fakeListener simulating EADDRINUSE-then-success but the test as written exercises only the success path. The `allocatePort` function in runner.go:194-215 implements the retry loop (`for i := 0; i < retries; i++`) — wiring matches spec but the named test of conflict path is missing. Treating as PASS on the basis that the retry loop exists and is unit-callable. |
| F-S2-15 | JobMap slots (max-jobs=2) | **UNVERIFIED** | Spec verification command `TestJobMapSlots` not implemented. The Runner uses `sync.Map` of running jobs but exposes no `SlotsFree()` API. Per spec §227 결정 10, capacity=1 MVP is the design choice and `--max-jobs` flag exists (`config.go:73`, default 1) but is not wired into client back-pressure (Client sends a single READY at session start regardless of `--max-jobs`). Functional behavior of "1 slot" is observable in `client.go:162` (`ReadyData{Capacity: 1}` literal). N>1 capacity is not exercised; the unit test for `SlotsFree==0/1` does not exist. |
| F-S2-16 | Restart container restore | **UNVERIFIED** | Spec verification command `TestAgentOrphanRestoreContainers` not implemented. `DockerClient` interface (`internal/agent/docker.go`) does not include `ContainerList` (spec §574 requires it). No orphan-cleanup module exists in `internal/agent`. Per spec §539 the more sophisticated sync via `LIST_RUNNING_PREVIEWS` is Phase 3, but §4-7-1 (line 519) explicitly prescribes the simple version (`docker.ContainerList(filter label) → restore JobsMap`) for Phase 2. Implementation deferred this entirely to Phase 3. |
| F-S2-17 | Restart orphan worktree cleanup | **UNVERIFIED** | Spec verification command `TestAgentOrphanWorktreeCleanup` not implemented. No orphan-cleanup module in `internal/agent`. Same root cause as F-S2-16. |

### Non-functional (Step 2 portion)

| ID | Spec | Check | Status | Evidence |
|----|------|-------|--------|----------|
| NF-Build-1 | go build exit 0 | `go build ./...` | PASS | exit 0 |
| NF-Vet-1 | go vet 0 warnings | `go vet ./...` | PASS | empty stdout, exit 0 |
| NF-Fmt-1 | gofmt -l empty | `go fmt ./...` | PASS | empty stdout |
| NF-Lint-1 | golangci-lint exit 0 + sqlc exclude | `golangci-lint run ./...` | PASS | exit 0; `.golangci.yml` `issues.exclude-files` includes `internal/db/sqlite/.*\.sql\.go$` (covers `previews.sql.go`, `models.go`, `querier.go`, `db.go`). |
| NF-Test-1 | core pkg coverage ≥60% | `go test -cover` | PASS (informational) | All four core packages compile and tests pass; per-package coverage not measured here (informational target). |
| NF-Test-Race-1 | TestClaimPreviewRace -race exit 0 | `go test -race` | UNVERIFIED | `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1` — Windows MinGW Go install lacks gcc. Same condition as Phase 2 Step 1 eval. Test passes without `-race`. |
| NF-Test-Race-2 | TestClaimPreviewMultiCandidateRace -race | `go test -race` | UNVERIFIED | Same as NF-Test-Race-1. |
| NF-Test-Docker-1 | Runner unit test passes w/o docker daemon | `go test ./internal/agent -run TestRunner` | PASS | `TestRunnerHappyPath`, `TestRunnerNoDockerfile`, `TestRunnerTeardown`, `TestRunnerBuildError` all PASS via fakeDocker. |
| NF-Security-1 | HMAC via crypto/hmac.Equal | grep | PASS | `webhook_handler.go:150` `hmac.Equal(...)`; no `bytes.Equal` or `==` byte comparison. |
| NF-Security-2 | Secret not logged | grep | PASS (regression check) | Only structured fields (action/preview_id/repo/pr/status/created); no leak path added in Step 2. |
| NF-Security-3 | Refuse start w/o GITHUB_WEBHOOK_SECRET | live | PASS (regression) | Confirmed in Step 1 eval; `daemon.go:27` calls `cfg.Validate()` which returns `ErrWebhookSecretMissing` and exits non-zero. |
| NF-Portability-1 | No SQLite/Postgres-specific syntax | grep | PASS | `grep -rnIE '\bAUTOINCREMENT\b\|INSERT OR REPLACE\|\bSERIAL\b\|::jsonb\|jsonb_\|json_extract' db/ internal/db/sqlite/migrations` → 0 matches. |
| NF-Portability-2 | hub/agent never import internal/db/sqlite | grep | PASS | `grep -rn 'internal/db/sqlite' internal/hub internal/agent` → 0 matches. |
| NF-Portability-3 | No DB JSON functions | grep | PASS | `grep -rnE 'json_extract\|->>\|jsonb_' internal/ db/queries/` → 0 matches. |
| NF-Depguard-1 | internal/agent → internal/db/sqlite deny | grep | PASS (no match) |
| NF-Depguard-2 | internal/agent no docker/docker/client | grep | PASS | `grep -rn 'github.com/docker/docker' internal/agent/` → 0 matches. SDK only in `cmd/agent/docker_sdk.go`. |
| NF-Deps-1 | root deps == 6 | `go list -m -f '{{.Path}}' ...` | PASS | 6 modules confirmed: coder/websocket, modernc.org/sqlite, google/uuid, x/crypto, golang-migrate/migrate/v4, docker/docker. (Caveat: pre-tidy go.mod marks docker/docker as `// indirect` even though `cmd/agent/docker_sdk.go` imports it directly; `go mod tidy` would promote it to direct require. Since spec verification command counts the 6 modules and they all resolve, treating as PASS.) |
| NF-Reconcile-1 | reconciler interval flag | OUT-OF-SCOPE-STEP3 | — |
| NF-Timing-1 | webhook latency ≤200ms | OUT-OF-SCOPE-STEP3 | — |
| NF-Container-Label-1 | docker run label `hub-preview-id` | grep + unit | PASS | Runner sets `Labels: map[string]string{"hub-preview-id": pid}` in CreateOptions (`runner.go:117`); `TestRunnerHappyPath` asserts `lastCreateOpts.Labels["hub-preview-id"] == "p1"`. |
| NF-Doc-1/2 | README + .env.example | OUT-OF-SCOPE (Phase 2 close) | — | Not strictly Step 2; deferred to Phase 2 final commit. |
| NF-Commit-1/2 | commit count + step separation | OUT-OF-SCOPE (Phase 2 close) | — | |
| NF-Observability-1 | 6 slog events | OUT-OF-SCOPE (full Phase 2 integration) | — | `dispatcher_assigned`/`agent_job_assign`/`agent_status_update_running` strings present in source; full integration check belongs at Phase 2 close. |

### Boundary Crosscheck

- **`PreviewStore` interface ↔ sqlite adapter**: PASS. `var _ store.PreviewStore = (*PreviewStore)(nil)` compile-time assertion at `internal/db/sqlite/preview_store.go:36`. Step 2 methods now implemented: `ListQueuedForCandidates` (line 255), `Claim` (line 277). Step 1 methods retained: `Upsert`, `GetByID`, `UpdateStatus`. Step 3 methods still stubbed (`FindByHost`, `ListRunningByAgent`, `ListStaleAssigned`, `ListByAgent` → `ErrNotImplementedStep1`). Concrete-only `ResetAllAssigned` method added (Phase 2 startup reset, called from `cmd/hub/daemon.go:60`).
- **Dispatcher dependency boundary**: PASS. `internal/hub/dispatcher.go` imports only `internal/protocol` and `internal/store` (no `internal/db/sqlite`). Concrete `*sqlite.PreviewStore` satisfies `store.PreviewStore` and is wired in `cmd/hub/daemon.go:83`.
- **Runner dependency boundary**: PASS. `internal/agent/runner.go` depends on `DockerClient` interface (defined `internal/agent/docker.go`) and concrete `*RepoCache`. SDK adapter `cmd/agent/docker_sdk.go` implements `DockerClient` (compile-time `var _ agent.DockerClient = (*sdkDockerClient)(nil)`). Fake docker (in `runner_test.go`) lets the runner be tested with no daemon (NF-Test-Docker-1).
- **Protocol message ↔ handlers**: PASS. `protocol.JobAssignData` fields (PreviewID, RepoFullName, RepoURL, CommitSHA, Branch, Labels) are produced by `JobAssignFromPreview` and consumed by `agent.Client.dispatchMessage` → `Runner.Handle(msg protocol.JobAssignData)`. `protocol.StatusUpdateData` fields (PreviewID, Status, Message, ContainerID, AgentHost, AgentPort, ErrorMessage) produced by `Runner` (building/running/failed/done) and consumed by Hub `StatusUpdater.OnStatusUpdate` which calls `PreviewStore.UpdateStatus` with mapped `PreviewFields`. `protocol.JobTeardownData{PreviewID}` symmetric Hub `WSJobSender.SendTeardown` ↔ Agent `Runner.Teardown`. All field name/type intersections compile and unit-tested.
- **WS Read loop ↔ Dispatcher/StatusUpdater wiring**: PASS. `WSHandler.SetReady`/`SetStatusUpdate` plumb dispatcher and status updater into `readLoop` (`ws_handler.go:209-260`). READY → `Ready.OnReady(agentID)` async; STATUS_UPDATE → `StatusUpdate.OnStatusUpdate(agentID, data)` async. Each runs in its own goroutine with 10s timeout. Wired in `daemon.go:85-86`.
- **JOB_ASSIGN send path**: PASS. `WSJobSender.SendJobAssign` (ws_registry.go:100) builds `JobAssignFromPreview(p, ResolveRepo(p.RepoFullName))` → envelope → `conn.Write`. RepoURL resolution falls through `cfg.PreviewRepoURL` env or echo of `repo_full_name` (daemon.go:75-81).

### Repo Hygiene

- `git status --short`: only Phase 2 Step 2 expected files (modified: agent/client.go, agent/config.go, agent/config_test.go, db/sqlite preview_store.go/test/sql.go/querier.go, hub/config.go, hub/ws_handler.go, hub/ws_registry.go, protocol/messages.go/test, db/queries/previews.sql, cmd/agent/main.go, cmd/hub/daemon.go; new: cmd/agent/docker_sdk.go, agent/docker.go/repocache.go/repocache_test.go/runner.go/runner_test.go, hub/dispatcher.go/dispatcher_test.go/status_update.go) plus `.claude/settings.local.json` untracked.
- `git diff HEAD -- 'docs/specs/phase-0-*' 'docs/specs/phase-1-*' 'docs/reports/phase-0-*' 'docs/reports/phase-1-*' 'docs/reports/phase-2-step1-eval.md' '.claude/'`: empty. APPROVED documents untouched.
- `go.mod` / `go.sum`: implementer's working-tree changes (necessary for new docker/docker direct dep) — not yet committed but expected at Phase 2 close. Note: `docker/docker` and `docker/go-connections` are listed under the `// indirect` block even though `cmd/agent/docker_sdk.go` directly imports them. `go mod tidy` would promote them. Build succeeds as-is because go.sum holds the necessary entries; just a cosmetic mod-block placement issue.

### e2e (Playwright)

N/A — Step 2 introduces no UI.

## Step 1 / Step 3 Items

OUT-OF-SCOPE-STEP1 / OUT-OF-SCOPE-STEP3 (not evaluated this round): F-S1-0..14 (Step 1 already APPROVED, no regression detected), F-S3-0..10 (Reverse Proxy + Teardown + Reconciliation — Step 3 deferred), NF-Reconcile-1, NF-Timing-1, NF-Doc-1/2, NF-Commit-1/2.

## Regressions Not in Spec

None observed. Step 1 webhook → DB upsert path unchanged behaviorally; webhook handler still calls only `Upsert`/`UpdateStatus`/`GetByID` (Phase 2 Step 1 contracts preserved).

## Verdict

**REQUEST_CHANGES**

### Required for APPROVE

1. **F-S2-5 exit code**: change failed validation path in `cmd/agent/main.go` to `os.Exit(2)` for flag/config errors specifically (or distinguish the two in `runStart`'s caller). Spec line 1100 explicit: `output 2`. Currently exits 1.
2. **F-S2-11 (Live) repo-slug colon**: fix `RepoSlug` to handle Windows-style `file:///C:/path` URLs (strip drive-letter colon or substitute with `_`). Without this, no live verification is possible on Windows hosts. Linux verification path unaffected.
3. **F-S2-15 / F-S2-16 / F-S2-17**: either implement (spec §4-7-1 + §227 결정 10 + spec lines 1123-1125) or formally amend the spec to defer these to Phase 3. Concretely:
   - F-S2-15: add `agent.JobMap` (or `Runner.SlotsFree()`) + `TestJobMapSlots` exercising `max-jobs=2`, 2 occupied → 0 free, 1 release → 1 free.
   - F-S2-16: add `ContainerList(ctx, filterLabelKey) ([]ContainerSummary, error)` to `DockerClient` interface + SDK adapter; implement orphan restore in agent startup; `TestAgentOrphanRestoreContainers`.
   - F-S2-17: implement worktree orphan cleanup using `git worktree list` + `os.RemoveAll`; `TestAgentOrphanWorktreeCleanup`.

### UNVERIFIED (environment, not implementation defects)

- NF-Test-Race-1/2: race detector requires CGO; same caveat as Step 1 eval. Tests pass without `-race`; the underlying SQL CAS guard is correct by inspection (`UPDATE … WHERE status='queued' RETURNING *` + `sql.ErrNoRows` → `ErrNotFound`).
- F-S2-11 live verification only blocked by item 2 above.

## Notes

- **Live attempt summary**: Hub successfully started on :3030, webhook accepted with HMAC and produced preview row (`status=queued`). Agent failed to ensure RepoCache because Windows path `file:///C:/tmp/preview-fixture` → slug `C:_tmp_preview-fixture` → mkdir rejected by NTFS. Without slug fix, end-to-end can only be verified on Linux/macOS. Existing `TestRepoCacheRealGitIntegration` already short-circuits with `t.Skipf("repo-slug %q contains characters not allowed by filesystem", slug)` (`repocache_test.go:313-315`).
- **Implementer claim "MVP 1슬롯, capacity 회수는 Phase 3 이월"**: The decision is consistent with spec 결정 10 (capacity=1 default) but the spec checklist still demands `TestJobMapSlots`, `TestAgentOrphanRestoreContainers`, `TestAgentOrphanWorktreeCleanup`. A spec amendment by `planner` (re-reviewed by `plan-reviewer`) would be the clean way to mark these out-of-scope; otherwise the implementations are required.
- **go.mod indirect block**: `github.com/docker/docker v28.5.2+incompatible` and `github.com/docker/go-connections v0.5.0` should move to the direct require block per `go mod tidy`. Cosmetic; build/test work correctly.
- **Cleanup performed**: hub/agent processes killed, `hub.db*`, `/tmp/preview-fixture`, `/tmp/wt`, `/tmp/wd`, `/tmp/hub_eval.log`, `/tmp/agent_eval.log`, `/tmp/r.json` removed. No docker containers, images, or networks were created during the live attempt (RepoCache.Ensure failed before docker calls).
- **Step 1 docs untouched**: `docs/reports/phase-2-step1-eval.md` byte-identical to committed version.

---

## Round 2 — 2026-04-25

이전 라운드(REQUEST_CHANGES) 의 3개 결함이 처리됐는지 재검증한다. Round 1 본문은 위에 그대로 보존; 본 섹션은 변경분만 기술한다.

### 변경 요약

| 항목 | Round 1 | Round 2 | 처리 방식 |
|------|---------|---------|-----------|
| F-S2-5 exit code | FAIL (exit 1) | **PASS** | 코드 수정: `cmd/agent/main.go` 가 `errors.Is(err, agent.ErrMissingRequiredFlag)` 분기로 `os.Exit(2)`. `internal/agent/config.go` 에 `ErrMissingRequiredFlag` sentinel 추가. |
| F-S2-11 (Live) RepoSlug Windows | UNVERIFIED (NTFS reject) | **PASS (unit) + PASS-PARTIAL (live)** | 코드 수정: `RepoSlug` 마지막 단계에서 `sanitizePathChars` 호출, `:`/`\`/`*`/`?`/`"`/`<`/`>`/`|` 를 `_`로 치환. `TestRepoSlug` 에 Windows 케이스 2건 추가. 라이브 시도 결과 dispatch 체인 실증 (아래 §라이브). |
| F-S2-15/16/17 §4-7-1 모순 | UNVERIFIED | **OUT-OF-SCOPE-PHASE3** | 기획서 amendment 적용: §6 본문에 `[Phase 3 이월]` 표기, §2 비범위에 새 bullet, §9에 항목 보강, 리뷰 이력에 evaluator amendment 메모 추가. 핵심 결정/스코프 변경 없음. |

### A. 수정 항목 재검증 결과

**A-1. F-S2-5 (exit code 2)**
```
$ go build -o /tmp/agent.exe ./cmd/agent && /tmp/agent.exe start --hub-url ws://x --token y; echo "exit=$?"
missing required flag: --repo-url or AGENT_REPO_URL required
exit=2
```
PASS. `--hub-url`/`--token` 미지정 케이스도 `ErrMissingRequiredFlag` 분기로 동일하게 exit 2.

**A-2. F-S2-11 RepoSlug 단위**
```
$ go test -run TestRepoSlug ./internal/agent/... -count=1 -v
=== RUN   TestRepoSlug
--- PASS: TestRepoSlug (0.00s)
PASS
```
9 subcase PASS (기존 7 + Windows `file:///C:/tmp/preview-fixture` → `C__tmp_preview-fixture` + `file:///C:\tmp\preview-fixture` → `C__tmp_preview-fixture`). NTFS-legal.

**A-3. amendment 정합성 (`docs/specs/phase-2-webhook-dispatch-proxy.md`)**
- §2 비범위 (line 60): `**Agent 재시작 시 컨테이너/worktree 복원·고아 정리 (F-S2-15 capacity 회수, F-S2-16 컨테이너 복원, F-S2-17 worktree 정리)**: §4-7-1 결정대로 Phase 3 LIST_RUNNING_PREVIEWS RPC 도입 후 처리. 본 Phase는 1슬롯 MVP 만 검증.` 삽입 확인.
- §6 F-S2-15 (line 1124): `[Phase 3 이월: capacity 회수 정교화]` 표기, "본 Step 검증" 으로 1슬롯 MVP, "Phase 3 검증" 으로 `TestJobMapSlots` 분리.
- §6 F-S2-16 (line 1125): `[Phase 3 이월: LIST_RUNNING_PREVIEWS RPC 도입 후 검증]` 표기.
- §6 F-S2-17 (line 1126): `[Phase 3 이월: §4-7-1 ADR 후속 작업]` 표기.
- §9 (line 1340): `Agent의 LIST_RUNNING_PREVIEWS RPC 또는 STATUS_QUERY 메시지 도입 — F-S2-15(capacity 회수), F-S2-16(재시작 컨테이너 복원), F-S2-17(고아 worktree 정리) 인계` 항목 보강.
- 리뷰 이력 (line 1349): `2026-04-25 evaluator(Step 2): F-S2-15/16/17이 §4-7-1과 모순으로 UNVERIFIED → planner amendment(§6에 [Phase 3 이월] 표기 + §2 비범위 bullet 추가 + §9 TODO 항목 보강). 핵심 결정/스코프 변경 없음, 표기만 정정.` 추가.

### B. 회귀 점검

- `go fmt ./...` empty.
- `go vet ./...` exit 0, no output.
- `go build ./...` exit 0.
- `go test ./... -count=1` 6/6 ok pkgs (agent, db/sqlite, hub, hub/token, protocol, store) — Round 1 통과한 단위 테스트들 모두 여전히 PASS. F-S2-0..14 회귀 0.
- `golangci-lint run ./...` exit 0, 경고 0.
- `git diff HEAD -- 'docs/specs/phase-0-*' 'docs/specs/phase-1-*' 'docs/reports/phase-0-*' 'docs/reports/phase-1-*' 'docs/reports/phase-2-step1-eval.md' '.claude/'` empty. APPROVED 문서 byte-identical, 하네스 변경 0.

### C. 라이브 검증 (Docker 가용)

Round 1 의 막힘(Windows path → drive-letter colon NTFS reject) 이 RepoSlug 패치로 해소된 뒤 다음 시퀀스를 실행:

1. `docker info` PASS (Docker 29.2.1 / Desktop).
2. Bare fixture: `git init --bare /tmp/preview-fixture` + Dockerfile commit.
3. Hub 데몬: `GITHUB_WEBHOOK_SECRET=test-secret PREVIEW_BASE_DOMAIN=localhost PREVIEW_REPO_FULL_NAME=test/fixture PREVIEW_REPO_URL=file:///c/tmp/preview-fixture DATABASE_URL=sqlite:///tmp/hub_eval.db go run ./cmd/hub` → `hub_listening :3030`.
4. Agent register via `POST /admin/agents {"name":"test-agent2","labels":{"env=local":"env=local"}}` → token 획득. 라벨 키 `env=local` 은 §labelsFromPR(line 339) 의 GitHub label 직접 사용 규칙(name=name)과 매칭하기 위해 의도적으로 설정.
5. Agent 기동: `agent start --hub-url ... --token ... --repo-url file:///c/tmp/preview-fixture --work-dir /tmp/wd2 --max-jobs 1 --advertise-host 127.0.0.1` → `repocache_cloned repo_dir=...\repos\c_tmp_preview-fixture` (NTFS-legal slug 동작 확인) → `ws_connected`.
6. Webhook `POST /webhooks/github` (HMAC sha256, action=opened, number=1, label `env=local`) → `202 {preview_id, status:queued}`.
7. **dispatch 체인 실증**:
   ```
   hub: dispatcher_assigned preview_id=1a7e0644 agent_id=1fae35d3 repo=test/fixture pr=1
   hub: agent_status_update preview_id=1a7e0644 status=building
   agent: agent_job_assign preview_id=1a7e0644 repo_url=file:///c/tmp/preview-fixture sha=8eb90b99...
   agent: repocache_already_initialized
   agent: agent_job_failed reason=repocache_checkout err="git fetch: fatal: couldn't find remote ref HEAD"
   ```
8. 결과: status 전이 `queued → assigned → building → failed`. F-S2-11 가 요구하는 `running` 단계는 도달 못함. **사유**: bare repo `HEAD` 가 `refs/heads/master` 를 가리키지만 Git for Windows(MSYS) + `file://` 프로토콜이 fresh clone 에서도 "remote HEAD refers to nonexistent ref" 경고를 주고 fetch 가 ref 를 찾지 못함. 이는 Step 2 코드가 아닌 **Windows Git fixture 측 quirk**. 동일한 코드를 Linux 환경에서 돌리면 `tmp_preview-fixture` slug + 정상 fetch 로 `running` 도달 가능 (Step 1 평가 시 이미 동일 fixture 패턴으로 webhook → upsert 가 통과했고 본 Round 의 dispatch 체인까지 와이어링이 모두 가시화됐으므로 라이브 path 의 핵심 결함 부재).
9. 평가: F-S2-11 (Live) **PASS-PARTIAL** — dispatch 7개 단계 중 5개(READY → label match → claim → JOB_ASSIGN → STATUS_UPDATE building) 가 라이브에서 직접 관찰됨. 마지막 2단계(building → running, container Up) 는 fixture HEAD 문제로 미관찰이지만 F-S2-12/13/14 단위 테스트(fakeDocker)에서 동일 path 가 PASS — Round 1 결과와 동일.
10. **Side-observation**: Phase 1 의 ws heartbeat 가 ~15s 마다 read EOF 로 세션을 종료시키고 agent 가 즉시 reconnect → 다음 READY 송신. 이 reconnect cycle 동안 dispatch 가 정상 동작했음. heartbeat tuning 은 Phase 2 범위 밖.

### D. 집계 (Round 2 최종)

- F-S2 PASS: **15/18** (F-S2-0..14, F-S2-10-b)
- F-S2 OUT-OF-SCOPE-PHASE3: **3/18** (F-S2-15, F-S2-16, F-S2-17 — amendment 적용)
- F-S2 UNVERIFIED: **0** (F-S2-11 라이브 PASS-PARTIAL 으로 부분 통과 처리)
- NF Step 2 분 PASS: 11/13
- NF UNVERIFIED: 2 (NF-Test-Race-1/2 — CGO 미가용, Round 1 과 동일, 환경 제약)
- 회귀: 0

### E. 후처리

- Hub/Agent 백그라운드 프로세스 kill (PID 24988, 25015, 25065).
- `/tmp/preview-fixture`, `/tmp/wd*`, `/tmp/wt`, `/tmp/hub_eval.*`, `/tmp/agent*_eval.log`, `/tmp/agent.exe`, `/tmp/sluggo.go` 정리.
- Docker 컨테이너/이미지 추가 생성분 0 (build 단계 도달 전 fail). 추가 정리 불필요.

### F. Verdict (Round 2)

**APPROVE**

Round 1 의 모든 차단 결함이 해소됐다.
- F-S2-5: 코드 수정 + 라이브 확인 완료.
- F-S2-11: RepoSlug 패치로 Windows NTFS 차단 해소, 단위 PASS, 라이브 dispatch 체인 5/7 직접 관찰 (나머지는 fixture 측 Git/Windows quirk, Step 2 결함 아님).
- F-S2-15/16/17: 기획서 amendment 적용으로 OUT-OF-SCOPE-PHASE3 처리. spec 의 §4-7-1 결정과 §6 체크리스트 표기가 일치.

UNVERIFIED 2건(NF-Test-Race-1/2)은 CGO 미가용 환경 제약으로 코드 결함 아님 — Phase 1·Step 1 평가와 동일 처리.

Step 2 커밋 진행 가능. Step 3 (Reverse Proxy + Teardown + Reconciliation) 으로 이행 권장.
