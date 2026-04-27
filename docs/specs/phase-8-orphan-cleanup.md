# Phase 8 — Orphan Cleanup (Worktree wiring + Compose 프로젝트 정리)

Status: **REVIEW_APPROVED**
Author: planner
Date: 2026-04-26

---

## 1. Phase 개요

### 1-1. 배경

Phase 0~7 까지 Agent 는 두 종류의 영속 상태(persistent state)를 호스트에 남긴다.

1. **워크트리** — `{workDir}/repos/{slug}/worktrees/preview-{previewID}` 에 git worktree.
2. **컨테이너** — Dockerfile 모드는 `name=preview-{previewID}` 단일 컨테이너 + `hub-preview-id` 라벨, compose 모드는 `docker compose --project-name preview-{previewID}` 프로젝트 단위(라벨 미부착).

Agent 가 정상 종료 / 비정상 종료 / OOM 후 재기동되면 다음 두 종류의 orphan 이 잔존한다.

- **컨테이너 orphan** — 이미 Phase 3 에서 `RestoreOrphans` 가 `hub-preview-id` 라벨 기반으로 Dockerfile 모드 컨테이너를 `runner.jobs` 맵에 복원해 해소함.
- **워크트리 orphan** — `internal/agent/repocache_multi.go` 의 `PruneStaleWorktrees(ctx, activeIDs []string) (int, error)` 가 이미 구현되어 있고 단위 테스트(`repocache_multi_test.go`)도 통과하지만, **`cmd/agent/main.go` 에서 호출하는 wiring 이 없다**. 결과적으로 재기동마다 worktree 누적 → 디스크 점유.
- **compose 프로젝트 orphan** — compose 모드 컨테이너에는 `hub-preview-id` 라벨이 부착되지 않으므로(`runner.go:handleCompose` 가 라벨 미설정), `RestoreOrphans` 가 감지하지 못한다. Agent 재기동 후 `runner.jobs` 맵에 등록되지 않아 TEARDOWN 메시지가 와도 `Teardown` 의 `jobs.Load` 분기에서 즉시 `done` 처리되며 실제 `docker compose down` 이 호출되지 않는다(`runner.go:295`). 컨테이너는 영구 잔존.

### 1-2. 목표

본 Phase 는 위 두 잔존(워크트리·compose 프로젝트)을 단일 Phase 로 묶어 처리한다.

1. **워크트리 wiring** — `cmd/agent/main.go` 에서 `RestoreOrphans` 가 반환하는 복원된 previewID 목록(`[]string`)을 `cache.PruneStaleWorktrees(ctx, activeIDs)` 에 전달해 1 회 호출. 에러는 best-effort.
2. **Compose 프로젝트 정리** — 새 함수 `PruneComposeOrphans(ctx, cmd CmdRunner, runner *Runner, logger *slog.Logger) (int, error)` 신설. `docker compose ls --format json` 으로 실행 중 프로젝트 목록을 얻어 `preview-` 접두사 + `runner.RunningPreviewIDs()` 에 없는 것에 대해 `docker compose --project-name {name} down` 실행. `cmd/agent/main.go` 가 `RestoreOrphans` 직후 호출.
3. 두 호출 모두 best-effort — 실패해도 Agent 부팅은 계속.

### 1-3. 비목표 (이 Phase 가 해결하지 않는 것)

- **이미지 / 볼륨 orphan 정리** — `docker image prune` / `docker volume prune` 류는 Phase 8 비범위. 이미지 누적 정책은 후속 Phase.
- **`hub-preview-id` 라벨을 compose 컨테이너에도 부착** — compose 의 multi-service 환경에서 라벨을 어디에 부착할지(서비스마다 / 프로젝트 단위) 의사결정이 필요. 본 Phase 는 "이미 라벨이 없는 기존 compose 프로젝트도 안전하게 정리" 를 우선. compose 라벨화는 Phase 9 후보.
- **주기적 재실행 (loop / ticker)** — 본 Phase 의 두 정리는 Agent 부팅 시 1 회만. 주기적 cleanup 은 후속 Phase.
- **Worktree 정리에서 compose override 파일(`.preview-override-{pid}.yml`) 삭제** — Phase 5 의 `Teardown` 이 책임. Worktree 자체가 제거되면 override 파일도 같이 사라지므로 별도 처리 불필요.
- **다른 Agent 인스턴스가 띄운 compose 프로젝트와의 충돌** — 1 호스트 1 Agent 가정(`AGENTS.md`). 동일 호스트에 다중 Agent 가 도는 환경은 Phase 7 까지의 가정과 동일하게 미지원.
- **Compose 프로젝트 정리 실패 시 STATUS_UPDATE / READY 메시지 영향** — 본 Phase 의 두 호출은 Hub 와 무관. Hub 메시지 스키마 변경 0.
- **Worktree 정리에 의해 컨테이너가 마운트한 디렉토리가 사라지는 사이드이펙트** — Phase 6 까지의 Agent 는 worktree 를 컨테이너에 마운트하지 않는다(compose 가 자체 build context 로만 사용 + Dockerfile 모드는 image build 후 worktree 무관). 따라서 worktree 삭제가 active 컨테이너에 영향 0.

### 1-4. 성공 기준 (요약)

(각 항목 뒤 괄호는 §6 의 대응 F-* ID — 권장 A 반영.)

- `cmd/agent/main.go` 가 `RestoreOrphans` 직후 두 호출을 차례로 수행:
  1. `cache.PruneStaleWorktrees(ctx, activeIDs)` (activeIDs = `RestoreOrphans` 반환) (F-1, F-2)
  2. `agent.PruneComposeOrphans(ctx, runner.Cmd(), runner, logger)` (F-21)
- 두 호출은 모두 best-effort: 에러는 WARN 로그 + 계속 진행. (F-3, F-4)
- `PruneComposeOrphans` 의 단위 테스트(fakeCmdRunner 기반)가 다음 케이스 통과:
  - `docker compose ls --format json` 출력이 빈 배열 → 0 반환, down 호출 0. (F-7)
  - 출력에 `preview-p2`, `preview-p4`, `other-x` 3 개. `runner` 에 `p1` 만 등록 → `preview-p2`/`preview-p4` 두 건 모두 down 호출, `other-x` 는 무시(접두사 미일치), 반환값 2. (F-8, F-12)
  - `docker compose ls` 가 비-zero 종료 → 에러 반환, down 호출 0. (F-9)
  - `docker compose down` 1 건 실패해도 나머지 프로젝트는 계속 처리, 반환된 에러는 첫 번째 에러. (F-11)
- `PruneStaleWorktrees` 와 `PruneComposeOrphans` 의 호출 위치/순서가 일관 — wiring 회귀 시 변동 0. (F-6, F-21)
- `runner.SetReadySender(c)` / `runner.SetMaxJobs(cfg.MaxJobs)` 두 호출이 cleanup 이후로 이동 — 기존 `RestoreOrphans` 이전 위치에서 제거. (F-21, F-24)

---

## 2. In / Out of Scope

### 2-1. In Scope

- **Agent 측 코드**
  - `internal/agent/compose_orphan.go` — **신규** — `PruneComposeOrphans` + `composeProject` dto + 내부 `parseComposeLs` 헬퍼.
  - `internal/agent/compose_orphan_test.go` — **신규** — fakeCmdRunner 기반 단위 테스트(아래 §6-2).
  - `cmd/agent/main.go` — wiring 2 줄 추가(`RestoreOrphans` 직후, `runner.SetReadySender(c)` 이전 — 결정 4 참조).
- **테스트**
  - `compose_orphan_test.go` — 신규 단위 테스트 7 케이스(F-7 ~ F-13).
  - 회귀: 기존 `repocache_multi_test.go` (`PruneStaleWorktrees` 단위) + `orphan_restore_test.go` (`RestoreOrphans` 단위) 그대로 통과.
- **문서**
  - 본 기획서. README/USAGE 변경은 비범위(Agent 부팅 동작이 silent 강화이고, 운영자가 알 새 명령/옵션 0).

### 2-2. Out of Scope

- **새 CLI 플래그 / env 변수** — 두 정리는 default 동작. opt-in/out 옵션 도입 안 함(결정 5 참조).
- **Hub 메시지 스키마 / Repository 인터페이스 변경** — 없음. 본 Phase 는 Agent 단독.
- **`PruneStaleWorktrees` 자체 로직 변경** — 이미 구현·검증됨. 호출만 추가.
- **compose ls 출력 포맷 v1/v2 호환성 매트릭스** — Docker Compose v2.x 만 지원. v1(legacy `docker-compose`) 가정 X. AGENTS.md 의 환경 가정 그대로.
- **TEARDOWN 메시지 처리 흐름 변경** — `Teardown` 의 `jobs.Load` miss → `done` 송신 분기는 그대로. 본 Phase 는 부팅 시 1 회만 정리.

### 2-3. Deferred (다음 Phase 후보)

