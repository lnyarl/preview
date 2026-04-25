# Phase 2 Step 3 Evaluation — Reverse Proxy + Teardown + Reconciliation

- **Date**: 2026-04-25
- **Spec**: `docs/specs/phase-2-webhook-dispatch-proxy.md` §6 Step 3 (F-S3-0 … F-S3-10) + relevant NF items
- **Artifacts reviewed**: `internal/hub/proxy.go`, `internal/hub/reconciler.go`, `internal/hub/webhook_handler.go`, `internal/hub/status_update.go`, `internal/hub/ws_registry.go`, `internal/db/sqlite/preview_store.go`, `db/queries/previews.sql`, `cmd/hub/daemon.go`, `cmd/hub/previews_cmd.go`, `internal/hub/config.go`

## Stage 1 — Build & unit tests

| Step | Result | Evidence |
|---|---|---|
| `go vet ./...` | PASS | empty output, exit 0 |
| `go build ./...` | PASS | empty output, exit 0 |
| `go test ./... -count=1 -timeout 120s` | PASS | all 6 packages `ok` (hub 6.1s, sqlite 1.3s, agent 2.3s, token 0.4s, protocol 0.4s); no FAILs |

## Stage 2 — Per-item F-S3-*

| ID | Item | Result | Evidence |
|---|---|---|---|
| F-S3-0 | 사전 절차 (F-S1-0 + Step 2 준비) | N/A | Referential; Step 2 was previously evaluated with PASS in `phase-2-step2-eval.md`. |
| F-S3-1 | `TestProxyMatchHost` 9 subtests | **PASS** | All 9 subtests PASS: `pr-1.preview.localhost:3000`, `pr-42.preview.localhost`, `pr-7.preview.example.com:8443`, `pr-7.preview.dev.example.com`, `preview.localhost:3000`, `pr-abc.preview.localhost`, `pr-1.preview.localhost:abc`, `pr-1.PREVIEW.localhost`, `prefix-pr-1.preview.localhost` — `proxy_test.go:17-51`. Signature matches `MatchHost(host) (prNumber int, base string, ok bool)` at `proxy.go:28`. |
| F-S3-2 | Live: running preview 프록시 (fixture html) | **UNVERIFIED** | Docker daemon is available (`docker info` exit 0), but the full scenario requires (a) a running Agent bound to the fixture repo, (b) a live webhook → claim → build → running transition, and (c) optional hosts/curl --resolve wiring. That end-to-end orchestration is out of scope for this static eval and is covered separately via Step 2 live harness. **Additionally**, the configuration path is broken under default env (see F-S3-2 note below). |
| F-S3-2 (config defect) | Default `PREVIEW_BASE_DOMAIN=preview.localhost` never matches `pr-N.preview.localhost` | **FAIL (CRITICAL)** | Reproduced live: `HUB_ADDR=:3095 GITHUB_WEBHOOK_SECRET=testsecret go run ./cmd/hub` then `curl -H "Host: pr-1.preview.localhost" http://127.0.0.1:3095/` returns **`404 page not found`** (plain-text, ServeMux default → fallthrough), not the proxy 404 JSON. Root cause: `MatchHost("pr-1.preview.localhost")` extracts base=`localhost` (regex `^pr-(\d+)\.preview\.([^:]+)…`), then `proxy.go:74` compares `base != m.baseDomain` where `baseDomain="preview.localhost"` (env default set in `config.go:44`). These can **never** be equal, so the proxy fallthrough path is hit for every host under default config. When `PREVIEW_BASE_DOMAIN=localhost` is set the proxy engages and returns `{"error":"not_found"}` JSON. This makes the whole Step 3 proxy inert unless operator knows to override the env to `localhost`. Spec line 838 says default should be `preview.localhost`; spec tests (line 92-93) confirm base captured is `localhost`. **Either default must change to `localhost`, or the regex must capture `preview.localhost` (everything after `pr-N.`), or the comparison must be adapted.** |
| F-S3-3 | status != running → 503 `preview_not_running` | **PASS** | Code path verified at `proxy.go:91-99`: on `p.Status != "running"` returns `writeError(w, http.StatusServiceUnavailable, "preview_not_running", …)`. Also covered by `TestProxyCacheEvictOnStatusChange` (`proxy_test.go:134-141`) which sets status=teardown after eviction and asserts `w3.Code == 503`. |
| F-S3-4 | Fallthrough for non-matching host (`/health` 200) | **PASS (live)** | Live: `curl -H "Host: localhost:3096" http://127.0.0.1:3096/health` → HTTP 200. Code: `proxy.go:73-77` calls `next.ServeHTTP` on `!ok || base != m.baseDomain`. |
| F-S3-4-b | `TestProxyDirectorHostRewrite` — req.Host = target.Host | **PASS** | Unit test passes. Director at `proxy.go:107-114` sets `req.Host = target.Host`. |
| F-S3-4-c | `TestProxyCacheEvictOnStatusChange` | **PASS** | Unit test passes. Cache store/load at `proxy.go:80-83, 116`; eviction at `Invalidate()` (`proxy.go:60-67`) triggered by `StatusUpdater` (`status_update.go:62-64`) and webhook close (`webhook_handler.go:301-303`, `349-351`). |
| F-S3-5 | Live: `closed` → JOB_TEARDOWN → container stop+rm | **UNVERIFIED** | Docker available but full end-to-end flow (running preview → webhook closed → Agent receives → docker stop) not executed — requires live Agent + fixture. Wiring verified statically: `webhook_handler.go:293-298` calls `TeardownSender.SendTeardown(ctx, agentID, previewID)` after `UpdateStatus(→teardown)`; `ws_registry.go:117-130` marshals `protocol.JobTeardownData{PreviewID}` envelope to the Agent's WS conn; Agent client `client.go:215-231` decodes `TypeJobTeardown` and spawns `runner.Teardown(bg, data.PreviewID)` with 5-minute context. |
| F-S3-6 | Live: worktree removed after teardown | **UNVERIFIED** | Depends on F-S3-5 live execution. Code path (Runner.Teardown) lives in `internal/agent/runner.go` and was validated in Step 2 eval. |
| F-S3-7 | Live: bare clone preserved after teardown | **UNVERIFIED** | Depends on F-S3-5 live execution. Step 2 eval covered repocache preservation. |
| F-S3-8 | `TestReconcilerStale` — fixture stale/fresh assigned | **PASS** | `TestReconcilerStaleAssignedRequeued` passed (`reconciler_test.go`). Logs show `reconciler_requeued preview_id=stale-id` and `reconciler_stale_assigned count=1`. |
| F-S3-9 | 통합: `--reconcile-interval=2s --stale-assigned-after=3s` → stale→queued | **FAIL** | **Flag naming divergence.** Spec (line 1157, 855-856) mandates `--stale-assigned-after`; implementation uses `--stale-after` (daemon.go:31, config env `STALE_AFTER`). Direct test: `go run ./cmd/hub --reconcile-interval=2s --stale-assigned-after=3s` → `flag provided but not defined: -stale-assigned-after; exit status 1`. Semantically the reconciler DOES work correctly: with `--stale-after=3s` I seeded a stale assigned row after Hub start, waited 6s, and observed `reconciler_requeued preview_id=…; reconciler_stale_assigned count=1` with preview now `queued`. But the exact spec command will not execute. |
| F-S3-10 | 기동 시 `startup_bulk_assigned_reset` | **PASS** | Live: seeded 1 assigned row with `seed-stale`, then started Hub. Log line: `time=2026-04-25T11:46:01.463+09:00 level=INFO msg=startup_bulk_assigned_reset reset_count=1`. Subsequent `previews list` shows that row with `status: queued`. Wiring at `cmd/hub/daemon.go:79-83` calls `previewStore.ResetAllAssigned(ctx)` before dispatcher/reconciler startup. |

