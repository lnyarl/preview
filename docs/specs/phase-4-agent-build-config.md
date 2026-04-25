# Phase 4 — Hub-managed Agent Build Configuration

Status: **APPROVED**
Author: planner
Date: 2026-04-25

---

## 1. Phase 개요

### 1-1. 배경

현재(Phase 0~3) Agent 의 빌드 로직(`internal/agent/runner.go`)은 다음과 같이 **하드코딩**되어 있다.

- 빌드 명령: `docker build -t preview-{previewID}:latest <worktree>` (Docker SDK 직접 호출, 외부 셸 미사용)
- 컨테이너 노출 포트: `ExposedPort: 80` (host:80 매핑)

이 모델은 "사용자 앱이 항상 컨테이너 80 포트로 떠 있고, 단일 Dockerfile 빌드로 충분"하다는 가정을 강제한다. 실제 사용자 앱은 다양하다 — 빌드 전 자산 컴파일이 필요한 모노레포, 노출 포트가 3000/8080/8000 인 일반적인 웹 앱 등을 지원할 수 없다.

### 1-2. 목표

Hub Admin UI 에서 **Agent 단위로** 다음 두 항목을 설정·저장하고, 그 설정을 WS 프로토콜로 Agent 에 푸시해 Runner 가 따르도록 한다.

1. **Build commands** — worktree 에서 순서대로 실행할 다중 셸 명령 라인 (각 라인 = 1 개 명령).
2. **Container port** — `docker run -p HOST:CONTAINER` 의 CONTAINER 측 포트.

설정은 DB(Hub) 에 저장되며 진실의 원천(SoT)이다. Agent 는 메모리에만 보관하며, 재시작 후에는 HELLO/WELCOME 핸드셰이크 직후 Hub 가 푸시하는 `AGENT_CONFIG` 를 받아 다시 채운다.

### 1-3. 비목표 (이 Phase 가 해결하지 않는 것)

- 빌드 명령의 syntax/semantic 검증 (셸은 `sh -c` 가 평가)
- 환경변수 외 비밀(secret) 주입 — Phase 5 이후
- Agent 별 Docker daemon 옵션, registry 인증, BuildKit cache 등
- Runner 의 docker SDK → 셸 명령 마이그레이션 외의 동작 변경 (포트 동적 할당 알고리즘, jobs map, teardown 흐름 등은 그대로 유지)
- 빌드 명령이 `docker build` 가 **아닌** 경우의 이미지 태그 추적 — 합의는 §3 결정 5, §4-2 의 환경변수 계약으로 해결

### 1-4. 성공 기준 (요약)

- Admin UI `/admin/agents/{id}` 페이지가 SSR 로 렌더되고 build_commands / container_port 폼을 가진다.
- POST `/admin/agents/{id}/config` 는 DB 갱신 + 연결된 Agent 가 있으면 `CONFIG_UPDATE` 를 즉시 푸시한다.
- HELLO 직후 Hub 는 `AGENT_CONFIG` 를 한 번 송신한다.
- Agent 는 in-memory config 를 thread-safe 하게 보관, Runner.Handle 이 매번 그것을 읽어 빌드/실행에 반영한다.
- 설정이 비어 있거나(NULL/empty/0) 도착 전이면 기본값(`docker build -t $PREVIEW_IMAGE .`, port 80)으로 동작한다.

---

## 2. In / Out of Scope

### 2-1. In Scope

- DB: `agents` 테이블 컬럼 2 개 추가(마이그레이션 0003).
- `store.Agent` 구조체 + `store.AgentStore` 인터페이스 메서드 확장.
- sqlc 쿼리/생성 코드, sqlite 구현체.
- Hub Admin UI: `GET /admin/agents/{id}` SSR 페이지 + `POST /admin/agents/{id}/config` 처리.
- 기존 `agents.gohtml` 목록의 각 행에 "Configure" (또는 Name 클릭 → 상세) 링크 추가.
- 새 protocol 메시지 타입 2 개: `AGENT_CONFIG`, `CONFIG_UPDATE` (와이어 페이로드 동일).
- Hub: HELLO → WELCOME 다음에 `AGENT_CONFIG` 송신.
- Hub: `POST /admin/agents/{id}/config` 처리 시 연결된 Agent 에 `CONFIG_UPDATE` 푸시.
- `WSJobSender` 패턴 확장: `SendAgentConfig(ctx, agentID, cfg)` 메서드.
- Agent: in-memory config 컴포넌트 + 수신 라우팅 + Runner 가 그것을 사용하도록 변경.
- Runner: 빌드 단계가 셸 명령 시퀀스를 실행, 컨테이너 포트가 config 에서 결정.
- 환경변수 4 개 주입: `$PREVIEW_ID`, `$PREVIEW_IMAGE`, `$PREVIEW_SHA`, `$PREVIEW_BRANCH`.
- 단위 테스트 + e2e (Playwright) — 폼 작성·저장·CONFIG_UPDATE 도달 검증.

### 2-2. Out of Scope

- Build command syntax 검증, dry-run, lint.
- Multi-line shell here-doc, pipefail, working directory 변경 등의 셸 옵션 노출.
- Build cache, BuildKit, registry login.
- `container_port` 외의 추가 포트(여러 포트 노출).
- Per-preview override (preview/PR 별로 build commands 다르게 — 이번에는 Agent 단위만).
- 설정 변경 시 진행 중 build 의 강제 재시작/취소.
- 보안: 명령 실행 권한 분리, sandbox 등 — 운영자가 자기 머신을 신뢰한다는 전제.
- 설정 변경 로그/감사(audit log) 별도 저장 — `slog` 만 남긴다.

### 2-3. Deferred (다음 Phase 후보)

- 환경변수 사용자 정의(secret 주입 포함)
- `BUILD_TIMEOUT`, `RUN_ARGS`, `DOCKER_BUILD_ARGS` 등 추가 필드
- Per-repo / per-PR 설정 override
- 설정 변경 history/audit log
- 설정 적용 결과 헬스체크(첫 build 가 실패하면 알림)
- `dockerfile_missing` 분기 제거(임의의 빌드 명령이 들어오면 Dockerfile 이 없을 수도 있음 — 본 Phase 결정 4 참조)

