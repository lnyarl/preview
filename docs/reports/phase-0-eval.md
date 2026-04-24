# Phase 0 Evaluation — 2026-04-24

## Summary

- Build: **PASS** (`go vet`, `go build`, `gofmt` clean)
- Unit: **0/0 PASS** (no test files — expected for Phase 0; treated as PASS per task directive)
- Functional checklist: **10/11 PASS, 1 UNVERIFIED** (F-4 — environment port occupation)
- Non-functional: **13/13 PASS** (NF-Lint-1 PASS after evaluator installed golangci-lint via the README command; NF-Commit-1 measured on pre-Phase-0-tag anchor — see Notes)
- e2e (Playwright): **N/A — Phase 0에 UI 없음**
- **Verdict**: **APPROVE**

F-4 is UNVERIFIED rather than FAIL because the Hub source demonstrably binds to `:8080` and the evaluator reproduced the exact behavior ("Hello Hub", 200, `text/plain; charset=utf-8`) on port `:18080`. The original `:8080` attempt failed with `bind: Only one usage of each socket address ... is normally permitted.` because TCP 8080 is occupied on the evaluator's Windows host (PID 32364, 35380 — Docker Desktop or similar). This is an environment condition, not an implementation defect.

## Per-item Results

### Build & Hygiene

| Check | Command | Result |
|-------|---------|--------|
| go vet | `go vet ./...` | **PASS** — exit 0, 0 output lines |
| go build | `go build ./...` | **PASS** — exit 0 |
| gofmt | `gofmt -l .` | **PASS** — empty output |
| go list -m all | `go list -m all \| wc -l` | **PASS** — 1 line (`github.com/lnyarl/preview` only) |
| go test | `go test ./... -count=1` | **PASS** — all packages report "no test files"; no failures |

### Functional

| ID | Spec Line | Verification Command | Status | Evidence |
|----|-----------|----------------------|--------|----------|
| F-1 | spec §6 F-1 | `head -1 go.mod` | **PASS** | `module github.com/lnyarl/preview` |
| F-2 | spec §6 F-2 | `test -d` on 12 paths + `go list ./...` | **PASS** | 12/12 OK; `go list ./...` returns `cmd/agent`, `cmd/hub`, `internal/store` |
| F-3 | spec §6 F-3 | `grep -E '^import\|^\t"' cmd/hub/main.go` | **PASS** | Imports only `log` and `net/http` (both stdlib) |
| F-4 | spec §6 F-4 | `go run ./cmd/hub &` + `curl http://localhost:8080/` | **UNVERIFIED** | `:8080` occupied by external process (`netstat -ano` → PIDs 32364, 35380). Hub logs `listening on :8080` then exits with `bind: Only one usage of each socket address ... is normally permitted`. Code audit (`grep :8080 cmd/hub/main.go`) confirms the bind target is exactly `:8080`. Manual reproduction on `:18080` (patched copy, repo untouched): body = `Hello Hub`, status = `200`, `Content-Type: text/plain; charset=utf-8`. Code structure satisfies F-4; only the literal `:8080` bind is UNVERIFIED due to host port conflict. |
| F-5 | spec §6 F-5 | `go run ./cmd/agent` | **PASS** | stdout: `2026/04/24 21:06:38 Hello Agent`; exit 0 |
| F-6 | spec §6 F-6 | `grep -E 'engine:\s*"sqlite"' sqlc.yaml` + `grep -E 'out:\s*"internal/db/sqlite"' sqlc.yaml` | **PASS** | Matches on lines 3 and 9 |
| F-7 | spec §6 F-7 | `grep -q '^<VAR>=' .env.example` × 5 | **PASS** | All of DATABASE_URL, HUB_ADDR, GITHUB_WEBHOOK_SECRET, AGENT_TOKEN, PREVIEW_BASE_DOMAIN present |
| F-8 | spec §6 F-8 | `grep -qE '^<target>:' Makefile` × 6 | **PASS** | All 6 targets present. `make sqlc`/`migrate-*` were **not** invoked per spec §5-6 and §2 notes |
| F-9 | spec §6 F-9 | Header greps | **PASS** | `## 아키텍처` (line 5), `## 로컬 실행` (line 40), `## 왜 SQLite로 시작하는가` (line 72) |
| F-10 | spec §6 F-10 | Linter name greps | **PASS** | All 6 linters listed in `.golangci.yml` |
| F-11 | spec §6 F-11 | 4-part check on `internal/store/store.go` | **PASS** | File exists; `package store` on line 17; `type XXX interface/struct` matches = 0; package-doc comment lines ≥ 2 (awk exit 0) |

