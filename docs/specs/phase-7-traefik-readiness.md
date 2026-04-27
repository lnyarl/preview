# Phase 7 — Traefik Readiness Probe (`STATUS_UPDATE running` 송신 전 라우터 활성화 확인)

Status: **REVIEW_APPROVED** (plan-reviewer 3차 승인 — 1차 M1~M4 + S1~S5 + 경미 4건, 2차 필수 3건 + 권장 5건 모두 반영)
Author: planner
Date: 2026-04-26

---

## 1. Phase 개요

### 1-1. 배경

Phase 6 에서 multi-service path-prefix 라우팅을 Traefik Docker provider 로 위임했다. 현재 구현(`internal/agent/runner.go`)의 송신 시점은 다음과 같다.

- **compose 모드** (`handleCompose`): `docker compose up -d` 가 0 종료되자마자 `STATUS_UPDATE running` + `preview_urls` 송신.
- **Dockerfile 모드** (`handleDockerfile`): `ContainerStart` 가 성공하자마자 동일 송신.

문제는 두 시점 모두 **컨테이너 기동의 종료 시점**이지 **Traefik 라우터의 활성화 시점**이 아니라는 것이다. Traefik v3 의 Docker provider 는 Docker events 기반이라 일반적으로 빠르지만, 다음 경합이 잔존한다.

1. `docker compose up -d` 는 컨테이너가 `created → running` 으로 전이하기 직전(또는 직후) 에 반환할 수 있다. Traefik 이 같은 Docker 이벤트를 받기까지의 IPC 지연 + provider 의 내부 디바운스(`providers.providersThrottleDuration` default 2s) 가 최소 수백 ms ~ 수 초 걸린다.
2. compose 모드는 1 개 PR 안에 여러 서비스를 띄우므로, 마지막 서비스의 라벨이 Traefik 에 반영되기 전까지 일부 라우터만 enabled, 일부는 미존재 상태가 된다.
3. `--max-jobs > 1` 환경에서는 동시 다수 PR 의 컨테이너 생성이 폭주해 Traefik provider 의 throttle 윈도우 안에서 라벨 반영이 직렬화된다.

결과적으로 Hub 가 `STATUS_UPDATE running` 을 받아 대시보드에 `preview_urls` 링크를 노출하면, 운영자가 즉시 클릭해도 Traefik 이 `404 page not found` (no matching router) 를 반환하는 경우가 관찰된다. Phase 6 의 §9 R1 으로 문서화되었고, §10 "Phase 7 후보 6" 으로 이월된 항목이다.

### 1-2. 목표

다음 4 축을 동시에 도입한다.

1. **Traefik API 활성화** — `EnsureTraefik` 의 cmd 에 `--api.insecure=true` 를 (조건부) 추가하고, 컨테이너 내부 8080 → 호스트 `TraefikAPIPort` (기본 9080) 로 바인딩한다. preview-net 격리 + 호스트 측은 운영자가 firewall/loopback 으로 제한.
2. **Readiness 폴링 함수** — `internal/agent/traefik_ready.go` 에 `WaitTraefikRouters(ctx, apiBase, routerNames, timeout)` 신규. Traefik REST API `GET /api/http/routers/{name}@docker` 를 폴링해 모든 라우터가 `status:"enabled"` 가 될 때까지 대기. 타임아웃 시 WARN 로그 + best-effort 진행.
3. **Runner 통합** — `handleCompose` / `handleDockerfile` 양쪽이 `ServiceLabels` 로 만든 라우터 이름 집합을 추출해 `WaitTraefikRouters` 호출. 결과와 무관하게 `STATUS_UPDATE running` 을 송신하지만, 폴링이 성공했을 때 기록되는 `agent_traefik_ready` 로그가 운영자 디버깅 단서.
4. **`CreateOptions.PortBindings` 마이그레이션** — 현재 단일 `HostPort`/`ExposedPort` 로는 Traefik 의 80 + 8080 두 포트를 바인딩할 수 없다. `CreateOptions` 의 포트 필드를 `PortBindings []PortBinding` 슬라이스로 일원화하고, 기존 호출 지점(traefik.go, runner.go, cmd/agent/docker_sdk.go) 을 모두 마이그레이션한다.

### 1-3. 비목표 (이 Phase 가 해결하지 않는 것)

- **Traefik dashboard / 인증** — `--api.dashboard=false` 를 유지. `--api.insecure=true` 는 raw API endpoint 만 활성화한다. 운영자가 dashboard 를 원하면 후속 Phase.
- **HTTPS / TLS** — Phase 6 비범위 그대로 이월. entrypoints.web 1 개만 사용.
- **앱 자체의 ready 검증** — Traefik 라우터가 enabled 라는 것은 "Traefik 이 라우팅을 알고 있다"는 의미이지 "백엔드 컨테이너가 200 을 응답한다"는 의미는 아니다. 앱 healthcheck 는 후속 Phase 의 `.preview.yml.healthcheck` 항목으로 이월.
- **Traefik 미들웨어 자체의 ready 검증** — `.preview.yml.services.<n>.strip:true` 일 때 Agent 는 stripprefix 미들웨어 라벨을 같이 단다. 이 경우 Traefik 가 라우터 status 를 `warning` 으로 일시 보고할 수 있는데(미들웨어 binding 이 라우터보다 늦게 발견되는 transient race), 본 Phase 는 라우터 status 만 확인하고 미들웨어의 별도 검증은 하지 않는다(§4-3 응답 표 참조). 미들웨어 단위 readiness 는 후속 Phase.
- **Traefik file provider 모드** — 본 Phase 는 Phase 6 의 Docker provider 를 그대로 사용. file provider 도입 시 readiness 의미가 바뀌므로 별도 Phase.
- **타임아웃 후 자동 재시도/롤백** — 타임아웃은 운영자에게 경고만 보낸다. `STATUS_UPDATE failed` 로 자동 전환하지 않는다(결정 4 의 best-effort 정책 참조).
- **API 인증/RBAC** — `--api.insecure=true` 는 API 엔드포인트를 인증 없이 노출한다. 본 Phase 는 호스트 측 firewall/loopback bind 로 제한하는 운영 가이드만 추가. JWT 등 인증 후속 Phase.
- **Traefik 외 reverse proxy 의 readiness 추상화** — `ReadinessChecker` 인터페이스 등의 추상화는 도입하지 않는다. Traefik 단일 구현. Phase 6 의 "사이드카 1 개로 한정" 원칙 유지.

### 1-4. 성공 기준 (요약)

- `EnsureTraefik` 가 `TraefikSpec.APIHostPort > 0` 일 때 `--api.insecure=true` 와 8080 포트 바인딩을 포함해 컨테이너를 띄운다. `APIHostPort == 0` 이면 Phase 6 와 동일한 동작.
- `WaitTraefikRouters(ctx, "http://127.0.0.1:9080", ["abc-app","abc-api"], 30s)` 가 모든 라우터가 `status:"enabled"` 일 때 nil, 타임아웃 시 `ErrTraefikRoutersTimeout` 반환.
- `Runner.handleCompose` / `handleDockerfile` 가 컨테이너 기동 직후 `WaitTraefikRouters` 호출, 결과와 무관하게 `STATUS_UPDATE running` 을 송신.
- 정상 케이스: 폴링이 약 100 ms ~ 2 s 사이에 종료, `agent_traefik_ready` 로그.
- 타임아웃 케이스: `agent_traefik_ready_timeout` WARN 로그 + 여전히 `STATUS_UPDATE running` 송신.
- `CreateOptions.PortBindings` 마이그레이션 후 Phase 6 까지의 단위 테스트가 회귀 0 으로 통과.
- `--traefik-api-port`/`AGENT_TRAEFIK_API_PORT` (기본 9080) 와 `--router-ready-timeout`/`AGENT_ROUTER_READY_TIMEOUT` (기본 30s) 두 플래그가 Config·ParseConfig·main.go 에 일관 wire.

---

## 2. In / Out of Scope

### 2-1. In Scope

- **Agent 측**
  - `internal/agent/docker.go` — `PortBinding` 타입 신규 + `CreateOptions.PortBindings []PortBinding` 도입, 기존 `HostPort`/`ExposedPort` 두 필드 제거.
  - `internal/agent/traefik.go` — `TraefikSpec.APIHostPort int` 추가. `specHash` 의 입력에 `APIHostPort` 포함. `EnsureTraefik` 의 cmd 와 PortBindings 갱신 (조건부 `--api.insecure=true` + 8080 바인딩).
  - `internal/agent/traefik_ready.go` (신규) — `WaitTraefikRouters` + 내부 HTTP 폴링 + sentinel 에러.
  - `internal/agent/runner.go` — `traefikAPIPort int` + `routerReadyTimeout time.Duration` 필드 추가, `SetTraefikAPIPort` / `SetRouterReadyTimeout` setter 추가, `waitRouters(pid, cfg) error` 헬퍼, `handleCompose` / `handleDockerfile` 가 jobs.Store + STATUS_UPDATE 직전에 `waitRouters` 호출.
  - `internal/agent/labels.go` — `RouterNames(previewID, cfg PreviewConfig) []string` 신규(라우터 이름 결정 1 곳으로 중앙화).
  - `internal/agent/config.go` — `Config.TraefikAPIPort int` (default 9080) + `Config.RouterReadyTimeout time.Duration` (default 30s) 추가, `--traefik-api-port` / `--router-ready-timeout` 플래그 + env 매핑.
  - `cmd/agent/docker_sdk.go` — `ContainerCreate` 가 `opts.PortBindings` 를 순회해 `nat.PortMap` / `ExposedPorts` 구성하도록 수정. `--api.insecure=true` 활성 시 8080 포트가 호스트로 바인딩.
  - `cmd/agent/main.go` — `runner.SetTraefikAPIPort(cfg.TraefikAPIPort)` + `runner.SetRouterReadyTimeout(cfg.RouterReadyTimeout)` wire. `EnsureTraefik` 에 `APIHostPort: cfg.TraefikAPIPort` 인자 추가.
- **테스트**
  - `WaitTraefikRouters` 단위 테스트(httptest server fake): 1 회 polled 성공, 2 회 후 성공, 타임아웃, 컨텍스트 취소, 부분 성공(라우터 1개만 enabled) 미허용.
  - `EnsureTraefik` 단위 테스트 보강: APIHostPort > 0 일 때 PortBindings 에 8080 + cmd 에 `--api.insecure=true` 포함 / 0 일 때 미포함. specHash 가 APIHostPort 변경에 반응.
  - `Runner.handleCompose` / `handleDockerfile` 단위 테스트 보강: WaitTraefikRouters 가 호출되는지(주입된 fake), 타임아웃 시 STATUS_UPDATE running 이 여전히 송신되는지.
  - `PortBinding` / `CreateOptions` 마이그레이션 회귀: Phase 6 의 traefik_test, runner_test, orphan_restore_test 에서 `HostPort`/`ExposedPort` 사용처가 모두 `PortBindings` 로 갱신되어도 통과.
  - `RouterNames` 단위 테스트: services 3 개에서 `[pid-a, pid-b, pid-c]` 순서가 알파벳 오름차순.
  - `Config`/`ParseConfig`: 새 두 플래그의 기본값/유효 범위/env 우선순위.
- **문서**
  - 운영 가이드: `--api.insecure` 의 보안 의미 + 호스트 firewall/loopback 권장 1 단락.

### 2-2. Out of Scope

- **`STATUS_UPDATE` 와이어 메시지 스키마 변경** — `running` 송신 자체는 그대로. 추가 필드 없음.
- **앱 healthcheck (`/healthz`)** — 후속 Phase.
- **`--api.dashboard=true`** — 본 Phase 는 raw endpoint 만. dashboard 활성화는 후속 Phase 의 인증과 함께 다룬다.
- **Traefik 의 `entrypoints.traefik.address`** — `:8080` 으로 명시 고정 (S4 — 결정 12). Phase 6 의 `entrypoints.web.address=:80` 와 분리되어 충돌 없음. 운영자가 다른 포트로 변경하는 옵션은 본 Phase 비범위.
- **Per-PR ready 정책** — 모든 PR 에 동일한 `RouterReadyTimeout` 적용. `.preview.yml` 에서 PR 별 오버라이드 후속 Phase.
- **`waitRouters` 실패 시 `STATUS_UPDATE failed` 자동 전환** — 결정 4 에서 best-effort 결정. failed 전환은 운영자 정책 차이가 있어 단일화 불가.
- **HostPort 미바인딩 컨테이너의 expose-only 의미 검증** — `PortBindings` 의 `HostPort == 0` 분기는 라벨 라우팅의 `ExposedPorts` 만 활용(외부 노출 X). 본 Phase 의 두 가지 sample fixture: (a) Traefik 컨테이너의 `{ContainerPort: 80, HostPort: spec.HostPort}` + `{ContainerPort: 8080, HostPort: spec.APIHostPort}` — **양쪽 다 호스트 바인딩** 케이스, (b) Dockerfile 모드의 preview 컨테이너 `{ContainerPort: svc.Port, HostPort: 0}` — **expose-only(외부 미노출)** 케이스. 두 케이스 모두 SDK 어댑터의 PortBindings 처리 분기로 커버.

### 2-3. Deferred (다음 Phase 후보)