---

## 3. 설계 결정 (Design Decisions)

> 8 개 이상의 명시적 결정과 근거. 구현 단계에서는 이 결정만 그대로 따른다.

### 결정 1 — 설정의 진실의 원천(SoT)은 Hub DB

**선택**: `agents.build_commands` (TEXT, NULL 허용) + `agents.container_port` (INTEGER, NULL 허용) 두 컬럼.
**근거**: Agent 는 "수신·실행"만 하므로 stateless 측에 가깝다. 재시작 시 잃어도 Hub 가 재푸시하면 복구된다. Per-agent UI 가 이미 Hub 측에 있으므로 동선이 짧다.
**대안 기각**: Agent 측 파일/플래그 — 운영자가 SSH 들어가야 하므로 Vercel 류 셀프호스팅 UX 와 어긋남.

### 결정 2 — NULL/empty/0 = "기본값을 적용한다" (sentinel)

**선택**: `build_commands IS NULL` 또는 빈 배열 → `docker build -t $PREVIEW_IMAGE .` 1 개로 간주. `container_port IS NULL` 또는 0 → 80 으로 간주. 변환은 Agent Runner 진입 시점(`resolveConfig()`)에서 일관 적용.
**근거**: "처음 등록한 Agent 도 즉시 동작" 이라는 Phase 0~3 의 기본 약속을 유지. 또한 와이어 페이로드의 `[]`/`0` 을 "기본값 사용" 의미로 명시 → 클라이언트 호환성 확보.
**대안 기각**: 마이그레이션에서 DEFAULT 채우기 — 사용자 의도(미설정)와 명시적 설정을 구분하지 못함.

### 결정 3 — 빌드 명령은 `sh -c` 로 1 라인씩 직렬 실행, worktree = CWD

**선택**: 각 라인을 `sh -c "<line>"` 로 별도 프로세스 실행. 모든 명령은 `Dir = worktree`, env 는 부모 + 4 개 주입 변수. 한 명령이라도 exit code != 0 이면 즉시 실패(`fail()` → STATUS_UPDATE failed).
**근거**: 셸 의존성 1 개(`sh`)로 최대 호환성. 명령마다 별 프로세스이므로 한 라인의 `cd` 가 다음 라인에 영향 없음 — 사용자가 한 라인에 `cd && ...` 로 묶어야 함을 reference 표에서 명시.
**대안 기각**: bash heredoc 한 덩어리 → 운영체제 의존(Windows agent 대응 어려움), 디버깅 시 어느 라인이 죽었는지 모호.

### 결정 4 — Dockerfile 강제 검사 제거 (임의 빌드 허용)

**선택**: 현재 `Handle()` 의 `os.Stat(filepath.Join(worktree, "Dockerfile"))` 를 제거. 대신 빌드 명령 실행 결과로 평가. 단, **기본 빌드 명령** (`docker build ...`) 사용 시는 사용자가 Dockerfile 을 가지고 있다는 것이 전제이므로 명시 가이드만 reference 표에 적는다.
**근거**: `make build && docker build -f deploy/Dockerfile .` 같은 패턴을 막지 않기 위함.
**대안 기각**: Dockerfile 검사를 옵트인 플래그 — 추가 필드만 늘림.

### 결정 5 — `$PREVIEW_IMAGE` 의 값과 `docker run` 의 image

**선택**: `$PREVIEW_IMAGE = "preview-<previewID>:latest"`. Agent 는 빌드 명령 실행 후 항상 이 태그로 `ContainerCreate` 한다. 사용자는 빌드 명령에서 반드시 이 태그로 빌드해야 한다(기본 명령은 이미 그렇다).
**근거**: Hub/Agent 가 이미지 태그를 추적할 단일 규약. 컨테이너에 붙는 라벨(`hub-preview-id`)은 그대로 유지.
**대안 기각**: 사용자가 임의의 태그 → Runner 가 그것을 어떻게 알지에 대한 추가 매개변수 필요.

### 결정 6 — `docker run -p` 도 셸로 실행할 것인가? — **아니오**, Docker SDK 유지

**선택**: 빌드만 셸 명령. **컨테이너 생성/시작/중지/제거**는 그대로 Docker SDK(`r.docker.ContainerCreate/Start/...`)로 한다. `ExposedPort` 만 config 의 `container_port` 로 치환.
**근거**: 컨테이너 라이프사이클 추적(라벨, jobs 맵, orphan_restore) 이 SDK 호출에 강하게 결합되어 있다. 모두 셸로 옮기면 본 Phase 범위 폭증.
**대안 기각**: 전부 셸 → 라벨 부착·검색·잔존 컨테이너 복원 로직을 모두 재작성해야 함.

### 결정 7 — Hub 단방향 푸시: ACK 없음

**선택**: `AGENT_CONFIG`/`CONFIG_UPDATE` 둘 다 단방향. Agent 는 ACK 메시지를 보내지 않는다. 적용 실패는 다음 build 의 STATUS_UPDATE(failed)로만 관측된다.
**근거**: 메시지가 작고 idempotent. WS 가 살아있다는 것은 직전 PING/PONG 으로 보장되며, 끊긴 동안의 변경은 다음 HELLO 의 AGENT_CONFIG 가 흡수한다.
**대안 기각**: ACK 추가 — 본 Phase 에서 가시적 가치가 없음.

### 결정 8 — `AGENT_CONFIG` 와 `CONFIG_UPDATE` 페이로드 구조는 동일

**선택**: 두 메시지의 `data` JSON 스키마는 완전히 같다. 이름만 다르다 — 의도가 다르기 때문(초기 동기화 vs 변경 푸시).
**근거**: Agent 측 적용 로직이 단일 경로(`store/replace`)로 끝난다. 메시지 분기는 로깅·관측을 쉽게 할 뿐.
**대안 기각**: 한 타입(`CONFIG_SET`)만 사용 → 로그 파싱 시 두 사건의 흐름을 못 가른다.

