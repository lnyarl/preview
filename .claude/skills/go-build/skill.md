---
name: go-build
description: Preview 프로젝트(Go 기반 PR Preview 서비스)에 Go 코드를 구현할 때 사용한다. 모노레포 구조(/cmd, /internal, /db), sqlc 설정, modernc.org/sqlite 드라이버, Repository 인터페이스 패턴, 표준 SQL, 이식성 원칙, 작은 단위 커밋을 모두 강제한다. go-implementer 에이전트의 주력 스킬. "Go 구현", "Hub 코드", "Agent 코드", "sqlc 생성", "Makefile" 관련 작업에서 반드시 트리거.
---

# Go Build 스킬

## 목적

승인된 Phase 기획서를 Go 코드로 옮길 때, **Go 관용구 + Preview 프로젝트 규칙**을 모두 강제한다. 관용구 위반, 이식성 원칙 이탈, 작은 단위 커밋 원칙 위반을 사전에 막는다.

## 프로젝트 구조 (절대 규칙)

```
/cmd
  /hub/main.go         - Hub 진입점 (얇게, 실제 로직은 internal/hub로)
  /agent/main.go       - Agent 진입점
/internal
  /hub                 - Hub 도메인 로직 (서비스, 핸들러)
  /agent               - Agent 도메인 로직
  /store               - Repository 인터페이스 (DB 추상화) — 비즈니스 로직이 의존
  /db
    /sqlite            - sqlc 생성 코드 + store 인터페이스 구현체
  /protocol            - Hub↔Agent 메시지 타입 (공유)
/db
  /migrations          - SQL 마이그레이션 (golang-migrate 호환)
  /queries             - sqlc용 쿼리 파일 (.sql)
  /schema              - sqlc용 스키마 파일 (.sql)
/docs                  - 아키텍처 문서, Phase 기획서, 리뷰 보고서
go.mod
sqlc.yaml
Makefile
README.md
.env.example
.golangci.yml
```

- 진입점(`/cmd/*/main.go`)은 **얇게**: flag 파싱, config 로드, 서비스 기동만.
- 비즈니스 로직은 `internal/hub`, `internal/agent`.
- DB 접근은 반드시 `internal/store`의 인터페이스를 통해. `internal/hub`에서 sqlc 생성 코드를 직접 import하면 **위반**.

## 기술 스택 (고정)

| 영역 | 선택 |
|------|------|
| Go | 1.22+ |
| HTTP | `net/http` 표준 라이브러리 (프레임워크 금지) |
| WebSocket | `github.com/coder/websocket` |
| SQLite 드라이버 | `modernc.org/sqlite` (순수 Go, CGO 없음) |
| sqlc | 최신 stable |
| 마이그레이션 | `golang-migrate/migrate` |
| Docker 제어 (Agent) | `github.com/docker/docker/client` |
| Reverse proxy | `net/http/httputil` |

의존성 추가는 기획서에 명시되어 있거나 표준 라이브러리로 대체 불가능할 때만. `go get` 전에 사용자 확인 필요하면 질의.

## 이식성 원칙 (코드 리뷰 때 가장 먼저 검사)

1. **Repository 인터페이스 경유** — 비즈니스 로직은 `internal/store`의 인터페이스 타입만 참조. sqlc 생성 구조체·함수를 직접 받지 않는다.
2. **표준 SQL** — 금지어 사전:
   - SQLite: `AUTOINCREMENT`(특수 동작), `INSERT OR REPLACE`, `PRAGMA` 런타임 의존
   - Postgres: `SERIAL`, `jsonb` 연산자(`->`, `@>`), `RETURNING`은 OK (SQLite 3.35+ 지원)
3. **ID는 TEXT(UUID 문자열)**, **timestamp는 ISO8601 TEXT 또는 Go `time.Time` 파싱**.
4. **반정형 데이터는 TEXT(JSON)** — labels 같은 건 `encoding/json` marshal/unmarshal.
5. **DB URL 분기** — `DATABASE_URL=sqlite://...` / `postgres://...` 파싱해서 드라이버 선택. 추후 Postgres 추가 시 `internal/db/postgres`에 sqlc 새로 생성하고 store 어댑터만 교체.