- 앱 healthcheck 표준 (`/healthz` + retry policy).
- Traefik dashboard 활성화 + 인증 (API token / mTLS).
- Traefik file provider 도입 — Hub 가 dynamic config 를 직접 만든다.
- Per-PR readiness override (`.preview.yml.readiness.timeout`).
- Phase 6 R10 의 worktree orphan 후속 보강 — readiness 와 무관하지만 R 모음에 함께 추적.
- Multi-arch / Windows agent — Phase 6 후보 7 그대로 이월.

---

## 3. 설계 결정 (Design Decisions)

> 각 결정마다 (선택, 근거, 버려진 대안, 되돌릴 때 비용) 4 요소.

### 결정 1 — Traefik API 접근 방법: `--api.insecure=true` + 호스트 포트 바인딩 + Agent in-process HTTP 폴링

**선택**: Traefik cmd 에 `--api.insecure=true` 를 추가해 컨테이너 내부 8080 에 raw API 활성화. `APIHostPort` 호스트 포트로 바인딩(default 9080, 운영자가 `--traefik-api-port` 로 변경 가능). Agent 프로세스가 `http://127.0.0.1:{APIHostPort}/api/http/routers/{name}@docker` 를 `net/http` 로 폴링한다.

**근거**:
- Traefik 의 표준 readiness 신호 — 라우터 status 가 직접 노출됨. polling 간격을 Agent 측이 통제(100 ms 시작, 지수 backoff 250 ms 최대).
- API 엔드포인트가 컨테이너 내부 별도 entrypoint(`entrypoints.traefik.address=:8080`)에 떠있으므로 `entrypoints.web.address=:80` 의 운영 트래픽과 격리.
- Agent 가 `localhost` 로 접근 → 별도 네트워크 hop 없음.

**준비 판정 규칙**: 응답의 `status` 필드가 정확히 문자열 `"enabled"` 일 때만 ready. `"disabled"`, `"warning"`, 빈 문자열, 누락 등 **그 외 모든 값은 미준비로 간주해 polling 계속**한다 (§4-3 응답 표). 특히 `strip:true` service 의 stripprefix 미들웨어 binding race 로 인한 transient `warning` 도 미준비로 본다(§1-3 비목표 참조 — 미들웨어 자체 ready 는 본 Phase 비범위, 단 라우터 status 가 enabled 가 될 때까지 대기하면 결과적으로 미들웨어 binding 도 안정화됨).

**대안 기각**:
- **Docker exec 로 컨테이너 내부 curl/wget** — Traefik 공식 이미지(`traefik:v3.1`)가 scratch + ca-certs 만 포함, curl/wget 부재. 별도 sidecar 컨테이너를 띄우면 의존이 폭증.
- **단순 HTTP 탐침 `GET http://{advHost}:{traefikPort}/{previewID}/`** — Traefik 이 라우터 미존재 시 반환하는 `404` 와 앱이 정상 라우팅된 후 `404` 를 반환하는 경우(예: Next.js 의 미정의 path) 가 구분 불가. false positive/negative 양쪽 발생.
- **고정 sleep(예: 2 s)** — Traefik provider throttle 가 환경에 따라 100 ms ~ 5 s 변동. sleep 이 짧으면 race 잔존, 길면 모든 PR 에 불필요한 대기. 비결정적.
- **Docker events stream 구독** — `containerd` events 만 보고 Traefik 의 내부 상태는 알 수 없다. Traefik 의 디바운스 윈도우를 측정 못함.
- **`status == "warning"` 도 ready 로 간주** — warning 의미가 Traefik 버전·상황에 따라 다르고(미들웨어 미발견, TLS cert 만료 임박 등) 운영자 디버깅 단서가 흐려짐. enabled 정확 일치만 ready 로 단순화.

**되돌릴 때 비용**: `traefik_ready.go` 1 파일 제거 + `Runner.waitRouters` 헬퍼 1 곳 제거 + `EnsureTraefik` 의 cmd/PortBindings 분기 1 곳 되돌림. 작음.

### 결정 2 — `CreateOptions` 포트 표현을 `PortBindings []PortBinding` 슬라이스로 일원화

**선택**: `CreateOptions` 의 `HostPort`/`ExposedPort` 두 필드를 제거하고 다음 슬라이스 1 개로 대체.

```go
// PortBinding 은 컨테이너 포트 → 호스트 포트 매핑 1 건.
type PortBinding struct {
    ContainerPort int    // 컨테이너 내부 포트 (필수, 1..65535)
    HostPort      int    // 호스트 바인딩 포트. 0 이면 expose only(외부 미노출).
    Protocol      string // "tcp" 기본. ("udp" 도 SDK 어댑터에서 허용하지만 Phase 7 사용처는 모두 tcp)
}
```

`PortBindings` 가 빈 슬라이스이면 SDK 어댑터는 `nat.PortMap` / `ExposedPorts` 모두 비운다(Dockerfile 모드의 path-prefix 라우팅처럼 외부 포트 미사용 케이스). 길이 ≥ 1 이면 각 항목을 nat 구조에 매핑.

**근거**:
- Traefik 의 80(웹) + 8080(API) 두 포트 바인딩이 본 Phase 의 직접 요구.
- 단일 필드를 두고 "두 번째 포트" 만 추가하는 식은 추상화 누설(`ExtraHostPort` 같은 이름이 금세 불어남).
- 슬라이스 모델은 향후 DB(5432), Redis(6379) 등 multi-port 컨테이너에도 그대로 적용.

**대안 기각**:
- **`ExtraHostPort int` 같은 한 필드 추가** — Traefik 전용 명명. 일반화 어려움.
- **두 옵션 병행 (`HostPort`/`ExposedPort` + `Extra`)** — 내부 분기 2 배. 호출자가 어느 쪽을 쓰는지 헷갈림.
- **마이그레이션 미실시, Phase 7 만 `PortBindings` 도입** — Phase 6 의 단일 포트 호출자(`runner.handleDockerfile`, `traefik.go`)가 신구 두 모델을 동시에 보유. 코드 일관성 깨짐.

**되돌릴 때 비용**: 영향 파일 5 개(`docker.go`, `traefik.go`, `runner.go`, `cmd/agent/docker_sdk.go`, fake docker mock 들). 마이그레이션이 mechanical(grep + 변환) 이라 작업량은 명확하나 회귀 위험은 모든 호출 지점 동시 변경. PR 1 개로 묶어 처리한다(§8 Step 1).

### 결정 3 — `TraefikAPIPort` 기본값 = 9080, `0` 으로 명시 시 비활성

**선택**: `Config.TraefikAPIPort` default = 9080. 운영자가 `--traefik-api-port=0` 또는 `AGENT_TRAEFIK_API_PORT=0` 으로 명시하면 `--api.insecure=true` 미포함 + 8080 바인딩 미생성 + `WaitTraefikRouters` 호출 자체 skip(Phase 6 동작과 동일). `1..65535` 범위 외 값은 `ParseConfig` 에서 에러.

**근거**:
- 본 Phase 의 목적("running 직전에 라우터 활성화 확인")을 default-on 해야 운영자가 즉시 혜택을 본다.
- 9080 은 IANA 등록 서비스 없이 비교적 비어있다(8080 은 Traefik web 트래픽 호스트 포트와 충돌 가능, 9090 은 Prometheus 관용).
- `0` sentinel 로 비활성을 표현 → 추가 boolean 플래그(`--enable-readiness`) 불필요.

**대안 기각**:
- **default 0 (opt-in)** — 신규 운영자가 Phase 6 의 race 를 그대로 겪는다. 본 Phase 의 의의 약화.
- **default 8080** — Traefik web 호스트 포트(`--traefik-port`) 의 default 와 충돌. 두 포트가 같으면 docker 가 bind 실패.
- **default = 자동 할당(`net.Listen(":0")`)** — Agent 재시작마다 specHash 변경 → Traefik 재생성이 항상 발생. specHash 안정성 깨짐.

**되돌릴 때 비용**: `config.go` 의 default 상수 1 곳. 작음.

### 결정 4 — `WaitTraefikRouters` 타임아웃 후 동작은 best-effort: WARN 로그 + 여전히 STATUS_UPDATE running 송신

**선택**: `WaitTraefikRouters` 가 `ErrTraefikRoutersTimeout` 또는 `ctx.Err()` 을 반환하면 Runner 는 `agent_traefik_ready_timeout` WARN 로그를 남기고 **그대로 STATUS_UPDATE running 흐름을 진행**한다. `STATUS_UPDATE failed` 로 전환하지 않는다.

**근거**:
- 라우터가 결국 enabled 될 가능성이 높다(컨테이너 자체는 떠있음). running 을 보내지 않으면 Hub 대시보드가 영원히 building 으로 남고 운영자 혼동.
- 타임아웃 = "운영자가 정한 한계를 넘었다"는 신호이지 "라우터 활성화가 영원히 실패했다"는 신호 아님. failed 로 전환하면 Teardown 트리거되어 컨테이너가 즉시 죽음 — 회복 불가.
- 운영자 디버깅 단서는 WARN 로그 + 후속 access log 의 404 발생 패턴.

**대안 기각**:
- **타임아웃 시 STATUS_UPDATE failed** — 위 근거의 회복 불가 문제.
- **타임아웃 시 무한 대기** — JOB_ASSIGN handler 가 영원히 점유, slot 1 개 영구 lock. `--max-jobs` 의 의미 붕괴.
- **타임아웃 시 STATUS_UPDATE running + warning_message 필드** — 와이어 스키마 변경(StatusUpdateData 확장) 이 본 Phase 비범위. 후속 Phase 에서 같이.

**되돌릴 때 비용**: `runner.go.waitRouters` 의 에러 처리 분기 1 곳. 작음. 단, 정책을 바꾸려면 §1-2 의 목적 자체를 재정의해야 함(주의).

### 결정 5 — `specHash` 입력에 `APIHostPort` 포함 → 변경 시 Traefik 컨테이너 재생성

**선택**: `specHash(s TraefikSpec)` 의 입력 문자열을 `Image|HostPort|Network|APIHostPort` (4 필드) 로 확장. `--traefik-api-port` 변경 시 Agent 재시작에서 Traefik 컨테이너가 stop+rm+재생성.

**근거**:
- API 포트 바인딩은 컨테이너 호스트 포트 매핑 변경 → Docker 가 재생성을 요구. specHash 가 이를 반영하지 않으면 EnsureTraefik 가 no-op 으로 판단해 컨테이너에 8080 바인딩 누락.
- 의도된 동작이며 운영자 가이드에 "API 포트 변경 시 Traefik 재시작 발생" 명시.
- Phase 6 의 `Image|HostPort|Network` 와 형식 통일.

**대안 기각**:
- **specHash 무변경, EnsureTraefik 가 별도로 PortBindings 비교** — 비교 로직이 specHash 의 단순함을 깸. 두 비교 결과가 어긋나면 운영 디버깅 어려움.
- **API 포트 변경 시 Agent 재시작 강제 X, 다음 Traefik 재생성 사이클까지 적용 지연** — 운영자가 변경 직후 새 포트로 폴링 시도 → 실패 → 디버깅 길어짐.

**되돌릴 때 비용**: `specHash` 의 입력 문자열 1 줄. 작음.

### 결정 6 — Polling 간격은 100ms → 250ms 지수 backoff, max 250ms

**선택**: `WaitTraefikRouters` 내부 polling 간격은 100 ms 에서 시작해 매 시도마다 1.5 배 증가, 250 ms 에서 cap. `time.NewTimer` 가 아닌 `time.NewTicker` 의 reset 패턴 또는 `time.After` 누적.

**근거**:
- 정상 케이스의 Traefik 디바운스 윈도우(평균 100~500 ms) 를 첫 1~3 회 polling 으로 커버.
- max 250 ms cap 으로 30 s 타임아웃 안에서 ~120 회 시도 — Traefik API 에 과한 부하 X.
- 첫 시도가 즉시 성공할 수도 있으므로 first-shot polling 을 sleep 없이 1 회 시도 후 backoff 시작.

**대안 기각**:
- **고정 200 ms** — 정상 케이스에서 평균 100 ms 더 대기. 누적되면 사용자 체감 느림.
- **선형 backoff (100, 200, 300, ...)** — 30 s 안에서 60 회 미만 — Traefik API 호출 자체는 여유, 대신 timeout 검출 정확도 떨어짐.
- **Listen long-polling / SSE** — Traefik API 가 long-polling/SSE 지원 안 함(REST 만).

**되돌릴 때 비용**: 폴링 함수 내부 상수 2~3 줄. 작음.

### 결정 7 — Traefik API URL 은 `127.0.0.1:{APIHostPort}` (loopback 만), 운영자에게 firewall 권장

**선택**: `WaitTraefikRouters` 의 base URL 은 `http://127.0.0.1:{APIHostPort}`. SDK 어댑터의 PortBinding 은 `HostIP: "127.0.0.1"` (Phase 6 의 `0.0.0.0` 와 다름). 운영자가 외부 접근이 필요하면 직접 `--traefik-api-port` 와 firewall 정책으로 노출.

**근거**:
- `--api.insecure=true` 는 인증 없는 raw endpoint. 외부 노출 시 라우터/서비스 정보 모두 공개 → 보안 위험.
- Agent 가 localhost 폴링이면 충분.
- Phase 6 의 web 트래픽(80→TraefikPort) 은 운영자가 외부 노출하길 원하므로 `0.0.0.0` 유지.