### 결정 9 — Agent 측 in-memory config 의 동기화는 `sync.RWMutex` + 값 복사

**선택**: `agent/config.go` 에 `Holder` 구조체. 내부에 `sync.RWMutex`, `BuildCommands []string`, `ContainerPort int`. `Replace(cfg)` 와 `Snapshot() Config` 두 메서드만 노출. Snapshot 은 슬라이스 deep copy 후 반환(외부에서 수정해도 안전).
**근거**: 빈도가 매우 낮은 쓰기(한 번/연결, 사용자 저장 시), 빈번한 읽기(매 Handle). 컨테이너 build 도중 변경되더라도 `Snapshot()` 로 받은 본은 일관된 상태.
**대안 기각**: `atomic.Pointer[Config]` — 더 가볍지만 Phase 0~3 의 코드는 표준 라이브러리 + RWMutex 패턴으로 일관됨.

### 결정 10 — UI 렌더에 표시되는 "기본값" vs "저장된 값"

**선택**: build_commands 가 NULL 이면 textarea **placeholder** 로 기본 명령을 표시(저장은 빈 문자열). container_port 가 NULL 이면 input value 비우고 placeholder=80.
**근거**: 사용자가 명시적으로 "기본값 사용" 을 의도하는 것을 식별 가능하게(서버에 저장되어 있는 값과 placeholder 를 구분). 운영자가 "기본값으로 되돌리기" 버튼 없이도 textarea 비우고 저장하면 reset 됨.
**대안 기각**: 마이그레이션에서 default 값 채우기 — 결정 2 와 동일하게 의도 구분 불가.

### 결정 11 — 진행 중 빌드는 **변경하지 않는다** (atomic for next job)

**선택**: `CONFIG_UPDATE` 는 즉시 in-memory Holder 만 갱신. 현재 진행 중인 `Handle()` 콜은 진입 시점에 받은 `Snapshot()` 을 끝까지 사용. 다음 JOB_ASSIGN 부터 새 설정이 보인다.
**근거**: 빌드 도중 명령 시퀀스가 바뀌면 결과 추적이 어렵다. 사용자 멘탈 모델("저장 → 다음 빌드부터 적용")과 일치.
**대안 기각**: 진행 중 빌드 abort 후 재시작 — 리스크 큼, 본 Phase 범위 외.

### 결정 12 — `container_port` 검증

**선택**: UI 단에서 `1..65535` 범위, `Content-Type: application/x-www-form-urlencoded` 의 정수 파싱. 범위 밖 / 빈 값 / 0 / 비숫자 → 0 으로 정규화 후 저장(= "기본값"). 422 가 아니라 받아들이고 sentinel 처리.
**근거**: 결정 2 와 결정 10 의 일관 적용.
**대안 기각**: 422 반환 — 운영자에게 의미 모호.

### 결정 13 — `build_commands` 의 라인 normalization

**선택**: textarea 내용을 `\r?\n` 로 split → 각 라인 trim → 빈 라인 제거 → 슬라이스. 저장 형태(DB)는 raw textarea 텍스트 그대로(개행 포함). DB 의 TEXT 컬럼을 wire 페이로드(`[]string`)로 변환할 때만 split. 와이어에서 들어올 때도 동일 normalization.
**근거**: DB 에 array literal 을 안 넣어도 됨(이식성). split/trim 위치는 wire 변환 시점 한 곳.
**대안 기각**: DB 에 JSON array → SQLite/Postgres 둘 다 지원이지만 결정 2 (NULL = 기본값) 가 더 자연스럽다(빈 텍스트 != JSON `[]`).

---

## 4. 명세 상세

### 4-1. DB 스키마 변경

마이그레이션 파일:

- `db/migrations/0003_agent_build_config.up.sql`

```sql
ALTER TABLE agents ADD COLUMN build_commands TEXT;
ALTER TABLE agents ADD COLUMN container_port INTEGER;
```

- `db/migrations/0003_agent_build_config.down.sql`

```sql
ALTER TABLE agents DROP COLUMN container_port;
ALTER TABLE agents DROP COLUMN build_commands;
```

> SQLite 3.35+ 와 Postgres 모두 `ALTER TABLE ... DROP COLUMN` 지원. NULL 허용으로 추가하므로 기존 row 영향 없음.

`db/queries/agents.sql` 갱신: `GetAgentBuildConfig`, `SaveAgentBuildConfig` 쿼리 추가.

```sql
-- name: GetAgentBuildConfig :one
SELECT id, build_commands, container_port FROM agents WHERE id = ? LIMIT 1;

-- name: SaveAgentBuildConfig :exec
UPDATE agents SET build_commands = ?, container_port = ? WHERE id = ?;
```

`internal/store/store.go` 의 `Agent` 구조체 확장:

```go
type Agent struct {
    // ... 기존 필드 ...
    BuildCommandsRaw *string // nil = 기본값. 빈 문자열도 기본값으로 간주.
    ContainerPort    *int    // nil = 기본값(80).
}
```

`AgentStore` 인터페이스에 추가:

```go
GetBuildConfig(ctx context.Context, agentID string) (commands []string, port int, err error)
// SaveBuildConfig 는 raw textarea 텍스트를 그대로 받아 DB 에 저장한다.
// rawCommands 가 빈 문자열이면 build_commands = NULL.
// port == 0 이면 container_port = NULL (sql.NullInt64{Valid: false}).
// 즉 port 는 DB 에 절대 정수 0 으로 저장되지 않는다.
SaveBuildConfig(ctx context.Context, agentID string, rawCommands string, port int) error
```

**저장 계약 (Decision 13 통일)**:
- `build_commands` 컬럼 타입 TEXT: 사용자가 textarea 에 입력한 내용을 **줄바꿈(\n) 그대로** 저장한다. 읽을 때 `strings.Split(raw, "\n")` + 빈 줄 제거로 `[]string` 변환.
- `container_port` 컬럼 타입 INTEGER NULL: `port == 0` → `sql.NullInt64{Valid: false}` 로 NULL 저장. 읽을 때 NULL → `0` 반환.
- `GetBuildConfig` 반환 `commands []string`: 빈 슬라이스는 "기본값 적용" 의미(NULL DB 값과 동치).
- `port int` 반환: 0 은 "기본값 80 적용" 의미(NULL DB 값과 동치).

