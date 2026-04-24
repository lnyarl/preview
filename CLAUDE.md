# Preview — GitHub PR 프리뷰 환경 자동 배포 서비스

Vercel Preview의 셀프호스팅/오픈소스 버전. GitHub PR이 열릴 때마다 해당 브랜치를 자동으로 빌드·실행하여 `pr-{n}.preview.domain` 형태의 프리뷰 URL을 생성한다.

## 아키텍처 한눈에

```
  GitHub ──webhook──▶ ┌─────────────┐  Reverse Proxy (호스트 헤더 기반)
                     │     Hub      │ ◀──── pr-{n}.preview.domain
                     │ (Control     │
                     │   Plane)     │
                     └──────┬──────┘
                            │ WebSocket (outbound from agent)
                            │ Pull 방식: Agent가 READY → Hub가 JOB 전달
                            ▼
                     ┌─────────────┐  git clone / docker build / docker run
                     │    Agent    │───────────▶ [container:port]
                     │  (여러 개)  │
                     └─────────────┘
```

**두 컴포넌트**:

- **Hub** (`/hub`) — GitHub Webhook 수신, DB, WebSocket 서버, Agent 분배, Reverse Proxy, 관리자 대시보드
- **Agent** (`/agent`) — Hub에 outbound 연결, Pull 방식 작업 수신, Docker 컨테이너 생명주기 관리

## 핵심 설계 결정

| 결정                                     | 이유                                                                          |
| ---------------------------------------- | ----------------------------------------------------------------------------- |
| **Pull 방식 디스패치** (Agent→Hub READY) | 자연스러운 백프레셔. Agent가 capacity 있을 때만 일 받음                       |
| **Agent→Hub outbound WebSocket**         | Agent 머신에 inbound 포트 불필요. NAT/방화벽 뒤에서도 동작                    |
| **토큰 기반 Agent 인증**                 | GitHub Actions self-hosted runner 방식. Hub에서 등록·발급, Agent 설치 시 사용 |
| **Label 기반 라우팅**                    | PR에 label 지정 → 매칭되는 Agent에만 할당. 로컬 개발 시나리오 대응            |
| **상태 모델**                            | `queued → assigned → building → running → teardown → done \| failed`          |

상세 결정은 `docs/adr/` 참조.

## 기술 스택

| 영역          | 선택                                                      |
| ------------- | --------------------------------------------------------- |
| 언어          | TypeScript 5.x (strict mode)                              |
| 런타임        | Node.js 20 LTS                                            |
| HTTP          | Fastify                                                   |
| DB            | PostgreSQL 16 (ORM은 Phase 1에서 결정 — Prisma vs Kysely) |
| WebSocket     | `ws`                                                      |
| Docker 제어   | `dockerode`                                               |
| Reverse Proxy | `http-proxy` 또는 Fastify 플러그인                        |
| 검증          | Zod (공유 메시지 스키마)                                  |
| 테스트        | Vitest                                                    |
| 패키지 매니저 | pnpm (workspace)                                          |
| 로컬 환경     | Docker Compose (Postgres + Hub)                           |
| 프론트엔드    | Phase 1에서 결정 (HTMX+SSR vs 간단한 React)               |

## 모노레포 구조

```
/
├── hub/        — Control plane (Fastify 서버)
├── agent/      — Agent CLI
├── shared/     — 공유 타입·Zod 스키마 (메시지 프로토콜의 SSoT)
├── docs/       — 아키텍처 문서, ADR
├── _workspace/ — 팀 중간 산출물 (gitignored)
├── docker-compose.yml
└── README.md
```

## 코딩 컨벤션

- **TypeScript strict + noUncheckedIndexedAccess + exactOptionalPropertyTypes**
- **Zod를 신뢰하라**: JSON.parse 후 as 캐스팅 금지. 경계면에서 safeParse로 한 번 검증.
- **Fastify 플러그인 패턴**: 라우트는 도메인별 플러그인으로 분리.
- **DB 액세스는 repository 레이어에**: 라우트에서 직접 SQL/ORM 호출 금지.
- **ID/시간 통일**: nanoid (21자) / epoch ms.
- **타임아웃 필수**: 모든 외부 호출(git, docker, HTTP)에 명시적 타임아웃 + AbortSignal.
- **실패 경로 정리**: try/finally로 컨테이너/임시 디렉토리 정리.
- **YAGNI**: 현 Phase에 없는 기능을 위한 추상화 금지. 3번 반복되면 그때 리팩터.

## Phase 로드맵

