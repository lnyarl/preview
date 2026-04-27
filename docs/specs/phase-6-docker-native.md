# Phase 6 — Docker-native Multi-repo (`.preview.yml` + Traefik + Multi-repo RepoCache)

Status: **REVIEW_REVISED** (planner self-review 1차 9건 + 사용자 검토 2건 반영)
Author: planner
Date: 2026-04-25

---

## 1. Phase 개요

### 1-1. 배경

Phase 0~5 까지 누적된 모델은 다음과 같다.

- **Hub**: webhook → `previews` UPSERT → `JOB_ASSIGN` 디스패치. `JOB_ASSIGN.RepoURL` 은 Hub config 의 `PREVIEW_REPO_URL` 단일 값 (또는 fallback 으로 `repo_full_name` echo).
- **Hub Settings UI**: agent 별 `run_commands` (셸 명령 라인 시퀀스) + `container_port` (단일 포트) 를 폼으로 관리. `AGENT_CONFIG`/`CONFIG_UPDATE` 로 Agent 에 푸시.
- **Agent**: `--repo-url` 플래그 1 개로 1 개 레포 bare clone + per-preview worktree 운영. `Runner.Handle()` 은 Holder 스냅샷의 `run_commands` 를 `sh -c` 로 직렬 실행한 뒤, `docker run -p HOST:ContainerPort` 로 컨테이너 1 개를 띄우고 host port 를 advertise 한다.
- **Hub ProxyMiddleware**: `pr-{n}.<base-domain>` 호스트 헤더를 보고 그 preview 의 `(agent_host, agent_port)` 로 단일 포트 reverse proxy.

이 모델은 **단일 레포 + 단일 컨테이너 + Hub-managed run_commands** 를 강제한다. 운영자가 "frontend + admin 두 서비스를 한 PR 에서 띄우고 싶다", "Agent 한 대로 여러 레포 PR 을 받고 싶다", "compose.yml 그대로 쓰고 싶다" 같은 요구를 표현할 통로가 없다. 또한 컨테이너 외부 노출은 Hub→Agent reverse proxy 1 개에 모두 묶여 있어 multi-service 라우팅 자체가 표현 불가다.

### 1-2. 목표

다음 6 축을 동시에 도입한다.

1. **`.preview.yml` 설정 파일** — 빌드/실행 정의를 **레포 자신**이 가진다. Hub Settings 의 `run_commands`/`container_port` 를 제거하고, Agent 가 worktree 안의 `.preview.yml` 을 읽어 라우팅 라벨을 만든다.
2. **Traefik 사이드카 + `preview-net`** — Agent 시작 시 `preview-net` Docker network 와 Traefik 컨테이너를 1 개 띄운다. preview 컨테이너는 `preview-net` 에 붙고, Traefik 의 Docker provider 가 컨테이너 라벨을 읽어 path-prefix 라우팅을 자동 구성한다. preview 호스트 노출은 Traefik 단일 호스트 포트 1 개로 통일된다.
3. **Docker 자동 감지 (compose / Dockerfile)** — worktree 에 `docker-compose.yml`(또는 `compose.yml`)이 있으면 compose 모드, 없고 `Dockerfile` 만 있으면 Dockerfile 모드, 둘 다 없으면 즉시 fail. compose 모드는 Agent 가 동적 override 파일(`.preview-override-{previewID}.yml`)을 만들어 라벨/네트워크를 주입한다.
4. **Multi-repo Agent** — `--repo-url` 플래그 제거. RepoCache 를 `repoURL → *singleRepoCache` map 으로 바꾸고, 각 레포는 `{workDir}/repos/{slug}/` 에 독립적으로 캐시한다. Hub webhook 은 `repository.clone_url` 을 파싱해 `JobAssignData.RepoURL` 에 직접 실어 보낸다.
5. **Hub ProxyMiddleware 제거** — Phase 6 진입점은 Agent(Traefik) 직접 노출. `proxy_middleware.go` 와 관련 라우팅 코드를 제거한다(§3 결정 12).
6. **`run_commands` / `container_port` 완전 제거** — DB 컬럼 DROP, `AgentConfigData` 와이어 메시지 삭제, `Holder` 삭제(§3 결정 13).

### 1-3. 비목표 (이 Phase 가 해결하지 않는 것)

- **HTTPS 종단** — Traefik 은 HTTP 80(또는 config 으로 지정한 포트)만 바인딩한다. TLS termination, ACME 는 후속 Phase.
- **세분 라우팅 규칙** — `.preview.yml` 의 services 는 `port` + `path` + `strip` 의 3 개 축만 지원. host-rule, header-rule, middleware chain 등은 후속 Phase.
- **컨테이너 자원 제한** — `mem_limit`, `cpus` 등 docker-run 옵션은 노출하지 않는다. compose 사용자는 자기 compose 파일에서 직접 표현.
- **Per-PR override** — `.preview.yml` 는 PR 의 head SHA 의 worktree 에서 읽는다. Hub UI 에서 `.preview.yml` 를 덮어쓰는 매커니즘은 없다.
- **자동 cleanup of orphan compose projects** — Phase 3 의 HELLO sync 로 처리되는 컨테이너 자체 cleanup 외에, compose project 레벨의 orphan(=DB 에 없는 `preview-*` project) 일괄 정리는 후속 Phase.
- **Traefik 외 reverse proxy 선택지** — Caddy/nginx 등은 후속 Phase. 본 Phase 는 Traefik 사이드카 1 개로 한정.

### 1-4. 성공 기준 (요약)

- Agent 가 `--repo-url` 플래그 없이 부팅, `preview-net` 과 Traefik 컨테이너를 자동 기동한다.
- 서로 다른 두 레포(repoA, repoB)에서 PR 이 열리면, 같은 Agent 가 두 레포의 worktree 를 `repos/{slugA}/`, `repos/{slugB}/` 아래에 별도 캐시하고 두 PR 을 동시 처리한다.
- repoA 의 worktree 에 `.preview.yml` (frontend:3000:/front, admin:8080:/admin) + `docker-compose.yml` 이 있으면, Agent 가 override 파일을 생성해 `docker compose up -d` 후 `http://{agentHost}:{traefikPort}/{previewID}/front` 가 frontend 컨테이너의 3000 포트로, `/admin` 이 admin 컨테이너의 8080 포트로 path-prefix 라우팅된다.
- repoB 가 `.preview.yml` (app:3000:/) + `Dockerfile` 만 가진 경우, Agent 가 `docker build` → `docker run --label traefik...` 로 단일 컨테이너를 띄우고 같은 라우팅 모델을 만든다.
- PR closed → Hub 의 JOB_TEARDOWN → Agent 가 compose 인 경우 `docker compose --project-name preview-{id} down` + override 파일 삭제, Dockerfile 인 경우 `docker stop`/`rm`. preview-net 과 Traefik 자체는 Agent 가 떠 있는 동안 유지.
- STATUS_UPDATE(running) 의 `preview_urls` 필드에 service 별 URL map(`{"frontend":"http://...", "admin":"http://..."}`) 이 실려 Hub 대시보드에 service 링크 목록으로 표시.
- Hub UI 의 `run_commands` / `container_port` 폼 제거. DB 컬럼 DROP, `AgentConfigData` 와이어 및 `Holder` 코드 완전 제거.
- Hub ProxyMiddleware 제거. preview 접근은 `preview_urls` 의 Traefik 직접 URL 로 일원화.

---

## 2. In / Out of Scope

### 2-1. In Scope

- **Agent 측**
  - `internal/agent/preview_config.go` (신규) — `.preview.yml` 파서. `Service{Port int, Path string, Strip *bool}` 구조체.
  - `internal/agent/repocache.go` 리팩터: `RepoCache` → `MultiRepoCache` (repoURL → `*singleRepoCache`). 슬러그 규칙 그대로 (`RepoSlug`).
  - `internal/agent/runner.go`:
    - `Handle()` 에서 worktree 안에 compose / Dockerfile 자동 감지 후 분기.
    - compose 분기: override 파일 생성 + `docker compose -f docker-compose.yml -f .preview-override-{id}.yml --project-name preview-{id} up -d`.
    - Dockerfile 분기: `docker build -t preview-{id} .` + `docker run -d --network preview-net --label traefik...`.
    - Teardown 분기: 동일하게 compose / Dockerfile 별로 정리.
  - `internal/agent/traefik.go` (신규) — `EnsureNetwork`(preview-net) + `EnsureTraefik`(컨테이너 1 개 idempotent 기동).
  - `internal/agent/labels.go` (신규) — `.preview.yml` services 와 previewID 로부터 Traefik 라벨 묶음 생성. compose-mode override YAML 직렬화.
  - `internal/agent/config.go`: `--repo-url` / `AGENT_REPO_URL` 플래그 제거. `--traefik-port` (default 8080) / `--traefik-image` (default `traefik:v3.1`) 추가.
  - `cmd/agent/main.go`: 새 wiring (`MultiRepoCache`, traefik bootstrap, `.preview.yml` 파싱 의존 주입).
- **Hub 측**
  - `internal/hub/webhook_handler.go`: `pullRequestEvent.Repository.CloneURL` 추가. `handleUpsert` 에서 `clone_url` 도 함께 저장.
  - `internal/store` `Preview` 도메인 + 마이그레이션 0004: `previews.repo_clone_url TEXT NOT NULL DEFAULT ''` 컬럼 추가. (sqlc 쿼리/Upsert 갱신.)
  - `internal/hub/dispatcher.go`: `Dispatcher` 가 `RepoURLResolver` 대신 `Preview.RepoCloneURL` 을 그대로 `JobAssignData.RepoURL` 로 실음. `RepoURLResolver` 함수형 의존은 **제거**(§3 결정 6).
  - `internal/hub/config.go`: `PreviewRepoURL` 필드 제거(또는 deprecated 무시). `cmd/hub/daemon.go` 의 `resolveRepo` 함수 제거.
  - `internal/hub/proxy_middleware.go` **제거** + Hub 서버(`server.go`)의 ProxyMiddleware 연결 코드 제거(결정 12).
  - `internal/hub/admin_ui.go` + `views/agent_detail.gohtml`: build_commands / container_port 폼 필드 제거. `POST /admin/agents/{id}/config` 라우트는 **유지하되 본문이 비어있어도 200 으로 응답**(향후 다른 설정용으로 살려둠) — 결정 13.
  - `internal/hub/ws_handler.go` `sendAgentConfig` 호출 및 관련 코드 완전 제거(결정 13).
  - `internal/protocol/messages.go`:
    - `JobAssignData` 그대로 (`RepoURL` 의미만 "GitHub clone URL" 로 명확화).
    - `StatusUpdateData` 에 `PreviewURLs map[string]string `json:"preview_urls,omitempty"`` 추가.
    - `AgentConfigData`, `TypeAgentConfig`, `TypeConfigUpdate` 삭제(결정 13).
  - `internal/store/preview.go` `Preview` 구조체에 `PreviewURLs string` (JSON TEXT) 추가, `PublicURL` 필드 제거, `UpdateStatus` 의 `PreviewFields` 에도 동반.
  - `internal/store/agent.go` `AgentStore` 인터페이스 + sqlc 에서 `GetBuildConfig` / `SaveBuildConfig` 제거(결정 13).
  - Admin UI `previews_list/preview_detail` 에서 `PreviewURLs` 를 파싱해 service 별 링크 목록 표시.
- **테스트**
  - `.preview.yml` parser 단위 테스트.
  - `MultiRepoCache` 단위 테스트 (repoURL 2 개 동시 Ensure/Checkout/Remove, slug collision 케이스).
  - Runner compose / Dockerfile 분기 테스트 (CmdRunner fake + DockerClient fake).
  - Hub webhook clone_url 추출 단위 테스트.
  - Dispatcher: `JobAssignData.RepoURL` 이 preview row 의 clone URL 을 그대로 사용하는지 검증.
  - e2e (Playwright): repo fixture 2 개로 multi-repo + compose + .preview 라우팅 시나리오. (Phase 3 e2e harness 위에 추가.)