> 주의: 기존 `Create/Update*` 흐름은 build_commands/container_port 를 건드리지 않는다 — Agent 등록 시점에 설정이 비어 있는 것이 normal.

### 4-2. Protocol 메시지

`internal/protocol/messages.go` 에 추가:

```go
const (
    TypeAgentConfig  = "AGENT_CONFIG"
    TypeConfigUpdate = "CONFIG_UPDATE"
)

// AgentConfigData 는 AGENT_CONFIG / CONFIG_UPDATE 의 본문.
// BuildCommands 가 빈 슬라이스([]) 면 기본값(`docker build -t $PREVIEW_IMAGE .`).
// ContainerPort == 0 이면 기본값(80).
type AgentConfigData struct {
    BuildCommands []string `json:"build_commands"`
    ContainerPort int      `json:"container_port"`
}
```

JSON 예시 (저장된 값이 있는 경우):

```json
{
  "type": "AGENT_CONFIG",
  "data": {
    "build_commands": [
      "npm ci",
      "npm run build",
      "docker build -t $PREVIEW_IMAGE -f Dockerfile.deploy ."
    ],
    "container_port": 3000
  }
}
```

JSON 예시 (기본값):

```json
{
  "type": "AGENT_CONFIG",
  "data": {
    "build_commands": [],
    "container_port": 0
  }
}
```

ProtoVersion 은 `v1` 그대로 유지(메시지 추가는 호환). 레거시 Agent(Phase 0~3) 가 본 메시지 타입을 모르면 `default` 분기에서 debug 로그 후 무시 — Hub 측은 송신 실패가 아니라 적용 실패가 되며 Runner 는 기본값으로 동작 → 안전.

### 4-3. HTTP 엔드포인트

| 메서드 | 경로 | 핸들러 | 응답 | 비고 |
|---|---|---|---|---|
| GET | `/admin/agents/{id}` | `AdminUIHandler.agentDetail` | 200 SSR | Agent 상세 + build config 폼 |
| POST | `/admin/agents/{id}/config` | `AdminUIHandler.agentConfigSave` | 303 → `/admin/agents/{id}?msg=saved` | form 저장 + `CONFIG_UPDATE` 푸시 |
| GET | `/admin/agents` | (기존) `AgentsList` | 200 SSR | 각 행에 detail 링크 추가 |

405 / 404 / 500 처리는 기존 핸들러 패턴(`http.Error`) 동일.

라우트 등록은 `AdminUIHandler.Register` 에서 추가:

```go
mux.HandleFunc("GET /admin/agents/{id}", h.agentDetail)
mux.HandleFunc("POST /admin/agents/{id}/config", h.agentConfigSave)
```

### 4-4. Agent 상세 페이지 레이아웃 (ASCII)

파일: `internal/hub/views/agent_detail.gohtml`

```
+-----------------------------------------------------------+
|  Preview Admin                            [agents]        |
|  hgroup                                                    |
|   <h1>Agent: agent-home</h1>                              |
|   <h2>Status: online · Last seen: 2026-04-25T12:34:56Z</h2>|
+-----------------------------------------------------------+
|  Metadata                                                  |
|   ID:        9f1e...c4                                     |
|   Labels:    env=home,owner=alice                          |
|   Created:   2026-04-20T08:00:00Z                          |
|                                                            |
|  [back to /admin/agents]                                   |
+-----------------------------------------------------------+
|  Build Configuration                                       |
|   <form method=POST action=/admin/agents/{id}/config>      |
|     <label>Build commands (one per line)</label>           |
|     <textarea name=build_commands rows=6                   |
|       placeholder="docker build -t $PREVIEW_IMAGE ."       |
|     >npm ci\nnpm run build\ndocker build -t ...</textarea> |
|                                                            |
|     <label>Container port</label>                          |
|     <input type=number min=1 max=65535                     |
|       name=container_port placeholder=80 value=3000>       |
|                                                            |
|     <button type=submit>Save</button>                      |
|   </form>                                                  |
|                                                            |
|   {{if .SavedFlash}}<article role=alert>Saved.             |
|     CONFIG_UPDATE was {{.PushOutcome}}.</article>{{end}}   |
+-----------------------------------------------------------+
|  Available environment variables                           |
|   ┌──────────────────┬───────────────────────────────────┐ |
|   │ $PREVIEW_ID      │ UUID of the preview               │ |
|   │ $PREVIEW_IMAGE   │ preview-<id>:latest (build target)│ |
|   │ $PREVIEW_SHA     │ git commit sha                    │ |
|   │ $PREVIEW_BRANCH  │ git branch (may be empty)         │ |
|   └──────────────────┴───────────────────────────────────┘ |
|   Notes:                                                   |
|    - each line = independent `sh -c` run, cwd=worktree     |
|    - non-zero exit aborts the build                        |
|    - changes apply to the **next** build (in-flight builds |
|      keep their snapshot)                                  |
+-----------------------------------------------------------+
```

플래시 메시지 처리: `?msg=saved` (성공) / `?msg=saved_offline` (DB 저장 됐고 Agent 미연결) / `?msg=saved_push_failed` (DB 저장 됐고 푸시 실패).

### 4-5. Agent 상세 페이지 viewmodel

```go
type agentDetailView struct {
    Title             string
    AgentID           string
    Name              string
    Status            string
    LabelsString      string
    LastSeenString    string
    CreatedString     string
    BuildCommandsText string // raw textarea 내용. nil 일 때 빈 문자열.
    ContainerPort     int    // 0 이면 빈 input(=기본값).
    SavedFlash        bool
    PushOutcome       string // "delivered" | "agent offline" | "delivery failed"
    Error             string
}
```

### 4-6. Hub 동작

#### HELLO → WELCOME → AGENT_CONFIG