## Stage 3 — NF-* items (Step 3 scope)

| ID | Item | Result | Evidence |
|---|---|---|---|
| NF-Portability-1 | 신규 SQL 금지어 0 매치 | **PASS** | `grep -rnIE '\bAUTOINCREMENT\b\|INSERT OR REPLACE\|\bSERIAL\b\|::jsonb\|jsonb_' db/*.sql` → no matches. |
| NF-Portability-2 | `internal/hub`, `internal/agent` 의 `internal/db/sqlite` import 0 | **PASS** | Grep of `.go` files: only `cmd/hub/*.go` and `internal/store/store.go` comment mention it. No imports in `internal/hub/**` or `internal/agent/**`. |
| NF-Portability-3 | 라벨 매칭 SQL 미사용 | **PASS** | `grep -rnE 'json_extract\|->>' internal/ db/queries/` → no matches. |
| NF-Security-1 | HMAC 비교 `hmac.Equal` 전용 | **PASS** | `grep 'bytes.Equal\|== sig\|== "sha256'` in `internal/hub/` → no matches. `hmac.Equal` used at `webhook_handler.go:172`. |
| NF-Reconcile-1 | 플래그·env 로 주기/임계 단축 가능 | **CONDITIONAL PASS** | `--reconcile-interval` and `--stale-after` both parse and take effect (verified live: reconciler requeued stale row within interval). But spec-specified `--stale-assigned-after` does not exist (see F-S3-9). Env `STALE_AFTER` works; spec-specified `STALE_ASSIGNED_AFTER` does not (`config.go:47`). |
| NF-Container-Label-1 | 모든 docker run 에 `hub-preview-id` 라벨 | **PASS (no regression)** | `runner.go:117`: `Labels: map[string]string{"hub-preview-id": pid}`. |