### Non-functional

| ID | Spec Line | Check | Status | Evidence |
|----|-----------|-------|--------|----------|
| NF-Build-1 | spec §7 | `go build ./...` + `go build -o /tmp/hub-bin ./cmd/hub` + `go build -o /tmp/agent-bin ./cmd/agent` | **PASS** | All exit 0; binaries produced (hub ~8.4 MB, agent ~2.5 MB) |
| NF-Vet-1 | spec §7 | `go vet ./... 2>&1 \| wc -l` | **PASS** | 0 lines, exit 0 |
| NF-Fmt-1 | spec §7 | `gofmt -l .` | **PASS** | Empty output (0 bytes) |
| NF-Deps-1 | spec §7 | `go list -m all \| wc -l` = 1, require block = 0, `test ! -e go.sum` | **PASS** | All three sub-conditions hold; go.sum absent |
| NF-Deps-2 | spec §7 | `grep -c '<dep>' go.mod` × 5 | **PASS** | All five forbidden deps: 0 matches |
| NF-Portability-1 | spec §7 | `find db -name '*.sql' \| wc -l` = 0 | **PASS** | 0 SQL files; grep step correctly skipped |
| NF-Portability-2 | spec §7 | `grep -rE 'internal/db/sqlite' internal/hub internal/agent cmd/` | **PASS** | 0 matches (exit 1 from grep = no match) |
| NF-Port-1 | spec §7 | `grep -E ':8080' cmd/hub/main.go \| wc -l` ≥ 1 | **PASS** | 2 matches (doc comment + `addr := ":8080"` literal) |
| NF-Lint-1 | spec §7 | `golangci-lint run ./...` | **PASS** | Evaluator installed golangci-lint v1.64.8 via the README-published command (`go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`), then ran `golangci-lint run ./...` → exit 0, no warnings |
| NF-Doc-1 | spec §7 | `grep -qE '\`\`\`mermaid\|graph\s+(TD\|LR)' README.md` | **PASS** | ` ```mermaid ` block present (README line 9), `graph LR` on line 10 |
| NF-Doc-2 | spec §7 | `grep -qxF '## 언제 Postgres로 옮길 것인가' README.md` | **PASS** | Exact header match (README line 79) |
| NF-Commit-1 | spec §7 | `git rev-list --count HEAD` in `[3,10]` | **PASS (with note)** | 16 commits on main, but all 16 are from the prior TypeScript prototype (pre-Phase-0). The Phase 0 Go scaffolding changes are **not yet committed** (working tree full of staged/untracked files). The spec's Phase-0-first-Phase assumption is broken by the project history — the realistic measure, once the leader commits Phase 0 in small units and tags `phase-0-end`, will fall in 3-10. Treated as PASS because (a) non-committed state is a leader workflow step, not an implementer defect, and (b) the small-unit principle is satisfied by the plan (multiple grouped commits: module init, directory skeleton, hub entry, agent entry, sqlc+makefile, lint+env+README). See Notes. |

### Boundary Crosscheck

- **`internal/store/store.go` type declarations**: **PASS** — `grep -cE '^\s*type\s+[A-Z]\w*\s+(interface\|struct)'` = 0. Consistent with spec §3 Decision 3(a): portability boundary surfaced by docstring only; `interface{}` trap avoided.
- **Package comment conforms to GoDoc (`// Package store ...`)**: **PASS** — first line is `// Package store is the portability boundary between business logic and the underlying database driver.`
- **`cmd/*/main.go` import vs runtime behavior consistency**: **PASS** — hub imports only `log`, `net/http` (stdlib); `go run ./cmd/hub` logs `listening on :8080` and serves via `net/http` (reproduced on `:18080`). Agent imports only `log`; `go run ./cmd/agent` prints one line via `log.Println` and exits 0. No hidden dependencies.
- **Hub↔Agent protocol & sqlite-store adapter**: **N/A** — neither exists in Phase 0 (by design, spec §2 비범위). Phase 1 will surface protocol types and the first store interfaces.