### 2-2. Out of Scope

- **ProxyMiddleware path-prefix 인지 확장** — ProxyMiddleware 자체는 Phase 6 에서 제거. path-prefix 인지가 필요하다면 후속 Phase 에서 새 미들웨어로 재구현.
- **`agents.build_commands` / `container_port` 후속 정리** — Phase 6 마이그레이션 0004 에서 DROP 완료. 별도 Phase 불필요.
- **Compose v1 (`docker-compose` 별도 바이너리)** — Docker CLI plugin (`docker compose ...`) 만 지원. 운영자 가이드에 명시.
- **Traefik 자체 ACME / dashboard 인증** — dashboard 는 비공개(`--api.dashboard=false`), HTTPS 는 후속 Phase.
- **Compose project 의 `volumes:` 정의 검사** — host bind mount 가 `.preview-override` 와 충돌하면 사용자가 책임. Agent 는 override 파일이 자기 파일 1 개만 수정함을 보장.
- **Hub 의 `previews.public_url` 컬럼 의미 통합** — 기존 `public_url` (Phase 3 에서 `pr-{n}.<base-domain>` 으로 생성)은 그대로 두고, Phase 6 의 `preview_urls` 는 별도 컬럼으로 추가. UI 가 둘을 모두 표시하고, 운영자가 어느 진입점을 쓸지 선택한다.
- **Hub ProxyMiddleware path-prefix 인지** — Phase 6 진입점은 Traefik 직접 노출(결정 12). ProxyMiddleware 의 path-prefix 변환 지원은 Phase 7 후보 4 로 이월.
- **`pr-{n}.<base-domain>` subdomain 모델** — Phase 6 부터 사실상 미사용. 후속 Phase 에서 ProxyMiddleware 를 Traefik 연동으로 교체하거나 제거 예정.
- **`previews.preview_urls` 의 빈 문자열 처리** — `NOT NULL DEFAULT ''` 로 선언하므로 sqlc 생성 코드는 `string` 으로 매핑된다. 빈 문자열(`""`)은 "아직 URL 없음" sentinel 로 사용하고, Admin UI 렌더링 시 JSON parse 실패(빈 문자열) 는 링크 목록 생략으로 처리.

### 2-3. Deferred (다음 Phase 후보)

- HTTPS / wildcard cert (Traefik ACME) 통합.
- `.preview.yml` 의 host-rule / header-rule / middleware chain.
- Hub 가 Traefik 의 dynamic config 를 직접 만드는 모드(파일 provider) — 현재는 Docker provider 자동 감지에 의존.
- Compose project orphan cleanup (PR 강제 종료/Hub DB 손상 시 Agent 가 자가 청소).
- per-PR `.preview.yml` override (Hub UI 에서 임시 변경).
- Compose 의 `depends_on`/`healthcheck` 신뢰성 검증, Traefik 라우터 활성화 timing 검증.
- `agents.build_commands` / `container_port` 컬럼 DROP 마이그레이션 (0005).

---

## 3. 설계 결정 (Design Decisions)

> 각 결정마다 (선택, 근거, 버려진 대안, 되돌릴 때 비용) 4 요소.

### 결정 1 — 설정 파일은 레포 루트의 `.preview.yml` (또는 `.preview.yaml`)

**선택**: 레포 루트의 `.preview.yml` 을 우선 탐색하고, 없으면 `.preview.yaml` 을 탐색한다. worktree 가 만들어진 직후 Agent 가 읽는다. 둘 다 없으면 즉시 `failed` STATUS_UPDATE(`reason="preview_config_missing"`). 확장자 없는 `.preview.yml` 는 인식하지 않는다.
**근거**:
- 확장자가 있어야 에디터(VS Code, JetBrains 등)가 YAML 문법 강조를 적용한다. `.travis.yml`, `.github/workflows/*.yml` 등 업계 관용과 일치.
- "빌드 정의는 레포가 가진다"는 Vercel/Render 관용. PR 의 head SHA 가 정의를 결정하므로 변경이 PR 흐름과 함께 흘러간다.
**대안 기각**:
- 확장자 없는 `.preview.yml` — 에디터 문법 강조 없음. 운영자 편의 저하.
- Hub UI 에서 입력 — Phase 6 의 핵심 목적(레포 자체로 정의)에 배반.
**되돌릴 때 비용**: parser 의 파일명 탐색 1 곳 + 단위 테스트만 영향. 작음.

### 결정 2 — `.preview.yml` 부재 시 합리적 default 시도하지 않고 즉시 실패

**선택**: `.preview.yml` 가 없으면 `os.Stat` 단계에서 `failed` STATUS_UPDATE(`reason="preview_config_missing"`). Dockerfile 만 있다고 default services 를 가정하지 않는다.
**근거**:
- Phase 4 결정 4(Dockerfile 강제 검사 제거)와 대칭. 정의가 명시적으로 없는데 Agent 가 추측하면 운영자가 잘못된 라우팅을 디버깅하게 된다.
- `.preview.yml` 의 첫 service 가 라우팅 path 의 default 진입점이 되므로 이 정보가 빠지면 어차피 외부 URL 을 만들 수 없다.
**대안 기각**: `.preview.yml` 부재 시 `port=80, path="/"` 로 default — 운영자가 정의 누락을 알아차리지 못함.
**되돌릴 때 비용**: parser 의 NotFound 분기 1 곳. 작음.

### 결정 3 — `.preview.yml` 스키마는 services 맵 + (port, path, strip) 3 필드

**선택**:

```yaml
compose_file: ./docker/docker-compose.yml  # optional, 명시 시 auto-detect 건너뜀
dockerfile: ./backend/Dockerfile           # optional, 명시 시 auto-detect 건너뜀
services:
  <name>:
    port: <int, required, 1..65535>
    path: <string, required, must start with "/", no trailing "/" 허용>
    strip: <bool, default true>
```

- `compose_file` / `dockerfile` 둘 다 명시하면 `compose_file` 우선.
- 명시된 경로가 worktree 기준 상대경로로 존재하지 않으면 즉시 `failed` (`reason="build_file_not_found"`).
- 미명시 시 결정 9 의 auto-detect 로 fallback.
- `services` 가 비어 있거나 키가 0 개면 invalid.
- 같은 path prefix 가 두 service 에 중복되면 invalid (`duplicate_path`).
- service name 은 `[a-z0-9_-]+` 만 허용. (compose 서비스명 + Traefik 라우터명 안전.)
- compose 모드에서 `<name>` 은 compose 의 service 이름과 **정확히 일치**해야 한다. 일치하지 않으면 `unknown_compose_service`.
- Dockerfile 모드에서 services 가 2 개 이상이면 invalid (`dockerfile_multi_service`). 단일 컨테이너에 라우팅 path 를 둘 거는 모델은 본 Phase 에서 지원하지 않는다.

**근거**:
- compose/Dockerfile 이 항상 레포 루트에 있다고 보장할 수 없다. monorepo, 서브디렉토리 프로젝트 구조를 지원하려면 명시적 경로가 필요.
- 선택적 필드로 두어 기존 루트 배치 레포는 수정 불필요.
- 3 필드(port/path/strip)만으로 path-prefix 라우팅이 결정된다. host-rule/middleware 까지 노출하면 `.preview.yml` 가 또 다른 Traefik 설정 파일이 되어 학습 부담 폭증.
**대안 기각**:
- compose service 와 별도 alias 필드(`compose_service: foo`) — 추상화 한 겹 더, 디버깅 어려움.
- Dockerfile 모드에서 다중 service path 라우팅(같은 컨테이너 안에 여러 포트) — Dockerfile 단일 컨테이너 가정과 충돌, label 폭증.
**되돌릴 때 비용**: parser + Runner 분기 진입점 1 곳. 작음.

### 결정 4 — Traefik 사이드카 1 개를 Agent 가 자가 기동, lifecycle 은 Agent 와 동일

**선택**: Agent 시작 시 `EnsureNetwork(preview-net)` → `EnsureTraefik()` 순서로 idempotent 호출. Traefik 컨테이너 이름은 `preview-traefik` 고정. 이미 있으면 inspect 후 image/label 일치 여부 확인 후 재사용. 다르면 stop+rm 후 재생성. Agent 가 SIGTERM 받으면 Traefik 컨테이너는 그대로 둔다(다른 build job 들이 라우팅 의존).
**근거**:
- Agent 부팅이 Traefik 부팅을 항상 선행하므로 첫 build 시 라우팅이 즉시 동작. compose 의 `external: true` 네트워크와 호환.
- Traefik 을 Agent lifecycle 과 묶어두는 게 운영자 인지 모델("Agent 1 대 = preview-net + Traefik 1 개") 에 부합.
**대안 기각**:
- 운영자가 Traefik 을 사전 기동 — 셀프호스팅 UX 악화. README 에 한 줄 추가될 만큼 부담.
- Agent SIGTERM 시 Traefik 도 정리 — 다른 worker thread 의 진행 중 build 가 라우팅을 잃음.
**되돌릴 때 비용**: `traefik.go` 1 파일 제거 + cmd/agent/main.go wiring 한 줄 제거. 작음.

### 결정 5 — Compose 모드 override 파일은 `.preview-override-{previewID}.yml` (worktree 안)

**선택**: Agent 가 worktree 안에 `.preview-override-{previewID}.yml` 파일을 생성하고, `docker compose -f docker-compose.yml -f .preview-override-{id}.yml --project-name preview-{id} up -d` 로 실행. teardown 시 override 파일 삭제 + worktree remove.
**근거**:
- compose CLI 의 표준 override 메커니즘 사용 — 사용자의 `docker-compose.yml` 을 손대지 않으면서 라벨/네트워크만 주입.
- previewID 가 파일명에 들어가므로 multi-job 동시 실행에서도 충돌 없음.
- worktree 안에 두면 worktree remove 시 자연 정리(failsafe).
**대안 기각**:
- `/tmp` 같은 외부 디렉토리에 둔다 — orphan 파일 추적이 어렵고, compose context 가 worktree 인 점과 일관성 깨짐.
- override 없이 사용자 compose 에 라벨/네트워크 주입을 강제 — 사용자가 외부 라우팅을 알게 되어 추상화 누설.
**되돌릴 때 비용**: Runner 의 compose 분기 + label 직렬화 1 곳. 중간.

### 결정 6 — `RepoURLResolver` 의존 제거, `Preview.RepoCloneURL` 을 진실의 원천으로

**선택**: `internal/hub/dispatcher.go` 의 `RepoURLResolver func(repoFullName string) string` 인자를 제거. `Dispatcher.assignOnce` 가 `JobAssignData.RepoURL` 에 `preview.RepoCloneURL` 값을 그대로 사용. webhook 단계에서 `pullRequestEvent.Repository.CloneURL` 을 파싱해 `previews.repo_clone_url` 컬럼에 저장.
**근거**:
- multi-repo 에서 `repo_full_name → URL` 단일 매핑은 무의미. webhook payload 가 이미 정확한 clone_url 을 준다.
- Dispatcher 의 함수형 의존을 줄여 wiring 단순화.
**대안 기각**:
- Resolver 인터페이스를 multi-repo 매핑(`map[fullName]URL`)으로 확장 — Hub 운영자가 매핑을 따로 입력해야 함. webhook 이 이미 답을 가지고 있는데 중복.
- HELLO/JOB_ASSIGN 시 Agent 가 GitHub API 로 clone_url 조회 — 토큰/rate limit/지연 도입.
**되돌릴 때 비용**: `dispatcher.go` `NewDispatcher` 시그니처 변경 + `cmd/hub/daemon.go` wiring 변경. 중간. 마이그레이션 0004 가 동반되므로 DB rollback 가능성도 검토 필요.