**대안 기각**:
- **0.0.0.0 바인딩 후 운영자 가이드만 추가** — default-secure 원칙에 어긋남. 운영자가 가이드를 안 읽으면 사고.
- **Unix socket** — Traefik 이미지가 socket binding 옵션 없음(scratch + 단일 바이너리 entrypoint).
- **Docker network 내부 endpoint** — Agent 가 컨테이너 안에서 도는 경우엔 가능하지만, Phase 6 의 Agent 는 호스트 프로세스. 추가 가정 도입 부담.

**되돌릴 때 비용**: SDK 어댑터의 PortBinding HostIP 분기 1 곳 + `traefik_ready.go` 의 base URL hardcode 1 곳. 작음.

### 결정 8 — 라우터 이름 결정 로직은 `RouterNames(previewID, cfg, buildMode)` 1 곳에 중앙화 (buildMode 분기 포함)

**선택**: `internal/agent/labels.go` 에 다음 시그니처를 추가한다.

```go
// RouterNames 는 .preview.yml 의 services 와 previewID 로부터
// Traefik 라우터 이름 슬라이스를 알파벳 오름차순으로 반환한다.
// 라우터 이름은 ServiceLabels 내부의 "{previewID}-{svcName}" 규칙과 동일해야 한다.
//
//   - buildMode == buildModeCompose    → services 전체를 알파벳 오름차순으로 반환.
//   - buildMode == buildModeDockerfile → cfg.FirstService() 1 개만 반환.
//     (Dockerfile 모드는 단일 컨테이너만 띄우고 첫 service 의 라벨만 부착하므로,
//      다른 service 이름의 라우터를 polling 하면 영원히 미존재 — Phase 6 결정 10.)
//   - 그 외(unknown) → 빈 슬라이스 반환 (silent). 로깅 책임은 호출자(Runner.waitRouters).
//     본 함수는 logger 의존 0 → 순수 함수, 단위 테스트 단순.
func RouterNames(previewID string, cfg PreviewConfig, buildMode string) []string
```

`Runner.waitRouters` 와 `ServiceLabels` 가 모두 이 함수를 사용해 "라우터 이름은 한 곳에서만 결정" 원칙을 강제한다. 두 buildMode 가 동일 함수를 통과하므로 분기 의도가 한 곳에 명시된다.

**근거**:
- 현재 `labels.go` 의 `routerName := previewID + "-" + svcName` 가 두 곳(`ServiceLabels` 내부 + `WaitTraefikRouters` 호출자) 에서 중복 표현되면, 향후 라우터 명명 규칙 변경 시 한쪽만 갱신되어 silent bug.
- 정렬을 함수가 보장 → 폴링 로그/에러 메시지가 결정적(테스트하기 쉬움).
- Phase 6 의 `handleDockerfile` 는 `cfg.FirstService()` 1 개에만 `ServiceLabels` 를 부착(§runner.go handleDockerfile). 따라서 실제 Traefik 에 등록되는 라우터는 1 개. `RouterNames` 가 services 전체를 반환하면 N-1 개의 미존재 라우터에 대해 영원히 404 → `WaitTraefikRouters` 가 항상 timeout. buildMode 인자가 분기를 강제한다.

**대안 기각**:
- **호출자가 직접 `for name := range cfg.Services { append(...) }`** — 정렬 미보장 + 명명 규칙 중복.
- **ServiceLabels 가 `(map[string]string, []string)` 두 값 반환** — 시그니처 비대화. 호출자가 라우터 이름만 필요할 때도 라벨 맵을 받음.
- **함수 2 개로 분리 (`RouterNamesForCompose` / `RouterNamesForDockerfile`)** — 호출자가 분기 책임을 짐. buildMode 가 이미 Runner 가 들고 있는 정보 → 1 함수가 인자로 받는 게 호출자에게 더 단순.
- **`waitRouters(ctx, pid, cfg, buildMode)` 시그니처 분기, `RouterNames` 자체는 services 전체 반환** — `RouterNames` 의 의미("Traefik 에 실제 등록되는 라우터 이름")가 호출자에 따라 달라짐 → 함수 명세 깨짐.

**되돌릴 때 비용**: `labels.go` 의 함수 1 개 + 호출자 2 곳(`Runner.waitRouters`). 작음.

### 결정 9 — 단위 테스트는 `httptest.NewServer` 로 Traefik API stub, 실제 Traefik 컨테이너 미요구

**선택**: `WaitTraefikRouters` 단위 테스트는 `net/http/httptest` 의 server 로 Traefik API 응답 형태(`{"name":"...","status":"enabled"}`) 를 모킹. 실 Traefik 컨테이너 요구 X.

**근거**:
- Phase 6 NF-11 (runner_test 가 docker daemon 미요구) 의 정책 연장.
- Traefik API 응답은 단순 JSON, stub 비용 낮음.
- 실 Traefik 통합 테스트는 Phase 6 e2e harness 의 후속 보강(별 build tag).

**대안 기각**:
- **testcontainers-go 로 실 Traefik** — 외부 의존(testcontainers-go SDK + docker daemon) + CI 파이프라인 무거움. NF-1 의 외부 의존 최소화 원칙에 어긋남.
- **embedded Traefik 라이브러리** — Traefik 은 라이브러리로 import 되도록 설계되지 않음. 빌드 폭증.

**되돌릴 때 비용**: 테스트 파일 1 개. 작음.

### 결정 10 — `WaitTraefikRouters` 의 시그니처는 `func(ctx, baseURL, names []string, timeout time.Duration) error`

**선택**:

```go
// 패키지 변수: 테스트가 폴링 간격을 줄일 수 있도록 노출 (테스트 전용 setter 미사용,
// 일반 호출자는 기본값 사용).
var (
    traefikPollInitial = 100 * time.Millisecond
    traefikPollMax     = 250 * time.Millisecond
)

// ErrTraefikRoutersTimeout 는 timeout 안에서 모든 라우터가 enabled 가 되지 않았을 때 반환.
var ErrTraefikRoutersTimeout = errors.New("traefik routers not ready within timeout")

// WaitTraefikRouters 는 Traefik API 의 /api/http/routers/{name}@docker 를 폴링해
// names 의 모든 라우터가 status:"enabled" 가 될 때까지 대기한다.
//   - baseURL 예: "http://127.0.0.1:9080"
//   - names 가 비어있으면 즉시 nil (no-op)
//   - timeout <= 0 또는 baseURL == "" 이면 즉시 nil (probe disabled)
//   - ctx 취소 시 ctx.Err() 반환.
//   - 타임아웃 시 ErrTraefikRoutersTimeout 반환.
//   - 에러는 진단용 — 호출자가 best-effort 로 무시해도 무방.
func WaitTraefikRouters(ctx context.Context, baseURL string, names []string, timeout time.Duration) error
```

**근거**:
- 시그니처가 외부 의존 0 (도메인 타입 직접 사용) — 테스트 fake 주입 불필요, baseURL 만 바꾸면 됨.
- "probe disabled" 케이스(baseURL 빈 문자열 / timeout 0)를 함수 내부에서 처리해 호출자 분기 단순화.
- HTTP client 는 함수 내부에서 `&http.Client{Timeout: 1s}` 새로 생성 — 외부 client 주입 의존 없음(라우터 폴링은 호스트 localhost, 일반 client 면 충분).

**대안 기각**:
- **인터페이스 도입 (`type RoutersClient interface`)** — Traefik 단일 구현. 추상화 과잉.
- **시그니처에 `*http.Client` 주입** — 호출 지점 1 곳(Runner) 에서 생성/관리 부담 vs 함수 내부 hardcode 단순함. 후자 채택.
- **`context.WithTimeout(ctx, timeout)` 을 호출자가 만들고 함수는 ctx 만 받음** — 호출자가 두 종류 ctx 처리 부담. 함수가 timeout 인자를 같이 받는 게 호출자에게 더 단순.

**되돌릴 때 비용**: 시그니처 변경은 1 호출자(Runner) 만 영향. 작음.

### 결정 11 — `runner.waitRouters` 는 항상 호출하지만 `traefikAPIPort == 0` 또는 `routerReadyTimeout == 0` 이면 즉시 nil. polling 횟수·elapsed 도 함께 로깅 (Q4 채택)

**선택**: `Runner.waitRouters(ctx, pid, cfg, buildMode)` 헬퍼는 다음과 같이 동작.

```go
func (r *Runner) waitRouters(ctx context.Context, pid string, cfg PreviewConfig, buildMode string) {
    if r.traefikAPIPort == 0 || r.routerReadyTimeout <= 0 {
        return // probe disabled
    }
    // (F-35d) unknown buildMode 방어 — RouterNames 가 빈 슬라이스 반환하므로 silent skip 막음.
    if buildMode != buildModeCompose && buildMode != buildModeDockerfile {
        r.logger.Warn("agent_traefik_ready_unknown_buildmode",
            "preview_id", pid, "build_mode", buildMode)
        return
    }
    base := fmt.Sprintf("http://127.0.0.1:%d", r.traefikAPIPort)
    names := RouterNames(pid, cfg, buildMode) // M2: buildMode 분기.
    if len(names) == 0 {
        return // 라우터 0 개 — 검사 대상 없음 (방어적).
    }
    start := time.Now()
    err := WaitTraefikRouters(ctx, base, names, r.routerReadyTimeout)
    elapsed := time.Since(start)
    if err == nil {
        r.logger.Info("agent_traefik_ready",
            "preview_id", pid, "routers", names,
            "elapsed_ms", elapsed.Milliseconds()) // Q4: 디버깅 단서.
        return
    }
    r.logger.Warn("agent_traefik_ready_timeout",
        "preview_id", pid, "err", err.Error(), "routers", names,
        "elapsed_ms", elapsed.Milliseconds())
}
```

호출자(`Handle` 의 흐름)는 반환값을 받지 않는다. STATUS_UPDATE running 은 무조건 송신.

`elapsed_ms` 로깅은 plan-review 가 본 항목을 "기획서에서 결정" 하라고 요청한 사항(Q4) 의 채택 결과이다. polling 횟수는 `WaitTraefikRouters` 내부 변수라 별 시그니처 확장 없이 `elapsed_ms` 만으로 운영자가 rough estimate(대략적인 ready 소요 시간) 를 얻는다 — backoff 가 100 ms → 250 ms cap 으로 가변이라 elapsed 에서 정확한 polling 횟수를 역산하긴 어렵지만, "100 ms 미만이면 첫 시도 성공", "1 s 이상이면 다회 재시도 후 성공" 등 디버깅 단서로 충분. 정확한 횟수까지 필요한 경우 후속 Phase 에서 시그니처 확장.

**근거**:
- 결정 4 의 best-effort 정책을 코드 형태로 강제.
- 단일 헬퍼 1 곳에서 로깅 일관성 확보.
- disabled 케이스가 함수 시작에 명시되어 운영자가 코드 읽을 때 이해 즉시.

**대안 기각**:
- **헬퍼가 error 반환** — 호출자가 무시 또는 분기 처리하는데 두 호출 지점이 모두 무시 → 함수 시그니처 뜻 약화.
- **probe disabled 분기를 호출자(handleCompose 등)에 위치** — 두 곳 중복.

**되돌릴 때 비용**: `runner.go` 의 헬퍼 1 개 + 호출 지점 2 곳. 작음.

### 결정 12 — `--api.insecure=true` 활성화 시 cmd 추가 외 다른 Traefik 옵션 변경 없음 (dashboard 차단 유지)

**선택**: `EnsureTraefik` 의 cmd 를 다음과 같이 분기 (§4-2 와 동일).

```go
cmd := []string{
    "--providers.docker=true",
    "--providers.docker.exposedbydefault=false",
    "--providers.docker.network=" + spec.Network,
    "--entrypoints.web.address=:80",
    "--api.dashboard=false",
}
if spec.APIHostPort > 0 {
    cmd = append(cmd,
        "--api.insecure=true",
        "--entrypoints.traefik.address=:8080", // (S4) forward-compat: Traefik default 변경 위험 차단.
    )
}
```

`--api.dashboard=false` 유지(API endpoint 만 활성, dashboard 미공개).

**근거**:
- 최소 변경. 운영자가 dashboard 활성화를 원하면 후속 Phase 의 인증과 함께 도입.
- `--entrypoints.traefik.address=:8080` 은 현재 Traefik v3.1 default 와 동일해 redundant 하지만, **Traefik default(:8080) 가 향후 버전에서 변경될 위험 차단** 을 위해 명시적으로 고정 (S4 — plan-review 1차 반영). API 호스트 바인딩(`PortBindings: {ContainerPort: 8080, ...}`) 와의 정합성도 명시적.

**대안 기각**:
- **`--api=true` (insecure 없이)** — Traefik v3 의 `--api=true` 는 dashboard + API 모두 활성, dashboard 가 dashboard endpoint 를 노출. dashboard 차단 의도와 충돌.
- **`--api.insecure=true` + `--api.dashboard=true`** — dashboard 가 노출되며 인증 없음. 결정 7 의 보안 정책과 충돌.

**되돌릴 때 비용**: `traefik.go` 의 cmd 분기 1 줄. 작음.

### 결정 13 — `PortBinding.Protocol` 빈 문자열은 `"tcp"` 로 기본 처리, SDK 어댑터에서만 변환