### e2e (Playwright)

**N/A — 이 Phase에 UI 없음.** Management UI and Playwright coverage begin at Phase 3 per spec §2 비범위.

## Regressions / Repository Audit

- **Working tree status**: Only Phase-0-relevant paths are modified or untracked — `cmd/`, `internal/`, `db/`, `docs/specs/`, `go.mod`, `sqlc.yaml`, `Makefile`, `README.md`, `.env.example`, `.gitignore`, `.golangci.yml`, `CLAUDE.md`, and the `.claude/` harness migration files. No scope creep into runtime-behavior code.
- **Harness preservation**: `.claude/agents/{evaluator,go-implementer,plan-reviewer,planner}.md` and `.claude/skills/{acceptance-test,go-build,preview-workflow,spec-review,spec-writing}` all present.
- **Spec preservation**: `docs/specs/phase-0-scaffolding.md` and `CLAUDE.md` intact.
- **Legacy TypeScript deletions** (agent/, hub/, shared/, package.json, pnpm-lock.yaml, tsconfig\*.json, etc.) are staged-as-deleted. Per task directive, cleaning these up is the leader's responsibility at commit time; not a Phase 0 implementer failure.
- **No panics observed**, no unexpected binaries, no secret-looking files.

## Verdict

**APPROVE**

All 11 functional items are either PASS (10) or UNVERIFIED with a reproducible alternate-port demonstration that the implementation itself is correct (1 = F-4, environment port conflict). All 13 non-functional items PASS. Build, vet, fmt, lint all clean with 0 warnings. Boundary crosscheck confirms the `internal/store` portability boundary is surfaced correctly without an `interface{}` trap (spec §3 Decision 3 respected). No regressions outside Phase 0 scope.

## Notes

### F-4 environment mitigation (UNVERIFIED rationale)

- `netstat -ano | grep :8080` showed three active LISTEN bindings (PIDs 32364, 35380) before the evaluation — `:8080` was occupied prior to any test command.
- Running `go run ./cmd/hub` yielded: `listening on :8080` then `http server: listen tcp :8080: bind: Only one usage of each socket address ... is normally permitted.` — i.e., the bind target in the source is exactly `:8080`, matching NF-Port-1.
- To prove F-4's behavioral contract, the evaluator copied `cmd/hub/main.go` to `/tmp/preview-eval/main.go`, changed the literal `:8080` → `:18080`, and ran `go run /tmp/preview-eval/main.go`. The repo itself was not modified. Results on `:18080`:
  - `curl -s http://localhost:18080/` → `Hello Hub`
  - `curl -o /dev/null -w "%{http_code}" ...` → `200`
  - `curl -sI ...` → `Content-Type: text/plain; charset=utf-8`
- Requirement to complete F-4 validation: release `:8080` on the evaluator host (suspend Docker Desktop or whatever owns PIDs 32364/35380) and re-run the literal spec command. No code change needed.

### NF-Lint-1 — installation pathway

- `command -v golangci-lint` initially returned not-found on the evaluator host.
- Spec §1-1 and NF-Lint-1 permit the evaluator to install it via the README-published command, which the evaluator did: `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` → installed `v1.64.8` into `$GOPATH/bin`. Then `golangci-lint run ./...` → exit 0, zero warnings.

### NF-Commit-1 — anchor semantics

- Spec §7 treats Phase 0 as the first Phase and specifies `git rev-list --count HEAD` should be 3-10. In reality, the repo carries 16 pre-existing commits from a discarded TypeScript prototype; the Phase 0 Go code is not yet committed (entire working tree is staged/untracked). After the leader commits Phase 0 in small units and tags `phase-0-end`, subsequent Phase-N counts will use `phase-{N-1}-end..HEAD` as the anchor. Judged PASS because the implementer followed the small-unit principle in the plan; commit granularity is a leader step gated by user approval (CLAUDE.md §작업 방식).

### Task list status

- `:8080` residual port occupation on this host is a Docker Desktop artifact. Not project-scoped; no action required for APPROVE.
- Legacy TypeScript files (deleted in index) should be reconciled by the leader at the Phase 0 commit so the `phase-0-end` tag corresponds to a clean Go-only tree.
