# Preview — GitHub PR 프리뷰 환경 서비스

## 프로젝트 소개

Vercel Preview의 **셀프호스팅/오픈소스 버전**을 Go로 만드는 프로젝트. GitHub PR이 열리면 자동으로 프리뷰 환경을 띄우고, PR이 닫히면 정리한다.

## 아키텍처

두 컴포넌트로 구성된다.

### Hub (Control Plane)
- GitHub Webhook 수신 (PR `opened`/`synchronize`/`closed` 이벤트)
- 관리자 웹 대시보드 (Phase 3에서 구현. Phase 0~2는 JSON API만)
- Agent와 WebSocket 연결 유지 (Agent가 **outbound**로 연결)
- Job 큐 관리, **Pull 방식**으로 Agent에 작업 분배
- Reverse proxy: `pr-{n}.preview.<base-domain>` 호스트 헤더를 보고 해당 Agent의 포트로 프록시

### Agent (Data Plane)
- Hub에 outbound WebSocket으로 연결
- Pull 방식: `READY` 메시지로 일 요청 → Hub가 대기 중 job 전달
- 책임: git clone → docker build → docker run(포트 동적 할당) → URL을 Hub에 보고 → PR 닫히면 컨테이너 정리
- 하나의 Agent는 **여러 프리뷰 컨테이너를 동시에** 띄울 수 있음
- Agent에 label 붙일 수 있음 (예: `home`, `prod`) → label 기반 라우팅

## 핵심 설계 결정

- **Pull 방식 디스패치**: Agent가 capacity 있을 때만 일 가져감 → 자연스러운 백프레셔
- **Agent → Hub 방향 연결**: Agent 머신에 inbound 포트 불필요, NAT/방화벽 뒤에서도 동작
- **토큰 기반 Agent 인증**: Hub에서 Agent 등록 → 토큰 발급 → Agent 실행 시 토큰 사용 (GitHub Actions self-hosted runner 방식)
- **상태 모델**: `queued → assigned → building → running → teardown → done | failed`
- **Label 기반 라우팅**: PR 또는 Preview에 붙은 label과 매칭되는 Agent에만 할당

## 기술 스택

- Go 1.22+
- `net/http` 표준 라이브러리 (웹 프레임워크 **없이**)
- SQLite via `modernc.org/sqlite` (순수 Go 드라이버, CGO 없음)
- sqlc로 쿼리 코드 생성
- `github.com/coder/websocket` for WebSocket
- Agent의 Docker 제어: `github.com/docker/docker/client`
- Reverse proxy: `net/http/httputil`
- 마이그레이션: `golang-migrate/migrate`
- 개발환경: docker-compose는 "로컬 테스트용 샘플 앱 컨테이너" 정도만. **Hub와 Agent 자체는 `go run`으로 바로 띄움** (단일 바이너리 철학)

## 이식성 원칙 (매우 중요)

SQLite로 시작하지만 나중에 PostgreSQL로 교체 가능하게 설계한다.

### 1. Repository 인터페이스 패턴
- `internal/store` 패키지에 도메인별 repository 인터페이스 정의 (예: `AgentStore`, `PreviewStore`)
- sqlc 생성 코드는 `internal/db/sqlite` 하위에 두고, repository 구현체가 sqlc 코드를 감싸 인터페이스 만족
- 비즈니스 로직은 **항상 인터페이스에만 의존**
- Postgres 이전 시 `internal/db/postgres` 하위에 sqlc 새로 생성하고 repository 구현만 교체

### 2. 표준 SQL만 사용
- SQLite·Postgres 둘 다 지원하는 문법만
- 금지어: SQLite `AUTOINCREMENT` 특수 동작, `INSERT OR REPLACE`, Postgres `jsonb` 전용 연산자
- `RETURNING`은 OK (SQLite 3.35+부터 지원)
- ID는 UUID 문자열(TEXT)
- 타임스탬프는 ISO8601 문자열 또는 Go `time.Time` 파싱
- labels 같은 반정형 데이터는 TEXT(JSON 문자열)로 저장

### 3. 마이그레이션 도구
- `golang-migrate`, 파일은 `db/migrations/`
- 파일명: `0001_create_agents.up.sql` / `0001_create_agents.down.sql`

### 4. DB 드라이버는 환경변수 분기
- `DATABASE_URL=sqlite://./hub.db` 기본
- `DATABASE_URL=postgres://...`로 바꿀 수 있게 URL 파싱 분기

## 프로젝트 구조 (Go 관용 모노레포)