`internal/hub/ws_handler.go`:

```
1. readHello() (기존)
2. registry.add() (기존)
3. UpdateStatus(online) (기존)
4. writeEnvelope(WELCOME) (기존)
5. (NEW) AgentStore.GetBuildConfig(ctx, agent.ID) → cmds, port
6. (NEW) writeEnvelope(AGENT_CONFIG{cmds, port})
   - 실패 시: WELCOME 실패(→ session 종료)와 달리 **warn 로그 + session 계속** 정책.
   - 근거: config 수신 실패는 치명적이지 않음 — Agent 가 기본값으로 동작하기 때문. 반면 WELCOME 실패는 핸드셰이크 자체가 실패이므로 session 을 끊어야 한다. 두 단계의 성격이 다르므로 비대칭 처리가 의도적이다.
   - 구현: `if err := writeEnvelopeTimeout(conn, env); err != nil { h.Logger.Warn("agent_config_send_failed", ...); /* continue */ }`
7. go syncOnHello(...) (기존)
8. readLoop / pingTicker (기존)
```

신규 함수: `(h *WSHandler) sendAgentConfig(ctx, conn, agentID)`. 비활성화 토글: 새 의존(AgentStore.GetBuildConfig) nil 가능성을 다루지 않는다 (기존에도 store 는 필수).

#### POST /admin/agents/{id}/config

```
1. 폼 파싱: build_commands raw text + container_port int.
2. Normalize: split lines → trim → drop empty → []string. port: 1~65535 외는 0 (sentinel).
3. AgentStore.SaveBuildConfig(ctx, id, cmds, port).
4. ConnRegistry 에 해당 agent 의 wsConn 이 있으면:
     WSJobSender.SendAgentConfig(ctx, agentID, AgentConfigData{cmds, port})
   - 결과를 PushOutcome 으로 분류: delivered / not connected / delivery failed.
5. 303 redirect 로 detail 페이지에 ?msg=... 첨부.
```

`POST /admin/agents/{id}/config` 는 Phase 3 의 `POST /admin/agents/{id}/delete` 와 같은 SSR-only 엔드포인트로 한다. JSON Accept 분기는 본 Phase 미지원(deferred).

#### WSJobSender 확장

`internal/hub/ws_registry.go` 에 추가:

```go
func (s *WSJobSender) SendAgentConfig(ctx context.Context, agentID string, cfg protocol.AgentConfigData) error {
    c := s.Registry.connFor(agentID)  // 기존 SendTeardown 과 동일 패턴
    if c == nil {
        return fmt.Errorf("ws_job_sender: agent %s not connected", agentID)
    }
    env, err := protocol.NewEnvelope(protocol.TypeConfigUpdate, cfg)
    if err != nil { return err }
    b, _ := json.Marshal(env)
    return c.conn.Write(ctx, websocket.MessageText, b)
}
```

> `Registry.send` 같은 헬퍼는 존재하지 않는다. `connFor` → `c.conn.Write` 를 인라인하는 기존 `SendTeardown` 패턴을 그대로 따른다.

> HELLO 직후의 `AGENT_CONFIG` 송신은 WSHandler 가 conn 핸들에 직접 쓴다(레지스트리 등록 시점 race 회피). 즉 `WSJobSender` 는 **CONFIG_UPDATE** 푸시 전용으로 사용된다. 메시지 타입만 다르고 페이로드는 같다.

### 4-7. Agent 동작

#### in-memory Holder

파일: `internal/agent/config.go` (신규)

```go
type Config struct {
    BuildCommands []string  // 길이 0 = 기본값
    ContainerPort int       // 0 = 기본값(80)
}

type Holder struct {
    mu  sync.RWMutex
    cur Config
}

func NewHolder() *Holder { return &Holder{} }

func (h *Holder) Replace(c Config) { /* deep copy of slice into h.cur */ }
func (h *Holder) Snapshot() Config { /* RLock, deep copy */ }
```

#### 메시지 라우팅

`internal/agent/client.go` (또는 동등 파일) 의 read loop 에 케이스 추가:

```
case protocol.TypeAgentConfig, protocol.TypeConfigUpdate:
    var d protocol.AgentConfigData
    if err := env.Decode(&d); err != nil { warn; continue }
    holder.Replace(Config{BuildCommands: d.BuildCommands, ContainerPort: d.ContainerPort})
    logger.Info("agent_config_applied", "type", env.Type, "commands_n", len(d.BuildCommands), "port", d.ContainerPort)
```

#### Runner 수정

`internal/agent/runner.go` 변경:

- `Runner` 에 `cfg *Holder` 의존 추가, `NewRunner` 시그니처 확장.
- `Handle()` 진입 시점에 `snap := r.cfg.Snapshot()` → 진행 내내 같은 snap 사용.
- `os.Stat(Dockerfile)` 검사 제거(결정 4).
- 빌드 단계 교체:
  - `cmds := snap.BuildCommands; if len(cmds) == 0 { cmds = []string{"docker build -t $PREVIEW_IMAGE ."} }`
  - 환경 구성: `env := append(os.Environ(), "PREVIEW_ID="+pid, "PREVIEW_IMAGE="+tag, "PREVIEW_SHA="+msg.CommitSHA, "PREVIEW_BRANCH="+msg.Branch, "PORT="+strconv.Itoa(resolvedPort))`
  - `PORT` 환경변수를 추가로 주입한다(Q6 해결). Node.js/Go 등 대부분의 웹 프레임워크가 `process.env.PORT` 또는 `os.Getenv("PORT")` 를 읽어 리슨 포트를 결정한다. `resolvedPort = port if port != 0 else 80`.
  - **보안 주의**: `os.Environ()` 에는 Agent 프로세스가 받은 모든 환경변수(예: `HUB_TOKEN`, `DATABASE_URL` 등)가 포함된다. Phase 0~3 의 Docker SDK `ImageBuild` 는 호스트 env 를 컨테이너 빌드에 노출하지 않았지만, 이제 셸 실행은 프로세스 env 를 그대로 상속한다. 신뢰 모델: Agent 는 운영자가 소유·신뢰하는 머신에서 실행되므로 허용. 미래에 sandbox 가 필요하면 `os.Environ()` 대신 allowlist 방식으로 교체한다(Phase 5 후보). NF-Security-Env-1 로 이 사실을 명시 검증한다.
  - 각 라인을 `exec.CommandContext(ctx, "sh", "-c", line)` 로 실행, `Dir = worktree`, `Env = env`. stdout/stderr → discard 또는 logger.
  - 한 라인이라도 exit code != 0 → `r.fail(ctx, pid, "build_command", err)`.