- [ ] **Phase 0** — 모노레포 스캐폴딩 (이 문서가 만들어지는 시점)
- [ ] **Phase 1** — GitHub Webhook 수신, DB 스키마, PR 레코드 생성
- [ ] **Phase 2** — Agent 등록·토큰 발급, WebSocket 연결, 하트비트
- [ ] **Phase 3** — Job 큐, Pull 방식 디스패치, 상태 머신
- [ ] **Phase 4** — Agent의 git clone + docker build + run, 포트 보고
- [ ] **Phase 5** — Reverse Proxy (호스트 헤더 라우팅)
- [ ] **Phase 6** — 관리자 대시보드
- [ ] **Phase 7** — PR 닫힘 시 teardown, 장애 복구

## 작업 방식 — 하네스

이 프로젝트는 **전문 에이전트 팀** 하네스로 작업한다. `/preview-team`이 고정 팀:

| 에이전트       | 담당                                |
| -------------- | ----------------------------------- |
| `architect`    | Phase 계획·ADR·작업 분해            |
| `protocol-dev` | `/shared` 전담 (메시지·타입·Zod)    |
| `hub-dev`      | `/hub` (서버·DB·WS·프록시·대시보드) |
| `agent-dev`    | `/agent` (CLI·Docker·WS 클라이언트) |
| `qa-reviewer`  | 경계면 정합성·빌드·설계 일관성      |

스킬:

- **`/phase-playbook`** — Phase 진행 오케스트레이터. 새 Phase 시작 시 호출.
- **`/monorepo-scaffold`** — 모노레포 초기 구조 (Phase 0 전용).
- **`/ws-protocol-design`** — WebSocket 메시지 추가·변경.
- **`/boundary-qa`** — 경계면 검증 (qa-reviewer가 사용).

### 새 Phase 시작 시

사용자가 "Phase N 진행해줘"라고 하면 `phase-playbook` 스킬을 통해:

1. `architect`가 `_workspace/phase-{N}-plan.md` 작성 + 작업 분해 + ADR 필요 시 작성
2. `protocol-dev`가 먼저 `/shared` 변경 (다른 작업의 블로커)
3. `hub-dev` / `agent-dev`가 병렬 구현
4. 각 모듈 완료 직후 `qa-reviewer`가 검토 (Incremental QA)
5. Phase 종료 시 summary 작성 후 사용자 확인

### 중요 원칙

- **경계면 먼저 정한다**: 구현 전에 `/shared`의 contract를 확정한다. 그 후 양쪽 병렬 개발.
- **Incremental QA**: 전체 완성 후 한 번이 아니라, 모듈 단위로 즉시 검증.
- **Docs/ADR은 가볍게**: 중요한 결정만 ADR로, 모든 결정을 기록하지 않는다.

## 로컬 실행 (Phase 0 완료 후)

```bash
pnpm install
docker compose up -d postgres
pnpm --filter @preview/hub dev      # Hub 실행
pnpm --filter @preview/agent start  # Agent 실행 (다른 터미널)
```

## 환경

- OS: Windows 11 (사용자 환경)
- Shell: bash (Unix 문법, NOT PowerShell 문법)
- 경로: Windows 경로지만 bash에선 `/c/Users/...` 형식 사용 가능

## 참고

- 하네스 상세: `.claude/agents/`, `.claude/skills/`
- 아키텍처 결정: `docs/adr/`
- 중간 산출물: `_workspace/` (팀 작업 추적용, gitignored)

## 작업 방식

- 코드 작성전에 항상 기획서를 작성한다.
- 기획서는 md파일로 저장되어야 한다.
- 기획서는 반드시 리뷰를 거치고 수정하여 최대한 불확실성이 없도록 한다. 리뷰는 다른 에이전트가 해야한다.
- 기획서는 설계 이유나, 구현 아이디어등 논리적인 완벽을 추구한다. 실제 코드가 들어갈 필요는 없다.(들어가도 상관없음)
- 확인 가능한 기능/ 비기능 목록을 두어 구현이 완료된 뒤에 목록을 확인하여 구현이 제대로 되었는지 리뷰해야 한다. 리뷰는 다른 에이전트가 해야한다. (evaluator)
- 구현은 될 수 있는한 작은 단위로 한다.
- 주석도 코드다. 코드와 맞도록 변경되어야 한다.
- 구현 리뷰후 수정은 원인과 해결 방법을 메모리에 적어둔다.
- 변경은 작은 단위로 나눠서 커밋한다.
- 리뷰는 코드를 읽는것 뿐만이 아니라 테스트를 동반한다.
- 테스트는 단위테스트뿐만이 아니라 e2e테스트까지 해야한다. e2e테스트는 playwright를 이용한다.
- 배포는 확인을 받고 배포한다.