```
/cmd
  /hub/main.go         - Hub 진입점
  /agent/main.go       - Agent 진입점
/internal
  /hub                 - Hub 도메인 로직
  /agent               - Agent 도메인 로직
  /store               - Repository 인터페이스 (DB 추상화)
  /db
    /sqlite            - sqlc 생성 코드 + 구현체
  /protocol            - Hub↔Agent 메시지 타입 (공유)
/db
  /migrations          - SQL 마이그레이션 파일
  /queries             - sqlc용 쿼리 파일
  /schema              - sqlc용 스키마 파일
/docs
  /specs               - Phase 기획서
  /reports             - evaluator 검증 보고서
  /adr                 - 아키텍처 결정 기록 (선택)
go.mod
sqlc.yaml
Makefile
README.md
.env.example
.golangci.yml
```

## 작업 방식 (엄격 준수)

- 코드 작성 전에 항상 **기획서**를 작성한다.
- 기획서는 md 파일로 저장되어야 한다 (`docs/specs/phase-{N}-{slug}.md`).
- 기획서는 반드시 **리뷰**를 거치고 수정하여 최대한 불확실성이 없도록 한다. 리뷰는 **다른 에이전트**가 해야 한다 (`plan-reviewer`).
- 기획서는 설계 이유·구현 아이디어 등 논리적 완벽을 추구한다. 실제 코드가 들어갈 필요는 없다(들어가도 무방).
- **확인 가능한 기능/비기능 체크리스트**를 두어 구현 완료 후 목록을 확인해 구현이 제대로 되었는지 리뷰한다. 리뷰는 다른 에이전트(`evaluator`)가 한다.
- 구현은 될 수 있는 한 **작은 단위**로 한다.
- **주석도 코드다**. 코드와 맞도록 변경되어야 한다.
- 구현 리뷰 후 수정은 **원인과 해결 방법을 메모리에** 적어둔다.
- 변경은 **작은 단위로 나눠서 커밋**한다.
- 리뷰는 코드를 읽는 것뿐만이 아니라 **테스트**를 동반한다.
- 테스트는 단위테스트뿐만이 아니라 **e2e까지** 해야 한다. e2e는 **Playwright**를 이용한다 (UI 있는 Phase부터).
- 배포는 **확인을 받고** 배포한다. (커밋·푸시도 사용자 승인 후)

## 하네스 (에이전트 팀)

이 프로젝트는 `.claude/agents/`, `.claude/skills/`에 하네스가 구성되어 있다.

### 에이전트
- `planner` — Phase 기획서 작성
- `plan-reviewer` — 기획서 엄격 리뷰
- `go-implementer` — 승인 기획서 → Go 구현
- `evaluator` — 기능/비기능 체크리스트 + 단위·e2e 검증

### 스킬
- `spec-writing` — planner용 기획서 템플릿
- `spec-review` — plan-reviewer용 리뷰 체크리스트
- `go-build` — go-implementer용 Go 모노레포 구축 규칙
- `acceptance-test` — evaluator용 검증 파이프라인
- `preview-workflow` — 오케스트레이터(전 Phase 조율)

### 사용법
한 Phase를 진행할 때: `preview-workflow` 스킬을 트리거한다. 오케스트레이터가 PLAN 서브페이즈(planner + plan-reviewer)와 BUILD 서브페이즈(go-implementer + evaluator)를 순차 실행하고, 각 Phase 종료 시 사용자 승인 아래 커밋한다.

## Phase 로드맵 (요약)

- **Phase 0** — 스캐폴딩: 모노레포 구조, 빈 진입점, sqlc.yaml, Makefile, README, `internal/store` 인터페이스 껍데기
- Phase 1 이후 — 웹훅, DB 스키마, Agent 등록/토큰, WebSocket 연결, Job 큐, 디스패치, Docker 제어, Reverse proxy, 관리자 UI …

각 Phase 시작 전 `preview-workflow`를 트리거하고, 사용자 요구사항 → 기획서 → 리뷰 → 구현 → 검증 → 커밋 사이클을 밟는다.

## 코딩 규칙

Go 코드 작성 규칙은 `go-build` 스킬에 정의되어 있다.
`plan-reviewer`와 `evaluator`는 해당 스킬의 체크리스트를 기준으로 리뷰한다.

핵심 원칙만 요약:
- 레이어 의존성은 단방향 (cmd → hub/agent → store → db). 역방향·순환 금지.
- 파일 300줄 초과 시 분할. 각 파일 상단에 "책임" 주석 3~5줄.
- 상태 전이(`preview.status`)는 단일 진입점에서만. DB 연속 쓰기는 트랜잭션.
- SQLite 전용 문법 금지 (이식성 원칙 참조).