- `ExposedPort: 80` → `ExposedPort: portOrDefault(snap.ContainerPort)` (`port == 0 ? 80 : port`).
- 나머지(allocatePort, ContainerCreate/Start, jobs map, STATUS_UPDATE running) 변경 없음.

#### Agent 진입점 (`cmd/agent/main.go`)

`Holder` 생성 → `Client` 와 `Runner` 둘 다에 주입 (Client 는 메시지 도착 시 Replace 호출, Runner 는 Snapshot 호출). 초기 상태는 `Config{}` (빈 슬라이스, 0 포트) — 즉 기본값 동작.

### 4-8. 환경변수 계약

| 이름 | 값 | 비고 |
|---|---|---|
| `$PREVIEW_ID` | `msg.PreviewID` | UUID 문자열 |
| `$PREVIEW_IMAGE` | `"preview-" + msg.PreviewID + ":latest"` | 결정 5. Runner 가 `ContainerCreate` 에 사용하는 image 와 동일. 사용자는 빌드 명령에서 이 태그로 build 해야 함. |
| `$PREVIEW_SHA` | `msg.CommitSHA` | git commit sha |
| `$PREVIEW_BRANCH` | `msg.Branch` (빈 문자열 가능) | |

`os.Environ()` 위에 위 4 개를 append 한다. 즉 hub 가 export 한 임의의 env 도 빌드 명령에서 보인다(현재 Phase 0~3 의 동작과 일관).

---

## 5. 기능 체크리스트 (F-*)

### 5-1. DB / Store

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-1 | 마이그레이션 0003 up 으로 `build_commands` (TEXT NULL) 와 `container_port` (INTEGER NULL) 컬럼이 추가된다 | `PRAGMA table_info(agents)` 출력 확인 |
| F-2 | 마이그레이션 0003 down 으로 두 컬럼이 제거된다 | up → down → `PRAGMA table_info` 비교 |
| F-3 | 신규 Agent 등록 시 두 필드는 NULL 로 INSERT 된다 | `Create()` 후 SELECT 결과 NULL 확인 |
| F-4 | `AgentStore.SaveBuildConfig(id, rawCommands, port)` 가 row 의 두 컬럼을 갱신한다 | sqlite 에 SaveBuildConfig 호출 후 SELECT 결과 raw 텍스트 일치 확인 |
| F-5 | `AgentStore.SaveBuildConfig(id, "", 0)` 호출 시 `build_commands = NULL`, `container_port = NULL` 로 reset 된다 (정수 0 이 저장되지 않음) | sqlite SELECT 후 NULL 확인 |
| F-6 | `AgentStore.GetBuildConfig(id)` 가 NULL → `([]string{}, 0)` 반환한다 | 단위 테스트 |
| F-7 | `GetBuildConfig` 가 저장된 multi-line 텍스트를 normalize 한 `[]string` 으로 반환한다 | 단위 테스트 (`"a\n\nb\n"` → `["a","b"]`) |

### 5-2. Hub Admin UI

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-8 | `GET /admin/agents/{id}` 가 200 + agent 메타데이터 + 폼 + env 표를 렌더한다 | Playwright `browser_snapshot` |
| F-9 | 존재하지 않는 agentID → 404 | curl/Playwright |
| F-10 | 폼 placeholder 가 기본 명령(`docker build -t $PREVIEW_IMAGE .`) 과 80 을 표시한다 | DOM 어트리뷰트 검증 |
| F-11 | 저장된 값이 있을 때 textarea / input value 에 표시된다 | sqlite 에 직접 INSERT 후 페이지 reload |
| F-12 | `POST /admin/agents/{id}/config` 폼 제출 시 303 + `?msg=saved` 로 리다이렉트한다 | HTTP 응답 검사 |
| F-13 | 폼 제출 후 DB 의 두 컬럼이 입력 값으로 갱신된다 | SELECT 검증 |
| F-14 | 빈 textarea 제출 시 `build_commands` 가 NULL (또는 빈 문자열) 로 저장된다 | 동일 |
| F-15 | container_port 에 `1..65535` 외 값(0/음수/65536/문자) → 0 으로 정규화 | 단위 + 통합 |
| F-16 | `/admin/agents` 목록의 각 행 Name 이 detail 페이지로 이어지는 링크 | Playwright click |

### 5-3. Protocol / WS

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-17 | `protocol.TypeAgentConfig`, `protocol.TypeConfigUpdate` 상수가 정확한 문자열을 갖는다 | `go test` 단위 |
| F-18 | `AgentConfigData` JSON 직렬화/역직렬화 round-trip 동작 | 단위 테스트 |
| F-19 | HELLO → WELCOME 직후 Hub 가 `AGENT_CONFIG` 1 회 송신 (저장 값 없으면 `{[],0}`) | fake conn 으로 메시지 시퀀스 캡처 |
| F-20 | 저장된 값이 있는 Agent 에 대해 `AGENT_CONFIG` payload 가 정확하다 | 동일 |
| F-21 | `POST /admin/agents/{id}/config` 후 ConnRegistry 에 등록된 conn 으로 `CONFIG_UPDATE` 1 회 송신 | fake registry / WS 통합 |
| F-22 | Agent 가 미연결 상태에서 폼 저장 → DB 만 갱신, 푸시는 시도 안 함, `?msg=saved_offline` | 통합 |
| F-23 | Send 실패 시 `?msg=saved_push_failed`, DB 는 갱신된다 | 통합 |