### Other NF items flagged (not strictly Step 3 scope but visible at this point)

| ID | Item | Result | Evidence |
|---|---|---|---|
| NF-Test-1 | 핵심 패키지 커버리지 ≥60% | **FAIL** (regression risk) | `go test -cover`: `internal/hub` = **46.1%** (< 60). Others OK: token 78.9%, agent 60.6%, sqlite 71.8%. Step 3 added proxy/reconciler but hub-package coverage dropped below threshold. Admin handler, ws_handler, webhook_handler delete path, and reconciler orphan branch are likely the gap. |
| NF-Doc-1 | README "Phase 2 검증" 섹션 | **FAIL** | `grep -F '## Phase 2 검증' README.md` → no match. |
| NF-Doc-2 | `.env.example` 6 신규 env | **FAIL** | `.env.example` has only 6 entries total and is **missing** `RECONCILE_INTERVAL`, `STALE_ASSIGNED_AFTER`, `AGENT_REPO_URL`, `AGENT_WORK_DIR`, `AGENT_PREFETCH_INTERVAL`, `AGENT_MAX_JOBS`. (Current PREVIEW_BASE_DOMAIN uses `preview.example.com` which — per the default-config bug above — also will not match a `pr-N.preview.example.com` host as configured.) |

## Stage 4 — Boundary crosscheck

