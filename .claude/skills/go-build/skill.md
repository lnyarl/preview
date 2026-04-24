---
name: go-build
description: Preview 프로젝트(Go 기반 PR Preview 서비스)에 Go 코드를 구현할 때 사용한다. 모노레포 구조, sqlc 설정, modernc.org/sqlite 드라이버, Repository 인터페이스 패턴, 표준 SQL, 레이어링, 네이밍, 에러 처리, 이식성 원칙, 작은 단위 커밋을 모두 강제한다. go-implementer 에이전트의 주력 스킬. "Go 구현", "Hub 코드", "Agent 코드", "sqlc 생성", "Makefile", "리팩터링" 관련 작업에서 반드시 트리거.
---

# Go Build 스킬

프로젝트 정체성·아키텍처·기술 스택은 `AGENTS.md`에 있다. 이 스킬은 **코드를 실제로 작성·리뷰할 때 따르는 규칙**만 담는다.

## 1. 레이어와 의존성 방향

의존성은 **위→아래 단방향**. 역방향·순환 금지.

```
cmd/*             → internal/{hub,agent}
internal/hub      → internal/{store, protocol, platform}
internal/agent    → internal/{protocol, platform}
internal/store    → (인터페이스만. 구현·외부 import 금지)
internal/db/*     → 외부 DB 드라이버
internal/protocol → 의존성 없음 (순수 Go 타입)
internal/platform → 외부 어댑터 (Docker SDK, git, GitHub API)
```

**Import 금지 매트릭스**

| 이 패키지가 | 이 패키지를 import하면 위반 |
|---|---|
| `cmd/*` | `internal/db/*` (store 인터페이스 경유 필수) |
| `internal/hub` | `internal/db/*`, `internal/agent` |
| `internal/agent` | `internal/db/*`, `internal/hub` |
| `internal/store` | `internal/db/*`, sqlc 생성 타입 |
| `internal/protocol` | 어떤 내부 패키지도 금지 |

`internal/store`는 도메인 타입으로 값을 반환한다. sqlc 생성 구조체를 그대로 노출하면 이식성 원칙 위반.

## 2. sqlc 설정

`sqlc.yaml`:

```yaml
version: "2"
sql:
  - engine: "sqlite"
    queries: "db/queries"
    schema: "db/schema"
    gen:
      go:
        package: "sqlitestore"
        out: "internal/db/sqlite"
        sql_package: "database/sql"
        emit_json_tags: true
        emit_prepared_queries: false
        emit_interface: true
        emit_empty_slices: true
```

- `emit_interface: true` — Querier 인터페이스 생성, store 어댑터가 감싸기 편함.
- 생성 경로는 `internal/db/sqlite`. 위 매트릭스에 따라 **`internal/hub`·`internal/agent`에서 직접 import 금지**.

## 3. 표준 SQL 금지어 사전

이식성 원칙의 실무 버전. 코드 리뷰에서 가장 먼저 체크.

| 범주 | 금지 | 대안 |
|---|---|---|
| SQLite 전용 | `AUTOINCREMENT`, `INSERT OR REPLACE`, `INSERT OR IGNORE` | UUID TEXT + `INSERT ... ON CONFLICT ... DO UPDATE` |
| Postgres 전용 | `SERIAL`, `BIGSERIAL`, `jsonb` 연산자(`->`, `@>`, `?`) | UUID TEXT + Go 쪽 JSON 처리 |
| 타입 | BOOLEAN(SQLite 엔진 호환성 이슈), TIMESTAMP(파싱 차이) | INTEGER(0/1), TEXT(ISO8601) |
| 식별자 | AUTOINCREMENT 정수 ID | UUID 문자열 (TEXT PK) |

`RETURNING`은 양쪽 지원(SQLite 3.35+). OK.

반정형 데이터(labels 등)는 **TEXT 컬럼 + Go `encoding/json`**. DB JSON 함수 사용 금지.

## 4. 파일 구성

### 파일 크기
- **300줄 초과 시 분할**.
- 분할 기준: 구조체·인터페이스 5개 이상 → 별도 파일. 도메인별 핸들러 → `<domain>_handler.go`.
- `main.go`는 조립만. 100줄 이하 목표.

### 파일 상단 템플릿

```go
// Package agent implements the agent-side job runner and Hub client.
//
// 이 파일의 책임:
// - Hub와의 WebSocket 연결 유지와 재연결
// - HELLO / PING / PONG 메시지 처리
// - 상위 계층(Runner)에 JOB_ASSIGN 이벤트 전달
package agent

import (
    // 1) 표준 라이브러리
    "context"
    "fmt"

    // 2) 외부 패키지
    "github.com/coder/websocket"

    // 3) 내부 패키지
    "github.com/<user>/preview/internal/protocol"
)
```

- **파일 책임 주석 3~5줄 필수** (없으면 리뷰 거부).
- import 3그룹 빈 줄로 구분.
- 이후 순서: 상수 → 타입 → 생성자 → 공개 메서드 → 비공개 메서드.

## 5. 네이밍