### 결정 7 — `previews.repo_clone_url` 컬럼 추가, NOT NULL DEFAULT ''

**선택**: 마이그레이션 0004:

```sql
ALTER TABLE previews ADD COLUMN repo_clone_url TEXT NOT NULL DEFAULT '';
```

- Phase 5 까지의 row 는 빈 문자열로 채워진다. Dispatcher 는 `repo_clone_url == ""` 이면 fallback 으로 `repo_full_name` 을 echo (Phase 5 호환).
- 신규 webhook 처리부터는 항상 clone_url 을 채운다.
- DB rollback 시 `ALTER TABLE ... DROP COLUMN repo_clone_url` (SQLite 3.35+ / Postgres 모두 표준).

**근거**:
- 표준 SQL 만 쓴다. SQLite·Postgres 호환.
- `NOT NULL DEFAULT ''` 가 빈 문자열 sentinel 로 fallback 분기를 일관 처리.
**대안 기각**:
- 별도 테이블 `repos` 를 만들어 정규화 — 본 Phase 범위를 폭증시킨다(Hub 도메인 모델 변경 + sqlc 쿼리 다수 추가). multi-repo 라우팅에 충분한 구조는 단일 컬럼.
- NULL 허용 — 코드에서 `*string` 분기를 강제. 빈 문자열 sentinel 이 더 단순.
**되돌릴 때 비용**: 마이그레이션 0004 down + sqlc regen + 호출자 fallback 제거. 중간.

### 결정 8 — `RepoCache` 는 그대로 유지, `MultiRepoCache` 를 thin wrapper 로 신규 추가

**선택**: 기존 `internal/agent/repocache.go` 의 `RepoCache` 는 **무변경**. 동일 파일(또는 별도 `repocache_multi.go`)에 `MultiRepoCache` 를 새로 추가한다. `MultiRepoCache` 는 `map[repoURL]*RepoCache` 를 `sync.Mutex` 로 보호하며, `getOrCreate(repoURL)` 로 `RepoCache` 인스턴스를 lazily 생성·반환한다.

```go
type MultiRepoCache struct {
    workDir string
    logger  *slog.Logger
    mu      sync.Mutex
    repos   map[string]*RepoCache  // key: raw repoURL
}

func NewMultiRepoCache(workDir string, logger *slog.Logger) *MultiRepoCache

func (m *MultiRepoCache) Ensure(ctx context.Context, repoURL string) error
func (m *MultiRepoCache) Checkout(ctx context.Context, repoURL, previewID, sha string) (string, error)
func (m *MultiRepoCache) Remove(ctx context.Context, repoURL, previewID string) error
func (m *MultiRepoCache) StartPrefetch(ctx context.Context, repoURL string, interval time.Duration)
func (m *MultiRepoCache) PruneStaleWorktrees(ctx context.Context, activeIDs []string) (int, error)
```

각 메서드는 `m.getOrCreate(repoURL)` 로 대응하는 `*RepoCache` 를 얻은 뒤 동일 메서드를 위임한다. map 키는 raw repoURL (slug 가 아니라) — 같은 slug 의 다른 URL 도 분리 캐시 가능.

**근거**:
- `RepoCache` 기존 코드·테스트 무변경 → diff 최소화, 리뷰 부담 최소.
- `MultiRepoCache` 가 단순한 thin wrapper 라 로직이 없고 테스트도 "map 에 올바른 RepoCache 가 들어가는가" 수준으로 단순.
- `RepoCache` 를 독립적으로 단위 테스트 가능 상태 유지.
**대안 기각**:
- `RepoCache` 를 `MultiRepoCache` 로 리네임하고 내부를 `singleRepoCache` 로 unexport — 기존 테스트/코드 전체 rename, diff 폭증. 얻는 것 없음.
- 호출자가 매번 `NewRepoCache` ad-hoc 생성 — fetch mutex/prefetch ticker 가 매번 재생성, lifecycle 누설.
**되돌릴 때 비용**: `MultiRepoCache` 파일 삭제 + wiring 1 곳 변경. 작음.

### 결정 9 — Compose / Dockerfile 자동 감지 파일명 우선순위 고정

**선택**: `.preview.yml` 에 `compose_file` 또는 `dockerfile` 이 명시되어 있으면 auto-detect 를 건너뛰고 그 경로를 직접 사용한다(결정 3). 미명시 시 worktree 안에서 아래 우선순위로 탐색한다.

1. `docker-compose.yml`
2. `docker-compose.yaml`
3. `compose.yml`
4. `compose.yaml`

넷 다 없으면 `Dockerfile` 감지. `Dockerfile` 도 없으면 `failed` (`reason="no_build_artifact"`). compose 파일과 Dockerfile 이 공존하면 **compose 우선**. 운영자가 Dockerfile 모드를 강제하고 싶으면 compose 파일을 두지 않거나 `.preview.yml` 에 `dockerfile` 경로를 명시한다.
**근거**:
- compose 가 있다면 거의 항상 운영자의 의도가 compose. Dockerfile 은 compose 의 build context 로도 자주 같이 존재.
- `.dockerfile` 같은 별 명칭은 무시(GitHub 관용 외).
**대안 기각**:
- `.preview.yml` 안에 `mode: compose|dockerfile` 명시 강제 — Phase 의 "자동 감지" 목표와 충돌.
- `Dockerfile` 우선 — multi-service 가 compose 인 일반 패턴과 어긋남.
**되돌릴 때 비용**: Runner 분기 1 곳. 작음.

### 결정 10 — Dockerfile 모드는 `.preview.yml` 의 첫 service 1 개만 사용 + 검증

**선택**: 결정 3 대로 services 가 1 개여야 함. 1 개 service `s` 를 가지고:

- `docker build -t preview-{previewID} <worktree>` (cwd 는 worktree, build context 는 `.`).
- `docker run -d --name preview-{previewID} --network preview-net --label "traefik.enable=true" --label "traefik.http.routers.{previewID}-{s}.rule=PathPrefix(\`/{previewID}/{s}\`)" ... preview-{previewID}`.
- container port 는 `.preview.yml` services[s].port.
- strip 라벨: `traefik.http.middlewares.{previewID}-{s}-strip.stripprefix.prefixes=/{previewID}/{s}` + 라우터에 `middlewares={previewID}-{s}-strip` 부착(default true).

**근거**: 결정 3 의 단일 service 강제와 일관. Hub Settings 의 `container_port` 가 사라진 빈자리를 `.preview.services[0].port` 가 메운다.
**대안 기각**: Dockerfile 모드에서 여러 path 라우팅 — 같은 컨테이너에 여러 EXPOSE 포트 가정. 본 Phase 비범위.
**되돌릴 때 비용**: Dockerfile 분기 1 곳. 작음.

### 결정 11 — STATUS_UPDATE 는 service 별 URL 을 모두 `preview_urls` map 으로 송신

**선택**: STATUS_UPDATE(running) 의 `preview_urls map[string]string` 에 service 이름 → 전체 URL 을 담는다.
예: `{"frontend":"http://host:8080/{pid}/front","admin":"http://host:8080/{pid}/admin"}`.
Hub 는 이를 `previews.preview_urls TEXT`(JSON) 컬럼으로 저장. Admin UI 는 JSON 을 파싱해 service 별로 링크를 나열한다. "대표 URL" 단일 필드는 없다.
**근거**:
- service 가 여러 개인 경우(frontend + admin) 하나를 임의로 골라 노출하면 나머지 서비스 URL 을 운영자가 직접 조합해야 함. 모두 표시가 더 유용.
- Hub 가 `.preview.yml` 내용을 알 필요 없이 Agent 가 URL 을 전부 계산해 보낸다.
**대안 기각**:
- 알파벳 오름차순 첫 키를 단일 `preview_url` 로 — service 이름에 따라 의도하지 않은 URL 이 대표가 됨.
- Hub 가 `.preview.yml` config 를 직접 파싱 — Hub 에 불필요한 agent-side 지식 누설.
**되돌릴 때 비용**: `StatusUpdateData`, `Preview` 구조체, DB 컬럼 1개, Admin UI 렌더링 1곳. 중간.

### 결정 12 — Phase 6 진입점은 Agent(Traefik) 직접 노출, Hub ProxyMiddleware 는 미사용

**선택**:
- Phase 6 부터 preview 접근 진입점은 `preview_urls` 에 담긴 `http://{agentHost}:{traefikPort}/{previewID}/{svcPath}` 다. 운영자는 외부 reverse proxy / 방화벽에서 agentHost:traefikPort 를 직접 노출한다.
- Hub ProxyMiddleware(`pr-{n}.<base-domain>` → `agent_host:agent_port` forwarding) 는 **코드에서 제거한다**. `internal/hub/proxy_middleware.go` + Hub 서버의 ProxyMiddleware 연결 코드 + 관련 테스트 제거. `previews.public_url` 컬럼은 마이그레이션 0004 에서 DROP.
- `previews.public_url` (Phase 3) 는 그대로 유지(dead column 취급). 신규 `previews.preview_urls TEXT NOT NULL DEFAULT ''` 컬럼 추가(마이그레이션 0004 동반). Admin UI 는 `preview_urls` 가 비어있지 않으면 service 별 링크 목록 우선 표시.
- STATUS_UPDATE 의 `agent_port` 필드는 더 이상 Hub ProxyMiddleware 진입점으로 쓰이지 않는다. Phase 6 부터 `agent_port` 를 Hub 가 읽어도 ProxyMiddleware 경유로 라우팅하지 않도록 운영 가이드에 명시. (STATUS_UPDATE 에서 `agent_port` 필드 자체는 호환성을 위해 유지.)
**근거**:
- Hub proxy path-prefix 인지 변환을 Phase 6 in-scope 로 끌어들이면 e2e 검증 폭증. Agent 직접 노출이 더 단순하고 multi-service 라우팅 모델과 자연스럽게 맞는다.
- Hub proxy 의 `pr-{n}` subdomain 모델은 single-repo 단일 컨테이너 가정에서 설계된 것 — multi-repo + multi-service 환경에서는 subdomain 충돌(동일 PR 번호, 다른 repo) 문제도 있어 장기적으로 교체 대상.
**대안 기각**:
- ProxyMiddleware 가 `/{previewID}/...` path 를 주입해 Traefik 으로 forwarding — ProxyMiddleware 재작성 + previewID 로 preview row 재조회 필요. Phase 7 후보로 이월.
- ProxyMiddleware 코드 제거 — Phase 5 운영 환경 호환이 깨지고 본 Phase 범위를 벗어남.
**되돌릴 때 비용**: 운영 가이드 1 곳 + 후속 Phase 에서 ProxyMiddleware 확장 시 본 결정과 정합성 검토 필요. 작음.

### 결정 13 — `agents.build_commands` / `container_port` / `AGENT_CONFIG` 와이어 및 관련 코드 완전 제거