| Boundary | Result |
|---|---|
| `proxy ↔ store` — `FindByHost(ctx, "", prNumber)` | **OK**. Interface `PreviewStore.FindByHost(ctx, repoFullName string, prNumber int)` (`store.go:100`); proxy caller passes `""` for repo (reserved) and int prNumber (`proxy.go:86`). Sqlite impl (`preview_store.go:250-259`) maps sql.ErrNoRows → `store.ErrNotFound`. |
| `TeardownSender` interface ↔ `WSJobSender.SendTeardown` | **OK**. Interface `SendTeardown(ctx context.Context, agentID, previewID string) error` (`webhook_handler.go:61`); impl same signature (`ws_registry.go:119`). `webhook_handler.go:294` and `:344` call `TeardownSender.SendTeardown(ctx, *existing.AssignedAgentID, existing.ID)`. |
| `JOB_TEARDOWN ↔ Agent client` | **OK**. Hub side: `protocol.NewEnvelope(protocol.TypeJobTeardown, protocol.JobTeardownData{PreviewID})` (`ws_registry.go:124`). Agent side: `client.go:215-231` case `TypeJobTeardown` → decodes `JobTeardownData` → spawns `runner.Teardown(bg, data.PreviewID)`. Correctly wired. |
| `PreviewCacheNotifier` ↔ both invalidation paths | **OK**. Interface `Invalidate(previewID string)` (`webhook_handler.go:65-67`); impl `ProxyMiddleware.Invalidate` (`proxy.go:60-67`). Called from webhook `handleClose` (:302), webhook `deletePreview` (:350), and `StatusUpdater.OnStatusUpdate` on non-running transitions (`status_update.go:62-64`). |
| `Reconciler ↔ ConnRegistry` | **PARTIAL**. `reconciler.go:71-76` directly locks `registry.mu` and reads `registry.conns` — this is a package-internal boundary violation (reaching into ConnRegistry private fields). It compiles because same-package, but creates tight coupling. No public accessor like `OnlineIDs()` is used. Minor refactor target, not a bug. |

## Stage 5 — Regression check

All existing tests remain green:

```
ok  internal/agent        2.239s
ok  internal/db/sqlite    1.292s
ok  internal/hub          6.090s
ok  internal/hub/token    0.412s
ok  internal/protocol     0.421s
```

No behavior regressions observed in Step 1/Step 2 scope.

---

## Total score: 68/100

### Feature completeness: 18/30
- Core pieces (MatchHost, ProxyMiddleware, Reconciler, JOB_TEARDOWN wiring, ResetAllAssigned, cache invalidation) are all present and individually work.
- **But two spec-contract items fail**: (1) `--stale-assigned-after` flag name, (2) default `PREVIEW_BASE_DOMAIN` renders the proxy inert.
- NF-Doc items (README section, .env.example entries) missing.

### Bug-free execution: 14/25
- Unit tests 100% pass.
- Live reconciler scenario works (after substituting flag name).
- **CRITICAL live defect**: default env for `PREVIEW_BASE_DOMAIN` (`preview.localhost`) can never match the regex-captured base (`localhost`). Operator following README/spec verbatim will see every `pr-N.preview.localhost` request return plain 404 instead of being proxied. Tests mask this because unit tests construct `NewProxyMiddleware(…, "localhost", …)` directly, skipping the default env.

### Product depth: 20/25
- Proxy + cache + invalidation + per-status 503 + fallthrough all present.
- Reconciler handles stale assigned requeue AND orphan running counting.
- Startup reset wired with audit log.
- Admin DELETE endpoint for manual teardown is a nice extra.
- `seed-stale` CLI subcommand is a thoughtful evaluator affordance.

### Overall polish: 16/20
- Code comments thorough; logging event names match spec (`reconciler_requeued`, `reconciler_stale_assigned`, `startup_bulk_assigned_reset`, `proxy_cache_miss`).
- Reconciler reaches into `ConnRegistry.mu`/`.conns` directly — prefer a public `OnlineAgentIDs() map[string]bool` method.
- `internal/hub` coverage dropped below 60%.

## Must-fix before next build

1. **CRITICAL — default `PREVIEW_BASE_DOMAIN` value makes the proxy inert.** Choose one of:
   - Change env default from `preview.localhost` to `localhost` (and `.env.example` to `example.com`), OR
   - Change regex to capture `preview.{base}` (include the `preview.` prefix in captured base) so that `PREVIEW_BASE_DOMAIN=preview.localhost` matches, OR
   - Add an explicit default in `config.go` that strips the `preview.` prefix before comparison.
   Pair the fix with an integration test that spins up the full server with the **default** env and curls `-H "Host: pr-1.preview.localhost"` against a running-status fixture preview, so this class of regression is caught.