**선택**: `internal/agent/docker.go` 의 `PortBinding` 은 빈 Protocol 을 그대로 보관(zero value 보존). `cmd/agent/docker_sdk.go` 가 `nat.Port` 를 만들 때 `protocol == "" → "tcp"` 변환.

**근거**:
- 도메인 타입의 zero value 보존 → 테스트 fixture 가 `PortBinding{ContainerPort:80, HostPort:8080}` 로 단순 표현.
- Protocol 변환은 SDK adapter 책임 — 도메인은 모름.

**대안 기각**:
- **`PortBinding` 생성 시 default 강제(`NewPortBinding`)** — 생성자 함수 도입은 호출자 부담.
- **도메인에서 default 정규화** — adapter 의 책임이 도메인으로 누설.

**되돌릴 때 비용**: SDK adapter 의 변환 1 줄. 작음.

---

## 4. 명세 상세

### 4-1. `PortBinding` 타입 + `CreateOptions` 마이그레이션 (결정 2, 13)

**`internal/agent/docker.go` diff 요지**:

```go
// PortBinding 은 컨테이너 포트 → 호스트 포트 매핑 1 건.
//   - HostPort == 0 이면 expose only(외부 미노출).
//   - Protocol 빈 문자열이면 SDK adapter 가 "tcp" 로 처리 (결정 13).
type PortBinding struct {
    ContainerPort int
    HostPort      int
    Protocol      string
}

// CreateOptions 는 ContainerCreate 호출에 필요한 옵션.
type CreateOptions struct {
    Image        string
    Name         string
    Labels       map[string]string
    PortBindings []PortBinding   // Phase 7: 다중 포트 지원.
    Env          []string
    Networks     []string
    Volumes      []string
    Cmd          []string
}
```

기존 `HostPort int`, `ExposedPort int` 두 필드는 **삭제**. 모든 호출 지점이 동시에 마이그레이션된다(§4-7 영향 파일 목록 참조).

마이그레이션 매핑(grep + 수동 검증):

| 기존 (Phase 6) | 신규 (Phase 7) |
|---|---|
| `CreateOptions{HostPort: 8080, ExposedPort: 80}` | `CreateOptions{PortBindings: []PortBinding{{ContainerPort: 80, HostPort: 8080}}}` |
| `CreateOptions{ExposedPort: svc.Port}` (Dockerfile 모드, 외부 노출 X) | `CreateOptions{PortBindings: []PortBinding{{ContainerPort: svc.Port, HostPort: 0}}}` |

**SDK 어댑터(`cmd/agent/docker_sdk.go`) 변경 요지**:

```go
hostConf := &container.HostConfig{
    PortBindings: nat.PortMap{},
    Binds:        opts.Volumes,
}
exposed := nat.PortSet{}
hostIP := "127.0.0.1" // 결정 7: API 포트는 loopback 바인딩.
// 단, web 트래픽(컨테이너 80 / 호스트 traefikPort) 은 0.0.0.0 노출 필요 →
// "ContainerPort == 80" 인 경우에만 0.0.0.0 사용. 그 외(API 8080 포함)는 127.0.0.1.
for _, pb := range opts.PortBindings {
    proto := pb.Protocol
    if proto == "" {
        proto = "tcp"
    }
    key := nat.Port(strconv.Itoa(pb.ContainerPort) + "/" + proto)
    exposed[key] = struct{}{}
    if pb.HostPort > 0 {
        bindIP := hostIP
        if pb.ContainerPort == 80 {
            bindIP = "0.0.0.0"
        }
        hostConf.PortBindings[key] = []nat.PortBinding{{
            HostIP: bindIP, HostPort: strconv.Itoa(pb.HostPort),
        }}
    }
}
cfg := &container.Config{
    Image:        opts.Image,
    Labels:       opts.Labels,
    Env:          opts.Env,
    Cmd:          opts.Cmd,
    ExposedPorts: exposed,
}
```

`ContainerInspectResult` 의 `HostPort int` 는 **유지**(orphan_restore.go 가 사용). 단, multi-port 컨테이너에 대한 의미는 "첫 번째 호스트 바인딩" 이며, 본 Phase 의 orphan 복원 대상은 여전히 단일 포트 컨테이너.

### 4-2. `TraefikSpec.APIHostPort` + specHash + EnsureTraefik (결정 5, 7, 12, S4, S5)

**전제**: `spec.HostPort > 0` (Traefik 웹 포트는 항상 호스트 바인딩 필수). Phase 6 의 `ParseConfig` 가 `--traefik-port` 를 `1..65535` 범위로 강제(config.go 의 기존 검증)하므로 wiring 경로상 0 이 들어올 수 없다. `EnsureTraefik` 는 별 방어 없이 `spec.HostPort > 0` 가정. (테스트나 외부 library 사용에서 의도적으로 0 을 주입하면 docker 가 nat.PortBinding 단계에서 에러 — 본 Phase 추가 검증 X.)

**`internal/agent/traefik.go` diff 요지**:

```go
type TraefikSpec struct {
    Image       string
    HostPort    int    // 웹 트래픽 (default 8080)
    APIHostPort int    // Phase 7: Traefik API 호스트 바인딩 (default 9080, 0 = 비활성)
    Network     string
    Container   string
}

// ErrTraefikPortConflict 는 spec.HostPort == spec.APIHostPort > 0 인 경우 (S5).
// ParseConfig 가 1차 방어선이고, EnsureTraefik 는 wiring 우회 호출자에 대한 2차 방어선.
var ErrTraefikPortConflict = errors.New("traefik: HostPort and APIHostPort must differ")

func specHash(s TraefikSpec) string {
    h := sha256.Sum256([]byte(
        s.Image + "|" +
            strconv.Itoa(s.HostPort) + "|" +
            s.Network + "|" +
            strconv.Itoa(s.APIHostPort), // Phase 7: API 포트 변경 시 재생성 트리거.
    ))
    return hex.EncodeToString(h[:])
}

func EnsureTraefik(ctx context.Context, dc DockerClient, spec TraefikSpec) error {
    // (S5) 동일 포트 방어: ParseConfig 우회 wiring(테스트, library 사용)에 대한 2차 방어.
    if spec.APIHostPort > 0 && spec.HostPort == spec.APIHostPort {
        return ErrTraefikPortConflict
    }

    hash := specHash(spec)
    // ... (기존 inspect / stop+rm 로직 동일)

    cmd := []string{
        "--providers.docker=true",
        "--providers.docker.exposedbydefault=false",
        "--providers.docker.network=" + spec.Network,
        "--entrypoints.web.address=:80",
        "--api.dashboard=false",
    }
    pbs := []PortBinding{
        {ContainerPort: 80, HostPort: spec.HostPort}, // 0.0.0.0 (SDK adapter 분기)
    }
    if spec.APIHostPort > 0 {
        cmd = append(cmd,
            "--api.insecure=true",
            // (S4) Traefik default(:8080) 가 향후 버전에서 변경될 위험 차단 — redundant 라도 명시.
            "--entrypoints.traefik.address=:8080",
        )
        pbs = append(pbs, PortBinding{
            ContainerPort: 8080, HostPort: spec.APIHostPort,
        }) // 127.0.0.1 (SDK adapter 분기, ContainerPort != 80)
    }

    id, err := dc.ContainerCreate(ctx, CreateOptions{
        Image:        spec.Image,
        Name:         spec.Container,
        Labels:       map[string]string{traefikSpecLabel: hash},
        PortBindings: pbs,
        Networks:     []string{spec.Network},
        Volumes:      []string{"/var/run/docker.sock:/var/run/docker.sock:ro"},
        Cmd:          cmd,
    })
    // ... (이하 ContainerStart 동일)
}
```

### 4-3. `WaitTraefikRouters` 함수 (결정 1, 6, 9, 10)

**`internal/agent/traefik_ready.go` (신규)**:

```go
// 이 파일의 책임:
//   - Traefik REST API 의 라우터 status 를 폴링해 모든 라우터가 enabled 가 될 때까지
//     대기한다 (Phase 7, R1 완화).
//   - 외부 의존: 표준 라이브러리 net/http + encoding/json 만 사용. (NF-1)
//
// 참고: docs/specs/phase-7-traefik-readiness.md §4-3, 결정 1/6/10.
package agent

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "time"
)

var (
    traefikPollInitial = 100 * time.Millisecond
    traefikPollMax     = 250 * time.Millisecond
)

// ErrTraefikRoutersTimeout 는 timeout 내 모든 라우터가 enabled 가 되지 않은 경우.
var ErrTraefikRoutersTimeout = errors.New("traefik routers not ready within timeout")

// routerStatus 는 Traefik /api/http/routers/{name} 응답의 좁은 dto.
//   - Status: 결정 1 의 준비 판정 — "enabled" 정확 일치만 ready, 그 외 모두 미준비.
//   - Name: 디버깅 로그(오타로 인한 미스매치 등) 용도로 보관. allEnabled 가 응답의
//     name 이 요청한 name 과 다르면 warn 로그 1 회 (silent bug 방지, 경미 이슈 반영).
type routerStatus struct {
    Status string `json:"status"`
    Name   string `json:"name"`
}

// WaitTraefikRouters: §4-3 / 결정 10 의 시그니처 참조.
func WaitTraefikRouters(ctx context.Context, baseURL string, names []string, timeout time.Duration) error {
    if baseURL == "" || timeout <= 0 || len(names) == 0 {
        return nil // probe disabled 또는 검사 대상 없음.
    }

    deadline := time.Now().Add(timeout)
    interval := traefikPollInitial
    client := &http.Client{Timeout: 1 * time.Second}

    for {
        if err := ctx.Err(); err != nil {
            return err
        }

        ok, lastErr := allEnabled(ctx, client, baseURL, names)
        if ok {
            return nil
        }

        if time.Now().After(deadline) {
            if lastErr != nil {
                return fmt.Errorf("%w: last err: %v", ErrTraefikRoutersTimeout, lastErr)
            }
            return ErrTraefikRoutersTimeout
        }

        // backoff (cap 250 ms).
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(interval):
        }
        interval = time.Duration(float64(interval) * 1.5)
        if interval > traefikPollMax {
            interval = traefikPollMax
        }
    }
}

// allEnabled 는 names 의 모든 라우터가 enabled 일 때 (true, nil),
// 일부 미존재/disabled 일 때 (false, nil), HTTP 호출 자체가 실패하면 (false, err).
func allEnabled(ctx context.Context, client *http.Client, baseURL string, names []string) (bool, error) {
    var lastErr error
    for _, name := range names {
        url := baseURL + "/api/http/routers/" + name + "@docker"
        req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
        if err != nil {
            return false, err
        }
        resp, err := client.Do(req)
        if err != nil {
            lastErr = err
            return false, lastErr // 네트워크 에러는 다음 polling 사이클로.
        }
        if resp.StatusCode == http.StatusNotFound {
            _ = resp.Body.Close()
            return false, nil // 라우터 아직 미생성 — 정상 폴링 계속.
        }
        if resp.StatusCode != http.StatusOK {
            _ = resp.Body.Close()
            lastErr = fmt.Errorf("traefik api: %s -> HTTP %d", url, resp.StatusCode)
            return false, lastErr
        }
        var rs routerStatus
        if err := json.NewDecoder(resp.Body).Decode(&rs); err != nil {
            _ = resp.Body.Close()
            return false, err
        }
        _ = resp.Body.Close()
        if rs.Status != "enabled" {
            return false, nil
        }
    }
    return true, nil
}
```

에러 분류와 polling 재시도 정책:

| 응답 | 처리 |
|---|---|
| `200 OK` + `status:"enabled"` | 해당 라우터 OK, 다음 라우터 검사 |
| `200 OK` + `status:"disabled"` | 미준비 — 폴링 계속(다음 사이클) |
| `200 OK` + `status:"warning"` (M3) | 미준비 — 폴링 계속. stripprefix 미들웨어 binding race 등 transient 케이스가 여기에 해당. 라우터가 결국 enabled 로 안정화되면 자연 통과. |
| `200 OK` + `status:""` (빈값) 또는 누락 | 미준비 — 폴링 계속(결정 1 의 보수적 판정) |
| `404 Not Found` | 라우터 미생성 — 폴링 계속 |
| `500/502/503` | lastErr 갱신, 폴링 계속 |
| 네트워크 에러 (connection refused 등) | lastErr 갱신, 폴링 계속 |
| `ctx.Done()` | 즉시 `ctx.Err()` 반환. **ctx cancel 즉각 반응 보장**: `http.NewRequestWithContext(ctx, ...)` 로 ctx 를 client.Do 에 전달 → in-flight request 도 즉시 중단(NF-15). Sleep 단계의 select{ctx.Done, time.After} 로도 즉시 반응. |
| deadline 초과 | `ErrTraefikRoutersTimeout` (lastErr 가 있으면 wrap) |

### 4-4. `Config` + `ParseConfig` 변경

**`internal/agent/config.go` diff 요지**:

```go
type Config struct {
    HubURL             string
    Token              string
    AdvertiseHost      string
    LogLevel           string
    WorkDir            string
    PrefetchInterval   time.Duration
    MaxJobs            int
    TraefikPort        int
    TraefikImage       string
    TraefikAPIPort     int           // Phase 7: Traefik API 호스트 포트 (default 9080, 0=비활성)
    RouterReadyTimeout time.Duration // Phase 7: WaitTraefikRouters timeout (default 30s)
}
```

