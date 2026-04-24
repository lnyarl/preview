---
name: boundary-qa
description: Preview 서비스의 경계면(Hub↔Agent, Hub↔DB, Agent↔Docker, Hub↔GitHub) 정합성 검증. 모듈 완성 후 타입/스키마 불일치, 상태 머신 위반, race condition, 리소스 누수를 탐지. qa-reviewer 에이전트의 표준 점검 절차. 빌드/타입체크/린트 실행 포함.
---

# Boundary QA — 경계면 정합성 검증

Preview 서비스는 여러 시스템이 교차하는 분산 시스템이다. 대부분의 버그는 한 시스템 안이 아니라 **경계면**에서 발생한다. 이 스킬은 경계면 검증의 표준 절차.

## 언제 쓰는가

- 개발자가 모듈 완성 후 `qa-reviewer`에게 검토 요청할 때
- Phase 종료 직전 전체 빌드/경계 일관성 확인
- 경계 관련 이슈(타임아웃, 타입 불일치, 상태 꼬임)가 의심될 때

## 경계면 맵

```
[GitHub] --webhook--> [Hub HTTP] --DB--> [Postgres]
                           |
                           v
                      [Hub WS server]
                           ^
                           | (WebSocket, outbound from agent)
                           |
                        [Agent]
                           |
                           v
                      [Docker API]
                           |
                           v
                    [Container:port]
                           ^
                           | (reverse proxy routing)
                           |
                       [Hub HTTP]
```

각 화살표가 경계면. 양쪽의 shape이 일치해야 한다.

## 점검 절차

### Step 1 — 대상 식별

검토 요청 범위를 확인한다. "X 모듈 검토"면 다음을 파악:

- 어떤 파일들이 변경되었는가?
- 어느 경계면에 영향을 미치는가?
- DoD(Definition of Done)는 무엇인가?

### Step 2 — 교차 비교 (Crossing Check)

**핵심 원리**: 한 쪽만 보지 말고 양쪽을 동시에 열어 놓고 형태를 비교한다.

| 경계 | 비교할 양쪽 |
|---|---|
| WebSocket 메시지 | `shared/src/messages.ts` Zod ↔ Hub handler ↔ Agent sender |
| HTTP 요청/응답 | `shared/src/http.ts` ↔ Fastify 라우트 schema ↔ 클라이언트 호출 |
| DB 스키마 | Prisma/SQL ↔ repository 함수 반환 타입 ↔ API 응답 ↔ UI 타입 |
| Docker | Agent의 createContainer 옵션 ↔ Hub에 보고하는 port/labels ↔ Reverse proxy의 targetPort 조회 |
| Webhook | GitHub payload 스펙 ↔ Hub 수신 스키마 ↔ DB 저장 모델 |

**교차 비교가 아닌 것**: 파일 존재 확인, 함수 선언 확인, import 확인. 이건 "구현했다"는 자기 보고 수준. 교차 비교는 "양쪽이 같은 말을 하는가"를 확인하는 것.

### Step 3 — 상태 머신 검증

Job 상태는 `queued → assigned → building → running → teardown → done | failed`.

점검:
- 상태 전이 함수가 존재하고, 허용되지 않은 전이는 에러를 던지는가?
- DB의 status 컬럼과 코드의 상태 enum이 일치하는가?
- race 가능성: 두 요청이 동시에 상태를 바꾸려 할 때 어떻게 되나? (트랜잭션, optimistic lock, 조건부 UPDATE)

### Step 4 — 리소스 & 타임아웃

모든 외부 호출에 다음이 있어야 한다:

- [ ] 타임아웃 (기본값 금지, 명시적 값)
- [ ] 취소 신호 (AbortSignal)
- [ ] 실패 경로의 정리 (try/finally 또는 dispose)
- [ ] 에러 로깅 (무시 금지)

특히 확인:
- `docker run` → 컨테이너 정리
- `git clone` → 임시 디렉토리 정리
- WebSocket 연결 → 재연결 백오프
- DB 커넥션 → 풀 설정, 타임아웃

### Step 5 — 빌드 & 타입체크 & 린트

