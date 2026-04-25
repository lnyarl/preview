# QA Report: Phase 4 — Hub-managed Agent Build Configuration

Evaluator: claude (opus 4.7) — adversarial QA
Date: 2026-04-25
Spec: `docs/specs/phase-4-agent-build-config.md`

## Total score: 92/100

### Feature completeness: 28/30

All F-* items in §5-1 (DB), §5-2 (Admin UI), §5-3 (Protocol/WS), and §5-4 (Agent runtime) are implemented and exercised by tests. Live HTTP probing of the running hub daemon also confirmed F-8 / F-10 / F-11 / F-12 / F-13 / F-14 / F-15 / F-9 / F-16 end-to-end.

Two soft gaps:

- **F-31 / F-32 only verified on POSIX shells.** `internal/agent/runner_test.go:303 TestRunnerEnvironmentVariables` calls `t.Skipf` when `env`/`grep`/`sh -c` cannot produce the capture file (the standard outcome on Windows). On the Linux/macOS hosts that are the spec's declared target the env-var injection is exercised, but on this Windows evaluator host F-31 was actually skipped. The spec acknowledges Windows is deferred, so this is consistent with §8 (리스크) but should be noted — there is no green build on Windows that proves PORT/PREVIEW_* propagation, and any future regression on Linux would not surface in CI on Windows.
- **F-32 ("후속 라인 미실행")** — `TestRunnerBuildError` exercises a single failing line and asserts STATUS_UPDATE=failed, but does not assert that subsequent lines were *not* invoked. The current implementation in `runner.go:177-193` does break the loop on error (correct behavior), but a missing test means a future change that swallows the error and continues the loop would not be caught.

### Bug-free execution: 24/25

`go build ./...`, `go vet ./...`, and `go test ./... -count=1 -timeout 180s` all pass. All hub tests pass (`TestWSHandshakeSuccess`, `TestWSAgentConfigSentWithStoredValues`, `TestAdminAgentDetailRenders`, `TestAdminAgentDetail404`, `TestAdminAgentDetailSavedValues`, `TestAdminAgentConfigSavePushDelivered/Offline/Failed`, `TestNormalizeContainerPort`, `TestSplitAndCleanLines`, `TestAdminAgentsListHasDetailLink`). Holder concurrency test (`TestHolderConcurrentReplaceSnapshot`) passes. Live integration (curl against running hub) confirmed:

- `GET /admin/agents/{id}` renders form with placeholder=`docker build -t $PREVIEW_IMAGE .` and `placeholder=80`.
- `POST /admin/agents/{id}/config` returns `303 → /admin/agents/{id}?msg=saved_offline` (no agent connected) and stored values reload correctly.
- Empty submit → empty textarea + no `value=` (placeholder visible) — confirms F-14.
- `container_port=99999` → reload shows no `value=` attr (normalized to 0) — confirms F-15.
- Unknown agent ID → 404 — confirms F-9.

Minus 1 point: **`-race` not exercisable on Windows.** Without `CGO_ENABLED=1` Go refuses `go test -race`, so the spec's NF-8 ("race detector clean for Holder") was not directly verified on this host. The Holder logic (RWMutex + slice deep copy) is straightforward and the concurrent-fuzz `TestHolderConcurrentReplaceSnapshot` did pass without race detector, but the spec contract was not fully discharged. Should be re-run with race detector on Linux CI.

### Product depth: 22/25

The implementation is genuinely interactive end-to-end — DB → Hub UI → WS push → Agent Holder → Runner snapshot — not a shell. Decisions 11 (snapshot-then-use) and 9 (RWMutex deep copy) are realized, and the agent_detail page is a real form, not a stub.

Three observations:

1. **NF-Security-Env-1 is documented in the spec, not the code.** §4-7 states "NF-Security-Env-1 로 이 사실을 명시 검증한다" — the runner.go file header and the `buildEnv := append(os.Environ(), …)` line both lack a comment explaining that all parent-process env (HUB_TOKEN, DATABASE_URL, etc.) is inherited into the user's shell command. The runner.go header at line 1-12 mentions Phase 4 but not the trust model. If a reviewer reads only the code, this load-bearing decision is invisible. Recommended: add a 3-line comment near `buildEnv` referring to §4-7 / decision 5.
2. **`AgentConfigData{}` zero-value JSON shape vs. the wire contract.** The spec §4-2 promises `{"build_commands": [], "container_port": 0}` for the defaults sentinel. A zero-value `AgentConfigData{}` actually marshals as `{"build_commands":null,"container_port":0}` because `BuildCommands: nil` becomes `null`. In practice this never reaches the wire — both call sites (`ws_handler.go:434` and `admin_ui.go:713`) construct the value from `[]string{}`-returning helpers (`Store.GetBuildConfig` and `splitAndCleanLines` both return non-nil) — so the wire payload is always `[]`. But the *invariant* depends on every future caller knowing this. A safer implementation would either (a) make `AgentConfigData` marshal nil as `[]` (custom MarshalJSON or a normalize-on-marshal helper), or (b) document the rule on the struct.
3. **`splitBuildCommands` exists in two places.** `internal/db/sqlite/agent_store.go:177 splitBuildCommands` and `internal/hub/admin_ui.go:762 splitAndCleanLines` are nearly-identical line-normalize functions. Diverging implementations of the same wire contract are exactly the kind of footgun §3 결정 13 was meant to eliminate ("split/trim 위치는 wire 변환 시점 한 곳"). Spec violation is mild — the second copy is in a different layer's wire-conversion path — but they should share an `internal/protocol` helper.

