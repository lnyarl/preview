---
name: phase-playbook
description: Preview 서비스의 Phase 단위 작업 진행 오케스트레이터. "Phase N을 진행해줘", "다음 Phase로 넘어가자", 새 기능 구현 요청 시 반드시 이 스킬을 사용할 것. 팀 구성 → 작업 분해 → 병렬 실행 → QA → 종합의 전체 플로우를 담당.
---

# Phase Playbook — Preview 서비스 Phase 오케스트레이터

Preview 서비스는 Phase 단위로 기능을 붙여간다. 이 스킬은 한 Phase를 시작부터 끝까지 진행시키는 표준 플로우다.

## 언제 쓰는가

- 사용자가 "Phase 0/1/2…을 진행해줘" 혹은 "[기능]을 구현해줘"라고 말할 때
- 이전 Phase가 끝나고 다음 Phase로 넘어갈 때
- 큰 기능(feature)을 추가할 때

이 스킬을 쓰지 않는 경우: 1분 안에 끝나는 간단한 수정, 단순 질문, 문서 오탈자 수정.

## 팀 구성

이 프로젝트의 고정 팀은 다음과 같다:

| 에이전트 | 역할 | 담당 패키지 |
|---|---|---|
| `architect` | Phase 계획, 아키텍처 결정, 작업 분해, ADR | `docs/adr` |
| `protocol-dev` | 메시지 프로토콜/공유 타입 | `/shared` |
| `hub-dev` | Hub 서버·DB·WebSocket·프록시·대시보드 | `/hub` |
| `agent-dev` | Agent CLI·Docker·WebSocket 클라이언트 | `/agent` |
| `qa-reviewer` | 경계면 검증·빌드 검증·설계 일관성 | 전체 |

## 실행 플로우

### Step 1 — 팀 생성

TeamCreate로 `preview-team` 팀을 만들고 위 5명 에이전트를 멤버로 등록한다. 이미 팀이 있으면 생략하고 TaskList로 미완료 작업만 확인한다.

### Step 2 — architect에게 Phase 계획 의뢰

architect에게 다음을 요청한다:

```
Phase {N}: "{사용자가 준 요청}"을 진행한다.
다음을 산출하라:
1. _workspace/phase-{N}-plan.md — 목표, DoD, 리스크, 작업 분해 (의존성 포함)
2. 중요한 결정은 docs/adr/NNNN-{slug}.md로 기록
3. 분해된 작업을 TaskCreate로 만들고, 각 작업의 owner를 해당 에이전트로 지정
```

architect의 산출물(`_workspace/phase-{N}-plan.md`)을 읽고 계획이 타당한지 검토한다. 모호하거나 범위가 과도하면 축소 요청.

### Step 3 — 작업 할당 확인

TaskList로 생성된 작업을 확인한다. 다음을 점검:

- 모든 작업에 owner가 있는가?
- `/shared` 수정이 필요한 작업은 `protocol-dev`에게, 다른 패키지 작업보다 먼저 시작되도록 blockedBy 관계가 걸려있는가?
- 작업 수가 팀 크기 대비 과도하지 않은가? (에이전트당 4~6개 권장)

문제 있으면 architect에게 조정 요청.

### Step 4 — 병렬 실행

팀원들이 TaskList를 보고 작업을 집어가도록 한다. 의존성(blockedBy)이 해결되는 순서대로 자연스럽게 진행. 리더는 다음만 주시:

- 작업이 너무 오래 멈춰있지 않은지 (팀원에게 SendMessage로 상태 문의)
- 팀원 간 설계 충돌 (→ architect에게 조정 요청)
- `/shared` 변경이 후속 작업을 블록하는지 (→ protocol-dev 우선 처리)

### Step 5 — 점진적 QA

**중요**: 전체 완성 후 한 번 QA가 아니다. 각 에이전트가 한 모듈(=TaskComplete)을 끝낼 때마다 `qa-reviewer`에게 검토 요청한다.

```
qa-reviewer: {모듈} 검토 요청
- 대상: {파일 경로들}
- DoD: {phase plan에서 해당 모듈의 완료 기준}
- 검증 포인트: 경계면 정합성, 빌드 통과, 상태 머신
```

qa-reviewer가 `CHANGES_REQUESTED`면 원작자에게 수정 Task 생성. `APPROVED`면 다음 모듈로 넘어감.

### Step 6 — Phase 종료

모든 작업이 `completed`되고 qa-reviewer가 전체 `APPROVED` 낼 때 Phase 종료. 리더는 다음을 정리:

1. `_workspace/phase-{N}-summary.md` — 달성된 것, 남긴 이슈, 다음 Phase 제안
2. 사용자에게 요약 보고 + 디렉토리 구조 스냅샷 + 다음 Phase 확인 질문
3. Phase 간 중간 아티팩트는 `_workspace/`에 남겨둠 (감사 추적용)

## 데이터 전달 프로토콜

- **작업 조율**: TaskCreate/TaskUpdate (owner, blockedBy, status)
- **실시간 소통**: SendMessage (다른 팀원에게 빠른 질문, 피드백)
- **공식 산출물**: 파일 기반
  - 계획/결정: `_workspace/phase-{N}-*.md`, `docs/adr/NNNN-*.md`
  - 코드: 해당 패키지 디렉토리
  - 검토: `_workspace/qa-{module}-report.md`

## 에러 핸들링

| 상황 | 대응 |
|---|---|
| 작업이 두 번 연속 실패 | architect에게 작업 분해 재검토 요청 후 재할당 |
| 팀원 간 설계 충돌 | 양측 근거를 파일로 요약 → architect 중재 |
| 빌드 실패 | qa-reviewer가 로그 발췌 → 원작자에게 수정 Task |
| 범위 초과 감지 | 사용자에게 즉시 보고, 현재 Phase 범위 축소 제안 |

## 모델 설정

모든 Agent 도구 호출 시 `model: "opus"` 파라미터를 명시한다.

## 테스트 시나리오

**정상 흐름** (Phase 1: Webhook 수신 구현):
1. architect가 plan 작성: shared에 Webhook payload 타입 추가 → hub에 엔드포인트 추가 → DB에 PR 테이블
2. protocol-dev가 shared 타입 먼저 작성 (다른 작업의 blocker)
3. hub-dev가 병렬로 DB 스키마와 엔드포인트 구현
4. qa-reviewer가 각 완료마다 검토 → APPROVED → Phase 종료

**에러 흐름** (프로토콜 충돌):
1. hub-dev와 agent-dev가 동시에 shared 변경 요청
2. protocol-dev가 두 요청의 스키마 충돌 발견 → architect에게 에스컬레이션
3. architect가 ADR로 결정 → protocol-dev 작업 → 양쪽 개발자 재개