```bash
pnpm -r typecheck
pnpm -r lint
pnpm -r build
pnpm -r test    # 테스트가 있다면
```

하나라도 실패하면 `CHANGES_REQUESTED` 확정. 빌드 실패 시 로그를 report에 발췌.

### Step 6 — 자동화된 수평 검증 (Grep 기반)

경계 양쪽을 grep으로 찾아 같이 본다:

- WebSocket 메시지 type 추가됐을 때:
  - `Grep "case '{NEW_TYPE}'"` → Hub와 Agent 양쪽에 handler가 있는가?
  - `Grep "z.literal\(" shared/src/messages.ts` → discriminated union에 등록됐는가?
- DB 컬럼 추가됐을 때:
  - migration 파일 확인 → repository 반환 타입 확인 → API 응답 타입 확인 → UI 사용처 확인

### Step 7 — 리포트 작성

`_workspace/qa-{module}-report.md`에 다음 구조로:

```markdown
# QA Report: {module}

Reviewer: qa-reviewer  |  Date: {YYYY-MM-DD}  |  Verdict: APPROVED | CHANGES_REQUESTED

## Summary
한 문단으로 결과 요약.

## Blocker
(있으면) 병합/다음 단계 진행을 막는 이슈.
- [BUG] {file:line} — {문제 설명} → {제안}

## Major
주요 이슈 (기능 작동에 문제).

## Minor
코드 품질, 일관성 이슈.

## Nit
사소한 것 (선택적).

## Build Log (failures only)
(실패 시 발췌)
```

## Severity 기준

| Severity | 기준 | 대응 |
|---|---|---|
| Blocker | 기능이 동작하지 않거나 빌드 실패, 경계면 불일치로 런타임 에러 | 반드시 수정 |
| Major | 특정 시나리오에서 실패, race 가능성, 리소스 누수 | 현재 Phase 내 수정 |
| Minor | 타입 느슨함, 중복 코드, 컨벤션 위반 | 다음 Phase에 모아 수정 가능 |
| Nit | 네이밍 취향, 주석 등 | 기록만 |

## 자주 놓치는 경계면 버그 패턴

1. **"양쪽에서 같은 이름인데 다른 의미"**: `status` 필드가 job 관점과 agent 관점에서 다른 값 domain을 가짐.
2. **"선언은 맞는데 직렬화가 틀림"**: Date를 JSON에 넣으면 string, Zod는 Date를 기대 → runtime 실패.
3. **"unreachable 상태 전이가 reachable"**: 에러 경로에서 `done`으로 바로 점프하여 정리 로직 건너뜀.
4. **"이중 등록"**: 같은 job을 두 agent가 들고 가는 race. assign을 조건부 UPDATE로 atomic 하게.
5. **"포트 보고 누락"**: Agent가 container 실행 후 URL을 Hub에 보고하기 전에 웹훅 종료 이벤트 도착 → orphan 컨테이너.
6. **"DB 상태 변경 후 외부 호출 실패"**: 트랜잭션 안에 외부 호출 금지. 또는 사가/보상 트랜잭션.
7. **"WebSocket 재연결 시 중복 구독"**: 핸들러가 연결마다 add 되는데 cleanup이 없음.

## 샘플 점검 스크립트 (grep 기반)

필요 시 `scripts/` 디렉토리에 스크립트로 번들링 가능. 현재는 수동:

```bash
# Zod 메시지 타입 목록
grep -oh "type: z.literal('[A-Z_]\+')" shared/src/messages.ts | sort -u

# Hub handler가 처리하는 type
grep -oh "case '[A-Z_]\+'" hub/src/ws/**/*.ts | sort -u

# 두 목록을 diff
```

## 산출물 체크

완료 시:
- [ ] 리포트 파일 작성됨
- [ ] Verdict 명시됨 (APPROVED / CHANGES_REQUESTED)
- [ ] CHANGES_REQUESTED인 경우 수정 Task가 TaskCreate로 생성됨
- [ ] architect에게 설계-구현 괴리 관련 이슈 보고됨 (해당 시)