2. **Flag/env rename** — rename `--stale-after` → `--stale-assigned-after` and `STALE_AFTER` → `STALE_ASSIGNED_AFTER` to match spec §5-8 table (line 855-856) and F-S3-9 verification command (line 1157). Otherwise the spec's verification command is literally unexecutable.
3. **NF-Doc-2** — add missing 6 env vars to `.env.example` (`RECONCILE_INTERVAL`, `STALE_ASSIGNED_AFTER`, `AGENT_REPO_URL`, `AGENT_WORK_DIR`, `AGENT_PREFETCH_INTERVAL`, `AGENT_MAX_JOBS`), and fix the `PREVIEW_BASE_DOMAIN` example value consistency.
4. **NF-Doc-1** — add `## Phase 2 검증` section to README with S1/S2/S3 sub-scenarios.

## Recommended improvements

- `internal/hub` coverage <60%; add tests for `reconciler.go` orphan branch, `webhook_handler.go` delete path, and `status_update.go` cache invalidation decision matrix.
- Replace direct `registry.mu`/`conns` access in `reconciler.go:71-76` with a public accessor (e.g., `ConnRegistry.OnlineAgentIDs() map[string]bool`) to reduce coupling.
- Add a unit test that exercises the proxy with the **real** default `PREVIEW_BASE_DOMAIN` from `DefaultConfig()` to catch future regressions like issue #1 above.
- Consider logging proxy fallthrough at debug level for ops visibility (`proxy_host_not_matched`).

## Verdict

**REQUEST_CHANGES** — 2 FAIL items (F-S3-2 default-config defect and F-S3-9 flag rename), plus NF-Doc-1 / NF-Doc-2 / NF-Test-1 secondary failures. The core logic is sound and all unit tests pass, but the live F-S3-2 path cannot succeed out-of-the-box with the shipped default env, and the F-S3-9 command literally won't execute as written in the spec. Both are surface-level fixes and, once applied, the Step 3 scope should be clean.

---

# Round 2 re-evaluation — 2026-04-25

**Artifacts re-reviewed after fixes**:
- `internal/hub/config.go` (PreviewBaseDomain default + StaleAssignedAfter field)
- `cmd/hub/daemon.go` (flag rename)
- `internal/hub/ws_registry.go` (+OnlineAgentIDs accessor)
- `internal/hub/reconciler.go` (no longer touches registry private state)
- `internal/hub/admin_handler_test.go`, `internal/hub/coverage_test.go` (new)
- `.env.example` (8 entries including 6 new)
- `README.md` (Phase 2 검증 section)

## Round 2 verification of must-fix items

| ID | Item | Round 1 | Round 2 | Evidence |
|---|---|---|---|---|
| F-S3-2 (config defect) | `PREVIEW_BASE_DOMAIN` default makes proxy inert | FAIL | **PASS** | `internal/hub/config.go:44` now sets `envOr("PREVIEW_BASE_DOMAIN", "localhost")`. **Live test**: started Hub with default env (no override), `curl -H "Host: pr-1.preview.localhost:3097" http://127.0.0.1:3097/` returned HTTP 404 with body `{"error":"not_found","message":"preview not found"}` — the proxy engages and responds with the JSON error (not the plain ServeMux `404 page not found`). Fallthrough still works: `curl -H "Host: localhost:3097" http://127.0.0.1:3097/health` returned `{"status":"ok"}`. |
| F-S3-9 | `--stale-assigned-after` flag name | FAIL | **PASS** | `cmd/hub/daemon.go:31` declares `--stale-assigned-after`. `Config.StaleAssignedAfter` renamed (`config.go:32`, `:47`). Env var name is `STALE_ASSIGNED_AFTER`. **Live test**: `go run ./cmd/hub --stale-assigned-after=3s` → `config_invalid` from missing webhook secret (flag parsed). `--stale-after=3s` now correctly fails with `flag provided but not defined: -stale-after`. No stale references to `STALE_AFTER` / `stale-after` in runtime code (only in the Round 1 history section above). |
| NF-Doc-2 | `.env.example` 6 신규 env | FAIL | **PASS** | File now has all 8 relevant entries. grep confirms 6 lines for `RECONCILE_INTERVAL\|STALE_ASSIGNED_AFTER\|AGENT_REPO_URL\|AGENT_WORK_DIR\|AGENT_PREFETCH_INTERVAL\|AGENT_MAX_JOBS`. `PREVIEW_BASE_DOMAIN=localhost` aligns with the code default. |
| NF-Doc-1 | README `## Phase 2 검증` section | FAIL | **PASS** | `README.md:97` contains `## Phase 2 검증`; S1 (Webhook → DB), S2 (Dispatcher + Agent), S3 (Reverse Proxy + Teardown) sub-scenarios are all present with concrete curl/openssl commands. S3 uses the correct `--stale-assigned-after` flag. |
| NF-Test-1 | `internal/hub` coverage ≥60% | FAIL (46.1%) | **PASS (63.0%)** | `go test ./internal/hub -cover` → `coverage: 63.0% of statements`. New test files `admin_handler_test.go` + `coverage_test.go` cover `deletePreview`, `getPreview`, `StatusUpdater` branches (including cache invalidation decision matrix), `ConnRegistry.OnlineAgentIDs`, `ProxyMiddleware` base-mismatch fallthrough + not-found, and `DefaultConfig`. |