`ParseConfig` 추가 플래그:

```go
traefikAPIPort = fs.Int("traefik-api-port",
    envInt("AGENT_TRAEFIK_API_PORT", 9080),
    "Traefik API host binding port (0 disables readiness probe)")
routerReady = fs.String("router-ready-timeout",
    envOr("AGENT_ROUTER_READY_TIMEOUT", "30s"),
    "Traefik router readiness probe timeout (e.g. 30s, 0 disables)")
```

검증 + 변환 (fs.Parse 후 본문):

```go
// (1) traefikAPIPort 범위 검증.
if *traefikAPIPort < 0 || *traefikAPIPort > 65535 {
    return Config{}, fmt.Errorf("invalid --traefik-api-port %d: must be 0..65535", *traefikAPIPort)
}
// (2) 동일 포트 차단 (S5 1차 방어).
if *traefikAPIPort > 0 && *traefikAPIPort == *traefikPort {
    return Config{}, fmt.Errorf("--traefik-api-port and --traefik-port must differ (both = %d)", *traefikPort)
}
// (3) router-ready-timeout: string → Duration 변환 (Phase 6 의 prefetch-interval 패턴과 동일).
rrt, err := time.ParseDuration(*routerReady)
if err != nil {
    return Config{}, fmt.Errorf("invalid --router-ready-timeout %q: %w", *routerReady, err)
}
if rrt < 0 {
    return Config{}, fmt.Errorf("invalid --router-ready-timeout %q: must be >= 0", *routerReady)
}
// (4) Config 채움.
return Config{
    // ... (기존 필드)
    TraefikAPIPort:     *traefikAPIPort,
    RouterReadyTimeout: rrt,   // 0 허용 (probe 비활성).
}, nil
```

요약:
- `traefikAPIPort` 가 0 이면 그대로 사용(비활성). 0 미만 / 65535 초과 시 에러.
- `traefikAPIPort > 0 && traefikAPIPort == traefikPort` 이면 에러(`must differ`).
- `router-ready-timeout` 은 `time.ParseDuration` 결과를 `Config.RouterReadyTimeout` 에 채움. 파싱 실패 시 에러. 음수 에러. 0 허용(probe 비활성).

### 4-5. `Runner` 변경

**`internal/agent/runner.go` diff 요지**:

```go
type Runner struct {
    docker             DockerClient
    cache              *MultiRepoCache
    cmd                CmdRunner
    hub                HubSender
    advHost            string
    traefikPort        int
    traefikAPIPort     int           // Phase 7
    routerReadyTimeout time.Duration // Phase 7
    logger             *slog.Logger
    ready              ReadySender
    maxJobs            int
    jobs               sync.Map
    paused             atomic.Bool
    inFlight           atomic.Int64
}

// SetTraefikAPIPort 는 Traefik API 호스트 포트를 설정한다 (Phase 7, 0 = probe 비활성).
func (r *Runner) SetTraefikAPIPort(port int) { r.traefikAPIPort = port }

// SetRouterReadyTimeout 는 readiness probe timeout 을 설정한다 (Phase 7).
func (r *Runner) SetRouterReadyTimeout(d time.Duration) { r.routerReadyTimeout = d }

// waitRouters: 결정 11 의 헬퍼 — buildMode 분기 + elapsed 로깅 포함.
// (코드 본체는 결정 11 참조)
```

`Handle` 의 호출 순서(변경 후):

```
(1) building 송신
(2) cache.Ensure / Checkout
(3) LoadPreviewConfig
(4) detectBuildFiles → mode (compose/dockerfile) 결정 → handleCompose 또는 handleDockerfile
(5) waitRouters(ctx, pid, cfg, mode)   ← Phase 7 신규 (결정 11, M2 의 buildMode 전달)
(6) PreviewURLs 계산 + jobs.Store
(7) STATUS_UPDATE running 송신
```

`waitRouters` 위치는 (4) 이후, (5)~(7) 사이. `mode` 는 (4) 의 `detectBuildFiles` 가 이미 반환하는 값이므로 추가 계산 없이 그대로 전달. handleCompose/handleDockerfile 자체에는 손대지 않고 Handle 의 흐름에 1 줄 추가 — 두 분기에 중복 호출 X.

**NF-3(300줄) 분할 정책**: 현재 `runner.go` 가 379 줄. Phase 7 추가(필드 2 + setter 2 + waitRouters 헬퍼 ~20줄 + Handle 의 1 줄) 로 ~405 줄 도달 → 300 줄 초과. 따라서 본 Phase 에서 다음과 같이 분할한다.

- `internal/agent/runner.go`: Handle/Teardown 의 공통 흐름, 필드, NewRunner, Pause, fail, RegisterRestoredJob, allocatePort 잔류.
- `internal/agent/runner_ready.go` (신규): `waitRouters` 헬퍼 + `SetTraefikAPIPort` + `SetRouterReadyTimeout` + 관련 필드 주석. (필드 자체는 `Runner` struct 가 1 곳이라 runner.go 에 잔류, setter 만 분리.)
- 분할 후 `runner.go` 가 ~370 줄로 유지(±5 줄 변동), `runner_ready.go` 가 ~50 줄.

분할 정책은 §4-7 영향 파일 목록 + §6-4 의 NF 항목과 일관.

### 4-6. wiring (`cmd/agent/main.go`)

```go
runner := agent.NewRunner(docker, cache, nil, c, cfg.AdvertiseHost, logger)
runner.SetTraefikPort(cfg.TraefikPort)
runner.SetTraefikAPIPort(cfg.TraefikAPIPort)             // Phase 7
runner.SetRouterReadyTimeout(cfg.RouterReadyTimeout)     // Phase 7
c.SetRunner(runner)

if err := agent.EnsureTraefik(ctx, docker, agent.TraefikSpec{
    Image:       cfg.TraefikImage,
    HostPort:    cfg.TraefikPort,
    APIHostPort: cfg.TraefikAPIPort,                     // Phase 7
    Network:     "preview-net",
    Container:   "preview-traefik",
}); err != nil {
    logger.Warn("ensure_traefik_failed", "err", err.Error())
}
```

### 4-7. 영향받는 파일 목록

| 파일 | 변경 종류 |
|---|---|
| `internal/agent/docker.go` | `PortBinding` 신규, `CreateOptions.HostPort/ExposedPort` 제거 + `PortBindings` 추가 |
| `internal/agent/traefik.go` | `TraefikSpec.APIHostPort` 추가, `ErrTraefikPortConflict` 신규, `specHash` 입력 갱신, `EnsureTraefik` cmd/PortBindings 분기 + S5 동일 포트 방어 |
| `internal/agent/traefik_ready.go` | **신규** — `WaitTraefikRouters` + `ErrTraefikRoutersTimeout` |
| `internal/agent/labels.go` | `RouterNames(pid, cfg, buildMode)` 신규 (M2: buildMode 분기) |
| `internal/agent/runner.go` | 필드 2 개 추가, `Handle` 흐름에 5단계 추가, Dockerfile 분기의 `ExposedPort: svc.Port` → `PortBindings: ...` 마이그레이션. (NF-3 분할로 setter/헬퍼는 `runner_ready.go` 로 이동.) |
| `internal/agent/runner_ready.go` | **신규** — `SetTraefikAPIPort` / `SetRouterReadyTimeout` setter + `waitRouters` 헬퍼 (NF-3 분할 정책) |
| `internal/agent/config.go` | 필드 2 개 + 플래그 2 개 + 검증 (포트 범위, 동일 포트, duration) |
| `cmd/agent/docker_sdk.go` | `ContainerCreate` 가 `PortBindings` 슬라이스 처리, HostIP 분기(80=0.0.0.0, 그 외=127.0.0.1). `ContainerInspectResult.HostPort` 채움 로직은 Phase 6 와 동일(첫 번째 host 바인딩) — 본 Phase 에서 변경 없음 (S1, §11 Q7 참조). |
| `cmd/agent/main.go` | wiring 2 줄 (`SetTraefikAPIPort`, `SetRouterReadyTimeout`) + `EnsureTraefik` 호출에 `APIHostPort` 인자 |
| `internal/agent/traefik_test.go` | fake docker 의 `CreateOptions` 사용처 갱신, APIHostPort 케이스 추가, S5 동일 포트 케이스 추가 |
| `internal/agent/runner_test.go` | fake docker 의 `CreateOptions` 사용처 갱신. waitRouters/readiness 케이스는 별 파일로 분리 (다음 줄). |
| `internal/agent/runner_ready_test.go` | **신규** (NF-3 사전 분리) — F-27 ~ F-34 + F-29 의 buildMode 검증 등 readiness 관련 케이스 전담. |
| `internal/agent/orphan_restore_test.go` | `CreateOptions`/`ContainerInspectResult` 사용처 갱신 |
| `internal/agent/labels_test.go` | `RouterNames` 단위 테스트 추가 (compose/dockerfile 분기) |
| `internal/agent/config_test.go` | 새 플래그 단위 테스트 추가 (default, env, 동일 포트 에러, duration parse) |
| `internal/agent/traefik_ready_test.go` | **신규** — `WaitTraefikRouters` 단위 테스트 (httptest stub) |
| `tests/e2e/agent_harness_test.go` | **(M1)** `fakeDockerClient.ContainerInspect` 가 `c.opts.HostPort` 직접 참조(line 127) → `c.opts.PortBindings` 첫 항목으로 마이그레이션. 빈 슬라이스 안전 처리: `if len(c.opts.PortBindings) > 0 { res.HostPort = c.opts.PortBindings[0].HostPort }` 패턴. 그 외 fake 메서드의 `CreateOptions` 사용처 동반 갱신. |

총 신규 파일 4 개(traefik_ready.go, runner_ready.go, traefik_ready_test.go, runner_ready_test.go), 수정 파일 약 12 개.

### 4-8. 디렉토리 트리 (변경 후 요지)

```
internal/agent/
├── docker.go              (수정: PortBinding 추가)
├── traefik.go             (수정: APIHostPort + ErrTraefikPortConflict)
├── traefik_ready.go       (신규: WaitTraefikRouters)
├── traefik_test.go        (수정)
├── traefik_ready_test.go  (신규)
├── labels.go              (수정: RouterNames + buildMode 분기)
├── labels_test.go         (수정)
├── runner.go              (수정: Handle 흐름)
├── runner_ready.go        (신규: setter + waitRouters 헬퍼, NF-3 분할)
├── runner_test.go         (수정: CreateOptions 마이그레이션만)
├── runner_ready_test.go   (신규: readiness 케이스 전담, NF-3 사전 분리)
├── config.go              (수정: 2 필드 + 검증)
├── config_test.go         (수정)
├── orphan_restore_test.go (수정)
└── ... (그 외 무변경)

cmd/agent/
├── docker_sdk.go        (수정: PortBindings 처리)
└── main.go              (수정: wiring)

tests/e2e/
└── agent_harness_test.go (M1: fakeDockerClient PortBindings 마이그레이션)
```

---

## 5. 시퀀스 다이어그램 (ASCII)

### 5-1. 정상 경로 (Compose 모드, readiness 1~3 회 polling 후 성공)

```
Hub                  Agent (Runner)         Traefik API           Docker
 | JOB_ASSIGN(p) -->|                      :9080                  |
 |                  | (Phase 6 흐름)        |                      |
 |                  | docker compose up -d ----------->            |
 |                  |                      |                      | (containers up,
 |                  |                      |                      |  Traefik provider
 |                  |                      |                      |  picks up labels)
 |                  | waitRouters(p, cfg)   |                      |
 |                  |   names=[p-front,    |                      |
 |                  |          p-admin]    |                      |
 |                  | --GET /api/http/routers/p-front@docker -->  |
 |                  | <--- 404 (not yet) ---                      |
 |                  | (sleep 100ms)        |                      |
 |                  | --GET /api/http/routers/p-front@docker -->  |
 |                  | <--- 200 enabled ---                         |
 |                  | --GET /api/http/routers/p-admin@docker -->  |
 |                  | <--- 200 enabled ---                         |
 |                  | log: agent_traefik_ready                     |
 | <-- STATUS_UPDATE running (preview_urls)                       |
```

### 5-2. 타임아웃 경로 (best-effort 진행)

```
Hub                  Agent (Runner)         Traefik API
 | JOB_ASSIGN(p) -->|                      :9080
 |                  | docker compose up -d --> ...
 |                  | waitRouters(p, cfg)   |
 |                  | --GET /api/http/routers/p-app@docker -->
 |                  | <--- 404 ---
 |                  | (반복 polling, 30s 경과)
 |                  | log: agent_traefik_ready_timeout (WARN)
 | <-- STATUS_UPDATE running (preview_urls)
 | (운영자가 클릭 시 일시적으로 Traefik 404 가능, 곧 자가 회복)
```

### 5-3. probe 비활성 경로 (`--traefik-api-port=0`)

```
Hub                  Agent (Runner)
 | JOB_ASSIGN(p) -->|
 |                  | docker compose up -d --> ...
 |                  | waitRouters(p, cfg, "compose")
 |                  |   r.traefikAPIPort==0 → return immediately
 | <-- STATUS_UPDATE running   (Phase 6 와 동일 동작)
```