- **Phase 9 후보 1**: compose 모드 컨테이너에 `hub-preview-id` 라벨 부착(서비스 단위로) — compose orphan 도 라벨 기반 동일 경로로 통합.
- **Phase 9 후보 2**: 주기적 cleanup ticker (디스크 워치독).
- **Phase 9 후보 3**: 이미지/볼륨 prune 정책.
- **Phase 9 후보 4**: orphan 발견 시 Hub 에 텔레메트리 송신(운영 가시성).

---

## 3. 설계 결정 (Design Decisions)

> 각 결정마다 (선택, 근거, 버려진 대안, 되돌릴 때 비용) 4 요소.

### 결정 1 — `docker compose ls --format json` 으로 프로젝트 목록 획득 (`docker ps` 라벨 스캔이 아님)

**선택**: `r.cmd.Output(ctx, "docker", "compose", "ls", "--format", "json")` 으로 실행 중 compose 프로젝트의 JSON 배열을 받아 파싱한다. 라벨 기반 컨테이너 스캔을 사용하지 않는다.

**근거**:
- Compose v2 의 표준 명령 — 프로젝트 단위가 1차 객체. 컨테이너 다수를 프로젝트로 묶어 1 줄로 보여줌.
- 라벨 스캔(`docker ps --filter label=com.docker.compose.project=preview-*`) 은 다음 단점:
  - prefix 매칭이 docker filter 에 없음(정확 일치만). 클라이언트 측 필터가 필요해 결국 동일.
  - 컨테이너 단위 결과를 프로젝트로 group-by 해야 하는 추가 가공.
  - 컨테이너 0 개(이미지만 있는 비정상 프로젝트) 는 누락.
- `compose ls` 는 `Status` 필드로 `running(N)` / `exited(N)` 등을 직접 노출 — 정리 정책에 활용 가능(본 Phase 는 모두 정리 대상으로 처리, §결정 6 참조).

**대안 기각**:
- **`docker ps --filter label=com.docker.compose.project=...` + 클라이언트 필터** — 위 단점 3 가지.
- **Docker SDK ContainerList + LabelExtract** — Phase 6 의 `DockerClient` 는 `ContainerList` 를 가지지만 라벨 prefix 필터가 없고, compose 의 multi-service 를 group-by 하는 부담을 SDK 어댑터에 둠. CmdRunner 한 번 호출이 단순.
- **Compose API SDK (`github.com/docker/compose/v2`)** — 외부 의존 폭증(NF-1 위반). compose 명령을 shell 로 호출하는 Phase 6 의 정책과 일관.

**되돌릴 때 비용**: `compose_orphan.go` 1 파일 + `parseComposeLs` 헬퍼. 작음.

### 결정 2 — `CmdRunner.Output` 시그니처를 그대로 사용 + `Runner.Cmd() CmdRunner` getter 신규 (인터페이스 무변경)

**선택**: `internal/agent/repocache.go` 의 `CmdRunner` 인터페이스(`Run + Output`)를 그대로 재사용. `PruneComposeOrphans(ctx, cmd CmdRunner, runner *Runner, logger *slog.Logger)` 의 첫 `cmd` 인자가 이 인터페이스. main.go 는 `runner.Cmd()` getter 로 Runner 내부 `execRunner{}` 인스턴스를 꺼내 주입.

**배경**: plan-review 1차에서 다음 사실이 확정됐다.
- `internal/agent/repocache.go:31` 의 `execRunner` 는 unexported 타입.
- `internal/agent/runner.go:55` 의 `Runner.cmd CmdRunner` 필드도 unexported (NewRunner 가 `execRunner{}` 로 주입).
- 따라서 main.go 가 "별도 `execRunner{}` 인스턴스를 만들어 주입" 하는 것이 **현실적으로 불가능**. 이를 해결하려면 (a) execRunner export, (b) `Runner.Cmd()` getter, (c) `PruneComposeOrphans` 시그니처에서 `cmd` 인자 제거하고 `runner.cmd` 직접 사용 — 셋 중 택일.

**선택은 (b)**: `internal/agent/runner.go` 에 다음 1 줄 메서드 추가.

```go
// Cmd 는 Runner 가 내부에서 docker/git 명령 실행에 사용하는 CmdRunner 를
// 외부 호출자(예: main.go 의 PruneComposeOrphans wiring) 에 노출한다.
// Phase 8: orphan cleanup 의 cmd 주입 경로 (결정 2).
func (r *Runner) Cmd() CmdRunner { return r.cmd }
```

main.go 의 wiring 은 `agent.PruneComposeOrphans(ctx, runner.Cmd(), runner, logger)`.

**근거**:
- 옵션 (a)(execRunner export) 는 `repocache.go` 의 다른 노출 표면을 늘리고, 운영 시 `execRunner{}` 를 직접 만드는 호출자가 새로 생기는 것이 본 Phase 의 의도가 아님.
- 옵션 (c)(cmd 인자 제거) 는 단위 테스트의 fake 주입을 강제로 `*Runner` 통째로 만들어야 하게 만듦 — 단위 테스트 단순성 ↓. 또한 `PruneComposeOrphans` 가 Runner 의 비공개 필드에 직접 의존 → 캡슐화 약화. Q2 에서 본 항목을 비교 검토했고 (b) 가 명시적 인자 + 단위 테스트 단순성 양쪽 다 우위.
- 옵션 (b)는 1 줄 getter — 영향 표면 최소. `runner.cmd` 가 이미 `CmdRunner` 인터페이스 타입이라 caller 가 internals 에 결합하지 않음.
- `Output(ctx, "git", "rev-parse", ...)` 등 기존 호출과 동일한 시그니처 재사용. fake 테스트 헬퍼(`fakeRunner`, `fakeCmd`) 도 그대로 적용.

**대안 기각**:
- **(a) `execRunner` export 또는 `NewExecRunner()` 팩토리** — repocache.go 의 노출 표면 확대 + 호출자가 `agent.NewExecRunner()` 를 별도 인스턴스로 들고 다닐 이유 없음(Runner 가 이미 들고 있음).
- **(c) `cmd` 인자 제거 + `runner.cmd` 직접 사용** — 단위 테스트가 `*Runner` 통째 fake 필요. `Runner` 는 docker/cache/hub/logger 등 다중 의존 → fake harness 비대.
- **별도 `ComposeRunner` 인터페이스** — 메서드 같음, 추상화 과잉.
- **`runner.cmd` 필드를 export (`Cmd CmdRunner`)** — 필드 직접 노출은 외부에서 mutate 가능 → 캡슐화 깸. getter 가 read-only 보장.

**영향 파일** (§4-5 갱신): `internal/agent/runner.go` 에 `Cmd()` getter 1 줄 추가. `cmd/agent/main.go` 의 wiring 인자가 `runner.Cmd()`.

**되돌릴 때 비용**: getter 1 줄 + main.go wiring 1 줄. 작음.

### 결정 3 — `docker compose ls --format json` 출력 스키마 (Compose v2 공식 포맷)

**선택**: 출력은 **JSON 배열**. 각 항목의 필드:

```json
[
  {
    "Name": "preview-abc123",
    "Status": "running(3)",
    "ConfigFiles": "/path/to/docker-compose.yml,/path/to/.preview-override-abc123.yml"
  },
  {
    "Name": "other-app",
    "Status": "running(1)",
    "ConfigFiles": "/path/to/other.yml"
  }
]
```

