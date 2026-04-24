# Preview — GitHub PR 프리뷰 환경 자동 배포 서비스

Vercel Preview의 **셀프호스팅/오픈소스 버전**. GitHub PR이 열리면 해당 브랜치를 자동으로 빌드·실행하여 `pr-{n}.preview.domain` 형태의 프리뷰 URL을 생성한다.

> **Phase 0 상태**: 모노레포 스캐폴딩만 완료. 실제 Webhook/WebSocket/Docker 제어 로직은 Phase 1 이후 추가.

## 아키텍처

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

- **Hub** (`/hub`) — GitHub Webhook 수신, DB, WebSocket 서버, Agent 분배, Reverse Proxy, 관리자 대시보드
- **Agent** (`/agent`) — Hub에 outbound 연결, Pull 방식 작업 수신, Docker 컨테이너 생명주기 관리
- **Shared** (`/shared`) — 공유 타입·Zod 스키마 (메시지 프로토콜의 Single Source of Truth)

## 핵심 설계 결정

| 결정                                     | 이유                                                                          |
| ---------------------------------------- | ----------------------------------------------------------------------------- |
| **Pull 방식 디스패치** (Agent→Hub READY) | 자연스러운 백프레셔. Agent가 capacity 있을 때만 일 받음                       |
| **Agent→Hub outbound WebSocket**         | Agent 머신에 inbound 포트 불필요. NAT/방화벽 뒤에서도 동작                    |
| **토큰 기반 Agent 인증**                 | GitHub Actions self-hosted runner 방식. Hub에서 등록·발급, Agent 설치 시 사용 |
| **Label 기반 라우팅**                    | PR label → 매칭되는 Agent에만 할당. 로컬 개발 시나리오 대응                   |
| **상태 모델**                            | `queued → assigned → building → running → teardown → done \| failed`          |

상세 결정은 `docs/adr/` 참조.

## 기술 스택

| 영역          | 선택                                                   |
| ------------- | ------------------------------------------------------ |
| 언어          | TypeScript 5.x (strict + `exactOptionalPropertyTypes`) |
| 런타임        | Node.js 20 LTS (권장), ≥20.11                          |
| HTTP          | Fastify 4                                              |
| DB            | PostgreSQL 16 (ORM은 Phase 1에서 결정)                 |
| WebSocket     | `ws` (Phase 2부터)                                     |
| Docker 제어   | `dockerode` (Phase 4부터)                              |
| 검증          | Zod                                                    |
| 테스트        | Vitest (단위/통합), Playwright (e2e, Phase 6부터)      |
| 패키지 매니저 | pnpm 9.15.0 (workspace)                                |
| 로컬 환경     | Docker Compose                                         |

## 저장소 구조

```
/
├── hub/        — Control plane (Fastify)
├── agent/      — Agent CLI
├── shared/     — 공유 타입·스키마
├── docs/       — 아키텍처 문서, ADR
├── docker-compose.yml
├── package.json  pnpm-workspace.yaml  tsconfig.base.json
├── eslint.config.js  .prettierrc  .editorconfig
└── .env.example
```

## 사전 요구사항

- **Node.js ≥ 20.11** (권장: Node 20 LTS, `.nvmrc` 참조)
- **pnpm 9.15.0** — `corepack enable` 으로 자동 활성화
  - Windows에서는 관리자 PowerShell로 `corepack enable` 필요할 수 있음
  - Defender 성능을 위해 `%LOCALAPPDATA%\pnpm` 을 제외 경로로 추가 권장
- **Docker Desktop** (WSL2 백엔드 권장, Windows)
- **Git**

## 로컬 실행

```bash
# 1) 의존성 설치 (모든 워크스페이스 링크 자동 생성)
corepack enable
pnpm install

# 2) Postgres 기동
docker compose up -d postgres

# 3) Hub 개발 서버 (포트 3000)
pnpm dev:hub
# 확인: curl http://localhost:3000/
# → {"hello":"hub","shared":"0.0.0"}

# 4) Agent CLI (다른 터미널)
pnpm dev:agent
# → Hello Agent (agent=0.0.0, shared=0.0.0)
```

## 스크립트 레퍼런스

루트에서 실행:

| 명령                           | 설명                               |
| ------------------------------ | ---------------------------------- |
| `pnpm build`                   | 전 워크스페이스 TS 컴파일          |
| `pnpm typecheck`               | 컴파일 없이 타입만 검증            |
| `pnpm lint`                    | ESLint (flat config)               |
| `pnpm format` / `format:check` | Prettier write / check             |
| `pnpm test`                    | Vitest 전체 (현재 테스트 케이스 0) |
| `pnpm dev:hub` / `dev:agent`   | 해당 패키지 watch 개발             |

## Phase 로드맵

- [x] **Phase 0** — 모노레포 스캐폴딩 (현재)
- [ ] **Phase 1** — GitHub Webhook 수신, DB 스키마, PR 레코드 생성
- [ ] **Phase 2** — Agent 등록·토큰 발급, WebSocket 연결, 하트비트
- [ ] **Phase 3** — Job 큐, Pull 방식 디스패치, 상태 머신
- [ ] **Phase 4** — git clone + docker build + run, 포트 보고
- [ ] **Phase 5** — Reverse Proxy (호스트 헤더 라우팅)
- [ ] **Phase 6** — 관리자 대시보드 + Playwright e2e
- [ ] **Phase 7** — PR 닫힘 시 teardown, 장애 복구

## ADR

- [ADR-0001 모노레포 구조](./docs/adr/0001-monorepo-structure.md)

## 라이선스

TBD.