### 5-4. Agent runtime

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-24 | Agent 시작 직후 Holder 는 빈 Config (기본값으로 동작) | 단위 |
| F-25 | `AGENT_CONFIG` 수신 후 Holder.Snapshot 이 그 값을 반환한다 | 단위 (fake client) |
| F-26 | `CONFIG_UPDATE` 수신 후 Holder 가 즉시 새 값으로 교체된다 | 단위 |
| F-27 | Holder.Replace 는 슬라이스 deep copy 한다 (외부 슬라이스 mutate 영향 X) | 단위 |
| F-28 | Runner.Handle 진입 시 Snapshot 을 1회 받고 그 값으로 끝까지 진행한다 (도중 Replace 무영향) | 단위 (race 가능 시나리오) |
| F-29 | BuildCommands 가 비어 있으면 `docker build -t $PREVIEW_IMAGE .` 1 줄 실행 | fake exec runner / 통합 |
| F-30 | BuildCommands 의 각 라인이 순서대로 별 프로세스에서 실행, cwd=worktree | 동일 |
| F-31 | 4 개 환경변수 (`$PREVIEW_ID`, `$PREVIEW_IMAGE`, `$PREVIEW_SHA`, `$PREVIEW_BRANCH`) 가 셸 명령에 가시 | 명령에서 `env > out.txt` 후 검증하는 통합 테스트 |
| F-32 | 명령 1 라인이라도 exit != 0 → STATUS_UPDATE failed 송신, 후속 라인 미실행 | 단위 |
| F-33 | ContainerPort != 0 일 때 `ContainerCreate.ExposedPort = ContainerPort` | fake DockerClient |
| F-34 | ContainerPort == 0 일 때 `ExposedPort = 80` (기본값) | 동일 |
| F-35 | 기존 Phase 2/3 의 동적 host port 할당 / 라벨 / jobs map 등록 / STATUS_UPDATE running 흐름은 변하지 않는다 | 회귀 테스트 |

### 5-5. End-to-end

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-36 | Playwright: agent 등록 → /admin/agents/{id} 진입 → 폼 입력 → 저장 → 페이지 reload 시 입력 값 보임 | e2e |
| F-37 | Playwright: 위 + 연결된 fake Agent 가 `CONFIG_UPDATE` 수신을 캡처한다 | e2e (Agent stub) |
| F-38 | Playwright: 빈 폼 저장 → reload 시 placeholder 만 보이고 value 없음 | e2e |

---

## 6. 비기능 체크리스트 (NF-*)

| ID | 항목 | 검증 방법 |
|---|---|---|
| NF-1 | 외부 의존성(go.mod) 추가 0 개 | `go mod tidy` diff 검사 |
| NF-2 | 새 파일은 모두 책임 주석(3~5 줄) 헤더 보유 | grep `// 이 파일의 책임:` |
| NF-3 | 어떤 새 파일도 300 줄을 넘지 않는다 | `wc -l` |
| NF-4 | `agent_detail.gohtml` 은 layout 을 상속하고 다른 SSR 페이지와 동일한 패턴(Pico CDN, hgroup) 으로 작성 | 시각/구조 리뷰 |
| NF-5 | sqlc 생성 코드는 `internal/db/sqlite` 하위에만 있고 비즈니스 로직은 `store.AgentStore` 인터페이스만 import | depguard / grep |
| NF-6 | SQLite 전용 문법 사용 0 (DROP COLUMN 은 SQLite 3.35+ + Postgres 호환) | 마이그레이션 리뷰 |
| NF-7 | 새 protocol 메시지가 `internal/protocol` 외에서 string literal 로 비교되지 않는다 (상수 사용) | grep `"AGENT_CONFIG"` 외 사용처 검사 |
| NF-8 | Holder 의 Replace/Snapshot 동시 호출에 대한 race 검출기 통과 | `go test -race` |
| NF-9 | `go test ./...` green | CI |
| NF-10 | `go vet ./...` 와 `golangci-lint run` clean | CI |
| NF-11 | `slog` 키 명명 컨벤션 일관 (`agent_config_*`, `admin_ui_agent_config_*`) | grep |
| NF-12 | layered dependency 단방향 유지 (`internal/agent` 가 `internal/hub` 를 import 하지 않음) | depguard |
| NF-13 | Phase 3 e2e 시나리오(Agent 등록·삭제·preview rebuild) 는 모두 통과 (회귀 없음) | 기존 e2e 재실행 |

---

## 7. 단계 분할 (구현·평가용)

evaluator 가 두 번에 나눠 검증할 수 있도록 본 Phase 를 다음 두 Step 으로 나눈다. 두 Step 모두 끝나야 Phase 4 종료.

### Step S1 — DB + Admin UI + Hub 푸시 인프라

**범위**:
- 마이그레이션 0003 (up/down)
- sqlc 쿼리 + sqlite 구현체
- `store.Agent` 필드 + `AgentStore.GetBuildConfig/SaveBuildConfig`
- `internal/protocol` 상수·DTO 추가
- `WSJobSender.SendAgentConfig`
- `WSHandler` 의 HELLO 직후 `AGENT_CONFIG` 송신
- Admin UI: `agent_detail.gohtml`, `agentDetail` GET, `agentConfigSave` POST, agents 목록의 detail 링크
- 기본값 sentinel (NULL/empty/0) 처리
- 단위 테스트 + WS 통합 테스트(메시지 시퀀스 캡처)
- Playwright e2e: F-8~F-16, F-21, F-22, F-23, F-36, F-38

**완료 기준**:
- F-1 ~ F-23, F-36, F-38 모두 통과
- **F-21/F-22/F-23 검증 방법**: 실제 Agent 바이너리 없이 `ConnRegistry` 에 `fakeConn`(테스트용 pipe 또는 `net.Pipe()` 기반 `*websocket.Conn`)을 직접 등록해 CONFIG_UPDATE 수신 여부를 검증한다. 실제 Agent 에 배포·연결 검증은 S2 의 통합 테스트로 이월.
- 기존 Agent 가 본 Step 만 적용된 Hub 와 호환(`AGENT_CONFIG` 를 무시해도 동작) — 구 Agent 바이너리로 회귀 테스트