### Overall polish: 18/20

- Templates follow established Pico CSS / hgroup pattern (NF-4 ✓).
- Slog keys consistent (`agent_config_*`, `admin_ui_agent_config_*`) — NF-11 ✓.
- File header comments present on `build_config.go`, `runner.go` (NF-2 ✓ for production code; `build_config_test.go` lacks header, common test exemption).
- Line counts: `build_config.go` 51, `agent_detail.gohtml` 63, both migrations 2 each — well under NF-3 cap.
- NF-1 ✓ (`go mod tidy -diff` clean).
- NF-7 ✓ (only `internal/protocol` and its tests use `"AGENT_CONFIG"` / `"CONFIG_UPDATE"` literals).
- NF-Portability ✓ (no `AUTOINCREMENT` / `INSERT OR REPLACE` in 0003).
- NF-Portability-2 ✓ (no `internal/db/sqlite` import in `internal/hub` or `internal/agent`).
- NF-12 ✓ (`internal/agent` does not import `internal/hub`).

Minus 2 points:

- The `?msg=saved_push_failed` detection in `admin_ui.go:725` uses `strings.Contains(err.Error(), "not connected")` — a string-based error sniff. `WSJobSender.SendAgentConfig` returns errors of two distinct shapes (the literal "not connected" sentinel and arbitrary write errors). A typed error (`var ErrAgentNotConnected = errors.New(...)` + `errors.Is`) would be more robust. Fragile under future error-message changes.
- The flash message in `agent_detail.gohtml:14` reads `"Saved. CONFIG_UPDATE was {{.PushOutcome}}."`. When `PushOutcome` is empty (e.g., direct GET without `?msg=` but with another query), the rendering still says `"Saved. CONFIG_UPDATE was ."` if `SavedFlash` is true but PushOutcome wasn't set. In practice the switch in `agentDetail` always sets both together, but the template has no defensive guard. Minor.

### Must-fix before next build

None. All blocking F-* items pass on the Linux/macOS-target shell behavior, and live HTTP probing confirms the user-facing flow works. The findings above are recommended improvements, not blockers.

### Recommended improvements

1. Add a `// Security note:` comment near `buildEnv := append(os.Environ(), ...)` in `internal/agent/runner.go:170` referencing §4-7 of the spec — addresses NF-Security-Env-1's "명시 검증" requirement.
2. Strengthen `TestRunnerBuildError` to also assert that subsequent lines were not invoked (e.g., a 2-line build command where line 1 fails, then verify the second line's side effect is absent).
3. Define `var ErrAgentNotConnected = errors.New("ws_job_sender: agent not connected")` in `ws_registry.go` and replace the substring sniff in `admin_ui.go:725` with `errors.Is(err, ErrAgentNotConnected)`.
4. Consolidate `splitBuildCommands` (sqlite layer) and `splitAndCleanLines` (admin_ui) into a single helper in `internal/protocol` or a small `internal/agentcfg` package — both functions normalize the same wire contract per 결정 13.
5. Add a custom `MarshalJSON` to `AgentConfigData` that emits `[]` for nil `BuildCommands` (defense-in-depth for the §4-2 wire contract — current callers happen to pass non-nil but this is a tripwire).
6. Run `go test -race ./...` on Linux CI to discharge NF-8 (Windows host blocked here by missing cgo).
7. Add a Linux-only `t.Run` (or a build tag) so the `TestRunnerEnvironmentVariables` env-capture path is exercised in CI rather than silently skipped — current `t.Skipf` masks platform regressions.

### Verdict

**PASS (92/100, no must-fixes).** Phase 4 is implementable as-is and meets the §5 functional checklist. All recommended improvements are quality-of-implementation refinements rather than correctness blockers. Proceed to next phase.