**선택**:
- DB 컬럼 `agents.build_commands`, `agents.container_port` 를 마이그레이션 0004 에서 **DROP**.
- `AgentStore.GetBuildConfig` / `SaveBuildConfig` 메서드 제거. sqlc 쿼리 제거.
- Admin UI `agent_detail.gohtml` 에서 `build_commands` / `container_port` 폼 필드 제거. `POST /admin/agents/{id}/config` 라우트 제거.
- WS Handler `sendAgentConfig` 및 `AGENT_CONFIG` / `CONFIG_UPDATE` 송신 경로 완전 제거.
- `internal/agent/holder.go` 파일 삭제. `Runner.Handle` 의 Holder 스냅샷 사용 코드 제거.
- `internal/protocol/messages.go` 의 `AgentConfigData`, `TypeAgentConfig`, `TypeConfigUpdate` 상수 제거.
- `cmd/agent/main.go` 의 Holder 주입 wiring 제거.
**근거**:
- `.preview.yml` 파일이 진실의 원천이 되므로 Hub-managed run_commands 는 더 이상 의미 없음. dead code 를 남기면 미래 기여자에게 혼란을 주고, Phase 7 에서 별도 PR 을 내야 하는 부채만 생김.
- Phase 6 는 Hub + Agent 동시 업그레이드를 전제(§9 R12)하므로 와이어 호환 유지 필요 없음.
**대안 기각**:
- dead column / deprecated 유지 — 코드베이스에 진실의 원천이 두 개로 보임. 혼란 유발.
**되돌릴 때 비용**: 마이그레이션 0004 down + 관련 코드 복원. 중간(하지만 이 방향으로 돌아갈 이유 없음).

### 결정 14 — Traefik 호스트 포트는 `--traefik-port` config (default 8080), advertise host 는 기존 `advertise-host` 재사용

**선택**: `agent.Config.TraefikPort int (default 8080)`. `agent.Config.TraefikImage string (default "traefik:v3.1")`. STATUS_UPDATE 의 `preview_url` 호스트 부분은 `cfg.AdvertiseHost` 가 비어있지 않으면 그것을 사용, 비면 `127.0.0.1`. Traefik 컨테이너는 `-p {TraefikPort}:80` 으로 호스트 바인딩.
**근거**:
- 운영자 머신에서 8080 이 이미 점유된 흔한 케이스 대비 가능.
- AdvertiseHost 는 Phase 1 부터 있는 필드 — 재사용으로 신규 플래그 최소화.
**대안 기각**:
- random host port + Hub 에 보고 — Traefik 1 개를 random 에 두면 외부 reverse proxy / 방화벽 설정이 매번 변경됨. 운영자 부담.
- `--advertise-host` 와 별개의 `--traefik-host` — 99% 케이스에서 동일 값. 플래그 추가는 비용.
**되돌릴 때 비용**: config.go 2 필드 + traefik.go 의 호스트 바인딩 1 곳. 작음.

### 결정 15 — `.preview.yml` 변경 감지는 SHA 단위, 동일 SHA 재사용 시 캐시 사용 없음 (단순)

**선택**: Agent 는 `.preview.yml` 를 매 build 마다 worktree 에서 새로 읽는다. parser 결과를 캐시하지 않는다.
**근거**:
- worktree 는 SHA 별 별도. 같은 SHA 를 두 번 build 하는 케이스가 적고, 파일 read+yaml parse 비용은 ms 단위.
- 캐시 도입은 invalidation 책임을 추가한다(파일 변경 감지).
**대안 기각**: parsed result 를 in-memory 캐시 — 본 Phase 의 정확성보다 미시 최적화. 비용 > 효용.
**되돌릴 때 비용**: 캐시 도입 시 parser 호출자만 영향. 작음.

### 결정 16 — `preview-net` Docker network 는 bridge, 외부 attachable

**선택**: `docker network create --driver bridge --attachable preview-net` 로 idempotent 생성. 이미 있으면 inspect 후 driver=bridge 일치 확인 후 재사용. compose override 의 `networks: preview-net: external: true` 가 이 이름을 그대로 참조.
**근거**:
- `attachable` 은 운영자가 디버깅 시 임시 컨테이너를 같은 네트워크에 붙일 수 있게 함. overlay 는 swarm 가정이라 본 Phase 비범위.
**대안 기각**: 매 preview 마다 별 network 생성 — Traefik 의 단일 진입점이 깨짐.
**되돌릴 때 비용**: traefik.go 의 EnsureNetwork 1 곳. 작음.

### 결정 17 — Compose project 이름 = `preview-{previewID}`, 컨테이너 이름 = `preview-{previewID}-{service}` (compose 디폴트)

**선택**: compose 모드에서 `--project-name preview-{previewID}` 를 항상 명시. teardown 은 같은 project name 으로 `down`. Dockerfile 모드의 단일 컨테이너 이름은 `preview-{previewID}` (project 개념 없음).
**근거**:
- compose 의 project 이름 prefix 가 컨테이너/네트워크/볼륨 이름에 일관 prefix 로 들어가 cleanup 정확성 보장.
- previewID 가 UUID 이므로 충돌 가능성 무시 가능.
**대안 기각**:
- project 이름 자유화 — orphan cleanup 시 매핑 깨짐.
**되돌릴 때 비용**: Runner 의 compose 명령 인자 1 곳 + teardown 동일. 작음.

### 결정 18 — YAML 파싱은 `gopkg.in/yaml.v3`

**선택**: 외부 의존 1 개 추가. `.preview.yml` 와 override 파일 쓰기 둘 다 같은 패키지 사용.
**근거**:
- Go 표준 라이브러리 YAML 없음. yaml.v3 는 사실상 표준.
- compose override 를 직접 YAML 직렬화하므로 marshal 도 필요.
**대안 기각**:
- JSON 만 지원 — 결정 1 위배.
- 외부 의존 없이 raw 파서 자작 — 비용 폭증, 호환성 검증 부담.
**되돌릴 때 비용**: `go.mod` 1 줄 + import 2~3 곳. 작음. (NF-1 외부 의존 0 원칙의 예외 — Phase 5 와 달리 본 Phase 는 의존 1 개 추가가 정당화됨.)

---

## 4. 명세 상세

### 4-1. `.preview.yml` YAML 스키마

```yaml
# .preview.yml (예시)
services:
  frontend:
    port: 3000        # 컨테이너 내부 포트 (1..65535)
    path: /front      # URL path prefix, 반드시 "/" 로 시작
    strip: true       # default true. true 면 upstream 에 prefix 제거 후 전달
  admin:
    port: 8080
    path: /admin
    # strip 생략 시 default true
```

#### 4-1-1. 파서 인터페이스

```go
// internal/agent/preview_config.go
package agent

type PreviewService struct {
    Port  int
    Path  string
    Strip bool // 파싱 시 default true 적용
}

type PreviewConfig struct {
    ComposeFile string                    // optional, 명시 시 auto-detect 건너뜀
    Dockerfile  string                    // optional, 명시 시 auto-detect 건너뜀
    Services    map[string]PreviewService // key = service name
}

// LoadPreviewConfig 는 worktree 안의 .preview 를 읽고 검증 후 반환한다.
// 부재 시 ErrPreviewConfigMissing.
// 검증 실패 시 ErrPreviewConfigInvalid (reason 메시지 포함).
func LoadPreviewConfig(worktree string) (PreviewConfig, error)

// FirstService 는 정렬된 첫 service name 과 그 정의를 반환한다 (결정 11).
func (c PreviewConfig) FirstService() (name string, svc PreviewService, ok bool)
```

#### 4-1-2. 검증 규칙

| 규칙 | 위반 시 reason |
|---|---|
| services 가 비었거나 nil | `services_empty` |
| service name 이 `^[a-z0-9_-]+$` 미일치 | `service_name_invalid` |
| port 가 0 또는 음수 또는 >65535 | `port_invalid` |
| path 가 `/` 로 시작하지 않음 | `path_invalid` |
| 두 service 의 path 가 동일 | `duplicate_path` |
| `compose_file` 명시 + 파일 미존재 | `build_file_not_found` |
| `dockerfile` 명시 + 파일 미존재 | `build_file_not_found` |
| YAML 파싱 자체 실패 | `yaml_parse: <원본>` |

### 4-2. MultiRepoCache API (결정 8)

`RepoCache` 는 무변경. `MultiRepoCache` 는 `repocache_multi.go` 에 추가.

```go
// internal/agent/repocache_multi.go
package agent

type MultiRepoCache struct { /* §3 결정 8 */ }

func NewMultiRepoCache(workDir string, logger *slog.Logger) *MultiRepoCache

// Ensure 는 repoURL 의 bare clone 을 만든다(idempotent).
func (m *MultiRepoCache) Ensure(ctx context.Context, repoURL string) error

// Checkout 은 repoURL 의 sha 에 대한 worktree 를 만든다.
// 반환된 worktreePath 는 {workDir}/repos/{slug(repoURL)}/worktrees/preview-{previewID}.
func (m *MultiRepoCache) Checkout(ctx context.Context, repoURL, previewID, sha string) (string, error)

// Remove 는 repoURL 의 previewID worktree 를 정리한다. 없으면 no-op.
func (m *MultiRepoCache) Remove(ctx context.Context, repoURL, previewID string) error

// StartPrefetch 는 repoURL 별로 ticker 를 띄운다. 같은 repoURL 두 번 호출 시 두 번째는 no-op (이미 있는 ticker 재사용).
func (m *MultiRepoCache) StartPrefetch(ctx context.Context, repoURL string, interval time.Duration)

// PruneStaleWorktrees 는 {workDir}/repos/*/worktrees/preview-* 를 모두 스캔해
// activeIDs 에 없는 디렉토리를 제거한다 (R13). 부재한 repo 디렉토리는 무시.
// Agent 시작 시 Hub HELLO sync 결과(또는 그 직전 빈 슬라이스 = 보수적으로 미정리)로 1회 호출.
// 반환: 정리된 worktree 디렉토리 갯수 + 첫 에러.
func (m *MultiRepoCache) PruneStaleWorktrees(ctx context.Context, activeIDs []string) (int, error)
```

내부 데이터:

```
{workDir}/
  repos/
    {slug(repoA-url)}/        ← bare clone
      HEAD, objects, ...
      worktrees/
        preview-{idA1}/
        preview-{idA2}/
    {slug(repoB-url)}/
      ...
```

prefetch ticker 는 repoURL 단위 1 개. `sync.Map[repoURL]*tickerHandle`. cancel 은 ctx 의존.

### 4-3. Traefik bootstrap (결정 4, 14, 16)

```go
// internal/agent/traefik.go
package agent

type TraefikSpec struct {
    Image     string  // default "traefik:v3.1"
    HostPort  int     // default 8080
    Network   string  // "preview-net"
    Container string  // "preview-traefik"
}

// EnsureNetwork 는 preview-net (bridge, attachable) 을 idempotent 보장.
func EnsureNetwork(ctx context.Context, dc DockerClient, name string) error

// EnsureTraefik 는 컨테이너가 spec 과 일치하면 no-op,
// 다르면 stop+rm 후 재생성. spec 일치 여부는 컨테이너 라벨
// "preview.traefik.spec=<sha256(image|hostPort|network)>" 비교로 판정.
// 라벨이 부재하면(외부에서 만든 컨테이너) 재생성 대상.
func EnsureTraefik(ctx context.Context, dc DockerClient, spec TraefikSpec) error
```

spec 해시 계산:

```go
func specHash(s TraefikSpec) string {
    h := sha256.Sum256([]byte(s.Image + "|" + strconv.Itoa(s.HostPort) + "|" + s.Network))
    return hex.EncodeToString(h[:])
}
```

`EnsureTraefik` 가 ContainerCreate 시 `Labels["preview.traefik.spec"] = specHash(spec)` 를 함께 설정하고, 다음 호출에서 inspect 결과의 라벨을 읽어 비교한다. 이로써 image/HostPort 외 명령 인자가 변경되어도(추후 결정 4 에서 entrypoints 를 추가하는 경우 등) spec 해시 함수만 갱신하면 일관 비교가 가능.

Traefik 컨테이너 실행 인자(요지):