### 5-4. Dockerfile 모드 단일 라우터 polling (M2 분기 효과)

```
Hub                  Agent (Runner)         Traefik API           Docker
 | JOB_ASSIGN(p) -->|                      :9080                  |
 |                  | (Phase 6 흐름)        |                      |
 |                  | docker build + ContainerCreate + ContainerStart
 |                  |       --network preview-net + labels (FirstService 만)
 |                  | waitRouters(p, cfg, "dockerfile")
 |                  |   names=RouterNames(p, cfg, "dockerfile")
 |                  |        = [p-app]   ← cfg.FirstService() 1 개만 (M2)
 |                  | --GET /api/http/routers/p-app@docker -->
 |                  | <--- 200 enabled ---
 |                  | log: agent_traefik_ready (elapsed_ms=...)
 | <-- STATUS_UPDATE running (preview_urls)
```

비교: 만약 `RouterNames` 가 services 전체(예: `[p-app, p-admin]`) 를 반환했다면 `p-admin` 라우터는 Dockerfile 모드에서 영영 생성되지 않아 `WaitTraefikRouters` 가 항상 `ErrTraefikRoutersTimeout` 으로 끝남(M2 의 핵심 회피 사례).

---

## 6. 기능 체크리스트 (F-*)

### 6-1. `PortBinding` / `CreateOptions` 마이그레이션

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-1 | `CreateOptions{PortBindings: nil}` 호출 시 SDK adapter 가 `nat.PortMap` 과 `ExposedPorts` 모두 비움 | 단위 (sdkDockerClient — go test build 만, 실 docker 호출 X. fake 로 대체 가능 시 fake) |
| F-2 | `PortBindings: [{ContainerPort:80, HostPort:8080}]` 시 SDK 가 nat 키 `80/tcp`, host bind `0.0.0.0:8080` (결정 7) | 단위 |
| F-3 | `PortBindings: [{ContainerPort:8080, HostPort:9080}]` 시 SDK 가 nat 키 `8080/tcp`, host bind `127.0.0.1:9080` (결정 7) | 단위 |
| F-4 | `PortBindings: [{ContainerPort:5432, HostPort:0}]` 시 nat ExposedPorts 만 채우고 PortMap 은 비움 (expose only) | 단위 |
| F-5 | `PortBindings: [{ContainerPort:80, HostPort:8080}, {ContainerPort:8080, HostPort:9080}]` 두 항목 모두 처리, 결정 7 의 HostIP 분기 정확 | 단위 |
| F-6 | `PortBinding.Protocol == ""` 이면 SDK 가 `tcp` 로 변환 (결정 13) | 단위 |
| F-7 | Phase 6 의 `runner.handleDockerfile` 가 `ExposedPort: svc.Port` 대신 `PortBindings: [{ContainerPort: svc.Port, HostPort: 0}]` 로 호출 | 단위 (fake docker call args assert) |
| F-8 | Phase 6 의 traefik_test 가 `CreateOptions` 마이그레이션 후 회귀 통과 | `go test ./internal/agent/...` |
| F-8b | **(M1)** `tests/e2e/agent_harness_test.go` 의 `fakeDockerClient.ContainerInspect` 가 `c.opts.PortBindings` 빈 슬라이스 케이스에서 panic 없이 `HostPort: 0` 반환 (`if len(...)>0` 가드) | 단위 (e2e harness 단독 빌드 + 빈 PortBindings 컨테이너 fixture) |
| F-8c | **(M1)** `tests/e2e/agent_harness_test.go` 가 PortBindings 마이그레이션 후 `go build ./tests/e2e/...` 통과 | CI |

### 6-2. `WaitTraefikRouters`

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-9 | `baseURL==""` 이면 즉시 `nil` 반환 (probe disabled) | 단위 |
| F-10 | `timeout==0` 이면 즉시 `nil` 반환 (probe disabled) | 단위 |
| F-11 | `names==nil` 이면 즉시 `nil` 반환 (검사 대상 없음) | 단위 |
| F-12 | 모든 라우터 첫 polling 에서 `200 enabled` 응답 시 nil 반환, 호출 횟수 = `len(names)` | 단위 (httptest server, request counter) |
| F-13 | 1 번째 polling 에서 `404`, 2 번째 polling 에서 `200 enabled` → nil 반환, 총 호출 횟수 ≥ `len(names)+1` | 단위 (state machine fake) |
| F-14 | timeout 초과 시 `errors.Is(err, ErrTraefikRoutersTimeout)` 참 | 단위 (server 가 영원히 404 반환) |
| F-15 | `ctx` 취소 시 `ctx.Err()` 반환 (`ErrTraefikRoutersTimeout` 아님) | 단위 |
| F-16 | server 가 `500` 반환해도 timeout 안에서 polling 계속, 마지막 에러를 `ErrTraefikRoutersTimeout` 에 wrap | 단위 |
| F-17 | 네트워크 에러(서버 미기동) 시 polling 계속, timeout 도달 시 ErrTraefikRoutersTimeout(wrap) | 단위 (httptest 서버 close 후 호출) |
| F-18 | 라우터 N 개 중 (N-1) 개만 enabled, 마지막 1 개가 disabled 면 nil 반환 X (전체 만족만 성공) | 단위 |
| F-19 | **(S2)** polling 간격 backoff 검증 — 테스트가 `traefikPollInitial=1ms`, `traefikPollMax=4ms` 로 swap 후 timeout=20ms 안에서 항상 404 응답하는 server 에 호출된 polling **횟수가 5~7 회 범위** 에 들어옴. 계산 근거: backoff 시퀀스 sleep = 1, 1.5(→1), 2.25(→2), 3.375(→3), 4, 4, ... → 누적 sleep 이 17 ms 도달까지 약 6 회 시도(첫 시도는 즉시 실행, 이후 sleep 누적). 정확한 expected = 6, 허용 범위 ±1 (스케줄러 jitter). 절대 wallclock 비검증 — 횟수만. R8 의 flakiness 해소. | 단위 (NF-10 의 패키지 변수 swap + httptest counter) |
| F-20 | URL 형식이 `{baseURL}/api/http/routers/{name}@docker` 정확 일치 | 단위 (request URL assert) |
| F-20b | **(M3)** server 가 `200 OK + status:"warning"` 반환 시 미준비로 간주, polling 계속 | 단위 |
| F-20c | **(M3)** server 가 `200 OK + status:""` (빈값) 반환 시 미준비로 간주, polling 계속 | 단위 |
| F-20d | **(경미)** server 가 응답에 다른 `name` 을 담아 반환 시(예: 요청 `p-app`, 응답 `name:"p-other"`) `WaitTraefikRouters` 는 status 만 보고 ready 판정 — 그러나 호출 단위가 mismatch 를 감지하면 추후 디버깅 가능하도록 routerStatus.Name 을 보관(코드 리뷰 단계에서 확인) | 코드 리뷰 |

### 6-3. `EnsureTraefik` (APIHostPort 분기)

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-21 | `APIHostPort > 0` 시 cmd 에 `--api.insecure=true` 포함 | 단위 (fake docker `createCalls` 의 Cmd 슬라이스 assert) |
| F-22 | `APIHostPort > 0` 시 PortBindings 에 `{ContainerPort:8080, HostPort:APIHostPort}` 포함 | 단위 |
| F-23 | `APIHostPort == 0` 시 cmd 에 `--api.insecure` 미포함 + PortBindings 에 8080 미포함 (Phase 6 동작) | 단위 |
| F-24 | `specHash` 가 APIHostPort 변경에 반응(0 → 9080 → 9081 세 케이스의 해시 모두 다름) | 단위 |
| F-25 | 기존 컨테이너의 specHash 와 신규 specHash 가 다르면 stop+rm+재생성 (Phase 6 정책 그대로 동작) | 단위 |
| F-26 | **(S5)** `APIHostPort == HostPort > 0` 입력에 대해 (a) ParseConfig 가 1차 방어 (F-40 와 중복 검증) (b) `EnsureTraefik` 가 진입 즉시 `ErrTraefikPortConflict` 반환 — 두 방어선 모두 단위 테스트 | 단위 (config + traefik 양쪽) |
| F-26b | **(S5)** `APIHostPort == 0` 이면 `HostPort` 와 무관하게 `EnsureTraefik` 가 conflict 미체크(probe 비활성 케이스의 정상 동작) | 단위 |

### 6-4. `Runner` 통합

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-27 | `Runner.SetTraefikAPIPort(9080)` + `SetRouterReadyTimeout(30s)` 호출 후 `r.traefikAPIPort == 9080` | 단위 |
| F-28 | `Handle` 가 detectBuildFiles + 분기 후 STATUS_UPDATE running 송신 직전에 `waitRouters(ctx, pid, cfg, "compose")` 1 회 호출 — buildMode 인자 정확 전달 | 단위 (fake httptest server 의 호출 횟수 + 라우터 이름 슬라이스 assert) |
| F-29 | 같은 호출이 dockerfile 분기에서도 1 회: `waitRouters(ctx, pid, cfg, "dockerfile")` — names 가 `cfg.FirstService()` 1 개만 polling 됨 (M2 검증) | 단위 (httptest server 가 받은 요청 path 의 라우터 이름이 정확히 1 개) |
| F-30 | `traefikAPIPort == 0` 인 Runner 는 `waitRouters` 가 즉시 return → fake server 호출 횟수 0 | 단위 |
| F-31 | `routerReadyTimeout == 0` 도 동일 (probe disabled) | 단위 |
| F-32 | fake server 가 영원히 404 → `Handle` 가 STATUS_UPDATE running 을 여전히 송신 (best-effort, 결정 4) | 단위 (HubSender fake 의 송신 메시지 assert) |
| F-33 | 정상 폴링 성공 시 logger 가 `agent_traefik_ready` (level=Info) 1 회 기록 | 단위 (slog handler 캡처) |
| F-34 | 타임아웃 시 logger 가 `agent_traefik_ready_timeout` (level=Warn) 1 회 기록 | 단위 |
| F-35 | **(M2)** `RouterNames(pid, cfg, "compose")` 가 services 3 개에 대해 `[pid-a, pid-b, pid-c]` 알파벳 오름차순 반환 | 단위 |
| F-35b | **(M2)** `RouterNames(pid, cfg, "dockerfile")` 가 services 3 개여도 `[pid-{firstService}]` 1 개만 반환 — `cfg.FirstService()` 의 알파벳 오름차순 첫 키 | 단위 (services [c,a,b] → result [pid-a]) |
| F-35c | **(M2)** `RouterNames(pid, cfg, "dockerfile")` 에서 services 가 비어있으면 빈 슬라이스 반환 (호출자가 폴링 스킵) | 단위 |
| F-35d | **(M2)** `RouterNames(pid, cfg, "unknown")` 같은 알 수 없는 buildMode 는 빈 슬라이스만 반환 (logger 없음, 순수 함수). WARN 로그 책임은 `Runner.waitRouters` 가 buildMode 검사 후 `agent_traefik_ready_unknown_buildmode` 로 1 회 기록 (silent timeout 방지) | 단위 (RouterNames 빈 슬라이스 반환 + Runner.waitRouters 의 logger capture 로 warn 1 회 검증) |
| F-36 | `RouterNames` 의 결과가 `ServiceLabels` 내부의 routerName 명명과 정확히 일치 (한 곳 결정 8) — compose 모드 services 3 개에 대해 `RouterNames(...,"compose")` 결과의 각 항목이 `ServiceLabels(pid, name, svc)` 의 라벨 키 prefix `traefik.http.routers.{name}.` 의 `{name}` 과 일치 | 단위 (양쪽 함수의 출력 비교) |

### 6-5. `Config` / `ParseConfig`

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-37 | `--traefik-api-port` 미지정 시 default 9080 | 단위 |
| F-38 | `--traefik-api-port=0` 명시 시 `Config.TraefikAPIPort == 0` (정상 허용) | 단위 |
| F-39 | `--traefik-api-port=70000` 시 ParseConfig 에러 | 단위 |
| F-40 | `--traefik-api-port=8080` 이고 `--traefik-port=8080` 이면 ParseConfig 에러 (`must differ`) | 단위 |
| F-41 | `--router-ready-timeout=30s` 시 `Config.RouterReadyTimeout == 30*time.Second` | 단위 |
| F-42 | `--router-ready-timeout=0` 시 `Config.RouterReadyTimeout == 0` (probe disabled, 허용) | 단위 |
| F-43 | `--router-ready-timeout=-5s` 시 ParseConfig 에러 | 단위 |
| F-44 | `--router-ready-timeout=foo` (parse 실패) 시 ParseConfig 에러 (Phase 6 prefetch 와 동일 패턴) | 단위 |
| F-45 | env `AGENT_TRAEFIK_API_PORT=9090` 가 default 9080 보다 우선 | 단위 |
| F-46 | env `AGENT_ROUTER_READY_TIMEOUT=10s` 가 default 30s 보다 우선 | 단위 |