### Step S2 — Agent runtime (셸 빌드 + 포트) + 통합

**범위**:
- `internal/agent/config.go` (Holder)
- `internal/agent/client.go` 의 message routing 케이스 추가
- `internal/agent/runner.go` 의 빌드 로직 셸화 + 포트 적용 + Dockerfile 검사 제거
- `cmd/agent/main.go` 의 Holder 주입
- 단위 테스트(F-24~F-34)
- 회귀 테스트(F-35)
- Playwright e2e: F-37 (Hub UI 저장 → Agent 셸 명령 실제 실행 캡처)
- 기본값 동작 회귀: 설정 미저장 Agent 가 phase 3 fixture preview 빌드 성공

**완료 기준**:
- F-24 ~ F-37 통과
- NF-1 ~ NF-13 통과
- 사용자 시연: 저장된 build_commands 로 Node.js 샘플 앱이 3000 포트에서 떠 reverse proxy 로 접근 가능

---

## 8. 리스크와 완화

| 리스크 | 완화 |
|---|---|
| 빌드 도중 `CONFIG_UPDATE` 가 와도 race 없음을 보장해야 한다 | 결정 11 의 Snapshot-then-use. F-28 로 검증. |
| 셸 명령이 매우 길어 stdout/stderr 가 메모리/디스크 폭주 | discard 로 흘려보내는 기존 패턴 유지(필요 시 logger.Debug 로 라인 단위 출력 옵션) — 본 Phase 외. |
| 빈 build_commands sentinel 로 의도한 "기본값" 인지 사용자 실수인지 모호 | UI placeholder + reference 표 + 저장 후 detail 페이지에 "current effective: defaults" 안내 (NF-4 시각). |
| 기존 Phase 0~3 단위/통합 테스트가 docker SDK 호출 검증을 가정 | 결정 6 으로 `ContainerCreate/Start/Stop/Remove` 는 그대로 유지 → 회귀 0. F-35 로 명시 검증. |
| `ALTER TABLE DROP COLUMN` 이 SQLite 3.35 미만에서 실패 | go.mod 의 `modernc.org/sqlite` 는 3.35+ 등가 — 명시 verify(NF-6) |
| Windows agent 의 `sh -c` 부재 | 본 Phase 의 운영 환경은 Linux/macOS. Windows 지원은 deferred — README 명시 |

---

## 9. 변경 파일 목록 (참고; 구현자가 자유 결정)

```
db/migrations/
  0003_agent_build_config.up.sql     [신규]
  0003_agent_build_config.down.sql   [신규]
db/queries/agents.sql                 [수정] 쿼리 2개 추가
internal/db/sqlite/                   [수정] sqlc 재생성 + Repository 구현 추가
internal/store/store.go               [수정] Agent 필드 + AgentStore 메서드
internal/protocol/messages.go         [수정] 상수 + AgentConfigData
internal/hub/ws_handler.go            [수정] sendAgentConfig 호출
internal/hub/ws_registry.go           [수정] WSJobSender.SendAgentConfig
internal/hub/admin_ui.go              [수정] 라우트 + 핸들러 2개
internal/hub/views/agent_detail.gohtml [신규]
internal/hub/views/agents.gohtml      [수정] detail 링크
internal/agent/config.go              [신규] Holder
internal/agent/client.go              [수정] 메시지 라우팅
internal/agent/runner.go              [수정] 빌드 셸화 + 포트
cmd/agent/main.go                     [수정] Holder 주입
docs/specs/phase-4-agent-build-config.md [본 문서]
```

---

## 10. 미해결/확인 사항 (Open Questions)

1. **빌드 명령 stdout/stderr 의 가시성**: 실패 시 운영자가 어디서 로그를 봐야 하는가? 현재 가정: agent stdout(`slog`)에 라인 단위로 흘림. STATUS_UPDATE 의 `error_message` 에는 마지막 stderr 200 자 정도를 첨부. — 확인 필요.
2. **`$PREVIEW_IMAGE` 가 본 Phase 의 강제 규약인지**: 사용자가 `docker build` 가 아닌 `nerdctl`/`podman` 으로 빌드하면 태그를 어떻게 정렬할지. 본 Phase 는 "사용자가 반드시 이 태그로 빌드한다" 를 규약으로 두고, 결정 5 로 reference 표에 명시 — 추후 변경 가능.
3. **Agent 가 여러 개 연결되어 있는 멀티-Agent 환경에서 `CONFIG_UPDATE`** 는 단일 agent 만 대상으로 한다(설정이 agent 단위라서). 향후 "label group 단위 설정" 이 들어오면 push 대상이 N 개 → 본 Phase 는 1:1 만 고려.
4. **기존 Agent 바이너리(Phase 3) 호환성**: Hub 가 `AGENT_CONFIG` 를 송신하면 구 Agent 는 `default` 분기에서 무시 → Runner 는 in-memory Holder 가 비어 있어 기본값으로 동작 → 회귀 없음. 단, 구 Agent 는 Dockerfile 검사 로직이 살아 있으므로 사용자가 설정한 `npm ci` 같은 명령은 적용되지 않음(자연스러움). — 합의 필요.
5. **Admin UI 에서 "Reset to defaults" 명시 버튼**: 본 Phase 는 결정 10 으로 "textarea 비우고 저장 = reset" 으로 갈음. 별도 버튼 추가 여부 확인.
6. **Container 가 컨테이너 안에서 쓸 PORT env 자동 주입**: 사용자 앱이 `PORT=$container_port` 로 listen 하려면 `docker run -e PORT=...` 가 필요 — 본 Phase 는 미포함. 결정 6 으로 SDK 가 `Env` 를 받지 않으므로 추가 작업 필요. 확인 후 deferred 로 남길지 결정.
7. **POST 가 form-encoded 만 받는지, JSON 도 받는지**: 본 Phase 는 SSR-only(form). API 사용을 미리 열어두려면 `application/json` 분기 추가 — 합의 필요.

---

(끝)