## sqlc 설정 (sqlc.yaml)

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
        emit_exact_table_names: false
        emit_empty_slices: true
```

- `emit_interface: true` — sqlc가 Querier 인터페이스를 생성 → store 어댑터가 감싸기 편함.
- 생성 경로가 `internal/db/sqlite`이므로 **비즈니스 로직은 직접 import 금지**. 반드시 `internal/store` 어댑터 경유.

## Makefile 규칙

필수 타겟:
- `make build` — `cmd/hub`, `cmd/agent` 두 바이너리 빌드
- `make run-hub`, `make run-agent` — `go run`
- `make sqlc` — `sqlc generate`
- `make migrate-up`, `make migrate-down` — golang-migrate 호출, `DATABASE_URL` env 사용
- `make test` — `go test ./...`
- `make vet` — `go vet ./...`
- `make lint` — `golangci-lint run` (설정 있을 시)
- `make fmt` — `gofmt -w .`

### Makefile 이식성

- Windows에서도 돌아야 하므로 (사용자 환경: Windows 11 + bash), bash 쉘에서 동작하는 명령만 사용. Windows-only 경로 구분자·PowerShell 전용 구문 금지.
- Go 툴체인은 OS 무관하게 작동. 다만 `docker` 명령은 사용자 환경에 따라 없을 수 있으므로, Docker가 필요한 타겟은 별도 타겟으로 분리.

## Go 관용구

- 공개 식별자는 godoc 주석. 주석은 식별자 이름으로 시작 (`// Foo does ...`).
- 에러는 `fmt.Errorf("...: %w", err)` 래핑. sentinel 에러는 `var Err... = errors.New(...)`.
- context.Context는 첫 번째 인자.
- goroutine 생성 시 종료 경로를 반드시 갖도록 설계 (context cancel, done 채널).
- 패키지 이름은 짧고, 디렉토리와 일치.
- 테스트는 `*_test.go`, 테이블 드리븐 선호.

## 커밋 단위 규칙

- 한 커밋 = 한 논리적 변경. 기획서 체크리스트 항목과 1:1 매칭 선호.
- 커밋 메시지: `type(scope): subject` (conventional commit). 예: `feat(hub): add /health endpoint`, `chore: init Go module`.
- **`git commit` 실행은 사용자 승인 후에만**. 평상시엔 파일 수정만 하고, 사용자가 "커밋해" 하면 그때 실행.

## 빌드 위생 (제출 전 항상 실행)

```
go vet ./...
go build ./...
go test ./...
```

셋 다 통과해야 evaluator에게 넘긴다. 통과 못 하면 원인 수정. 우회(테스트 skip, vet 무시, --no-verify) 금지.

## 작업 흐름

1. 승인된 기획서 Read.
2. 체크리스트 항목을 Task로 분할 (TaskCreate).
3. 한 항목씩 in_progress → 구현 → vet/build/test → completed.
4. 블록되면 `SendMessage`로 planner(기획서 모호성) 또는 리더(외부 의존).
5. 전체 완료 시 `SendMessage`로 evaluator에게 `{구현 파일 목록, 체크리스트 매핑}` 전달.

## 에러·우회 금지

- 테스트 실패 시 skip/주석처리 금지. 원인 수정.
- vet 경고 무시 금지.
- `--no-verify` 금지 (CLAUDE.md 시스템 규칙).
- 테스트 없이 "잘 돌 거야"로 완료 선언 금지.

## 파일 크기 원칙

파일은 300줄을 넘기지 않는다. 넘으면 분할한다.
분할 기준:

구조체/인터페이스 정의가 5개 이상 → 별도 파일
핸들러가 도메인별로 나뉠 수 있음 → agent_handler.go, preview_handler.go
테스트 헬퍼는 <name>_helpers_test.go로 분리


main.go는 100줄 이하를 목표. 실제 로직은 internal/로.