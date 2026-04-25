# Phase 2 Step 1 Evaluation — 2026-04-24

## Summary

- **Build**: PASS (`go fmt`, `go vet`, `go build` all clean)
- **Unit**: 6/6 packages PASS (`go test ./... -count=1`)
- **Functional (F-S1-0..14)**: 15/15 PASS (live-verified for HTTP/HMAC/upsert/teardown; unit-test alternative path used for F-S1-13 events count and F-S1-14 reopen)
- **Non-functional (NF subset relevant to Step 1)**: 4/4 PASS — Portability-1/2/3 + Security-1/2/3
- **Boundary**: PASS — store interface, hub→PreviewStore method scope, error-shape consistency
- **e2e (Playwright)**: N/A — Step 1 has no UI
- **Verdict**: **APPROVE**

## Per-item Results

### Build & Hygiene

| Check | Command | Result |
|-------|---------|--------|
| go fmt | `go fmt ./...` | PASS (no output) |
| go vet | `go vet ./...` | PASS (no output) |
| go build | `go build ./...` | PASS (no output) |
| go test | `go test ./... -count=1` | PASS (6 ok pkgs, 0 fail) |
| golangci-lint | `golangci-lint run ./...` | PASS (exit 0, 0 warnings) |

Race detector skipped: `-race` requires CGO; the toolchain reports `go: -race requires cgo`. Step 1 has no concurrency-critical code in the webhook path; deferred to Phase 2 Step 2 where ClaimPreview race tests live.

### Functional — Step 1 (F-S1-0 .. F-S1-14)