```
docker run -d --name preview-traefik \
  --network preview-net \
  -p {HostPort}:80 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  traefik:v3.1 \
  --providers.docker=true \
  --providers.docker.exposedbydefault=false \
  --providers.docker.network=preview-net \
  --entrypoints.web.address=:80 \
  --api.dashboard=false
```

`exposedbydefault=false` 는 Traefik 이 라벨 `traefik.enable=true` 가 붙은 컨테이너만 라우팅 대상으로 삼게 한다. 다른 시스템 컨테이너 영향 없음.

### 4-4. Runner 분기 (결정 5, 9, 10, 17)

```go
// internal/agent/runner.go (개정 후)

func (r *Runner) Handle(ctx, msg) error {
    pid := msg.PreviewID
    // ... paused 거절 / inFlight 카운트 / building STATUS_UPDATE (Phase 4~5 그대로) ...

    // (1) MultiRepoCache.Ensure(repoURL) + Checkout(repoURL, pid, sha) → worktree.
    // (2) LoadPreviewConfig(worktree) → PreviewConfig (결정 2: 부재 시 즉시 실패).
    // (3) resolveMode(worktree, cfg) → (mode, buildFilePath).
    //     cfg.ComposeFile 명시 → mode="compose", path=cfg.ComposeFile (파일 존재 검증 포함).
    //     cfg.Dockerfile 명시  → mode="dockerfile", path=cfg.Dockerfile (파일 존재 검증 포함).
    //     미명시              → auto-detect (결정 9 우선순위).
    //     mode == ""          → fail("no_build_artifact").
    // (4) compose 모드:
    //     - 4a. validateComposeServices(composePath, cfg) → unknown_compose_service 시 fail.
    //          composePath 를 yaml.v3 Unmarshal map[string]any 후 services 키 집합과
    //          cfg.Services 키 집합을 비교: cfg.Services 의 모든 키가 compose services 에
    //          존재해야 한다 (compose 측이 더 많은 service 를 가지는 건 허용 — override 가
    //          미언급된 service 는 그대로 동작).
    //     - WriteOverride(worktree, ".preview-override-"+pid+".yml", labelsForCompose(pid, cfg))
    //     - Run(ctx, "docker", "compose",
    //         "-f", composeFile, "-f", overrideFile,
    //         "--project-name", "preview-"+pid, "up", "-d")
    // (5) dockerfile 모드:
    //     - len(cfg.Services) != 1 → fail("dockerfile_multi_service").
    //     - dc.ImageBuild(ctx, worktree, "preview-"+pid)  (또는 셸 docker build)
    //     - dc.ContainerCreate({Image, Network: "preview-net", Labels: labelsForSingle(pid, cfg), Name: "preview-"+pid})
    //     - dc.ContainerStart
    // (6) jobs.Store(pid, &runningJob{previewID, mode, projectOrContainer, worktreePath, repoURL})
    // (7) preview_urls 계산: cfg.Services 를 순회해 service 이름 → "http://"+advHost+":"+traefikPort+"/"+pid+svc.Path map 생성.
    // (8) STATUS_UPDATE running with PreviewURLs=previewURLsMap.
}
```

`runningJob` 구조체:

```go
type runningJob struct {
    previewID    string
    mode         string // "compose" | "dockerfile"
    projectName  string // compose only ("preview-<id>")
    containerID  string // dockerfile only
    worktreePath string
    repoURL      string // teardown 시 MultiRepoCache.Remove 에 필요
}
```

Teardown:

```go
func (r *Runner) Teardown(ctx, previewID) error {
    v, ok := r.jobs.Load(previewID); if !ok { /* 기존 fallback */ ... }
    job := v.(*runningJob)
    if job.mode == "compose" {
        // override 파일도 함께 들어 있으므로 down 시 -f 둘 다 명시
        _ = run(ctx, "docker", "compose",
            "-f", filepath.Join(job.worktreePath, composeFile),
            "-f", filepath.Join(job.worktreePath, ".preview-override-"+previewID+".yml"),
            "--project-name", job.projectName, "down", "--remove-orphans")
        _ = os.Remove(filepath.Join(job.worktreePath, ".preview-override-"+previewID+".yml"))
    } else {
        _ = r.docker.ContainerStop(ctx, job.containerID)
        _ = r.docker.ContainerRemove(ctx, job.containerID, RemoveOptions{Force: true})
    }
    _ = r.cache.Remove(ctx, job.repoURL, previewID)
    r.jobs.Delete(previewID)
    // STATUS_UPDATE done (기존)
}
```

### 4-5. Traefik 라벨 생성 (compose / Dockerfile 공통 규칙)

각 service `s` (port `P`, path `Pp`, strip `St`) 에 대해:

```
traefik.enable=true
traefik.docker.network=preview-net
traefik.http.routers.{previewID}-{s}.rule=PathPrefix(`{Pp full = "/"+previewID+Pp}`)
traefik.http.routers.{previewID}-{s}.entrypoints=web
traefik.http.services.{previewID}-{s}.loadbalancer.server.port={P}
[ if St: ]
  traefik.http.routers.{previewID}-{s}.middlewares={previewID}-{s}-strip
  traefik.http.middlewares.{previewID}-{s}-strip.stripprefix.prefixes={Pp full}
```

> 주의: `Pp full` 은 `/{previewID}{cfg.Path}` 로 합성된다. cfg.Path 가 `/front` 면 `/{previewID}/front`. previewID 는 UUID(36자) 라 PathPrefix 충돌 없음. strip=true 시 upstream 에 도달하는 경로는 `/` (또는 `/foo` 의 `/foo`)로 prefix 만 잘림.

#### 4-5-1. compose override YAML 형태

```yaml
# .preview-override-{previewID}.yml
services:
  frontend:
    networks:
      - preview-net
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=preview-net"
      - "traefik.http.routers.{previewID}-frontend.rule=PathPrefix(`/{previewID}/front`)"
      - "traefik.http.routers.{previewID}-frontend.entrypoints=web"
      - "traefik.http.services.{previewID}-frontend.loadbalancer.server.port=3000"
      - "traefik.http.routers.{previewID}-frontend.middlewares={previewID}-frontend-strip"
      - "traefik.http.middlewares.{previewID}-frontend-strip.stripprefix.prefixes=/{previewID}/front"
  admin:
    networks:
      - preview-net
    labels:
      - "traefik.enable=true"
      ...

networks:
  preview-net:
    external: true
```

`labels.go` 가 위 YAML 을 `gopkg.in/yaml.v3` Marshal 로 직렬화. service 이름은 `.preview.yml` 의 services 키 (= compose service 이름) 그대로.

### 4-6. Hub webhook 변경 (결정 6, 7)

```go
type pullRequestEvent struct {
    // ... 기존 ...
    Repository struct {
        FullName string `json:"full_name"`
        CloneURL string `json:"clone_url"`     // Phase 6 추가
    } `json:"repository"`
}
```

`handleUpsert` 에서 `preview.RepoCloneURL = p.Repository.CloneURL`. webhook 본문에 `clone_url` 이 빠진 경우(레거시 fixture) 에는 `""` 로 저장 + 경고 로그(`webhook_clone_url_missing`).

마이그레이션 0004 (up):

```sql
ALTER TABLE previews ADD COLUMN repo_clone_url TEXT NOT NULL DEFAULT '';
ALTER TABLE previews ADD COLUMN preview_urls TEXT NOT NULL DEFAULT '';  -- JSON map[service]url, Phase 6
ALTER TABLE previews DROP COLUMN public_url;   -- ProxyMiddleware 제거로 불필요
ALTER TABLE agents DROP COLUMN build_commands;
ALTER TABLE agents DROP COLUMN container_port;
```

down:

```sql
ALTER TABLE agents ADD COLUMN container_port INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN build_commands TEXT NOT NULL DEFAULT '';
ALTER TABLE previews ADD COLUMN public_url TEXT NOT NULL DEFAULT '';
ALTER TABLE previews DROP COLUMN preview_urls;
ALTER TABLE previews DROP COLUMN repo_clone_url;
```

sqlc 쿼리 `UpsertPreview`, `UpdatePreviewStatus`, `GetPreviewByID`, `ListPreviews`, `ListPreviewsByAgent` 모두 새 컬럼 포함하도록 수정. `Preview` 도메인 구조체에 `RepoCloneURL string` + `PreviewURLs string` 추가.

### 4-7. Hub Dispatcher 변경 (결정 6)

`internal/hub/dispatcher.go` 의 변경:

```go
// before:
type Dispatcher struct { ResolveRepo RepoURLResolver; ... }
// after:
type Dispatcher struct { /* ResolveRepo 제거 */ ... }
```

`assignOnce` 에서 `JobAssignData.RepoURL = preview.RepoCloneURL`. 빈 문자열이면 fallback 으로 `preview.RepoFullName` (Phase 5 호환). `cmd/hub/daemon.go` 의 `resolveRepo` 함수 + `RepoURLResolver` 의존 주입 코드 모두 제거.

### 4-8. Agent CLI 변경 (결정 14)

```go
// internal/agent/config.go
type Config struct {
    // ... 기존: HubURL, Token, AdvertiseHost, LogLevel, WorkDir, PrefetchInterval, MaxJobs ...
    // 제거: RepoURL string  ← Phase 6
    // 추가:
    TraefikPort  int    // default 8080
    TraefikImage string // default "traefik:v3.1"
}
```

`ParseConfig` 가 `--traefik-port` / `AGENT_TRAEFIK_PORT`, `--traefik-image` / `AGENT_TRAEFIK_IMAGE` 를 파싱. `--repo-url` / `AGENT_REPO_URL` 플래그·env 는 제거(있어도 무시 + warn 로그 1 회 권장 — 결정 13 와 같은 dead-flag 정책).

### 4-9. wiring (cmd/agent/main.go)

대략의 흐름:

```go
cfg, err := agent.ParseConfig(args)
// ...
docker, err := agent.NewDockerClient(...)
if err := agent.EnsureNetwork(ctx, docker, "preview-net"); err != nil { fatal }
if err := agent.EnsureTraefik(ctx, docker, agent.TraefikSpec{
    Image: cfg.TraefikImage, HostPort: cfg.TraefikPort,
    Network: "preview-net", Container: "preview-traefik",
}); err != nil { fatal }

cache := agent.NewMultiRepoCache(cfg.WorkDir, logger)
runner := agent.NewRunner(docker, cache, client, cfg.AdvertiseHost, logger)
runner.SetTraefikPort(cfg.TraefikPort) // preview_url 호스트 부분에 사용
// Phase 4 holder 주입 코드 제거 (결정 13)
runner.SetReadySender(client)
runner.SetMaxJobs(cfg.MaxJobs)
```

### 4-10. STATUS_UPDATE 변경 (결정 11, 12)

```go
type StatusUpdateData struct {
    // ... 기존 ...
    PreviewURLs map[string]string `json:"preview_urls,omitempty"` // Phase 6: service → URL
}
```

Hub 측 `StatusUpdateHandler.OnStatusUpdate` 에서 `data.PreviewURLs` 를 JSON 직렬화하여 `PreviewFields.PreviewURLs` 로 매핑하고 `UpdateStatus` 에 넘김. `data.PreviewURLs` 가 nil 이면 빈 문자열 저장.

### 4-11. 디렉토리 트리 (변경 후 요지)