| 대상 | 규칙 | 예 |
|---|---|---|
| 파일 | snake_case | `agent_store.go`, `job_dispatcher.go` |
| 패키지 | 단수·짧게·소문자 | `agent`(O), `agents`(X) |
| Repository 인터페이스 | `<Domain>Store` | `AgentStore`, `PreviewStore` |
| Repository 구현 | `sqlite<Domain>Store` | `sqliteAgentStore` |
| HTTP 핸들러 | `<Domain>Handler` | `AgentHandler` |
| 서비스 | `<Domain>Service` | `DispatcherService` |
| 조회 메서드 | `Get*`(단건) / `List*`(다건) / `Find*`(없을 수도) | `GetAgent`, `ListAgents` |
| 변경 메서드 | `Create`, `Update<Field>`, `Delete` | `UpdateAgentStatus` |
| Sentinel 에러 | 각 패키지 `errors.go`에 모음 | `store.ErrAgentNotFound` |

## 6. Go 관용구

- **공개 식별자는 godoc**. 주석은 식별자 이름으로 시작. `// AgentStore provides...`
- `context.Context`는 **첫 번째 인자**.
- **에러 wrapping 형식**:
  ```go
  return fmt.Errorf("<pkg>.<func>: <무엇을 하다 실패>: %w", err)
  // 예: "dispatcher.claim: update preview status: %w"
  ```
- Sentinel 에러는 `var ErrX = errors.New(...)`, 호출 측은 `errors.Is`로 비교.
- **로깅은 `log/slog` 구조화 로깅**:
  ```go
  logger.Info("preview claimed",
      "preview_id", preview.ID,
      "agent_id", agent.ID,
  )
  ```
  문자열 포맷에 값 박지 않음. 토큰·시크릿 로깅 금지.
- goroutine 생성 시 **종료 경로 필수** (context cancel, done 채널).
- 의존성은 **생성자 주입**. 전역 mutable 상태·`init()`·`panic()`(main 초기화 외) 금지.
- `map[string]interface{}` / `any` 남용 금지. 구조체 정의.

## 7. 상태 전이·트랜잭션

- 도메인 상태(`preview.status` 등) 변경은 **단일 진입점에서만**. 예: `DispatcherService` 외 다른 곳에서 `previews.status` UPDATE 금지.
- 두 개 이상 DB 쓰기가 연속되면 **반드시 트랜잭션**.
- 상태 변경 시 `preview_events` 이벤트 기록 (Go 헬퍼 경유, SQL 트리거 사용 금지 — 이식성).

## 8. 테스트

- `*_test.go` 같은 디렉토리.
- **테스트는 사용 예제**. 테이블 드리븐 선호.
- 외부 의존 있는 테스트는 `*_integration_test.go` + `//go:build integration` 태그.
- 스킵·주석처리로 실패 회피 금지. 원인 수정.

## 9. Makefile

Windows(bash) 호환. `docker` 필요한 타겟은 분리.

필수 타겟:

```make
.PHONY: build run-hub run-agent sqlc migrate-up migrate-down test vet lint fmt

build:
	go build -o bin/hub ./cmd/hub
	go build -o bin/agent ./cmd/agent

run-hub:
	go run ./cmd/hub

run-agent:
	go run ./cmd/agent

sqlc:
	sqlc generate

migrate-up:
	migrate -path db/migrations -database "$$DATABASE_URL" up

migrate-down:
	migrate -path db/migrations -database "$$DATABASE_URL" down 1

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	gofmt -w .
```

## 10. 커밋 규칙

- 한 커밋 = 한 논리적 변경. 가능한 한 기획서 체크리스트 항목과 1:1.
- 메시지: `type(scope): subject` (Conventional Commits).
  - 예: `feat(hub): add /health endpoint`, `chore: init Go module`, `refactor(store): extract AgentStore interface`
- **`git commit`·`git push`는 사용자 승인 후에만**. 평소엔 파일 변경까지.

## 11. 제출 전 빌드 위생

evaluator에 넘기기 전 **세 명령 모두 통과** 필수. 실패 시 원인 수정, 우회 금지.

```
go vet ./...
go build ./...
go test ./...
```

`--no-verify`, 테스트 skip/주석처리, vet 경고 무시 전부 금지.

## 12. 작업 흐름

1. 승인된 Phase 기획서를 Read.
2. 체크리스트 항목을 Task로 분할 (TaskCreate).
3. 항목별로 in_progress → 구현 → 빌드 위생(§11) → completed.
4. 기획서 모호성 발견 시 `SendMessage`로 planner에 질의. 외부 의존 블록 시 리더에 질의.
5. 전체 완료 시 `SendMessage`로 evaluator에게 `{구현 파일 목록, 체크리스트 매핑}` 전달.

## 13. AI가 자주 저지르는 실수 (사전 차단)

리뷰·구현 시 다음 패턴을 먼저 확인:

- [ ] **레이어 뚫기**: `cmd/`에서 `internal/db/*` 직접 import (§1 매트릭스 위반)
- [ ] **상태 전이 분산**: 여러 곳에서 `preview.status` UPDATE (§7)
- [ ] **트랜잭션 누락**: DB 연속 쓰기인데 단일 `tx` 미사용 (§7)
- [ ] **SQLite 전용 문법**: `AUTOINCREMENT`, `INSERT OR REPLACE` 등 (§3)
- [ ] **sqlc 타입 노출**: `internal/store` 반환 타입이 sqlc 생성 구조체 (§1)
- [ ] **파일 책임 주석 없음** (§4)
- [ ] **에러 non-wrapped**: `return err` 그대로 (§6)
- [ ] **로그에 포맷 문자열로 값 박음**: `slog.Info(fmt.Sprintf(...))` (§6)
- [ ] **신규 외부 의존성**: 기획서에 없고 표준 라이브러리로 대체 가능 (AGENTS.md 기술 스택)
- [ ] **300줄 초과 파일**: 분할 제안 없이 계속 추가 (§4)