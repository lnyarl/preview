---
name: acceptance-test
description: Preview 프로젝트에서 구현이 기획서의 기능/비기능 체크리스트를 실제로 만족하는지 엄격하게 검증할 때 사용한다. go vet·build·test 실행, 각 체크리스트 항목별 재현 명령 실행, Hub↔Agent 메시지·Store 인터페이스·HTTP shape 경계면 교차 비교, UI 존재 시 Playwright e2e 실행, 검증 보고서(docs/reports/phase-N-eval.md) 작성까지 수행한다. evaluator 에이전트의 주력 스킬. "구현 검증", "Phase N 평가", "기획서 체크리스트 확인" 요청에서 반드시 트리거.
---

# Acceptance Test 스킬

## 목적

구현된 코드가 승인된 Phase 기획서의 체크리스트를 **실제로** 만족하는지 검증한다. 코드 리뷰에만 의존하지 않고, 실행 기반 증거(`go test`, `curl`, `playwright`)를 수집한다. CLAUDE.md 규칙 "리뷰는 코드를 읽는 것뿐만이 아니라 테스트를 동반한다 ... e2e는 playwright를 이용한다."의 실천 도구.

## 검증 시작 전 준비

1. 승인된 기획서 파일과 구현 결과물(파일 목록, 체크리스트 매핑)을 `SendMessage`로 go-implementer에게 받음.
2. 저장소 현재 상태 확인: `git status`, `git log --oneline -20`.
3. 기획서의 "기능 체크리스트"와 "비기능 체크리스트"를 추출해 점검표 스켈레톤 생성.

## 검증 파이프라인

### 단계 1 — 빌드 위생 (bash/PowerShell)
```
go vet ./...         # 경고 0
go build ./...       # 모든 타겟 빌드
```
실패 시 즉시 FAIL 기록 후 go-implementer에게 피드백. 이후 단계는 진행하되, 빌드 실패 상태를 "Critical" 섹션에 표시.

### 단계 2 — 단위테스트
```
go test ./... -count=1
```
- 모든 테스트 PASS가 기본.
- Flaky 의심 시 최대 3회 재실행. 2회 이상 실패면 FAIL.
- 커버리지 수치는 기획서 비기능에 명시됐을 때만 수집: `go test -cover ./...`.

### 단계 3 — 기능 체크리스트 실행
각 `F-N` 항목의 "검증 방법"을 실제로 실행.

예시 유형:
- 엔드포인트: `curl -s -w '\nHTTP:%{http_code}\n' http://localhost:8080/...` 로 status·body 확인.
- CLI: `./bin/hub` 실행 후 stdout·exit code 확인.
- DB: 쿼리 실행 후 결과 row 확인.
- 파일 생성: `ls`, `cat`으로 내용 확인.
- sqlc 생성: `make sqlc` 실행 후 `internal/db/sqlite` 파일 diff 확인.

각 항목: **재현 명령**과 **실제 출력**을 보고서에 그대로 붙임.

### 단계 4 — 비기능 체크리스트
이식성 항목(필수 2개)은 아래 grep 검사로 기계적 확인.

**NF-Portability-1 (표준 SQL)**:
```
# SQLite 금지어
grep -rnI --include='*.sql' -E '\bAUTOINCREMENT\b|INSERT OR REPLACE' db/
# Postgres 금지어  
grep -rnI --include='*.sql' -E '\bSERIAL\b|::jsonb|jsonb_' db/
# 결과가 비어있어야 PASS
```

**NF-Portability-2 (Repository 인터페이스 경유)**:
```
# internal/hub, internal/agent에서 sqlc 생성 패키지를 직접 import하면 위반
grep -rnI --include='*.go' 'internal/db/sqlite' internal/hub internal/agent 2>/dev/null
# 결과가 비어있어야 PASS
```

성능 항목이면 `hey`·`curl -w` 등으로 간단 측정. 명시 지표가 없으면 "명시 지표 없음, 측정 건너뜀"으로 처리.

### 단계 5 — 경계면 교차 비교 (Boundary Crosscheck)
구현된 경계마다 수행.