```
internal/agent/
  preview_config.go         [신규] .preview 파서 + FirstService
  preview_config_test.go    [신규]
  repocache.go              [무변경] 기존 RepoCache 그대로
  repocache_multi.go        [신규] MultiRepoCache thin wrapper
  repocache_multi_test.go   [신규] 멀티 repoURL 시나리오
  repocache_test.go         [무변경]
  traefik.go                [신규] EnsureNetwork + EnsureTraefik
  traefik_test.go           [신규] DockerClient fake
  labels.go                 [신규] Traefik 라벨 묶음 + override YAML 직렬화
  labels_test.go            [신규]
  runner.go                 [수정] compose/Dockerfile 분기 + preview_url 계산
  runner_test.go            [수정] 분기 테스트
  config.go                 [수정] --repo-url 제거 + traefik 플래그 추가
  holder.go                 [삭제] (결정 13)
  ready.go / client.go      [무변경] (Phase 5)
cmd/agent/main.go           [수정] wiring (Holder 주입 제거)

internal/hub/
  webhook_handler.go        [수정] CloneURL 파싱
  dispatcher.go             [수정] RepoURLResolver 의존 제거
  config.go                 [수정] PreviewRepoURL 필드 제거
  proxy_middleware.go       [삭제] (결정 12)
  server.go                 [수정] ProxyMiddleware 연결 제거
  admin_ui.go               [수정] agent_detail 폼 정리
  views/agent_detail.gohtml [수정]
  ws_handler.go             [수정] sendAgentConfig 호출 제거 또는 빈 페이로드
internal/store/
  preview.go                [수정] RepoCloneURL + PreviewURL 필드
  agent.go                  [무변경] (build_commands/container_port 컬럼은 dead)
db/migrations/
  0004_previews_clone_url_and_preview_url.up.sql       [신규]
  0004_previews_clone_url_and_preview_url.down.sql     [신규]
db/queries/preview.sql      [수정]
internal/db/sqlite/         [재생성]
internal/protocol/messages.go [수정] StatusUpdateData.PreviewURL
cmd/hub/daemon.go           [수정] resolveRepo 제거
```

---

## 5. 시퀀스 다이어그램 (ASCII)

### 5-1. Compose 모드, 신규 PR

```
GitHub                Hub                  Agent           Docker(local)
   |  webhook(opened)    |                  |                  |
   | -----------------> |                  |                  |
   |                    | UPSERT preview    |                  |
   |                    | (repo_clone_url)  |                  |
   |                    | enqueue           |                  |
   |                    | <----- READY ---- |                  |
   |                    | JOB_ASSIGN(p,    |                  |
   |                    |  RepoURL=clone) ->|                  |
   |                    |                   | Ensure(repoA)    |
   |                    |                   |   git clone bare |
   |                    |                   | Checkout(p,sha)  |
   |                    |                   |   worktree add   |
   |                    |                   | LoadPreviewConfig|
   |                    |                   | detect compose   |
   |                    |                   | WriteOverride    |
   |                    |                   | docker compose   |
   |                    |                   |   -f -f --project|
   |                    |                   |   up -d -------->|
   |                    |                   |                  | (containers up,
   |                    |                   |                  |  Traefik provider
   |                    |                   |                  |  detects labels)
   |                    | <- STATUS_UPDATE  |                  |
   |                    |   running         |                  |
   |                    |   preview_url=... |                  |
   |                    | UPDATE previews   |                  |
   |                    |   status=running  |                  |
   |                    |   preview_url=... |                  |
```

### 5-2. PR closed → teardown

```
Hub                                Agent
 | JOB_TEARDOWN(p) -------------> |
 |                                | jobs.Load(p)
 |                                | mode==compose:
 |                                |   docker compose --project-name preview-p down
 |                                |   os.Remove(.preview-override-p.yml)
 |                                | mode==dockerfile:
 |                                |   docker stop preview-p
 |                                |   docker rm   preview-p
 |                                | MultiRepoCache.Remove(repoURL, p)
 | <- STATUS_UPDATE done -------- |
```

### 5-3. Multi-repo 동시 처리 (Agent 1, repoA + repoB 각 PR 1 개)

```
Hub          Agent             FS                     Docker
 | JA(pA,clone=repoA) ------> |                       |
 | JA(pB,clone=repoB) ------> |                       |
 |             | Ensure(repoA): repos/{slugA}/ bare    |
 |             | Ensure(repoB): repos/{slugB}/ bare    |
 |             | Checkout(repoA,pA,shaA) repos/{slugA}/worktrees/preview-pA
 |             | Checkout(repoB,pB,shaB) repos/{slugB}/worktrees/preview-pB
 |             | (각 worktree 의 .preview 읽기)        |
 |             | compose up --project-name preview-pA -> docker
 |             | compose up --project-name preview-pB -> docker
 | STATUS_UPDATE running pA <- (preview_url 포함)      |
 | STATUS_UPDATE running pB <-                         |
```

---

## 6. 기능 체크리스트 (F-*)

### 6-1. `.preview.yml` 파서

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-1 | `LoadPreviewConfig(worktree)` 가 정상 YAML 을 services 맵으로 파싱한다 | 단위 (fixture YAML) |
| F-2 | services 가 비었거나 nil 이면 `services_empty` 에러 | 단위 |
| F-3 | service name 이 `[a-z0-9_-]+` 미일치 시 `service_name_invalid` 에러 | 단위 (3 케이스: 대문자/공백/특수문자) |
| F-4 | port 0/음수/65536 시 `port_invalid` 에러 | 단위 (경계값) |
| F-5 | path 가 `/` 미시작 시 `path_invalid` 에러 | 단위 |
| F-6 | 두 service 의 path 동일 시 `duplicate_path` 에러 | 단위 |
| F-7 | strip 생략 시 default true 적용 | 단위 |
| F-8 | strip:false 명시 시 false 그대로 보존 | 단위 |
| F-9 | YAML 자체 파싱 실패 시 `yaml_parse: <원본>` 포함 에러 | 단위 (잘린 YAML) |
| F-10 | `.preview.yml` 파일 부재 시 `ErrPreviewConfigMissing` | 단위 (worktree 빈 디렉토리) |
| F-11 | `FirstService()` 가 service 이름의 알파벳 오름차순 첫 키를 반환 | 단위 (3 service 섞기) |
| F-11b | `compose_file` 명시 시 해당 경로를 반환하고 auto-detect 건너뜀 | 단위 |
| F-11c | `dockerfile` 명시 시 해당 경로를 반환 | 단위 |
| F-11d | `compose_file` 명시 + 파일 미존재 시 `build_file_not_found` 에러 | 단위 |
| F-11e | `compose_file` + `dockerfile` 둘 다 명시 시 `compose_file` 우선 | 단위 |