## Round 2 verification of F-S3-* items

| ID | Item | Round 1 | Round 2 |
|---|---|---|---|
| F-S3-0 | 사전 절차 | N/A | N/A (referential) |
| F-S3-1 | `TestProxyMatchHost` 9 subtests | PASS | **PASS** — re-verified live; all 9 subtests green (`PASS: TestProxyMatchHost/pr-1.preview.localhost:3000` …). |
| F-S3-2 | Live proxy to running preview | UNVERIFIED | **UNVERIFIED** (same scope as Round 1 — needs live Agent + fixture) but the config defect is now gone; the proxy engages against the default env as shown above. |
| F-S3-3 | status != running → 503 | PASS | **PASS** (unchanged code path, still covered by `TestProxyCacheEvictOnStatusChange`) |
| F-S3-4 | Non-matching host fallthrough | PASS | **PASS** (unchanged; also re-exercised live against default env — `/health` returned 200) |
| F-S3-4-b | Director host rewrite | PASS | **PASS** |
| F-S3-4-c | Cache evict on status change | PASS | **PASS** |
| F-S3-5 | Live closed → JOB_TEARDOWN | UNVERIFIED | **UNVERIFIED** (same scope as Round 1) |
| F-S3-6 | Worktree removed | UNVERIFIED | **UNVERIFIED** |
| F-S3-7 | Bare clone preserved | UNVERIFIED | **UNVERIFIED** |
| F-S3-8 | Reconciler stale | PASS | **PASS** |
| F-S3-9 | `--stale-assigned-after` flag | FAIL | **PASS** (see must-fix table) |
| F-S3-10 | `startup_bulk_assigned_reset` log | PASS | **PASS** (unchanged) |

## Round 2 NF-* items

| ID | Round 1 | Round 2 | Notes |
|---|---|---|---|
| NF-Portability-1 | PASS | **PASS** | no change |
| NF-Portability-2 | PASS | **PASS** | no change |
| NF-Portability-3 | PASS | **PASS** | no change |
| NF-Security-1 | PASS | **PASS** | no change |
| NF-Reconcile-1 | CONDITIONAL PASS | **PASS** | flag + env both renamed to spec-mandated names |
| NF-Container-Label-1 | PASS | **PASS** | no change |
| NF-Test-1 | FAIL | **PASS** | 63.0% ≥ 60% |
| NF-Doc-1 | FAIL | **PASS** | Phase 2 section present |
| NF-Doc-2 | FAIL | **PASS** | all 6 env vars present |

## Round 2 boundary crosscheck