**파싱 정책**:
- `encoding/json` 의 `json.Unmarshal([]byte(out), &[]composeProject{})` 1 회.
- 본 Phase 는 `Name` 필드만 사용. `Status` / `ConfigFiles` 는 `composeProject` 구조체에 정의는 하되 본 Phase 로직에서는 미참조(향후 정책 확장 여지).
- 빈 배열(`[]`) → 0 반환.
- JSON 파싱 실패 → 에러 반환(호출자 best-effort 로 무시).
- Compose v2 는 `--format json` 출력의 마지막에 trailing newline 1 개 — `json.Unmarshal` 이 자동 무시.
- **버전 가정 + 키 케이스 결론** (권장 B 반영, Q1 종결):
  - Docker Compose v2.x (Docker CE 24.0+ 동봉 기본). v1 (`docker-compose`) 미지원 — AGENTS.md 의 환경 가정과 동일.
  - `Name`/`Status`/`ConfigFiles` 의 PascalCase 키는 Compose v2 의 [`cmd/compose/ls.go` 의 `formatter.Project` 구조체](https://github.com/docker/compose/blob/v2.20.0/cmd/compose/ls.go) (Go struct field 이름 그대로 marshal) 가 v2.0 GA(2022-06) 부터 일관되게 노출하는 형식. v2.0 이전의 알파/베타 빌드에 lower-case 보고 사례가 일부 있었으나 본 프로젝트의 docker 24.0+ 가정 환경에서는 발생하지 않는다.
  - 따라서 본 Phase 는 PascalCase 단일 가정 — fallback 로직 미도입. R1 의 lower-case fallback 도 도입 X. Q1 종결.

**근거**:
- Compose v2 공식 포맷. Docker CLI 24+ 에서 안정.
- Lower-case key (`name`, `status`) 는 v2.0 이전 일부 빌드에만 존재. Q1 에서 Step 1 직전 1 회 확인.

**대안 기각**:
- **`--format table` + 텍스트 파싱** — 컬럼 폭이 환경마다 다름. fragile.
- **`--format yaml`** — JSON 보다 파싱 비용 높음. 표준 라이브러리에 yaml 미포함.
- **`docker compose ls -q`** — 프로젝트 이름만 나오지만 quote/escape 처리 검증 필요. JSON 이 더 정직.

**되돌릴 때 비용**: dto 구조체 + Unmarshal 1 줄. 작음.

### 결정 4 — wiring 위치: `RestoreOrphans` 직후, `runner.SetReadySender(c)` 이전

**선택**: `cmd/agent/main.go` 의 호출 순서를 다음과 같이 한다.

```go
// 현재 (Phase 7):
//   1. EnsureTraefik
//   2. NewRunner + SetTraefik(API)Port + SetRouterReadyTimeout
//   3. SetReadySender + SetMaxJobs
//   4. RestoreOrphans
//   5. SIGTERM goroutine

// Phase 8 후:
//   1. EnsureTraefik
//   2. NewRunner + SetTraefikPort 등
//   3. RestoreOrphans (← 위치 변경: SetReadySender 이전)
//   4. PruneStaleWorktrees(ctx, activeIDs)             ← Phase 8 신규
//   5. PruneComposeOrphans(ctx, runner.Cmd(), runner, logger)  ← Phase 8 신규 (결정 2 옵션 (b))
//   6. SetReadySender + SetMaxJobs                     (← 정리 후 READY 송신)
//   7. SIGTERM goroutine
```

**근거**:
- **READY 송신 전 정리** — `SetReadySender(c)` 호출 시 이미 `c` 의 reconnect loop 이 구동 중이므로(Phase 1), READY 가 곧 송신될 수 있다. READY 가 Hub 에 도착하면 Hub 가 새 JOB_ASSIGN 을 즉시 보낼 수 있다. 정리 도중에 신규 JOB_ASSIGN 이 들어오면 다음 race:
  - 신규 JOB 의 previewID 가 우연히 `runner.RunningPreviewIDs()` 에 없는 시점 + compose 프로젝트가 우연히 동일 이름 → cleanup 이 신규 컨테이너를 down 시킬 수 있음.
  - 따라서 cleanup 이 끝난 뒤 READY 를 송신하는 게 안전.
- **`RestoreOrphans` 직후** — `RestoreOrphans` 가 `runner.jobs` 에 등록한 previewID 들이 곧 `runner.RunningPreviewIDs()` 의 결과에 반영. cleanup 의 활성 기준 = "RestoreOrphans 후 jobs 맵 상태" 와 일치.
- **Worktree 정리는 컴포즈 정리보다 먼저** — 두 정리는 독립이지만, worktree 정리는 디스크 I/O, compose 정리는 docker 호출이라 실패 도메인이 분리. 순서가 동작에 영향 없음. 코드 가독성 위해 "디스크 → 컨테이너 외부 정리" 순.

**현재 main.go 의 호출 순서 변경 영향**:
- 현행 main.go 는 `RestoreOrphans` 가 `SetReadySender + SetMaxJobs` **이후** 위치(`main.go:101-108`). Phase 8 은 이를 `SetReadySender` **이전** 으로 옮긴다.
- `SetReadySender` 는 `runner.ready` 필드만 세팅. `RestoreOrphans` 는 ready sender 의존 0(코드 확인: orphan_restore.go 의 `runner.RegisterRestoredJob` 만 호출). 따라서 순서 변경이 동작에 영향 0.
- `SetMaxJobs` 는 `runner.maxJobs` 만 세팅. 마찬가지로 `RestoreOrphans` 의존 0.
- **단순 순서 변경이 아니라 "기존 두 줄(`SetReadySender` / `SetMaxJobs`) 을 RestoreOrphans 이전 위치에서 제거하고 cleanup 뒤로 이동"** 임을 §4-3 wiring 스니펫의 diff 마커로 명시(필수 2). F-21 + F-24 두 검증 항목으로 회귀 보장.

**대안 기각**:
- **READY 송신 후 cleanup** — 위 race 위험.
- **NewRunner 직후, RestoreOrphans 이전** — `runner.RunningPreviewIDs()` 가 빈 슬라이스 → 모든 compose 프로젝트가 orphan 으로 인식되어 진행 중 PR 까지 down. 치명적 회귀.
- **별도 goroutine 비동기 실행** — cleanup 도중 main goroutine 이 READY 송신 → 위 race. 게다가 정리 결과 로깅 시점이 비결정적.

**되돌릴 때 비용**: main.go 의 호출 순서 변경 4~6 줄. 작음.

### 결정 5 — 두 cleanup 모두 default-on, opt-out 플래그 미도입

**선택**: `--prune-stale-worktrees=false` / `--prune-compose-orphans=false` 같은 플래그를 도입하지 않는다. Agent 부팅 시 항상 실행.

**근거**:
- 두 cleanup 모두 idempotent + 안전(orphan 정의가 명확: 활성 jobs 맵에 없는 것).
- 운영자가 디버깅 목적으로 orphan 보존을 원하면 Agent 를 띄우지 않으면 됨(또는 `docker compose ls` 로 사전 확인 후 개별 정리).
- 플래그 도입은 wiring 부담 + 테스트 매트릭스 2 배.

**대안 기각**:
- **opt-in 플래그(`--enable-cleanup`)** — Agent 의 본래 책임("자기가 띄운 것을 자기가 정리")에 어긋남. 신규 운영자가 누적 디스크/컨테이너 보고 혼동.
- **opt-out 플래그(`--no-prune-orphans`)** — 일관 default 면 굳이 비활성화 옵션이 없어도 됨. Phase 7 의 `--router-ready-timeout=0` sentinel 패턴은 readiness 가 race 와 직결되어 디버깅 시 비활성화가 필요했지만, 본 Phase 는 그런 사례 없음.

**되돌릴 때 비용**: 추가 플래그 도입은 후속 Phase 에서 가능. 작음.

### 결정 6 — `Status` 필드 무시, 이름 prefix 만으로 정리 대상 판정

**선택**: `compose ls` 응답의 `Status` (`running(3)`, `exited(2)`, 등) 를 무시. **이름이 `preview-` 접두사 + `runner.RunningPreviewIDs()` 에 없으면 모두 down**.

**근거**:
- `exited` 상태도 디스크/리소스 누적이므로 정리 대상. `running` 만 정리하면 죽은 컨테이너가 영원히 남음.
- Status 분기를 두면 정책이 복잡해지고 테스트 매트릭스 증가.
- `docker compose down` 은 상태와 무관하게 idempotent — `exited` 프로젝트도 안전.

**대안 기각**:
- **`status:"running(*)"` 만 정리** — 위 단점.
- **`exited` 만 정리** — running 잔존이 가장 많은 사고 케이스(재기동 직전 compose up 후 Agent crash). 의의 없음.

**되돌릴 때 비용**: prefix 판정 로직 1 곳. 작음.

### 결정 7 — `docker compose down` 의 구체 cmd: `docker compose --project-name {name} down` (no `-v`, no `--rmi`)

**선택**: `cmd.Run(ctx, "docker", "compose", "--project-name", projectName, "down")`. `-v` (볼륨 삭제), `--rmi` (이미지 삭제), `--remove-orphans` 모두 미사용.

**근거**:
- 운영자가 compose 파일에 의도적으로 외부 볼륨(예: PostgreSQL 데이터)을 정의했을 수 있음. `-v` 가 데이터 손실로 이어질 위험.
- 이미지는 다음 PR 의 빌드 캐시 — `--rmi` 는 부하만 늘림.
- `down` 만으로 컨테이너 + 네트워크 정리. Phase 6 의 Teardown 과 같은 명령(`runner.go:303` 의 `compose down` 호출과 동일).

**대안 기각**:
- **`docker compose down -v --remove-orphans`** — 위 데이터 손실 위험.
- **`docker compose rm -fsv`** — 단계별 명령. `down` 한 줄이 더 단순.
- **`docker compose stop` + 별도 `rm`** — 2 개 명령. 단일 호출이 단순.

**되돌릴 때 비용**: cmd 인자 2~3 개 추가. 작음.

### 결정 8 — `PruneComposeOrphans` 의 에러 처리: 첫 에러 보관 + 나머지 계속, 호출자 best-effort

**선택**:
- `compose ls` 자체가 실패 → 즉시 에러 반환(파싱 불가).
- `compose down` 1 건 실패 → `firstErr` 에 보관, 나머지 프로젝트 계속 처리. 모든 down 시도 후 (정리 성공 카운트, firstErr) 반환.
- 호출자(`main.go`)는 `PruneStaleWorktrees` 와 동일하게 WARN 로그 + 계속.

**근거**:
- `PruneStaleWorktrees` 의 정책(`firstErr` + 계속)과 일관.
- `compose down` 1 건 실패가 다른 프로젝트 정리를 막을 이유 없음.
- 호출자가 에러를 fail-fast 로 처리하면 Agent 가 부팅 실패. 운영성 ↓.

**대안 기각**:
- **첫 에러 즉시 반환** — `PruneStaleWorktrees` 와 정책 불일치 + 부분 정리 후 멈춰 다음 부팅에서 재시도 → 누적 위험.
- **모든 에러 모아 `errors.Join`** — Go 1.20+ 가능하지만 본 프로젝트는 단일 에러 패턴 일관. 추가 의존 없이 `firstErr` 가 충분.

**되돌릴 때 비용**: 에러 처리 분기 3~4 줄. 작음.

### 결정 9 — `parseComposeLs(out string) ([]composeProject, error)` 를 별 함수로 분리

**선택**: JSON 파싱을 `PruneComposeOrphans` 본문에서 인라인하지 않고 `parseComposeLs(out string) ([]composeProject, error)` 로 분리.

**근거**:
- 단위 테스트가 파싱만 격리해 검증 가능(빈 출력, 잘못된 JSON, lower-case key, 정상).
- `PruneComposeOrphans` 의 본문이 길어지지 않음(NF-3 의 300 줄 제약 여유).

**대안 기각**:
- **인라인 `json.Unmarshal`** — 테스트가 fakeCmdRunner 를 반드시 통과해야 함. 파싱만 별 검증 불가.

**되돌릴 때 비용**: 함수 인라인 풀기 5 줄. 작음.

### 결정 10 — `composeProject` dto 는 `Name`, `Status`, `ConfigFiles` 3 필드만 정의 (forward-compat)

**선택**:

```go
// composeProject 는 `docker compose ls --format json` 응답의 1 항목.
// 본 Phase 로직은 Name 만 사용. Status/ConfigFiles 는 정의만 하고 미참조 —
// 향후 정책 확장 시(예: exited 프로젝트만 정리) 즉시 사용 가능.
type composeProject struct {
    Name        string `json:"Name"`
    Status      string `json:"Status"`
    ConfigFiles string `json:"ConfigFiles"`
}
```

**근거**:
- `encoding/json` 은 정의되지 않은 필드를 무시 → forward-compat. compose v2 가 새 필드를 추가해도 회귀 0.
- 3 필드 정의가 코드 자체의 의도(향후 정책 확장 가능성)를 문서화.

**대안 기각**:
- **`Name` 만 정의** — 향후 추가 시 dto 확장 부담. 비용 0 인 forward-compat 유지.
- **`map[string]any` 로 받음** — 타입 안전성 ↓. 테스트 가독성 ↓.

**되돌릴 때 비용**: dto 필드 추가/삭제 1 줄씩. 작음.

### 결정 11 — `PruneStaleWorktrees(ctx, activeIDs)` 의 activeIDs 가 Dockerfile 모드 previewID 만 포함하는 비대칭, 그러나 worktree 삭제는 안전

**선택**: `RestoreOrphans` 가 라벨(`hub-preview-id`) 기반이라 **Dockerfile 모드 previewID 만** 반환하는 비대칭 사실을 인정한다. 결과적으로 `PruneStaleWorktrees(ctx, activeIDs)` 의 activeIDs 에는 compose 모드로만 활성인 PR 의 previewID 가 빠져있고, 그 PR 의 worktree(`{workDir}/repos/*/worktrees/preview-{pid}`) 도 함께 삭제된다. **본 Phase 는 이를 의도된 안전 동작으로 수용한다**.

**근거 — 왜 안전한가**:
1. **Worktree 가 컨테이너에 mount 되지 않음**: Phase 6 까지의 Agent 는 worktree 를 (a) `docker compose up` 의 cwd 로 사용해 build context 만 만들고(컨테이너 안으로 mount X), (b) Dockerfile 모드의 `ImageBuild` 의 build context 만으로 사용. 두 경로 모두 빌드 후 worktree 미참조. 따라서 worktree 삭제가 active 컨테이너의 파일 시스템에 영향 0.
2. **Compose `down` 은 project-name 만으로 동작**: `docker compose --project-name preview-{pid} down` 은 compose 파일을 다시 읽지 않음(이미 만들어진 프로젝트 메타로 동작). worktree 삭제 후에도 compose teardown 이 정상 동작. (Phase 6 의 `runner.Teardown` 도 동일 명령 사용.)
3. **다음 JOB_ASSIGN 에서 `Checkout` 이 재생성**: worktree 가 사라지면 다음 JOB_ASSIGN 의 `cache.Ensure + Checkout` 흐름이 worktree 를 새로 만든다(`MultiRepoCache.Checkout` 의 첫 분기 — 디렉토리 미존재 시 `git worktree add`). 영구 손실 0.

**결과적 동작 시나리오** (활성 PR 이 compose 모드만 있는 케이스):
- Agent 재기동 → `RestoreOrphans` 가 라벨 매칭 0 건 → `activeIDs = []`
- `PruneStaleWorktrees(ctx, [])` 가 `{workDir}/repos/*/worktrees/preview-*` 를 **모두 삭제**
- `PruneComposeOrphans` 가 `runner.RunningPreviewIDs() == []` 기준으로 `preview-*` compose 프로젝트를 모두 down (compose 모드 활성 PR 도 down 됨)
- 다음 JOB_ASSIGN 에서 worktree 재생성 + 컨테이너 재기동

위 시나리오는 "Agent 재기동 시 compose 모드 활성 PR 이 모두 재기동된다" 를 의미한다. 본 Phase 는 이를 **의도된 동작**으로 수용 — compose 모드의 라벨 부재라는 본질적 한계의 결과이며, Phase 9 후보 1(compose 컨테이너 라벨 부착) 도입 시 자연 해소.

**Phase 9 라벨 부착 도입 후 재고**: §10 Phase 9 후보 1 에서 compose 모드 컨테이너에 `hub-preview-id` 라벨이 부착되면, `RestoreOrphans` 가 compose 모드 previewID 도 함께 반환 → `activeIDs` 비대칭 해소 → 본 결정의 "재기동" 시나리오도 사라진다. 본 결정에는 "Phase 9 라벨 부착 도입 후 재고" 레이블 부착.

**대안 기각**:
- **Worktree 정리 비활성화 (compose 모드 케이스 회피)** — 영구 디스크 leak 발생. 본 Phase 의 항목 1 자체가 무의미.
- **`PruneStaleWorktrees` 의 activeIDs 에 compose ls 결과의 previewID 도 합치기** — compose ls 가 실패하면 잘못된 activeIDs 로 worktree 삭제 → 더 위험. 또한 cleanup 두 호출이 호출 순서에 결합 → 코드 복잡도 ↑.
- **Worktree 보존 정책(예: 24시간 grace period)** — mtime 검사 도입 + 정책 결정 부담. 본 Phase 비범위.

**되돌릴 때 비용**: 본 결정은 코드 변경 0 (이미 `PruneStaleWorktrees` 가 그렇게 동작). 결정문 자체만 제거. 작음. 단, 정책 자체를 바꾸려면(예: compose-aware 보존) §1-2 목표 재정의 필요.

---

## 4. 명세 상세

### 4-1. `compose_orphan.go` (신규)

```go
// 이 파일의 책임:
//   - `docker compose ls --format json` 출력으로 실행 중 compose 프로젝트를 발견하고,
//     이름이 "preview-" 접두사이면서 runner.jobs 맵에 없는 (orphan) 프로젝트를
//     `docker compose --project-name {name} down` 으로 정리한다.
//   - 호출자(cmd/agent/main.go)는 best-effort 로 사용 — 실패 시 WARN + 계속.
//
// 참고: docs/specs/phase-8-orphan-cleanup.md §4-1, 결정 1/3/6/7/8.
package agent

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "strings"
)

const composeProjectPrefix = "preview-"

// composeProject 는 `docker compose ls --format json` 응답의 1 항목.
// (결정 10 참조: 본 Phase 는 Name 만 사용, 나머지는 forward-compat.)
type composeProject struct {
    Name        string `json:"Name"`
    Status      string `json:"Status"`
    ConfigFiles string `json:"ConfigFiles"`
}

// PruneComposeOrphans 는 실행 중 compose 프로젝트 중 runner.jobs 맵에 없는
// preview-* 프로젝트를 down 한다.
//
//   - 반환: 정리된 프로젝트 수, 첫 번째 에러(`compose ls` 실패 또는 down 1 건 실패).
//   - `compose ls` 실패 시 (0, err) 반환 — down 호출 0.
//   - down 1 건 실패해도 나머지 프로젝트는 계속 처리, 첫 에러를 보관.
//   - runner == nil 이면 (0, nil) — defensive (테스트 외 발생 X).
func PruneComposeOrphans(ctx context.Context, cmd CmdRunner, runner *Runner, logger *slog.Logger) (int, error) {
    if runner == nil || cmd == nil {
        return 0, nil
    }

    out, err := cmd.Output(ctx, "docker", "compose", "ls", "--format", "json")
    if err != nil {
        return 0, fmt.Errorf("compose ls: %w", err)
    }

    projects, err := parseComposeLs(out)
    if err != nil {
        return 0, fmt.Errorf("parse compose ls: %w", err)
    }

    active := make(map[string]struct{})
    for _, id := range runner.RunningPreviewIDs() {
        active["preview-"+id] = struct{}{}
    }

    var (
        pruned   int
        firstErr error
    )
    for _, p := range projects {
        if !strings.HasPrefix(p.Name, composeProjectPrefix) {
            continue
        }
        if _, ok := active[p.Name]; ok {
            continue
        }
        if derr := cmd.Run(ctx, "docker", "compose", "--project-name", p.Name, "down"); derr != nil {
            logger.Warn("agent_compose_orphan_down_failed",
                "project", p.Name, "err", derr.Error())
            if firstErr == nil {
                firstErr = derr
            }
            continue
        }
        logger.Info("agent_compose_orphan_pruned", "project", p.Name)
        pruned++
    }
    return pruned, firstErr
}

// parseComposeLs 는 `docker compose ls --format json` 출력을 파싱한다.
//   - 빈 출력 / 빈 배열 → (nil, nil).
//   - 잘못된 JSON → (nil, err).
func parseComposeLs(out string) ([]composeProject, error) {
    out = strings.TrimSpace(out)
    if out == "" || out == "[]" {
        return nil, nil
    }
    var ps []composeProject
    if err := json.Unmarshal([]byte(out), &ps); err != nil {
        return nil, err
    }
    return ps, nil
}
```

### 4-2. `compose_orphan_test.go` (신규)

```go
package agent

import (
    "context"
    "errors"
    "io"
    "log/slog"
    "strings"
    "testing"
)

// fakeCmd 는 Output 응답과 Run 호출 기록을 가진다.
type fakeCmd struct {
    lsOut    string
    lsErr    error
    downCmds [][]string // 각 호출의 args 기록
    downErrs map[string]error
}

func (f *fakeCmd) Run(ctx context.Context, name string, args ...string) error {
    f.downCmds = append(f.downCmds, append([]string{name}, args...))
    // args = ["compose","--project-name", projectName, "down"]
    if len(args) >= 3 {
        if e, ok := f.downErrs[args[2]]; ok {
            return e
        }
    }
    return nil
}

func (f *fakeCmd) Output(ctx context.Context, name string, args ...string) (string, error) {
    return f.lsOut, f.lsErr
}

func newDiscardLogger() *slog.Logger {
    return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// F-7: 빈 배열 → 정리 0
// F-8: orphan 1 + active 1 → 1 down
// F-9: ls 에러 → 정리 0
// F-10: 잘못된 JSON → 에러
// F-11: down 1 건 실패해도 나머지 계속
// F-12: 접두사 미일치(other-app) 무시
// F-13: nil runner / nil cmd 안전 처리
```

(실제 테스트 본문은 Step 1 구현 시 작성. 케이스 매핑은 §6-2 표 참조.)

### 4-3. `cmd/agent/main.go` 변경 요지

**현재(Phase 7) → Phase 8 의 diff** (필수 2 반영 — `SetReadySender`/`SetMaxJobs` 가 기존 위치에서 **제거되고** cleanup 뒤로 **이동** 됨을 `-`/`+` 마커로 명시. 결정 2 의 옵션 (b) 채택 결과 cmd 인자는 `runner.Cmd()`).

```go
   runner.SetTraefikPort(cfg.TraefikPort)
   runner.SetTraefikAPIPort(cfg.TraefikAPIPort)
   runner.SetRouterReadyTimeout(cfg.RouterReadyTimeout)
   c.SetRunner(runner)

-  // Phase 5: 다중 Job 슬롯 + READY 재전송 의존 주입.
-  runner.SetReadySender(c)
-  runner.SetMaxJobs(cfg.MaxJobs)
-
-  // Phase 3: orphan container restore.
-  if _, rerr := agent.RestoreOrphans(ctx, docker, runner, cfg.AdvertiseHost, logger); rerr != nil {
-      logger.Warn("agent_orphan_restore_failed", "err", rerr.Error())
-  }

+  // Phase 8: orphan cleanup 은 READY 송신 전에 (race 방지, 결정 4).
+  // Phase 3: orphan container restore.
+  activeIDs, rerr := agent.RestoreOrphans(ctx, docker, runner, cfg.AdvertiseHost, logger)
+  if rerr != nil {
+      logger.Warn("agent_orphan_restore_failed", "err", rerr.Error())
+  }
+  // Phase 8 항목 1: worktree orphan 정리.
+  if n, perr := cache.PruneStaleWorktrees(ctx, activeIDs); perr != nil {
+      logger.Warn("agent_prune_stale_worktrees_failed", "pruned", n, "err", perr.Error())
+  } else if n > 0 {
+      logger.Info("agent_prune_stale_worktrees", "pruned", n)
+  }
+  // Phase 8 항목 2: compose 프로젝트 orphan 정리. cmd 인자는 결정 2 옵션 (b) 의 getter.
+  if n, perr := agent.PruneComposeOrphans(ctx, runner.Cmd(), runner, logger); perr != nil {
+      logger.Warn("agent_prune_compose_orphans_failed", "pruned", n, "err", perr.Error())
+  } else if n > 0 {
+      logger.Info("agent_prune_compose_orphans", "pruned", n)
+  }
+
+  // Phase 5: 다중 Job 슬롯 + READY 재전송 의존 주입 (cleanup 후에 wire — 결정 4).
+  runner.SetReadySender(c)
+  runner.SetMaxJobs(cfg.MaxJobs)
```

요약:
- 기존 라인 (`SetReadySender` / `SetMaxJobs` / `RestoreOrphans` 블록) 3 묶음을 모두 제거.
- 새 블록을 (1) RestoreOrphans + activeIDs 캡처 → (2) PruneStaleWorktrees → (3) PruneComposeOrphans → (4) SetReadySender + SetMaxJobs 의 4 단계로 재구성.
- `runner.Cmd()` 는 결정 2 의 옵션 (b) 로 `internal/agent/runner.go` 에 신규 1 줄 메서드(`func (r *Runner) Cmd() CmdRunner { return r.cmd }`).

### 4-4. `docker compose ls --format json` 응답 스키마

| 필드 | 타입 | 의미 | 본 Phase 사용 |
|---|---|---|---|
| `Name` | string | 프로젝트 이름. compose v2 의 `--project-name` 인자가 그대로 노출 | **사용** (prefix 매칭) |
| `Status` | string | `"running(N)"`, `"exited(N)"` 등 사람용 표현 | 미사용 (결정 6) |
| `ConfigFiles` | string | 프로젝트가 로드한 compose 파일 절대 경로(콤마 구분) | 미사용 |

전체 응답: 위 객체의 **JSON 배열** (`[]`). 빈 결과는 `[]\n`. 주의: 다른 docker 명령(`docker ps --format json`)은 NDJSON(줄당 1 객체) 인 반면 `compose ls` 는 단일 배열. 본 Phase 는 배열 가정.

### 4-5. 영향받는 파일 목록

| 파일 | 변경 종류 |
|---|---|
| `internal/agent/compose_orphan.go` | **신규** — `PruneComposeOrphans`, `composeProject`, `parseComposeLs` |
| `internal/agent/compose_orphan_test.go` | **신규** — F-7 ~ F-17 단위 테스트 |
| `internal/agent/runner.go` | 1 줄 메서드 추가 — `func (r *Runner) Cmd() CmdRunner { return r.cmd }` (결정 2 옵션 (b)) |
| `cmd/agent/main.go` | wiring 재구성 — 기존 `SetReadySender`/`SetMaxJobs`/`RestoreOrphans` 3 블록 제거 + cleanup 4 단계 신규 (§4-3 diff) |

총 신규 파일 2 개, 수정 파일 2 개.

### 4-6. 디렉토리 트리 (변경 후 요지)

```
internal/agent/
├── compose_orphan.go        (신규)
├── compose_orphan_test.go   (신규)
├── runner.go                (수정: Cmd() getter 1 줄 추가 — 결정 2 옵션 (b))
├── repocache_multi.go       (무변경 — 이미 PruneStaleWorktrees 구현됨)
├── orphan_restore.go        (무변경 — 이미 RestoreOrphans 구현됨)
└── ... (그 외 무변경)

cmd/agent/
└── main.go                  (수정: wiring 재구성)
```

---

## 5. 시퀀스 다이어그램 (ASCII)

### 5-1. Agent 부팅 — 정상 경로

**시나리오** (필수 3 반영 — 모드별 일관성):
- `p1`, `p2`: Dockerfile 모드 활성 PR. `hub-preview-id` 라벨 부착 컨테이너 → `RestoreOrphans` 가 잡음. compose 프로젝트로는 등록되지 않으므로 `compose ls` 결과에 미등장.
- `p3`: 과거 Dockerfile 모드 PR 의 잔존 worktree(컨테이너는 이미 사라짐). `RestoreOrphans` 가 잡지 못함 → activeIDs 에 미포함 → `PruneStaleWorktrees` 가 worktree 디렉토리만 제거.
- `p4`: 과거 compose 모드 PR 의 잔존 프로젝트(컨테이너 라벨 없음). `compose ls` 에는 잡히지만 `runner.RunningPreviewIDs()` 에 미등록 → `PruneComposeOrphans` 가 down.
- `other-app`: 비-Agent 의 외부 compose 프로젝트(접두사 미일치) → 무시.

```
Agent main                  Docker                       Filesystem
 |                          |                            |
 | EnsureTraefik         -->|                            |
 | NewRunner                |                            |
 | RestoreOrphans        -->|  ContainerList(label)      |
 |  (결과: ["p1","p2"])     |  → p1, p2 (Dockerfile 모드) |
 |                          |  RegisterRestoredJob 2회   |
 |                          |                            |
 | PruneStaleWorktrees(["p1","p2"])                      |
 |                                       ReadDir repos → |
 |                                       slug/worktrees/ |
 |                                       preview-p1, p2, |
 |                                       p3 발견         |
 |                                       (p3 가 active   |
 |                                        에 없음)       |
 |                                  RemoveAll preview-p3 |
 |  log: agent_prune_stale_worktrees pruned=1            |
 |                          |                            |
 | PruneComposeOrphans   -->|  compose ls --format json  |
 |                          |  → [preview-p4, other-app] |
 |                          |  (p1/p2 는 Dockerfile 모드  |
 |                          |   이라 compose ls 미등장)  |
 |                          |  active = {preview-p1,     |
 |                          |            preview-p2}     |
 |                          |    (RunningPreviewIDs +    |
 |                          |     "preview-" 접두사)     |
 |                          |  preview-p4 → orphan       |
 |                          |  other-app → 접두사 미일치 |
 |                          |  compose --project-name    |
 |                          |    preview-p4 down         |
 |  log: agent_prune_compose_orphans pruned=1            |
 |                          |                            |
 | SetReadySender + READY 송신 (정리 후)                 |
```

### 5-2. `compose ls` 실패 경로

```
Agent main                  Docker
 | PruneComposeOrphans   -->|  compose ls --format json
 |                          |  ← exit 1, "Cannot connect to Docker"
 | (PruneComposeOrphans 가 (0, err) 반환)
 | log: agent_prune_compose_orphans_failed err=...
 | (Agent 부팅 계속, READY 송신)
```

### 5-3. orphan 0 개 경로

```
Agent main                  Docker
 | RestoreOrphans → []
 | PruneStaleWorktrees([]) → (0, nil)
 |   (worktrees 디렉토리 자체 미존재면 즉시 0 반환)
 | PruneComposeOrphans
 |   compose ls → []
 |   parseComposeLs → (nil, nil)
 |   loop 0 회 → (0, nil)
 | (로그 0 — 정리한 게 없으면 silent)
```

---

## 6. 기능 체크리스트 (F-*)

### 6-1. Worktree wiring

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-1 | `cmd/agent/main.go` 가 `RestoreOrphans` 반환값(activeIDs)을 캡처 | 코드 리뷰 (grep `activeIDs.*RestoreOrphans` in `cmd/agent/main.go`) |
| F-2 | `cache.PruneStaleWorktrees(ctx, activeIDs)` 가 부팅 시 1 회 호출됨 | 코드 리뷰 (grep `cache.PruneStaleWorktrees(ctx, activeIDs)` in `cmd/agent/main.go` — 매치 정확히 1 회) |
| F-3 | `PruneStaleWorktrees` 가 에러 반환 시 `agent_prune_stale_worktrees_failed` WARN 로그 + 부팅 계속(panic / os.Exit 호출 0) | 코드 리뷰 (grep `agent_prune_stale_worktrees_failed` in `cmd/agent/main.go` — `logger.Warn` 의 인자로만 등장; 같은 if 분기에 panic/Fatal 미등장) |
| F-4 | `PruneStaleWorktrees` 가 (n>0, nil) 반환 시 `agent_prune_stale_worktrees pruned={n}` Info 로그 1 회 | 코드 리뷰 (grep `agent_prune_stale_worktrees"` (key 명) in `cmd/agent/main.go` — `logger.Info` 호출에 `"pruned", n` attr 동반) |
| F-5 | `RestoreOrphans` 가 빈 슬라이스 반환 시 `PruneStaleWorktrees(ctx, [])` 가 호출되어 모든 worktree 가 삭제되더라도, 다음 JOB_ASSIGN 의 `cache.Ensure + Checkout` 흐름에서 worktree 가 정상 재생성된다 (결정 11 의 안전성 분석). **검증 시나리오**: (1) `RestoreOrphans` mock 이 `[]` 반환, (2) `PruneStaleWorktrees(ctx, [])` 호출 시 모든 `preview-*` worktree 디렉토리 RemoveAll, (3) 직후 `Handle(ctx, JOB_ASSIGN{previewID: "x"})` 가 정상 종료(에러 0). **레이블**: "Phase 9 라벨 부착 도입 후 재고" — Phase 9 후보 1(compose 라벨 부착)이 도입되면 본 시나리오 자체가 사라짐. | 단위 (`repocache_multi_test.go` 의 nil/empty 케이스 회귀) + 통합(선택, integration tag) |
| F-6 | `PruneStaleWorktrees` / `PruneComposeOrphans` 호출이 `runner.SetReadySender(c)` **이전** 위치 (결정 4) | 코드 리뷰 (`cmd/agent/main.go` 의 grep — `PruneComposeOrphans` 의 line 번호 < `SetReadySender` 의 line 번호) |

### 6-2. `PruneComposeOrphans` 단위

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-7 | `compose ls` 가 `[]` 반환 → `(0, nil)` + `Run` 호출 0 | 단위 (fakeCmd.lsOut="[]", downCmds 길이 == 0) |
| F-8 | `compose ls` 가 `[{Name:"preview-p2"},{Name:"preview-p4"}]` + runner 에 `p1` 만 등록 → `(2, nil)` + 두 `Run` 호출 발생. **각 호출의 args 정확값**(권장 D — `fakeCmd.Run` 의 기록은 `append([]string{name}, args...)` 로 name 포함 5 토큰): `["docker","compose","--project-name","preview-p2","down"]` 와 `["docker","compose","--project-name","preview-p4","down"]`. | 단위 |
| F-9 | `compose ls` 자체가 에러 → `(0, err)` + `Run` 호출 0 | 단위 (fakeCmd.lsErr != nil) |
| F-10 | `compose ls` 출력이 잘못된 JSON("not-json") → `(0, err)` + `Run` 호출 0 | 단위 |
| F-11 | `compose ls` → `[{Name:"preview-p2"},{Name:"preview-p4"}]`, `runner` 비어있음, `down` 호출 1 건이 에러 → `(1, firstErr)` + 두 down 호출 모두 발생 | 단위 (downErrs map 으로 `preview-p2` 만 에러, `preview-p4` 는 성공) |
| F-12 | `compose ls` → `[{Name:"preview-p2"},{Name:"other-app"}]`, runner 비어있음 → `(1, nil)` + `other-app` 은 무시. **Run 인자 검사**: 5 토큰 형태 `["docker","compose","--project-name","preview-p2","down"]` 1 회만 기록. | 단위 |
| F-13 | `cmd == nil` 또는 `runner == nil` → `(0, nil)` + 패닉 없음. **의도(권장 C)**: 본 방어는 "테스트 외 발생 X" 의 defensive guard. 호출자(`cmd/agent/main.go`)는 항상 두 인자를 전달 — production 에서 nil 진입은 코딩 실수. early return + (silent, log 없음). 함수 주석에 명시. | 단위 |
| F-14 | `compose ls` 출력이 빈 문자열("") → `(0, nil)` + `Run` 호출 0 (parseComposeLs 의 빈 처리) | 단위 |
| F-15 | `parseComposeLs` 가 정의되지 않은 추가 필드(`"Created":12345`)를 무시하고 정상 파싱 (forward-compat) | 단위 (json 입력에 추가 필드 포함) |
| F-16 | `parseComposeLs("[]\n")` (trailing newline) 이 `(nil, nil)` 반환 — Compose v2 의 실 출력 포맷 | 단위 |
| F-17 | active 매핑이 `runner.RunningPreviewIDs()` 의 각 id 에 `"preview-"` 접두사를 붙여 비교 — `RunningPreviewIDs` 가 `["abc"]` 면 `preview-abc` 만 active. compose 응답의 `Name="preview-abc"` 일 때 정리 대상 X. | 단위 (runner 에 abc 등록 + ls 응답에 preview-abc → down 호출 0) |

### 6-3. wiring 회귀 / 환경

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-18 | Phase 0~7 의 모든 단위 테스트 회귀 0 | `go test ./...` |
| F-19 | `go vet ./...` clean | CI |
| F-20 | `go build ./...` 성공 | CI |
| F-21 | `cmd/agent/main.go` 의 호출 순서가 `EnsureTraefik → NewRunner+SetTraefik(API)Port+SetRouterReadyTimeout → SetRunner → RestoreOrphans → PruneStaleWorktrees → PruneComposeOrphans → SetReadySender → SetMaxJobs` (결정 4) | 코드 리뷰 (`cmd/agent/main.go` 의 grep — 8 개 호출의 line 번호가 위 순서로 단조증가) |
| F-22 | `internal/agent` 가 `internal/hub` 또는 `cmd/*` import 0 (Phase 6 NF-6 유지) | grep |
| F-23 | `compose_orphan.go` 의 imports 가 표준 라이브러리만 (`context`, `encoding/json`, `fmt`, `log/slog`, `strings`) | grep |
| F-24 | **(필수 2)** 기존 `runner.SetReadySender(c)` / `runner.SetMaxJobs(cfg.MaxJobs)` 두 호출이 Phase 7 main.go 의 `RestoreOrphans` **이전** 위치에서 **제거**되어 있고, 정확히 1 곳(cleanup 4 단계 뒤)에만 등장 | 코드 리뷰 (`cmd/agent/main.go` 의 grep — 두 함수 호출이 각 정확히 1 회 매치 + 그 line 번호가 `PruneComposeOrphans` 의 line 번호보다 큼) |
| F-25 | `internal/agent/runner.go` 에 `func (r *Runner) Cmd() CmdRunner { return r.cmd }` 신규 1 줄 메서드(결정 2 옵션 (b)) — 시그니처/리턴값 정확 | 코드 리뷰 (`internal/agent/runner.go` 의 grep `func \(r \*Runner\) Cmd\(\) CmdRunner` — 정확 매치 1 회) |

---

## 7. 비기능 체크리스트 (NF-*)

| ID | 항목 | 검증 방법 |
|---|---|---|
| NF-1 | 외부 의존성 0 추가 — `compose_orphan.go` 는 표준 라이브러리만 사용 | `go mod tidy` diff |
| NF-2 | 신규 파일 모두 책임 주석(3~5 줄) 헤더 포함 | grep `// 이 파일의 책임:` |
| NF-3 | 어떤 파일도 300 줄을 넘지 않는다(테스트 파일 포함). 신규 파일 예상: `compose_orphan.go` ~80 줄, `compose_orphan_test.go` ~150 줄. | `wc -l internal/agent/*.go internal/agent/*_test.go cmd/agent/*.go` |
| NF-4 | `go vet ./...`, `golangci-lint run` clean | CI |
| NF-5 | `go test -race ./...` green | CI |
| NF-6 | 레이어 의존: `internal/agent` 가 `internal/hub` 또는 `cmd/*` import 0 | depguard / grep |
| NF-7 | `slog` 키 명명 컨벤션: `agent_prune_*` 패밀리(`agent_prune_stale_worktrees`, `agent_prune_stale_worktrees_failed`, `agent_prune_compose_orphans`, `agent_prune_compose_orphans_failed`, `agent_compose_orphan_pruned`, `agent_compose_orphan_down_failed`) | grep |
| NF-8 | `compose_orphan.go` 가 외부 docker daemon 미요구 — fakeCmdRunner 로 단위 테스트 가능 | CI 환경 docker 미설치에서 단위 테스트 통과 |
| NF-9 | `PruneComposeOrphans` 가 `runner.RunningPreviewIDs()` 호출 1 회만(반복 호출 금지) — 호출 도중 jobs 맵 변화 시 race 가능성 회피 | 코드 리뷰 |
| NF-10 | `compose down` 호출은 순차 — 동시 docker 호출로 인한 daemon 부하 회피 (Phase 5 의 maxJobs 와 무관, 부팅 1 회 cleanup 이라 충분히 빠름) | 코드 리뷰 |
| NF-11 | wiring 의 두 호출이 `RestoreOrphans` 결과의 `activeIDs` 와 `runner.RunningPreviewIDs()` 두 종류의 active 기준을 구분해서 사용 — worktree 정리는 RestoreOrphans 결과 직접 활용, compose 정리는 RunningPreviewIDs (RestoreOrphans 후 jobs 맵 등록 결과). 두 기준은 정상 케이스에서 동일하지만 의미가 다르므로 명세에 명시. | 코드 리뷰 + 본 기획서 §결정 4 |

---

## 8. 단계 분할 (구현·평가용)

본 Phase 는 **2 Step** 으로 나눈다.

### Step 1 — `compose_orphan.go` + 단위 테스트 (Agent 단독)

- `internal/agent/compose_orphan.go` 신규.
- `internal/agent/compose_orphan_test.go` 신규 — F-7 ~ F-17.
- `go test ./internal/agent/...` 통과.
- 이 단계 끝나면 wiring 미반영이지만 함수 자체 검증 완료.

### Step 2 — wiring + 회귀

- `cmd/agent/main.go` 변경 — 호출 순서 재배치 + 두 호출 추가.
- `go build ./...` + `go vet ./...` + `go test ./...` 회귀 0.
- F-1 ~ F-6, F-18 ~ F-23 통과.

---

## 9. 리스크와 완화

| 리스크 | 완화 |
|---|---|
| **R1** Compose v2 의 `compose ls --format json` 출력 스키마가 docker 버전마다 다름 — 특히 v2.0 이전의 lower-case 키(`name` vs `Name`) | Step 1 직전에 Q1 의 1 회 확인(소스/공식 문서). v2.20+ 는 PascalCase 안정. AGENTS.md 의 docker 24.0+ 가정으로 lower-case 케이스는 사실상 발생 X. 만약 발견되면 `composeProject` 의 json tag 를 양쪽으로(`json:"Name"`) + `parseComposeLs` 가 lower-case fallback 시도 한 번. |
| **R2** `docker compose ls` 가 데몬 미가동 / 권한 부재로 실패 — Agent 부팅마다 매번 cleanup 실패 로그 → 운영자 노이즈 | best-effort 정책으로 Agent 부팅 자체는 정상 진행. 노이즈는 1 회 / 부팅 1 회. 실 배포 환경은 데몬 항상 가동(Phase 6 의 `EnsureTraefik` 가 같은 데몬 사용 — 데몬 다운이면 EnsureTraefik 가 먼저 실패하므로 본 호출의 추가 로그는 redundant 정도). |
| **R3** 정리 도중 신규 JOB_ASSIGN 도착 → 동일 previewID 의 compose 프로젝트가 막 만들어지는 도중 down — race | 결정 4 의 wiring 위치(SetReadySender 이전)로 회피. SetReadySender 호출 전이면 READY 송신 자체가 안 일어나므로 Hub 가 신규 JOB 을 보내지 않음. F-6/F-21 로 검증. |
| **R4** 운영자가 의도적으로 `preview-`로 시작하는 비-Agent compose 프로젝트를 같은 호스트에 띄움 — 본 Phase 가 죽임 | 1 호스트 1 Agent + `preview-` namespace 점유 가정(AGENTS.md). 운영자 가이드에 "preview-* 프로젝트 이름은 Agent 전용" 명시 — 본 기획서 비범위(README 변경 비범위) 지만 Q3 로 추적. |
| **R5** `compose ls` 출력이 매우 큼(수백 프로젝트) — Agent 부팅 지연 | 일반적으로 1 호스트의 compose 프로젝트는 수십 개 미만. JSON 파싱 + 순차 down 합산 ~10 s 미만. 부팅 시 1 회만이라 주기적 부담 0. timeout 강제는 비범위. |
| **R6** `compose down` 도중 SIGTERM 수신 → 부분 정리 상태로 종료 | 본 Phase 의 `ctx` 는 main 의 root ctx — SIGTERM 시 cancel. `cmd.Run` 이 ctx 전파(execRunner 는 `exec.CommandContext`) → 진행 중 down 이 중단. 다음 부팅에서 재시도. Idempotent. |
| **R7** `RestoreOrphans` 가 활성 jobs 등록은 했지만 `RunningPreviewIDs()` 에 반영되기 전(반환 직후) 에 `PruneComposeOrphans` 가 호출될 수 있는가? | `RestoreOrphans` 의 `RegisterRestoredJob` 은 동기적 `r.jobs.Store` 호출(코드 확인). 함수 반환 시점에는 모든 등록 완료. 따라서 직후 `RunningPreviewIDs()` 호출이 누락 없이 반환. race 0. |
| **R8** Worktree 가 active 컨테이너의 build context 로 마운트되어 있으면 RemoveAll 이 fail | Phase 6 까지의 Agent 는 worktree 를 컨테이너 build context 로만 사용(명령 종료 시 컨테이너는 worktree 미참조). 마운트 케이스 없음. RemoveAll 실패 시 `PruneStaleWorktrees` 의 firstErr 보관 + 다음 worktree 계속(기존 구현). 디스크 leak 만 발생 — Agent 동작에 영향 0. |
| **R9** **(필수 4)** 운영자가 `preview-`로 시작하는 비-Agent compose 프로젝트(예: `preview-traefik`, `preview-grafana`)를 같은 호스트에 띄울 경우 본 Phase 가 down 시킬 수 있음. 특히 Phase 9 후보에서 Traefik 을 compose 화하면 `preview-traefik` 자체가 정리 대상이 될 수 있다. | **현 Phase 영향 0**: Phase 7 까지의 `EnsureTraefik` 는 `docker run` 기반이라 `compose ls` 에 미등장. **즉각 완화**: 1 호스트 1 Agent + `preview-*` namespace 는 Agent 전용이라는 가정(AGENTS.md). **장기 완화**: §10 Phase 9 후보 1(라벨 부착) 도입 후 라벨 화이트리스트(`hub-preview-id` 라벨이 부착된 프로젝트만 정리) 로 자연 해소. 또는 Phase 9 신규 후보로 "정리 화이트리스트(`--prune-compose-allowlist`) 도입" 검토. Q3 와 연동. |

---

## 10. 다음 Phase 연결점

- **Phase 9 후보 1** — compose 모드 컨테이너에 `hub-preview-id` 라벨 부착(서비스 단위) → orphan 발견 경로를 `RestoreOrphans` 와 통합. `PruneComposeOrphans` 는 단순 안전망으로 격하 또는 제거.
- **Phase 9 후보 2** — 주기적 cleanup ticker (디스크 워치독). 부팅 시 1 회 + 매 N 시간마다.
- **Phase 9 후보 3** — 이미지/볼륨 prune 정책. compose `down -v --rmi local` 또는 `docker system prune --filter` 신중 도입.
- **Phase 9 후보 4** — orphan 발견 시 Hub 텔레메트리 송신(STATUS_UPDATE 확장 또는 별 메시지) → 운영자 가시성.
- **Phase 9 후보 5** — `--prune-stale-worktrees=false` / `--prune-compose-orphans=false` opt-out 플래그 도입(결정 5 의 재고).
- **Phase 9 후보 6** — `--prune-compose-allowlist` 화이트리스트(R9 의 운영자 외부 `preview-*` 프로젝트 보호). 라벨 부착(후보 1) 도입 시 자연 흡수.

---

## 11. 미해결 / 확인 사항 (Open Questions)

| ID | 질문 | 잠정 처리 |
|---|---|---|
| Q1 | ~~`docker compose ls --format json` 의 정확한 키 케이스(`Name` vs `name`)가 docker 24.0 / 25.0 / 27.0 모두 동일한가?~~ | **종결(권장 B)** — Compose v2 의 [`cmd/compose/ls.go` 의 `formatter.Project` 구조체](https://github.com/docker/compose/blob/v2.20.0/cmd/compose/ls.go) 가 v2.0 GA(2022-06)부터 PascalCase(`Name`/`Status`/`ConfigFiles`)를 일관 marshal. AGENTS.md 의 docker 24.0+ 가정에서 lower-case 케이스는 발생 X. 결정 3 에 인용 근거 명시. fallback 미도입. |
| Q2 | `PruneComposeOrphans` 의 `cmd CmdRunner` 첫 인자를 `*Runner` 의 메서드(`runner.PruneComposeOrphans()`)로 옮길까? | 본 Phase 에서는 함수로 유지(결정 2 옵션 (b) — `runner.Cmd()` getter 로 cmd 주입 명시성 유지 + 단위 테스트 단순성). 메서드화는 후속 Phase. |
| Q3 | 운영자가 의도적으로 `preview-*` 이름의 외부 compose 프로젝트를 띄울 가능성 — `down` 사고 방지를 위해 추가 라벨 검사가 필요한가? | 본 Phase 는 1 호스트 1 Agent + `preview-` namespace 점유 가정으로 추가 검사 없음(R9 와 연동). Phase 9 후보 1(라벨 부착) 도입 시 라벨 기반 검증 추가, 또는 후보 6(allowlist) 검토. AGENTS.md 에 가정 명시 검토 — 본 Phase 비범위. |
| Q4 | ~~`parseComposeLs` 의 lower-case fallback 을 본 Phase 에 같이 도입할까(R1 사전 방어)?~~ | **종결(권장 B)** — Q1 종결과 함께 미도입 확정. |
| Q5 | `PruneComposeOrphans` 의 동시 cleanup(병렬 down) 이 부팅 지연 단축에 의미 있을까? | 일반적으로 orphan 수 < 5 → 순차로 충분. 병렬 도입은 docker daemon 부하 + 코드 복잡도 ↑ vs 이득 작음. 미도입. R5 와 일관. |
| Q6 | compose v2 가 `--format json` 외에 `--format=json=Name` 같은 필드 선택을 지원하나? 향후 출력량 절감 가능성. | v2.27+ 에서 Go template 지원 일부. 본 Phase 는 forward-compat 하게 모든 필드 받음(결정 10). 미사용 필드 무시 비용 0. 불도입. |

이후 새 Q 가 발견되면 본 섹션 아래에 (Q7, Q8 …) 으로 추가하고, plan-review 단계에서 다시 결의/비범위로 분리한다.

---

### Self-review / plan-reviewer 1차에서 처리된 항목 (DRAFT planner 2차)

plan-reviewer 1차의 필수 6 + 권장 4 항목 모두 반영:

| 라운드 1 지적 | 처리 결과 |
|---|---|
| **필수 1** §4-3 wiring 의 `execRunner`/`gitRunner` 변수 미존재 | 결정 2 갱신 — 옵션 (b) 채택: `internal/agent/runner.go` 에 `func (r *Runner) Cmd() CmdRunner` getter 1 줄 추가. main.go wiring 은 `runner.Cmd()` 인자. (a)/(c) 기각 근거 명시. §4-3 코드 블록 + §4-5 영향 파일 표(runner.go 추가) + §4-6 디렉토리 트리 + F-25 신규 모두 동기화. |
| **필수 2** §4-3 의 `SetReadySender`/`SetMaxJobs` 이동 미명시 | §4-3 코드 블록을 `-`/`+` 마커 diff 형태로 재작성 — 기존 두 줄이 `RestoreOrphans` 이전 위치에서 **제거**되고 cleanup 4 단계 뒤로 **이동** 됨을 시각적으로 명시. 결정 4 의 "현재 main.go 의 호출 순서 변경 영향" 에도 동일 사실 추가. F-21 의 검증 방법을 단조증가 line 번호 검사로 구체화 + F-24 신규(기존 두 줄이 정확히 1 곳에만 등장). §1-4 성공 기준에도 마지막 불릿 추가. |
| **필수 3** §5-1 시퀀스의 모드별 모순 | 시나리오 단락 신규 — `p1`/`p2`(Dockerfile, 라벨 있음, RestoreOrphans 가 잡음, compose ls 미등장), `p3`(과거 Dockerfile worktree 잔존, RestoreOrphans 미감지, PruneStaleWorktrees 가 정리), `p4`(과거 compose 모드 프로젝트, compose ls 만 잡음, PruneComposeOrphans 가 down). compose ls 결과를 `[preview-p4, other-app]` 로 수정해 active set `{preview-p1, preview-p2}` 와 분리. |
| **필수 4** §9 R-누락 (운영자 외부 `preview-*` 프로젝트) | R9 신규: 현 Phase 영향 0(EnsureTraefik 가 `docker run`) + Phase 9 라벨 화이트리스트 또는 후보 6(`--prune-compose-allowlist`) 도입. §10 Phase 9 후보 6 신규. Q3 와 연동. |
| **필수 5** F-2/F-3/F-4 검증 방법 모호 | "단위 / 코드 리뷰" 양립 표현 제거 → 모두 "코드 리뷰 (grep ...)" 단일화 + 정확한 grep 패턴/매치 횟수 명시. "어려우면" 같은 조건 표현 제거. |
| **필수 6** F-5 안전성 분석 | 결정 11 신규: `PruneStaleWorktrees(ctx, [])` 가 모든 worktree 를 삭제해도 안전한 3 가지 근거(worktree 미마운트 / `compose down` 의 project-name 단독 동작 / `Checkout` 재생성). "Phase 9 라벨 부착 도입 후 재고" 레이블 부착. F-5 검증 시나리오를 (1)~(3) 단계로 구체화. |
| **권장 A** §1-4 성공 기준 ↔ F-* ID 매핑 | §1-4 의 각 불릿 뒤에 대응 F-* ID 괄호 추가. |
| **권장 B** Q1/R1 PascalCase 검증 완결 | 결정 3 의 "버전 가정" 단락에 인용 근거(Compose v2.20 source link) 추가. Q1/Q4 종결(strikethrough). R1 의 fallback 미도입 확정. |
| **권장 C** F-13 nil-check 의도 | F-13 검증 항목에 의도 단락 추가 — defensive guard, production 미진입, early return + silent. 함수 주석에 명시. |
| **권장 D** F-8 Run 호출 인자 정확화 | F-8 / F-12 의 검증값을 `fakeCmd.Run` 의 실제 기록 형태("name 포함 5 토큰") 로 정정. 예시 슬라이스 구체값 명시. |

---

(끝)