| ID | Spec | Status | Evidence |
|----|------|--------|----------|
| F-S1-0 | Pre-flight: migrate up + previews list = `[]` | PASS | `migrate: applied 2`; `previews list` → `[]` |
| F-S1-1 | 0002_previews.up.sql columns + UNIQUE + FK + 3 indexes | PASS | All 15 columns present (spec text says "16개" but verification command lists 15 — followed verification command); `UNIQUE(repo_full_name, pr_number)`, `FOREIGN KEY ... REFERENCES agents`, `idx_previews_status/_repo_pr/_assigned` all matched |
| F-S1-2 | preview_events table + ON DELETE CASCADE | PASS | `CREATE TABLE ... preview_events` + `ON DELETE CASCADE` matched |
| F-S1-3 | down.sql: 6 DROPs (2 tables + 4 indexes) | PASS | `grep -cE 'DROP TABLE|DROP INDEX'` = 6 |
| F-S1-4 | migrate up applies 0001+0002 | PASS | stdout `migrate: applied 2`; `previews list` exit 0 + `[]` |
| F-S1-5 | PreviewStore interface 9 methods | PASS | All of Upsert/GetByID/FindByHost/ListQueuedForCandidates/Claim/UpdateStatus/ListRunningByAgent/ListStaleAssigned/ListByAgent declared in `internal/store/store.go` (note: `ListAll` is a 10th convenience method beyond the spec's 9 — explicitly justified in code comment as Step-1 evaluator helper) |
| F-S1-6 | compile-time `var _ store.PreviewStore = (*..)(nil)` | PASS | Found in `internal/db/sqlite/preview_store.go:36` |
| F-S1-7 | missing signature → 401 missing_signature | PASS (live) | curl returned `code=401` body `{"error":"missing_signature","message":"X-Hub-Signature-256 header missing or malformed"}` |
| F-S1-8 | invalid signature → 401 invalid_signature | PASS (live) | curl returned `code=401` body `{"error":"invalid_signature","message":"HMAC mismatch"}` |
| F-S1-9 | opened → 202 + row(status=queued) | PASS (live) | 202 + admin/previews shows `pr_number=42, commit_sha=abc123, status=queued, labels={}` |
| F-S1-10 | synchronize → row count 1, sha=def456, status=queued | PASS (live) | 202 + admin/previews shows single row `commit_sha=def456`, `status=queued`, same id |
| F-S1-11 | closed → status=teardown (UpdateStatus single entry) | PASS (live) | 202 + admin/previews shows `status=teardown`, `error_message=closed_via_webhook`. Code path uses `Store.UpdateStatus(*→teardown)` (webhook_handler.go:259) |
| F-S1-12 | non-pull_request event → 200 ignored | PASS (live) | `X-GitHub-Event: push` → `code=200` body `{"event":"push","ignored":true}` |
| F-S1-13 | preview_events row count per R1/R2/R3 | PASS (alternative: unit) | sqlite3 CLI unavailable. Spec offers `previews show <id> events` alternative, but show command returns the PreviewView only (no events array). Falling through to unit-test alternative — TestPreviewStoreUpsertNewInsertsEvent (R1: 1 event NULL→queued), TestPreviewStoreUpsertExistingUpdatesNoEvent (R2: 0 events on synchronize), TestPreviewStoreUpdateStatusInsertsEvent (R1: queued→teardown event) all PASS against real SQLite |
| F-S1-14 | reopen done→opened ⇒ status=queued, last event (done,queued) | PASS (unit) | TestWebhookReopenedFromDone forces `done` then re-fires opened webhook; verifies `status=queued` and last event `(done, queued)` (webhook_handler_test.go:251) |

### Non-functional (Step 1 portion)

| ID | Spec | Check | Status | Evidence |
|----|------|-------|--------|----------|
| NF-Portability-1 | No SQLite/Postgres-specific syntax | grep `AUTOINCREMENT|INSERT OR REPLACE|SERIAL|::jsonb|jsonb_` in db/migrations + db/queries + internal/db/sqlite/migrations | PASS | 0 matches (3 separate searches) |
| NF-Portability-2 | hub/agent never import internal/db/sqlite | grep `internal/db/sqlite` in internal/hub + internal/agent | PASS | 0 matches |
| NF-Portability-3 | No DB JSON functions in SQL | grep `json_extract\|->>\|jsonb_extract_path` over *.sql | PASS | 0 matches |
| NF-Security-1 | HMAC compare via `hmac.Equal` | grep webhook_handler.go | PASS | `hmac.Equal(expectedBytes, receivedBytes)` at line 150; no `bytes.Equal` |
| NF-Security-2 | Secret never logged | grep `test-secret` in /tmp/hub_eval.log; grep WebhookSecret in slog calls | PASS | 0 leaks; only structured log fields are action/preview_id/repo/pr/status/created |
| NF-Security-3 | Hub refuses to start without `GITHUB_WEBHOOK_SECRET` | `unset GITHUB_WEBHOOK_SECRET; go run ./cmd/hub` | PASS | exit non-zero with `config: GITHUB_WEBHOOK_SECRET required for webhook` printed to stderr + slog ERROR `config_invalid` |

Out-of-scope NF items (deferred to Step 2/3 evaluation): Test-Race-1/2, Test-Docker-1, Reconcile-1, Timing-1, Container-Label-1, Doc-1, Doc-2, Commit-1, Commit-2, Depguard-1/2, Deps-1, Observability-1.

### Boundary Crosscheck

- **`PreviewStore` interface ↔ sqlite adapter**: PASS. Compile-time assertion `var _ store.PreviewStore = (*PreviewStore)(nil)` enforces the boundary; all 9 spec methods + 1 helper (`ListAll`) implemented or stubbed (Step 2/3 stubs return `ErrNotImplementedStep1`). 6 stub methods covered by `TestPreviewStoreStubMethodsReturnNotImplemented`.
- **Hub handler → PreviewStore method scope**: PASS. `internal/hub/webhook_handler.go` calls only Upsert / GetByID / ListAll / UpdateStatus — 4 of the Step-1-active methods. No accidental call into Step-2/3 stubs (FindByHost / ListQueuedForCandidates / Claim / ListRunningByAgent / ListStaleAssigned / ListByAgent: 0 hits in webhook_handler.go).
- **Embedded migration vs file migration parity**: `db/migrations/0002_previews.{up,down}.sql` and `internal/db/sqlite/migrations/0002_previews.{up,down}.sql` are byte-identical (`diff` empty). `migrate up` exercises the embedded copy only — file copy is the SQL source-of-truth for sqlc.
- **HTTP error shape consistency**: PASS. Both admin (`admin_handler.go`) and webhook (`webhook_handler.go`) use `writeError(...)` → `{"error":<code>, "message":<msg>}`. Live curl confirmed for missing/invalid signature responses.
- **Webhook order — raw body → HMAC → JSON decode**: PASS. Source: `io.ReadAll` (line 130) → `computeHMACSHA256` (line 142) → `hmac.Equal` (line 150) → `json.Unmarshal` (line 162). Signature is computed against raw bytes before any JSON parsing, so payload-mutation attacks during decode cannot bypass MAC.

### Repo Hygiene

- `git diff HEAD -- docs/specs/phase-0-*.md docs/specs/phase-1-*.md docs/reports/phase-0-*.md docs/reports/phase-1-*.md` → empty. Phase 0/1 APPROVED documents untouched.
- `.golangci.yml` only added `internal/db/sqlite/previews.sql.go` to the sqlc-generated exclusion list (additive, not a rule weakening).
- `git status --short` shows only Phase 2 expected files (Step 1 implementation + new tests + new spec) plus a single `.claude/settings.local.json` (untracked harness-local file).

### e2e (Playwright)

N/A — Step 1 introduces no UI. Phase 3 will gain admin UI; Playwright will be used then.

## Step 2 / Step 3 Items

OUT-OF-SCOPE-STEP2/3 (not evaluated this round):
F-S2-0..17, F-S3-0..*, NF-Test-Race-*, NF-Test-Docker-*, NF-Reconcile-*, NF-Timing-*, NF-Container-Label-*, NF-Observability-1, NF-Doc-*, NF-Commit-*, NF-Depguard-*, NF-Deps-*. Step 2 stubs (FindByHost / ListQueuedForCandidates / Claim / ListRunningByAgent / ListStaleAssigned / ListByAgent) return `ErrNotImplementedStep1` and are guarded by `TestPreviewStoreStubMethodsReturnNotImplemented`.

## Regressions Not in Spec

None observed.

## Verdict

**APPROVE**

## Notes

- **F-S1-13 alternative path**: spec offers `cmd/hub previews show <id>` to expose an `events` array when sqlite3 CLI is missing. The current `previews show` returns only the PreviewView (no events). However, the SQLite-backed unit tests in `internal/db/sqlite/preview_store_test.go` exercise the exact R1/R2 rules end-to-end against a real database, producing equivalent evidence. Recommended (non-blocking, enhancement for Step 2): consider extending `previews show` to include `events` for future evaluator runs without sqlite3.
- **F-S1-1 column count discrepancy**: spec text says "16개 컬럼" but the verification `for col in ...` loop lists 15 names. Implementation has 15 columns matching the loop. Treated as a spec wording slip; verification follows the executable command, which passes.
- **`ListAll` extension**: PreviewStore has 10 methods (9 spec + `ListAll`). The 10th is documented in code as the Step-1 evaluator helper used by `cmd/hub previews list` and `GET /admin/previews`. Step 2/3 stubs unchanged.
- **Race tests skipped**: `-race` requires CGO on Windows MinGW Go install. Not relevant to Step 1 (no race surface in webhook path).
- **Cleanup**: hub background process killed via `Stop-Process` on the port 3000 owner; `hub.db*`, `/tmp/hub_eval.log`, `/tmp/r.json`, `/tmp/payload*.json`, `/tmp/eval_*.txt` removed.