### 6-2. MultiRepoCache (멀티-레포)

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-12 | `Ensure(repoA)` 와 `Ensure(repoB)` 가 서로 다른 디렉토리에 bare clone 을 만든다 | 단위 (file:// fixture 2 개) |
| F-13 | 같은 repoURL 두 번 `Ensure` 시 두 번째는 git clone 을 호출하지 않는다 (idempotent) | 단위 (CmdRunner fake call count) |
| F-14 | `Checkout(repoA, p1, sha)` worktree 경로가 `{workDir}/repos/{slug(repoA)}/worktrees/preview-p1` | 단위 |
| F-15 | `Remove(repoA, p1)` 후 worktree 디렉토리 부재, repos/{slug(repoA)} 자체는 보존 | 단위 |
| F-16 | repoA 와 repoB 의 fetch 가 별도 mutex 로 보호되어 동시 호출 시 race 없음 | 단위 (`go test -race` + 두 goroutine 동시 fetch) |
| F-17 | `StartPrefetch(repoA, 5m)` 두 번 호출 시 두 번째 ticker 가 추가 생성되지 않음 | 단위 |
| F-18 | 같은 slug 로 충돌하는 다른 repoURL 두 개 (예: case-only 차이) 가 별도 map 엔트리로 분리되는지 | 단위 |
| F-18b | `PruneStaleWorktrees(ctx, [pidA])` 가 worktrees/preview-pidA 는 보존하고 preview-pidB 디렉토리는 `RemoveAll` | 단위 (tmp dir fixture) |
| F-18c | `PruneStaleWorktrees` 가 빈 activeIDs 슬라이스에 대해 보수적 동작 — Agent 부팅 시점 Hub HELLO sync 미완료 케이스에서 모든 worktree 를 보존(즉 `len(activeIDs)==0` 인 경우 no-op + warn 로그) | 단위 |

### 6-3. Traefik bootstrap

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-19 | `EnsureNetwork(preview-net)` 가 부재 시 `docker network create --driver bridge --attachable preview-net` 1 회 호출 | 단위 (DockerClient fake) |
| F-20 | `EnsureNetwork` 가 이미 있으면 driver 일치 확인 후 create 호출하지 않음 | 단위 |
| F-21 | `EnsureTraefik` 가 컨테이너 부재 시 spec 인자대로 ContainerCreate + ContainerStart 호출 | 단위 |
| F-22 | `EnsureTraefik` 가 이미 있고 image/HostPort 일치 시 no-op | 단위 |
| F-23 | `EnsureTraefik` 가 이미 있고 image 가 다르면 stop+rm 후 재생성 | 단위 |

### 6-4. Runner 분기 (compose / Dockerfile)

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-24 | worktree 에 `docker-compose.yml` 만 있을 때 detect 결과가 `compose` | 단위 (tmp dir fixture) |
| F-24b | worktree 에 `docker-compose.yaml` 만 있을 때 detect 결과가 `compose` | 단위 |
| F-25 | worktree 에 `compose.yml` 만 있을 때 detect 결과가 `compose` | 단위 |
| F-25b | worktree 에 `compose.yaml` 만 있을 때 detect 결과가 `compose` | 단위 |
| F-26 | worktree 에 `docker-compose.yml` + `Dockerfile` 둘 다 있을 때 결과가 `compose` (결정 9) | 단위 |
| F-26b | 우선순위: `docker-compose.yml` + `compose.yaml` 공존 시 `docker-compose.yml` 이 선택됨 | 단위 |
| F-27 | worktree 에 `Dockerfile` 만 있을 때 결과가 `dockerfile` | 단위 |
| F-28 | worktree 에 셋 다 없을 때 즉시 STATUS_UPDATE failed (`no_build_artifact`) | 단위 (Runner fake) |
| F-29 | `.preview.yml` 부재 시 STATUS_UPDATE failed (`preview_config_missing`) | 단위 |
| F-30 | compose 모드에서 `.preview-override-{pid}.yml` 이 worktree 에 생성되고 §4-5 의 라벨 키 5 개(strip=false 시) 또는 7 개(strip=true 시) 가 모두 매칭: `traefik.enable=true`, `traefik.docker.network=preview-net`, `traefik.http.routers.{pid}-{s}.rule=PathPrefix(...)`, `traefik.http.routers.{pid}-{s}.entrypoints=web`, `traefik.http.services.{pid}-{s}.loadbalancer.server.port={P}`, (옵션) `traefik.http.routers.{pid}-{s}.middlewares=...`, (옵션) `traefik.http.middlewares.{pid}-{s}-strip.stripprefix.prefixes=...`. networks 항목에 `preview-net` 이 포함되고 외부 networks 선언이 `external: true` | 단위 (파일 read + YAML decode 후 키별 assert) |
| F-30b | `validateComposeServices` 가 `.preview.yml` 의 service 키가 compose 파일에 없는 경우 `unknown_compose_service` 로 STATUS_UPDATE failed | 단위 (compose fixture: services [a,b], .preview: services [c]) |
| F-31 | compose 모드 명령이 `docker compose -f docker-compose.yml -f .preview-override-{pid}.yml --project-name preview-{pid} up -d` 형태로 호출 | 단위 (CmdRunner fake) |
| F-32 | Dockerfile 모드에서 `services` 가 2 개 이상이면 STATUS_UPDATE failed (`dockerfile_multi_service`) | 단위 |
| F-33 | Dockerfile 모드에서 `docker build -t preview-{pid}` + `ContainerCreate` 가 `--network preview-net` 와 정확한 라벨 묶음으로 호출 | 단위 |
| F-34 | Teardown(compose) 시 `docker compose --project-name preview-{pid} down --remove-orphans` 호출 + override 파일 삭제 | 단위 |
| F-35 | Teardown(dockerfile) 시 `ContainerStop` + `ContainerRemove(force=true)` 호출 | 단위 |
| F-36 | Teardown 후 `MultiRepoCache.Remove(repoURL, pid)` 호출됨 | 단위 |

### 6-5. STATUS_UPDATE preview_urls

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-37 | 성공 시 STATUS_UPDATE.PreviewURLs 가 service 이름 → `http://{advHost}:{traefikPort}/{pid}{svc.Path}` 형태의 map | 단위 |
| F-38 | advHost 비어있을 때 `127.0.0.1` 사용 | 단위 |
| F-39 | Hub `OnStatusUpdate` 가 PreviewURLs 를 JSON 직렬화해 `previews.preview_urls` 컬럼으로 저장 | 단위 (PreviewStore fake) |
| F-40 | Admin UI 의 preview detail 이 preview_urls 가 비어있지 않으면 service 별 링크 목록으로 표시 | e2e (Playwright) |

### 6-6. Hub webhook + dispatcher

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-41 | webhook payload `repository.clone_url` 이 `previews.repo_clone_url` 로 저장 | 단위 (Upsert 호출 인자 검사) |
| F-42 | webhook payload 에 clone_url 누락 시 빈 문자열 저장 + warn 로그 | 단위 |
| F-43 | Dispatcher 가 `JobAssignData.RepoURL` 에 `preview.RepoCloneURL` 사용 | 단위 |
| F-44 | `RepoCloneURL == ""` (legacy row) 일 때 `RepoFullName` fallback 으로 echo (Phase 5 호환) | 단위 |
| F-45 | `cmd/hub/daemon.go` 의 `resolveRepo` 함수 / `PreviewRepoURL` config 가 코드에서 제거됨 | grep + go build |

### 6-7. UI / 와이어 정리 (deprecated)

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-46 | Admin UI `agent_detail` 페이지에 build_commands / container_port 입력 필드가 없음 | e2e (Playwright DOM 검사) |
| F-47 | `POST /admin/agents/{id}/config` 빈 body 처리 시 200 OK | 단위 |
| F-48 | Hub WS `sendAgentConfig` 호출이 build commands 를 송신하지 않음 (빈 페이로드 또는 호출 자체 제거) | 단위 (fake conn write 검사) |
| F-49 | Agent `Holder` / `Runner.RunCommands` 사용 코드 경로가 build 흐름에서 호출되지 않음 | grep + 단위 (Holder 미주입 상태에서도 정상 동작) |

### 6-8. CLI / wiring

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-50 | `agent.ParseConfig` 가 `--repo-url` / `AGENT_REPO_URL` 을 더 이상 요구하지 않음 (없어도 정상) | 단위 |
| F-51 | `--traefik-port 9000` 입력 시 `cfg.TraefikPort == 9000`, default 8080 | 단위 |
| F-52 | `--traefik-image traefik:v3.2` 입력 시 그대로 반영, default `traefik:v3.1` | 단위 |
| F-53 | `cmd/agent/main.go` 가 시작 시 `EnsureNetwork` + `EnsureTraefik` 을 wiring | grep + 단위 (DockerClient fake) |

### 6-9. 통합 / 회귀

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-54 | Phase 0~5 단위/통합 테스트 회귀 0 (변경된 시그니처 따라 갱신) | full test 재실행 |
| F-55 | repoA + repoB 두 PR 동시 처리 시나리오: Agent 가 두 worktree 를 별도 디렉토리에 두고 `docker compose --project-name preview-{pidA}` / `preview-{pidB}` 가 둘 다 떠 있음 | 통합 (fake docker + fake hub) |
| F-56 | e2e: repo fixture 1 개로 `.preview.yml` (frontend:3000:/front, admin:8080:/admin) + compose 사용해 PR 열기 → preview_url 두 개에 HTTP GET 시 각 컨테이너 응답이 path-prefix 라우팅으로 도달. fixture 컨테이너 두 개는 응답 본문에 자기 service 이름을 echo 하고(예: `frontend` 컨테이너는 `"service":"frontend"` 본문 / `admin` 컨테이너는 `"service":"admin"` 본문), `X-Preview-Service: <name>` 응답 헤더도 포함한다. e2e 가 `/{previewID}/front` 의 본문/헤더가 `frontend` 인지, `/{previewID}/admin` 의 본문/헤더가 `admin` 인지 정확히 검증. | Playwright + docker fixture (CI 환경에서 docker-in-docker 가능 시) |

---

## 7. 비기능 체크리스트 (NF-*)

| ID | 항목 | 검증 방법 |
|---|---|---|
| NF-1 | 외부 의존성: `gopkg.in/yaml.v3` 1 개만 추가. 그 외 0 (결정 18) | `go mod tidy` diff |
| NF-2 | 새/수정 파일 모두 책임 주석(3~5줄) 헤더 포함 | grep `// 이 파일의 책임:` |
| NF-3 | 어떤 파일도 300 줄을 넘지 않는다. **분할 정책**: `internal/agent/runner.go` 는 detect 함수 + Handle 의 공통 흐름(repo Ensure/Checkout/.preview load/STATUS_UPDATE 송신)만 잔류시키고, compose 분기는 `runner_compose.go` (override 작성 + `docker compose up/down` 실행 + Teardown 의 compose 경로), Dockerfile 분기는 `runner_dockerfile.go` (build/run + Teardown 의 dockerfile 경로) 로 분리. 라벨/override YAML 직렬화는 `labels.go`. spec 해시는 `traefik.go` 에 포함. | `wc -l internal/agent/*.go internal/hub/*.go` 후 모든 파일 ≤ 300 |
| NF-4 | `go vet ./...`, `golangci-lint run` clean | CI |
| NF-5 | `go test -race ./...` green (특히 MultiRepoCache + Runner concurrent) | CI |
| NF-6 | 레이어 의존: `internal/agent` 가 `internal/hub` 또는 `cmd/*` 를 import 하지 않음 | depguard / grep |
| NF-7 | SQLite·Postgres 호환 SQL: 0004 마이그레이션의 `ALTER TABLE ... ADD COLUMN ... NOT NULL DEFAULT ''` 는 두 DB 모두 표준 (이식성 원칙) | 마이그레이션 시각 리뷰 + sqlc generate 성공 |
| NF-8 | `slog` 키 명명: `agent_traefik_*`, `agent_compose_*`, `webhook_clone_url_*`, `repocache_multi_*` 일관 | grep |
| NF-9 | 신규 인터페이스 (`MultiRepoCache`, `DockerClient` traefik 메서드) 모두 fake mock 으로 단위 테스트 가능 | 테스트 코드 리뷰 |
| NF-10 | `.preview.yml` parser 가 외부 네트워크/디스크 접근 외 부수효과 없음 (pure read) | 코드 리뷰 |
| NF-11 | runner_test 가 실제 docker daemon 을 요구하지 않음 (fake DockerClient + CmdRunner 만 사용) | CI 환경에서 docker 미설치 시 단위 테스트 통과 |
| NF-12 | e2e (F-56) 가 docker daemon 필요 시 별도 빌드 태그(`integration`)로 분리해 PR CI 에서 빌드만 확인, 실 실행은 로컬 가이드에 한정 | Makefile target 분리 |
| NF-13 | Phase 4 의 Holder/AgentConfigData 와이어가 호환 유지(빈 페이로드 송신/수신 가능) | 단위 (Phase 5 시점 Agent 가 새 Hub 와 핸드셰이크해도 정상) |
| NF-14 | `previews.repo_clone_url` 추가 컬럼이 sqlc generate 에 반영되어 컴파일 통과 | `make sqlc` |
| NF-15 | 마이그레이션 0004 down 이 up 의 정확한 역순으로 동작 | `migrate down` 단위 테스트 |
| NF-16 | Traefik 라벨 문자열에 백틱(`)이 들어가 Go 소스에서 escape 가 정확 | 단위 (생성된 라벨 정확 매칭) |

---

## 8. 단계 분할 (구현·평가용)

본 Phase 는 변경 폭이 크다(파서/캐시/Runner/Hub webhook/UI/마이그레이션). **Step 4 단계** 로 나눈다. 각 step 종료 시 단위 테스트 통과 + 컴파일 통과를 보장. e2e 는 Step 4 에서 일괄.

### Step 1 — `.preview.yml` 파서 + Traefik 부트스트랩 + MultiRepoCache (Agent 측 인프라)

- `preview_config.go` + 단위 (F-1 ~ F-11)
- `traefik.go` + 단위 (F-19 ~ F-23)
- `repocache.go` 리팩터(MultiRepoCache) + 단위 (F-12 ~ F-18)
- `labels.go` + 단위 (라벨 직렬화 정확성, NF-16)
- 이 단계에선 Runner 흐름은 손대지 않는다. **단, NF-4 (vet/lint clean) 를 깨지 않기 위해**, Runner 와 cmd/agent/main.go 가 기존 `RepoCache` 시그니처를 호출하던 자리는 임시 어댑터 함수(`legacyEnsure(ctx) error { return m.Ensure(ctx, cfg.RepoURL) }` 같은 1줄) 로 호환시켜 Step 1 종료 시 `go build`/`go vet` 통과를 보장한다. Runner 의 진짜 분기는 Step 2 에서.

### Step 2 — Runner 분기 + STATUS_UPDATE preview_url + agent CLI/wiring + worktree prune

- `runner.go` / `runner_compose.go` / `runner_dockerfile.go` 분할 (NF-3 정책)
- Handle/Teardown 변경 (F-24 ~ F-37)
- `MultiRepoCache.PruneStaleWorktrees` 추가 (F-18b/c) + cmd/agent/main.go 의 부팅 시 1회 호출 wiring
- `config.go` 의 플래그 변경 (F-50 ~ F-53)
- `cmd/agent/main.go` wiring 갱신: `EnsureNetwork` → `EnsureTraefik` → `PruneStaleWorktrees` → Runner 시작
- 단위: Runner fake + Cache fake + Docker fake.
- 이 단계 끝나면 Agent 단독으로 docker fake 환경에서 compose/Dockerfile 분기 e2e 시뮬 가능.

### Step 3 — Hub 측 변경 (webhook clone_url + dispatcher + 마이그레이션 + UI 정리)

- 마이그레이션 0004 + sqlc 재생성 (NF-14, NF-15)
- `webhook_handler.go` (F-41 ~ F-42)
- `dispatcher.go` (F-43 ~ F-45) — `RepoURLResolver` 의존 제거
- `cmd/hub/daemon.go` `resolveRepo` 제거
- `admin_ui.go` + `views/agent_detail.gohtml` 정리 (F-46 ~ F-49)
- `protocol/messages.go` `StatusUpdateData.PreviewURL` 추가
- Hub 단위 테스트 갱신.

### Step 4 — 통합 / e2e / 회귀

- F-54 (Phase 0~5 회귀)
- F-55 (multi-repo 동시 처리, fake docker)
- F-56 (Playwright e2e, docker-in-docker / 로컬 docker)
- 운영자 가이드(README/USAGE) 갱신: `.preview.yml` 예시, `--traefik-port`, dual-mode (Hub proxy vs Traefik 직접) 설명.

---

## 9. 리스크와 완화

| 리스크 | 완화 |
|---|---|
| **R1** Traefik Docker provider 가 라벨을 인지하기까지의 지연으로 STATUS_UPDATE running 시점에 라우팅이 아직 활성화 안 됨 | running 송신은 `docker compose up -d` 또는 `ContainerStart` 성공 직후. Traefik provider 의 polling 간격(default 15s) 안에서 라우팅 준비. 본 Phase 는 별도 readiness probe 도입하지 않고 Phase 7 로 이월. 운영자 가이드에 "running 직후 1~5 초 race 가능" 명시. |
| **R2** Compose 파일명 우선순위 — Agent detect 순서와 운영자 의도가 다를 수 있음 | 결정 9 의 우선순위 (`docker-compose.yml` → `docker-compose.yaml` → `compose.yml` → `compose.yaml`) 를 검출 함수에 명시 + 단위 테스트 (F-24~F-26b). 4 가지 모두 지원. |
| **R3** `.preview.yml` 의 service 이름이 compose 의 service 이름과 불일치 → override 의 라벨이 존재하지 않는 service 에 붙어 무효 | 결정 3 의 `unknown_compose_service` 검증을 구현 시 추가: compose 파일을 가볍게 파싱(`yaml.v3` Unmarshal `map[string]any`)해 `services` 키 집합과 `.preview.services` 의 키 교집합/대칭차 검사. 단위 테스트 추가(F-30 보강). |
| **R4** `gopkg.in/yaml.v3` 외부 의존 추가가 NF-1(Phase 5 의 0 외부 의존 원칙)을 깬다 | 결정 18 에서 정당화 — YAML 파싱 자작 비용/검증 부담 대비 이득 압도. 후속 PR 에서 다른 의존 추가 시 동일한 정당화 절차 강제. |
| **R5** SQLite 의 ADD COLUMN ... NOT NULL DEFAULT '' 가 일부 SQLite 버전에서 제약을 완화시킴 (constant default 만 허용) — 본 Phase 의 default '' 는 안전하지만, Postgres 와 SQLite 의 동작 차이가 후속 마이그레이션에서 누적될 수 있음 | 본 Phase 는 default 가 상수 빈 문자열 1 개뿐이라 SQLite 3.35+ 에서 안전. 마이그레이션 코드 리뷰 시 future migration 의 non-constant default 가 들어오는 경우 별도 정책 필요(이식성 원칙 §2). |
| **R6** Agent 가 SIGTERM 후 Traefik 컨테이너가 남아 있는데 다른 Agent 가 같은 호스트에서 같은 이름(`preview-traefik`)으로 부팅 시도 → 충돌 | 운영자가 한 호스트에 Agent 2 대를 동시에 띄우지 않는 것이 표준 가정(Hub 1 대당 Agent N 머신). 동일 호스트 다중 Agent 는 본 Phase 비범위. README 에 명시. |
| **R7** 운영자가 `.preview.yml` 의 path 를 `/api` 같은 짧은 prefix 로 두고, 다른 PR 의 path 와 호스트 헤더 충돌 (cross-PR collision) | path 는 `/{previewID}{cfg.path}` 로 항상 prefix 됨 (결정 11). previewID 가 UUID 라 충돌 무시 가능. 운영자가 직접 라우터 rule 을 짤 수 없게 막아 둔 것이 보호장치. |
| **R8** Hub `dispatcher.go` 의 `RepoURLResolver` 제거가 외부에서 의존하던 테스트/하네스를 깰 수 있다 | 변경 전 grep 으로 호출 지점 전부 식별 + Step 3 단위 테스트 갱신. 본 Phase 12 의 결정 6 에서 명시. |
| **R9** 마이그레이션 0004 가 production DB 에 적용될 때 ROLLBACK 시점이 길어 락이 걸림 | `ALTER TABLE ADD COLUMN` 은 SQLite/Postgres 모두 metadata-only operation 으로 빠르다(Postgres 11+). 본 Phase 의 row 수 규모(수백~수천) 에서는 ms 단위. 운영 가이드에 명시. |
| **R10** `.preview-override-{pid}.yml` 가 worktree 안에 생성되어 git status 가 dirty 상태가 됨 → 다음 Checkout 에 영향? | worktree 는 `--detach` 로 만들어지고, 다음 PR 의 Checkout 은 별 previewID 디렉토리. teardown 시 override 파일을 명시 삭제 + worktree 자체를 RemoveAll(repocache.Remove). 따라서 dirty 가 다음 build 에 누설되지 않는다. 단, build 도중 panic 으로 Teardown 미호출 시 다음 prefetch/status 사이클이 worktree 디렉토리 자체를 정리하지 못할 수 있다 — Phase 3 HELLO sync 가 orphan 컨테이너만 정리하므로 worktree orphan 은 후속 Phase 에서 보강 필요. |
| **R11** Multi-repo 환경에서 동일 PR 번호가 두 레포에서 동시에 열리면 previewID UUID 가 다르므로 Traefik 라우팅 충돌 없음. Phase 6 에서 ProxyMiddleware 가 제거되었으므로 `pr-{n}.<base-domain>` 충돌 자체가 소멸. | 해소됨(결정 12). |
| **R12** Rolling upgrade 비호환 — 구 Agent (Phase 5) + 신 Hub 조합 시 신 Hub 가 `AGENT_CONFIG{RunCommands:[]}` 를 송신하고 구 Agent 의 Runner 가 빈 RunCommands 로 build 시도 → 모든 build 가 fail | 본 Phase 는 **Hub + Agent 동시 업그레이드**를 권고. README/USAGE 에 "Phase 6 적용 시 모든 Agent 를 동일 버전으로 함께 배포" 명시. 분리 업그레이드를 허용하는 dual-mode 는 본 Phase 비범위. 사용자 가이드 1줄로 회피. |
| **R13** worktree orphan — Agent 가 build 도중 panic/SIGKILL 로 죽으면 worktree 디렉토리와 override 파일이 남아 다음 Agent 부팅 시 disk leak | 본 Phase 는 in-scope 임시 안전장치로 Agent 시작 시 1회: `{workDir}/repos/*/worktrees/preview-*` 디렉토리를 스캔해, 디렉토리명에서 추출한 previewID 가 Hub HELLO sync 의 `running_previews` 에 없으면 `git worktree remove --force` + `os.RemoveAll`. (Phase 3 의 HELLO sync 가 컨테이너 단위로 정리 보고를 받은 직후 실행. 본 Phase Step 2 에서 `MultiRepoCache.PruneStaleWorktrees(activeIDs []string)` 메서드로 추가.) compose project 단위 orphan(컨테이너) cleanup 은 Phase 7 후보 5 로 이월. |

---

## 10. 다음 Phase 연결점

- **Phase 7 후보 1** — Traefik HTTPS / ACME 통합. 본 Phase 의 entrypoints.web 1 개 → entrypoints.websecure 추가 + 인증서 provider.
- **Phase 7 후보 2** — `.preview.yml` 스키마 확장: env, secret, healthcheck 힌트, host-rule (e.g., `host: api.<base>`).
- **Phase 7 후보 3** — Hub 가 Traefik 의 dynamic config 를 파일 provider 로 직접 만드는 모드(Docker provider 의존도 낮춤). 또는 새 ProxyMiddleware 재구현(path-prefix 인지 포함).
- **Phase 7 후보 5** — Compose project orphan cleanup. Phase 3 HELLO sync 가 컨테이너 단위로만 정리하는 한계를 project-level 로 확장.
- **Phase 7 후보 6** — Traefik readiness 검증(R1 완화) — running STATUS_UPDATE 송신 전 라우팅 활성화 확인.
- **Phase 7 후보 7** — Multi-arch / Windows agent (셸 의존 `sh -c` 가 본 Phase 에서 제거되었으므로 Windows 가능성 확장).

---

## 11. 미해결 / 확인 사항 (Open Questions)

| ID | 질문 | 잠정 처리 |
|---|---|---|
| Q1 | `.preview.yml` 의 `services.<name>.path` 가 `/` 자체일 때(루트 진입점) 다른 service 와 prefix 중첩 가능 — 결정 3 의 `duplicate_path` 검사로 충분한가? | path 동일성만 검사하고 prefix 포함 관계는 검사하지 않는다(결정 3). Traefik 의 라우터 우선순위(긴 prefix 먼저)가 자연 처리. 단위 테스트 보강 항목으로 추적(향후 PR). |
| Q2 | ~~Hub `previews.public_url` 과 `preview_urls` 두 컬럼의 UI 우선순위~~ | **해결됨**: ProxyMiddleware 제거(결정 12)로 `public_url` 은 dead column. `preview_urls` 만 사용. 토글 불필요. |
| Q3 | `--max-jobs > 1` 환경에서 Traefik 의 `--providers.docker.refreshSeconds` 가 default(15s) 라 동시 다수 PR 의 라우팅 활성화에 race — 본 Phase 의 R1 완화로 충분? | 본 Phase 비범위. Phase 7 후보 6 으로 이월. |
| Q4 | `.preview.yml` 와 compose 의 service 이름이 다른 경우 운영자 의도(다른 alias 쓰고 싶다)를 본 Phase 에서 봉쇄하는 게 맞나? | 결정 3 에서 봉쇄 결정. 후속 PR 에서 alias 필드 제안 시 별 결정 항목으로 다룸. |
| Q5 | Agent 가 Traefik 부트스트랩에 실패(예: docker socket 권한 없음) 시 Agent 시작을 막아야 하나, 아니면 warn 로그 + build 가 들어올 때마다 fail 해야 하나? | 결정 4 의 "Agent 부팅이 Traefik 부팅을 선행" 가정상 fail-fast (Agent 종료, exit 2) 가 옳다. wiring 에서 fatal. 운영자 가이드에 docker socket 권한 명시. |

이후 새 Q 가 발견되면 본 섹션 아래에 (Q6, Q7 …) 으로 추가하고, plan-review 단계에서 다시 결의/비범위로 분리한다.

### Self-review 1차에서 처리된 항목 (REVIEW_REVISED)

planner 자가 점검(spec-review 스킬, plan-reviewer 에이전트 spawn 불가 환경 사정상 self-review)에서 발견·반영한 9건:

| 라운드 1 지적 | 처리 결과 |
|---|---|
| B-1: 결정 9 본문에 `.yaml` 변형 미지원 누락 | 결정 9 본문에 추가. |
| B-2: 결정 13 의 Holder deprecated 표시 위치 모호 | (a) `holder.go` / (b) `messages.go AgentConfigData` / (c) `AGENTS.md` 3곳 명시. |
| C-1: 결정 12-b 의 `agent_port` 의미 변경이 ProxyMiddleware 에 미치는 영향 누락 | 결정 12-b 에 두 운영 모드 + path-prefix 비호환 명시. §2-2 비범위에도 추가. |
| C-2: compose service 이름 검증 단계 §4-4 누락 | §4-4 (4a) 단계로 `validateComposeServices` 추가. F-30b 신설. |
| C-3: `EnsureTraefik` spec 비교 기준 모호 | 컨테이너 라벨 `preview.traefik.spec=<sha256>` 기반 비교로 구체화. |
| D-1: F-30 라벨 정확성 기준 모호 | §4-5 의 5~7개 라벨 키 모두 매칭 으로 구체화. |
| D-2: F-56 path-prefix 도달 검증 방법 부정확 | fixture 컨테이너가 `X-Preview-Service` 헤더 + 본문 echo 하도록 명시. |
| D-3: NF-3 분할 정책 누락 | `runner_compose.go` / `runner_dockerfile.go` / `labels.go` 분할 정책 명시. |
| F-1 + 결정 13 rolling upgrade: §9 R10 의 worktree orphan 후속 Phase 이월이 안전망 부재 + 구 Agent + 신 Hub 호환성 누락 | §9 R12 (rolling upgrade) + R13 (worktree orphan in-scope `PruneStaleWorktrees`) 추가. F-18b/c 신설. §4-2 인터페이스 + §8 Step 2 갱신. §8 Step 1 의 NF-4 호환 임시 어댑터 정책 명시. |

---

(끝)