### 6-6. wiring / 회귀

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-47 | `cmd/agent/main.go` 의 `EnsureTraefik` 호출이 `APIHostPort: cfg.TraefikAPIPort` 를 인자로 전달 | 코드 리뷰 + 빌드 확인 |
| F-48 | `runner.SetTraefikAPIPort` 와 `SetRouterReadyTimeout` 가 `c.SetRunner(runner)` 직전에 호출 | 코드 리뷰 |
| F-49 | Phase 0~6 의 단위 테스트가 회귀 0 으로 모두 통과 | `go test ./...` |
| F-50 | `go vet ./...` clean | CI |
| F-51 | `golangci-lint run` clean | CI |
| F-52 | `go test -race ./internal/agent/...` green (특히 polling + STATUS_UPDATE 동시성) | CI |

### 6-7. e2e (선택)

| ID | 항목 | 검증 방법 |
|---|---|---|
| F-53 | fake Traefik API stub (`httptest`) 을 띄우고, Runner 가 STATUS_UPDATE "running" 수신 시까지 stub 의 `/api/http/routers/{name}@docker` 가 호출되었는지 | 통합 테스트 (build tag `integration`, optional) |

---

## 7. 비기능 체크리스트 (NF-*)

| ID | 항목 | 검증 방법 |
|---|---|---|
| NF-1 | 외부 의존성 0 추가 — `WaitTraefikRouters` 는 표준 라이브러리만 사용 (`net/http`, `encoding/json`, `errors`, `fmt`, `time`) | `go mod tidy` diff |
| NF-2 | 신규/수정 파일 모두 책임 주석(3~5 줄) 헤더 포함 | grep `// 이 파일의 책임:` |
| NF-3 | 어떤 파일도 300 줄을 넘지 않는다(테스트 파일 포함). **분할 정책 (구현용 파일)**: 현재 `runner.go` 379 줄 → Phase 7 추가로 ~405 줄 도달 예상 → `runner_ready.go` 신규 분리(SetTraefikAPIPort/SetRouterReadyTimeout setter + waitRouters 헬퍼). 분리 후 `runner.go` ~370 줄, `runner_ready.go` ~50 줄. `traefik_ready.go` 신규는 ~110 줄 예상. 폴링 분기 로직은 모두 `traefik_ready.go` 에 격리. **분할 정책 (테스트 파일)**: 현재 `runner_test.go` 가 Phase 6 까지의 케이스로 이미 비대 — Phase 7 이 F-27 ~ F-34 + F-29 의 buildMode 검증 + waitRouters/RouterNames 케이스 추가 시 300 줄 초과 가능. 따라서 본 Phase 에서 `runner_ready_test.go` 신규 분리 — `waitRouters` / readiness 관련 케이스 전담(Step 3 종료 시 `wc -l` 결과로 최종 결정, 사전적으로 분리 계획 명시). | `wc -l internal/agent/*.go internal/agent/*_test.go cmd/agent/*.go tests/e2e/*.go` 후 모든 파일 ≤ 300 |
| NF-4 | `go vet ./...`, `golangci-lint run` clean | CI |
| NF-5 | `go test -race ./...` green | CI |
| NF-6 | 레이어 의존: `internal/agent` 가 `internal/hub` 또는 `cmd/*` import 0 (Phase 6 NF-6 유지) | depguard / grep |
| NF-7 | `traefik_ready.go` 는 외부 네트워크 호출 외 부수효과 없음 (테스트 가능, http stub 으로 모킹) | 코드 리뷰 |
| NF-8 | `slog` 키 명명: `agent_traefik_ready`, `agent_traefik_ready_timeout` (Phase 6 의 `agent_traefik_*` 패밀리와 일관) | grep |
| NF-9 | runner_test, traefik_test 가 실제 docker daemon 요구 0 (fake DockerClient + httptest 만) | CI 환경 docker 미설치에서 단위 테스트 통과 |
| NF-10 | `WaitTraefikRouters` 의 polling 간격을 테스트가 줄일 수 있도록 `traefikPollInitial`/`traefikPollMax` 가 패키지 변수로 노출 (테스트 헬퍼 setter 없이 직접 swap) | 단위 테스트 코드 |
| NF-11 | `--api.insecure=true` 활성 시 호스트 바인딩이 `127.0.0.1` 로만 (외부 미노출, 결정 7) | `cmd/agent/docker_sdk.go` 의 HostIP 분기 + 단위 |
| NF-12 | Phase 6 → Phase 7 마이그레이션 시 운영자가 추가 작업 없이도 default(9080) 로 readiness 활성화. 단, 호스트 9080 이 사용 중이면 `--traefik-api-port` 변경 필요 — 운영 가이드에 명시 | README/USAGE 문서 |
| NF-13 | `PortBindings` 슬라이스의 결정적 순서 보존 (호출자가 준 순서대로 SDK 어댑터 처리) — 테스트 reproducibility | 단위 |
| NF-14 | `WaitTraefikRouters` 가 `http.Client{Timeout: 1s}` 로 개별 요청 타임아웃 보장 (서버 hang 방지) | 단위 (slow server fixture) |
| NF-15 | **(S3)** Polling 동안 `ctx.Done()` 반응 — 두 경로: (a) sleep 단계는 `select{ctx.Done(), time.After(interval)}` 로 즉시 반응. (b) HTTP request in-flight 단계는 `http.NewRequestWithContext(ctx, ...)` 로 ctx 를 client.Do 에 전달 → cancel 시 client 가 즉시 중단. 단, OS 의 connection close 지연으로 실측 cancel 반응 시간은 통상 ~1ms 이내, 최악 1 s (개별 요청 client.Timeout) 이내. 약속: **최대 1 s 지연 후 ctx.Err() 반환**. | 단위 (slow handler 가 응답 무한 지연하는 동안 cancel → 1.1 s 안에 ctx.Err() 반환 확인) |

---

## 8. 단계 분할 (구현·평가용)

본 Phase 는 **3 Step** 으로 나눈다. 각 Step 종료 시 단위 테스트 통과 + 컴파일 통과를 보장.

### Step 1 — `PortBinding` 마이그레이션 (도메인 + SDK + 호출자 + e2e harness 동시 갱신)

- `internal/agent/docker.go` 의 `CreateOptions` 변경.
- `internal/agent/traefik.go` 의 `EnsureTraefik` 호출 갱신 (APIHostPort 미도입, 단지 `PortBindings` 표현 마이그레이션).
- `internal/agent/runner.go` 의 `handleDockerfile` 갱신.
- `cmd/agent/docker_sdk.go` 의 `ContainerCreate` 갱신 (HostIP 분기 포함).
- 모든 fake docker mock 갱신 (`traefik_test.go`, `runner_test.go`, `orphan_restore_test.go`).
- **(M1)** `tests/e2e/agent_harness_test.go` 의 `fakeDockerClient` 갱신 — `c.opts.HostPort` 직접 참조(line 127) → `if len(c.opts.PortBindings) > 0 { res.HostPort = c.opts.PortBindings[0].HostPort }` 안전 인덱싱 패턴. 그 외 fake 메서드의 CreateOptions 사용처 동반 갱신.
- F-1 ~ F-8c 통과 (F-8b/F-8c 가 e2e harness 빌드 검증).
- 이 단계 끝나면 Phase 6 의 모든 동작이 PortBindings 표현으로 회귀 0 통과.

### Step 2 — `WaitTraefikRouters` + `RouterNames` + `EnsureTraefik APIHostPort` 도입

- `internal/agent/traefik_ready.go` 신규 + 단위 테스트 (`traefik_ready_test.go`) — F-9 ~ F-20d.
- `internal/agent/labels.go` 의 `RouterNames(pid, cfg, buildMode)` 추가 + 단위 테스트 — F-35 ~ F-36 (M2 분기 포함).
- `internal/agent/traefik.go` 의 `TraefikSpec.APIHostPort` + `specHash` 입력 갱신 + cmd/PortBindings 분기 + `ErrTraefikPortConflict`(S5) 신규 — F-21 ~ F-26b.
- `internal/agent/config.go` + `config_test.go` — F-37 ~ F-46.
- 이 단계 끝나면 Runner 미통합이지만 모든 인프라 단위 검증 완료.

### Step 3 — Runner 통합 + 분할 + wiring + 회귀

- `internal/agent/runner.go` 의 `traefikAPIPort`/`routerReadyTimeout` 필드 + `Handle` 흐름 5 단계 추가 (waitRouters 호출에 `mode` 인자 전달).
- `internal/agent/runner_ready.go` **신규** — `SetTraefikAPIPort` / `SetRouterReadyTimeout` setter + `waitRouters` 헬퍼(buildMode 분기 + elapsed 로깅 + unknown buildMode WARN). NF-3 분할 정책 충족.
- `internal/agent/runner_ready_test.go` **신규** — F-27 ~ F-34 의 readiness 관련 케이스 전담(NF-3 사전 분리).
- `cmd/agent/main.go` wiring (4-6).
- F-49 ~ F-52 회귀, NF-3 검증(`wc -l` 테스트 파일 포함).
- e2e (F-53) 는 옵션.

---

## 9. 리스크와 완화

| 리스크 | 완화 |
|---|---|
| **R1** 호스트 9080 포트 충돌 (다른 프로세스/Agent 가 이미 사용 중) | ParseConfig 에서 `traefikAPIPort == traefikPort` 케이스만 사전 차단. 일반 충돌은 docker 가 bind 실패로 감지 → `EnsureTraefik` 가 에러 로그 + Agent 가 fail-fast (Phase 6 결정 4 의 fatal 정책 그대로). 운영 가이드에 "9080 이 점유된 환경에선 `--traefik-api-port` 로 변경" 명시. |
| **R2** Traefik 컨테이너 자체가 8080 listener 를 열기 전에 Agent 가 폴링 시작 → connection refused 누적 | `WaitTraefikRouters` 가 네트워크 에러를 lastErr 로 보관하고 polling 사이클을 계속(§4-3 응답 표). Traefik 부팅 자체는 보통 1 s 미만. 30 s timeout 안에서 충분한 재시도 횟수 확보. F-17 단위 테스트로 검증. |
| **R3** `specHash` 변경(APIHostPort 신규 입력 포함) 으로 기존 Phase 6 운영자의 Traefik 컨테이너가 Phase 7 첫 부팅 시 stop+rm+재생성됨 → 진행 중인 다른 PR 의 라우팅이 잠시(< 5 s) 끊김 | 의도된 동작이며 운영 가이드에 "Phase 7 첫 적용 시 Traefik 1회 재시작" 명시. 재시작 동안 진행 중 PR 은 라우터 활성화 잃음 — 그러나 Traefik 재시작 후 Docker provider 가 즉시 재발견. 30 s 안에서 자가 회복. 운영자 권고: 트래픽 적은 시간대 적용. |
| **R4** `--api.insecure=true` 의 보안 노출 — API endpoint 가 인증 없이 라우터 목록/서비스 메타 모두 공개 | (a) 결정 7 의 HostIP=127.0.0.1 바인딩으로 외부 노출 차단. (b) `--api.dashboard=false` 유지로 dashboard 미노출. (c) 운영 가이드에 "별도 firewall 권장" 명시. (d) 후속 Phase 의 인증 도입 plan 명시. |
| **R5** `WaitTraefikRouters` 의 polling 이 `--max-jobs > 1` 환경에서 동시 다수 호출 → Traefik API 가 throttle 또는 hang | 각 polling 요청은 `http.Client{Timeout: 1s}` 로 개별 타임아웃. Polling 사이클이 250 ms cap 이라 동시 16 PR × 4 req/s = 64 req/s — Traefik API 부하 무시 가능. NF-14 로 단위 검증. |
| **R6** 운영자가 readiness 를 명시적으로 비활성화하고 싶을 때 (예: 디버깅, race condition 의도적 재현) | `--traefik-api-port=0` 또는 `--router-ready-timeout=0` 두 경로 모두 probe disabled. 결정 11 의 `waitRouters` 가 두 케이스 모두 즉시 return. F-30, F-31 로 단위 검증. |
| **R7** `traefik_ready.go` 가 외부 의존(`net/http`) 만 쓰지만, polling 횟수 누적으로 Agent 메모리/goroutine leak 발생 가능성 | 함수 내부 새로 생성한 `*http.Client` 가 `Handle` 종료 시 GC. goroutine 추가 spawn 0 (메인 goroutine 위에서 동기 폴링). leak 가능성 0. 코드 리뷰 항목으로 명시. |
| **R8** Test 환경의 시계 정확도 부족으로 backoff 검증(F-19) 가 flaky | F-19 는 ±20% 허용 + 짧은 timeout(예: 500 ms) + 작은 `traefikPollInitial` (테스트 시 1 ms) 로 안정화. polling 횟수만 검증하고 절대 시간은 검증 X 로 변경 가능. |
| **R9** `RouterNames` 와 `ServiceLabels` 의 routerName 표현이 어긋남 (코드 변경 시 한쪽만 갱신) | F-36 단위 테스트가 `RouterNames(pid, cfg)` 와 `ServiceLabels` 내부 키 prefix 를 직접 비교. 변경 시 silent bug 차단. |
| **R10** Phase 6 의 R1 (running 직후 1~5 s race) 가 본 Phase 로 완전 해소되지 않을 수 있다 — Traefik provider 가 라우터를 enabled 표시한 직후에도 백엔드 컨테이너 자체가 ready 가 아닐 수 있음 | 본 Phase 는 "Traefik 라우팅 활성화"만 보장. 백엔드 ready 는 비목표(§1-3). 운영자가 백엔드 healthcheck 가 필요하면 후속 Phase. README 에 명시. |
| **R11** `cmd/agent/docker_sdk.go` 의 HostIP 분기(`ContainerPort == 80 → 0.0.0.0, 그 외 → 127.0.0.1`) 가 향후 다른 사용처(예: DB 5432 외부 노출)에서 비직관적 | 본 Phase 의 사용처는 Traefik 1 곳뿐. 향후 신규 사용처 추가 시 `PortBinding.HostIP string` 필드 도입 검토 — 후속 Phase 의 결정 항목. 본 Phase 는 hardcode 분기로 충분. |