- **메시지 타입**: `internal/protocol` 의 타입 정의와 Hub·Agent 각각의 핸들러가 참조하는 필드명이 일치하는가. grep으로 필드명 교차 확인.
- **Store 인터페이스**: `internal/store`의 인터페이스 메서드 시그니처와 `internal/db/sqlite`의 구현체가 일치하는가. Go 컴파일러가 잡아주지만, 구현체가 존재하는지 체크.
- **HTTP 응답 shape**: 핸들러가 응답하는 구조체와 클라이언트(Agent·관리자 UI)가 기대하는 구조체가 일치하는가.

### 단계 6 — e2e (UI 있는 Phase에만)
UI가 있을 때만 수행 (Phase 3 이후 예상). Playwright MCP 도구 사용:
- `mcp__playwright__playwright_navigate` → 페이지 열기
- `mcp__playwright__playwright_click`, `playwright_fill` 등으로 조작
- `mcp__playwright__playwright_screenshot`, `playwright_get_visible_text`로 검증
- `mcp__playwright__playwright_console_logs`로 에러 감시

시나리오는 기획서의 기능 체크리스트 중 UI가 관여하는 항목에 대응. UI 없는 Phase에서는 "N/A"로 표기하고 넘어간다.

## 보고서 작성

저장 위치: `docs/reports/phase-{N}-eval.md`

템플릿:

```markdown
# Phase {N} Evaluation — {YYYY-MM-DD}

## Summary
- Build: PASS/FAIL
- Unit: X/Y PASS
- Functional: X/Y PASS
- Non-functional: X/Y PASS
- e2e: X/Y PASS (or N/A)
- **Verdict**: APPROVE | REQUEST_CHANGES

## Per-item Results

### Build & Hygiene
| Check | Command | Result |
|-------|---------|--------|
| go vet | `go vet ./...` | PASS / FAIL + 출력 |
| go build | `go build ./...` | PASS / FAIL + 출력 |
| go test | `go test ./...` | PASS X/Y |

### Functional
| ID | Spec | Verification | Status | Evidence |
|----|------|--------------|--------|----------|
| F-1 | ... | `curl ...` | PASS | (stdout/stderr 일부) |

### Non-functional (Portability 필수)
| ID | Spec | Check | Status | Evidence |
|----|------|-------|--------|----------|
| NF-Portability-1 | 표준 SQL | grep 금지어 | PASS | 매치 0건 |
| NF-Portability-2 | Store 인터페이스 경유 | grep import | PASS | 매치 0건 |

### Boundary Crosscheck
- protocol ↔ hub handlers: {일치/불일치}
- store interface ↔ sqlite adapter: {일치/불일치}
- HTTP shapes ↔ clients: {일치/불일치 / N/A}

### e2e (Playwright)
- 시나리오 1: ... (스크린샷 경로)
- 또는 "N/A — 이 Phase에 UI 없음"

## Regressions Not in Spec
(있으면 기록)

## Verdict
APPROVE | REQUEST_CHANGES

## Notes
- UNVERIFIED 항목의 이유, 환경 요건 등
```

## 판정 규칙

- **APPROVE**: 빌드·단위·기능·비기능·경계·e2e 전부 PASS 또는 합리적 UNVERIFIED.
- **REQUEST_CHANGES**: 하나라도 FAIL. 구체적 항목과 재현 명령을 go-implementer에게 전달.

## 에러 처리

- 테스트가 환경 문제로 실행 불가(Docker 데몬 없음 등)하면 UNVERIFIED + 필요 요건 명시. FAIL로 취급하지 않되 "미검증"임을 Summary에 명기.
- 기획서 "검증 방법"이 실행 불가능한 수준으로 모호하면 planner에게 `SendMessage`로 보정 요청. 해당 항목은 보류.

## 결과 전송

- `SendMessage`로 go-implementer에게 판정 + 실패 항목 전달 (REQUEST_CHANGES 시).
- `SendMessage`로 리더(오케스트레이터)에게 종합 결과 요약.
- 보고서 파일 경로를 함께 전달.