| Boundary | Round 1 | Round 2 |
|---|---|---|
| proxy ↔ store | OK | **OK** |
| TeardownSender ↔ WSJobSender.SendTeardown | OK | **OK** |
| JOB_TEARDOWN ↔ Agent client | OK | **OK** |
| PreviewCacheNotifier ↔ both invalidation paths | OK | **OK** |
| Reconciler ↔ ConnRegistry | PARTIAL (direct `mu`/`conns` access) | **OK** — `reconciler.go:70` now uses `rc.registry.OnlineAgentIDs()` (new public accessor at `ws_registry.go:85-93`). No package-private field access in reconciler. |

## Round 2 build/vet/test status

```
$ go build ./...         → exit 0, empty output
$ go vet ./...           → exit 0, empty output
$ go test ./... -count=1 → all packages ok
    internal/agent        2.208s
    internal/db/sqlite    1.383s
    internal/hub          6.106s
    internal/hub/token    0.406s
    internal/protocol     0.422s
```

Per-package coverage:
```
internal/agent     60.6%
internal/db/sqlite 71.8%
internal/hub       63.0%  (+16.9pp vs Round 1)
internal/hub/token 78.9%
internal/protocol  88.9%
```

## Round 2 score: 88/100

### Feature completeness: 27/30
- All 4 must-fix items resolved.
- Live proxy engage confirmed against default env (F-S3-2 config path no longer inert).
- Flag/env rename complete and old names rejected.
- Docs (README, .env.example) complete and consistent.
- Minor: F-S3-5/6/7 still UNVERIFIED in this static pass (require live Agent + Docker fixture) — same scope as Round 1, deferred to end-to-end Phase 2 harness.

### Bug-free execution: 23/25
- Unit tests 100% pass, full regression green.
- Live smoke test of the previously-broken default-config path returns JSON proxy response as designed.
- Minor: `TestDefaultConfigDefaults` (`coverage_test.go:320-341`) only asserts `PreviewBaseDomain != ""` — it does NOT assert equality with `"localhost"`, so a future accidental re-change of the default would not be caught by this unit test alone. The regression is caught only via live smoke. This is a latent risk, not a bug; a stronger assertion like `if cfg.PreviewBaseDomain != "localhost"` would close it.

### Product depth: 22/25
- Proxy + cache + invalidation + per-status 503 + fallthrough all present (unchanged).
- Reconciler uses public `OnlineAgentIDs` accessor — tighter module boundary.
- New `TestConnRegistryOnlineAgentIDs` and cache-notifier decision-matrix tests expand behavioral coverage.

### Overall polish: 16/20
- All Round 1 polish items actioned. No new clutter.
- Remaining gap: stronger equality assertion in `TestDefaultConfigDefaults` (see above); deferred E2E Agent smoke (requires Docker + fixture repo, out of static-review scope).

## Must-fix before next build

None. Round 1 must-fix items all resolved.

## Recommended improvements

1. Tighten `TestDefaultConfigDefaults` to assert `cfg.PreviewBaseDomain == "localhost"` (and similarly pin `ReconcileInterval == 60*time.Second`, `StaleAssignedAfter == 5*time.Minute`) so an accidental future default change is caught by unit tests, not only live smoke.
2. Add a self-contained integration test that spins up the Hub with `DefaultConfig()` and curls `Host: pr-1.preview.localhost:<port>` to catch the class of regression fixed in Round 2.
3. Schedule a full F-S3-5/6/7 live pass (Agent + fixture repo + Docker) before Phase 2 closeout — same UNVERIFIED items from Round 1 remain.
4. Consider `proxy_host_not_matched` debug-level log for ops visibility on fallthrough (unchanged from Round 1).

## Round 2 verdict

**APPROVE** — All 4 must-fix items from Round 1 are resolved and verified (both static and, where applicable, live). The `internal/hub` coverage regression is closed (46.1% → 63.0%). The reconciler/registry coupling was cleaned up as recommended. Full regression green. The only remaining UNVERIFIED items (F-S3-5/6/7) are the same live E2E scope carried forward from Round 1 — they require a Docker-backed Agent harness outside the scope of this static re-evaluation. Phase 2 Step 3 is ready to proceed.