---

## 10. 다음 Phase 연결점

- **Phase 8 후보 1** — 앱 healthcheck 표준 (`/healthz` + `.preview.yml.healthcheck.{path,interval,retries}`). Traefik 라우터 enabled + 앱 200 응답까지 모두 ready 로 통합.
- **Phase 8 후보 2** — Traefik dashboard + API 인증(JWT/mTLS). `--api.insecure=true` 제거 + 인증된 endpoint 로 폴링.
- **Phase 8 후보 3** — `PortBinding.HostIP string` 필드 도입 (R11 해소).
- **Phase 8 후보 4** — `STATUS_UPDATE` 와이어 확장 — `warning_message` 필드 도입 + readiness timeout 시 운영자 가시성 향상.
- **Phase 8 후보 5** — Traefik file provider 모드(Hub 가 dynamic config 직접 작성). 본 Phase 의 폴링은 Docker provider 한정 — file provider 채택 시 readiness 의미 재정의 필요.
- **Phase 8 후보 6** — Phase 6 에서 이월된 worktree orphan 후속 보강(R10 of phase-6).
- **Phase 8 후보 7** — Multi-arch / Windows agent (Phase 6 후보 7 그대로 이월).

---

## 11. 미해결 / 확인 사항 (Open Questions)

| ID | 질문 | 잠정 처리 |
|---|---|---|
| Q1 | `WaitTraefikRouters` 의 timeout 기본값 30 s 가 Traefik provider throttle(default 2 s) 에 비해 너무 긴가? | 보수적으로 30 s 유지. 실 운영 측정치가 누적되면 후속 Phase 에서 default 10 s 로 단축 검토. 운영자가 즉시 `--router-ready-timeout` 으로 조정 가능. |
| Q2 | `PortBinding` 에 `HostIP string` 필드를 본 Phase 에서 같이 도입할까? | 본 Phase 비범위(R11). Traefik 1 곳에서만 분기 필요. 신규 사용처 추가 시 도입. |
| Q3 | `--api.insecure=true` 의 보안 우려를 해소할 임시 방안 (UNIX socket, network policy 등)? | 본 Phase 는 loopback bind + firewall 가이드만. UNIX socket 은 Traefik 이미지 미지원(결정 7 대안 기각). 인증은 후속 Phase. |
| Q4 | `agent_traefik_ready` 로그에 polling 횟수 / 경과 시간을 같이 기록해 운영 디버깅 강화할까? | **결정(채택)**: 본 Phase 의 결정 11 에 반영 — `Runner.waitRouters` 가 `time.Now()` 로 elapsed 를 측정해 로그에 `elapsed_ms` 키로 기록. backoff 가 100 ms → 250 ms cap 가변이라 정확한 횟수 역산은 어렵지만, elapsed_ms 자체가 rough estimate (디버깅 단서) 로 충분. `WaitTraefikRouters` 시그니처는 단순함 유지(반환값 단일 error). 정확한 polling 횟수가 필요해지면 후속 Phase 에서 시그니처 확장. |
| Q5 | `RouterReadyTimeout=0` 의 의미가 "probe 비활성" 인지 "무한 대기" 인지 운영자에게 혼동 가능성? | 결정 11 에서 "비활성" 으로 정의. README + ParseConfig help 문구에 명시. 무한 대기는 절대 허용 X (R6 의 slot lock 문제). |
| Q6 | Phase 6 의 e2e harness(F-56) 와의 통합 — 본 Phase 의 readiness 가 e2e 의 race 를 줄여 e2e flakiness 도 동반 해소될 가능성? | 잠정: 본 Phase 의 e2e 는 fake Traefik(F-53) 만. Phase 6 e2e 는 별도. 둘의 통합은 후속 Phase. |
| Q7 | **(M4)** Traefik v3.1 의 `/api/http/routers/{id}` 응답 `status` 필드의 가능한 enum 값 공식 문서 확인 — 본 기획서가 가정하는 값(`enabled`, `disabled`, `warning`, 빈값) 외에 v3.1 에서 신규 status 가 있는지? | **잠정 처리**: 본 Phase 는 결정 1 의 보수적 정책("enabled 정확 일치만 ready, 그 외 모두 미준비") 으로 신규 status 가 추가되어도 미준비로 폴링 계속 — 안전. 단, **plan-review 가 Traefik v3.1 공식 문서/소스 인용을 요청** → Step 2 구현 직전에 `traefik/v3.1.x` 소스(`pkg/api/handler_http.go` 의 `RouterRepresentation.Status` 가능 값) 또는 official docs (`https://doc.traefik.io/traefik/v3.1/operations/api/`) 1 회 확인 + 결과를 본 Q7 결과로 갱신. 신규 enum 발견 시 §4-3 응답 표 + 결정 1 갱신. |
| Q8 | **(S1)** SDK 어댑터의 `ContainerInspectResult.HostPort` 채움 로직(80/tcp 우선 분기 — Phase 6 docker_sdk.go 의 `if portKey == "80/tcp"` 우선 검사) 이 Phase 7 의 multi-port 컨테이너(Traefik 80 + 8080) 에서 의도와 다르게 동작할 수 있는가? | **본 Phase 변경 없음**. orphan 복원 대상은 Phase 6 와 동일하게 단일-포트 preview 컨테이너 1 개로 한정 — Traefik 자체는 orphan 복원 대상 X(`hub-preview-id` 라벨 미부착). 따라서 80/tcp 우선 분기가 본 Phase 동작에 영향 없음. **Phase 8 검토 항목**: multi-port preview 컨테이너(예: DB+app) 가 도입되면 `ContainerInspectResult` 가 단일 `HostPort` 로 다중 바인딩 표현 못함 → `HostPorts []PortBinding` 등으로 확장 필요. |

이후 새 Q 가 발견되면 본 섹션 아래에 (Q9, Q10 …) 으로 추가하고, plan-review 단계에서 다시 결의/비범위로 분리한다.

---

### Self-review / plan-reviewer 1차에서 처리된 항목 (REVIEW_REVISED)

plan-reviewer 가 반환한 치명/중요/경미 11 건 모두 반영:

| 라운드 1 지적 | 처리 결과 |
|---|---|
| **M1** `tests/e2e/agent_harness_test.go` 영향 누락 | §4-7 영향 파일 표 + §8 Step 1 + §4-8 디렉토리 트리 + F-8b/F-8c 추가. 빈 슬라이스 안전 인덱싱(`if len > 0`) 패턴 명시. |
| **M2** Dockerfile 모드의 `RouterNames` 가 services 전체 반환 시 항상 timeout | 결정 8 갱신: `RouterNames(pid, cfg, buildMode)` 시그니처로 buildMode 인자 추가. dockerfile 모드는 `cfg.FirstService()` 1 개만 반환. §4-5 호출 흐름 + §결정 11 코드 + §5-4 신규 시퀀스 + F-29/F-35/F-35b/F-35c/F-35d 갱신·신설. |
| **M3** Traefik `status:"warning"` 미정의 | 결정 1 에 "준비 판정 규칙" 단락 추가 — enabled 정확 일치만 ready, 그 외(warning/disabled/빈값) 모두 미준비. §4-3 응답 표에 warning + 빈값 행 추가. §1-3 비목표에 미들웨어 자체 ready 항목 추가. F-20b/F-20c 신설. |
| **M4** Traefik v3.1 status enum 공식 근거 | §11 Q7 신규: Step 2 구현 직전 v3.1 소스/문서 확인 + 결과 갱신. 결정 1 의 보수적 정책으로 신규 enum 도 안전하게 처리 가능. |
| **S1** ContainerInspectResult.HostPort 80/tcp 우선 분기 | §11 Q8 신규: Phase 7 변경 없음 명시. multi-port 컨테이너 도입 시 Phase 8 검토. §4-7 영향 파일 표의 docker_sdk.go 줄에도 명시. |
| **S2** F-19 와 R8 모순 | F-19 갱신: 패키지 변수 swap(`traefikPollInitial=1ms`, `traefikPollMax=4ms`) + polling 횟수 검증(±1 회 허용), wallclock 비검증으로 일원화. R8 도 같은 정책으로 일관. |
| **S3** NF-15 의 ctx.Done() 즉각 반응 약속 vs http.Client Timeout=1s 지연 | §4-3 응답 표의 `ctx.Done()` 행 + NF-15 갱신: `http.NewRequestWithContext` 로 in-flight cancel 보장 + sleep select 즉시 반응 + "최대 1 s 지연 후 ctx.Err() 반환" 약속으로 약화. |
| **S4** entrypoints.traefik default(:8080) 버전 변경 리스크 | §4-2 EnsureTraefik cmd 분기에 `--entrypoints.traefik.address=:8080` 명시 추가(redundant but forward-compat). |
| **S5** EnsureTraefik 의 동일 포트 방어 부재 | §4-2 에 `ErrTraefikPortConflict` sentinel + 진입 즉시 방어 코드 + F-26/F-26b 갱신. ParseConfig 1차 + EnsureTraefik 2차 두 방어선. |
| **경미-1** dto Name 필드 활용 | §4-3 routerStatus 주석에 디버깅 로그 활용 명시(응답 name 미스매치 감지). F-20d 신설. |
| **경미-2** §5 시퀀스에 Dockerfile 모드 케이스 부재 | §5-4 시퀀스 신설(M2 와 연동). |
| **경미-3** runner.go 가 300 줄 근접 | §결정 11 + §4-7 + NF-3 + §8 Step 3: `runner_ready.go` 신규 분리 정책 명시. |
| **경미-4** Q4 의 "Step 3 구현 시 결정" 이 plan-review 의의와 충돌 | Q4 갱신 — 기획서에서 elapsed_ms 로깅 채택 결정. 결정 11 코드에 반영. |

#### plan-reviewer 2차 (필수 3 + 권장 5)

| 라운드 2 지적 | 처리 결과 |
|---|---|
| **2-1 (필수)** §결정 12 와 §4-2 의 EnsureTraefik cmd 분기 불일치 | 결정 12 코드 블록에 `--entrypoints.traefik.address=:8080` 추가, "default 그대로" 주석 제거, 근거 단락에 forward-compat 사유 추가. §4-2 와 일치. |
| **2-2 (필수)** §2-2 의 사실 오류 — Traefik 80 이 미바인딩이라고 잘못 기재 | §2-2 의 expose-only 단락 수정 — Traefik 80 + 8080 양쪽 다 호스트 바인딩, expose-only 의 실제 sample 은 Dockerfile 모드의 `svc.Port` (HostPort=0). 두 케이스로 명확히 분리 기술. |
| **2-3 (필수)** F-35d 의 logger 책임이 결정 8 시그니처와 모순 | (b) 안 채택 — `RouterNames` 시그니처 무변경(logger 인자 없음, 순수 함수, 빈 슬라이스만 반환). 결정 8 의 함수 주석에 "unknown 시 빈 슬라이스 반환, 로깅 책임 호출자" 명시. `Runner.waitRouters` 본문에 buildMode 검사 + `agent_traefik_ready_unknown_buildmode` WARN 1 회 기록 코드 추가. F-35d 의 검증 방법도 양쪽 함수에 분리 명시. |
| **2-4 (권장)** §4-4 ParseConfig 의 string → Duration 변환 단계 누락 | 검증 단락을 코드 블록으로 확장 — 4 단계(범위 → 동일 포트 차단 → ParseDuration → Config 채움) 명시. 음수 / 파싱 실패 / 동일 포트 모두 에러 case 코드 표현. |
| **2-5 (권장)** §4-2 의 `spec.HostPort > 0` 전제 명시 | §4-2 모듈 시작에 "전제: spec.HostPort > 0, Phase 6 의 ParseConfig 1..65535 검증으로 보장" 1 단락 추가. |
| **2-6 (권장)** F-19 expected polling count 수치 범위 명시 | 횟수 계산 근거(누적 sleep ~17 ms 도달까지 6 회 시도) + 허용 범위 5~7 회(±1) 명시. |
| **2-7 (권장)** "elapsed/150 으로 역산 가능" 표현 부정확 | 결정 11 단락 + Q4 둘 다 완화 — backoff 가변이라 정확 역산 어렵지만 rough estimate(<100ms 첫 시도 성공, ≥1s 다회 재시도) 디버깅 단서로 충분. 정확한 횟수 필요 시 후속 Phase 시그니처 확장. |
| **2-8 (권장)** NF-3 검증 대상에 테스트 파일 누락 | NF-3 의 wc 대상에 `internal/agent/*_test.go` 추가, runner_test.go 분리 정책으로 `runner_ready_test.go` 신규 사전 분리. §4-7 영향 파일 표 + §4-8 디렉토리 트리 + §8 Step 3 + 신규 파일 카운트(3 → 4) 모두 동기화. |

---

(끝)
