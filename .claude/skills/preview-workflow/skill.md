---
name: preview-workflow
description: Preview 프로젝트(Go 기반 PR Preview 서비스)의 한 Phase를 처음부터 끝까지(기획 → 리뷰 → 구현 → 검증) 오케스트레이션할 때 사용한다. planner·plan-reviewer·go-implementer·evaluator 네 에이전트로 팀을 구성하고, 기획 리뷰 루프와 구현 검증 루프를 관장하며, Phase별로 팀을 해체·재구성한다. "Phase N 시작", "Preview 프로젝트 Phase 진행", "기획부터 구현·검증까지" 같은 요청에서 반드시 트리거. 단순 단일 질문엔 트리거 금지.
---

# Preview Workflow Orchestrator

## 목적

한 Phase를 **기획 → 리뷰 → 구현 → 검증** 4단계로 끝까지 몰고 가는 오케스트레이터. CLAUDE.md의 작업 방식을 각 Phase마다 적용하며, 각 단계의 출력을 다음 단계의 입력으로 흐르게 한다.

## 실행 모드: 에이전트 팀

오케스트레이터(호출자)는 리더 역할. `TeamCreate`로 팀을 구성하고, `TaskCreate`/`TaskUpdate`로 작업을 추적한다. 팀원들은 `SendMessage`로 직접 통신하며 자체 조율.

## Phase별 팀 구성 전략

기획과 구현은 전문가 조합이 다르다. Phase를 **PLAN 서브페이즈 + BUILD 서브페이즈**로 나누고, 각 서브페이즈마다 팀을 재구성한다.

### PLAN 서브페이즈 팀
- 멤버: `planner`, `plan-reviewer`
- 패턴: 생성-검증 + 왕복 루프
- 종료 조건: plan-reviewer가 `APPROVE` 판정

### BUILD 서브페이즈 팀
- 멤버: `go-implementer`, `evaluator`
- 패턴: 생성-검증 + 수정 루프
- 종료 조건: evaluator가 `APPROVE` 판정

PLAN이 끝나면 해당 팀을 해체하고, 승인된 기획서 파일을 인수인계 산출물로 BUILD 팀에 전달한다.

## 오케스트레이터 절차

### Step 0 — Phase 스코프 확정
1. 사용자 또는 이전 Phase의 "다음 Phase 연결점"에서 이번 Phase 스코프 추출.
2. `docs/specs/phase-{N}-{slug}.md` 경로 결정.
3. TaskCreate로 Phase별 작업 목록 생성:
   - "Phase N 기획서 작성" (owner: planner)
   - "Phase N 기획서 리뷰" (owner: plan-reviewer, blockedBy: 작성)
   - "Phase N 구현" (owner: go-implementer, blockedBy: 리뷰 통과)
   - "Phase N 검증" (owner: evaluator, blockedBy: 구현)

### Step 1 — PLAN 서브페이즈
1. `TeamCreate` 로 `plan-team` 구성: `[planner, plan-reviewer]`.
2. `planner`에게 Phase 스코프 + 파일 경로 + 주의사항(이식성, 체크리스트 검증 방법 필수)을 메시지로 전달.
3. `planner`가 초안 작성 → `plan-reviewer`에게 리뷰 요청 → 피드백 반영 → 다시 리뷰 … 반복.
4. `plan-reviewer`가 `APPROVE`하면 PLAN 종료. 기획서 상태 `APPROVED`로 변경되었는지 확인.
5. 리더는 기획서 상태만 폴링하고, 왕복 내용에 간섭하지 않음. 단, 3회 이상 왕복 시 중재(근본 설계 재논의 제안, 필요 시 사용자 개입).
6. 팀 해체.

### Step 2 — BUILD 서브페이즈
1. `TeamCreate`로 `build-team` 구성: `[go-implementer, evaluator]`.
2. `go-implementer`에게 승인 기획서 경로 전달. 작은 단위 커밋 원칙 상기.
3. `go-implementer`가 체크리스트 항목별로 구현 진행 → 완료 후 `evaluator`에게 검증 요청.
4. `evaluator`가 보고서 작성 → `APPROVE` 또는 `REQUEST_CHANGES`.
5. `REQUEST_CHANGES` 시 `go-implementer`가 해당 항목만 수정 → 재검증. 이 루프는 최대 3회.
6. 3회 이상 FAIL이 반복되면 리더가 개입: planner 포함 3자 협의 제안, 기획서 수정이 필요한지 판정, 필요 시 PLAN 서브페이즈로 롤백.
7. `evaluator`가 `APPROVE`하면 BUILD 종료.
8. 팀 해체.

### Step 3 — 커밋 및 사용자 승인
1. 리더가 사용자에게 Phase 완료 보고 + 파괴적 액션(커밋·푸시) 승인 요청.
2. 승인 시 go-implementer에게 체크리스트 항목별 커밋 요청.
3. 커밋은 `type(scope): subject` 규칙. 한 커밋에 여러 체크리스트 항목을 묶지 않는다.
4. 푸시는 사용자 명시 승인 후에만.

## 데이터 전달 프로토콜

| 데이터 | 전달 방식 | 저장 위치 |
|--------|-----------|-----------|
| Phase 스코프 | 리더 → planner 메시지 | (메시지 본문) |
| 기획서 초안/수정본 | planner ↔ plan-reviewer 공유 파일 | `docs/specs/phase-{N}-*.md` |
| 리뷰 피드백 | plan-reviewer → planner 메시지 | (메시지 본문) |
| 승인 기획서 | planner → 리더 → go-implementer | 파일 경로 참조 |
| 구현 결과 | go-implementer → evaluator 메시지 | 파일 목록 + 체크리스트 매핑 |
| 검증 보고서 | evaluator → 리더 → go-implementer | `docs/reports/phase-{N}-eval.md` |

## 에러 핸들링

| 상황 | 대응 |
|------|------|
| 에이전트 무응답 | 1회 재시도 (같은 메시지 재전송). 재실패 시 리더가 상황 요약 후 사용자에게 보고. |
| plan 3회 왕복 | 리더 개입 → 근본 가정 재논의 제안 → 필요 시 사용자 중재 요청. |
| build 3회 FAIL | 리더 개입 → planner 호출해 기획서 재검토 여지 확인 → 필요 시 PLAN으로 롤백. |
| 환경 의존 검증 불가(Docker 없음 등) | evaluator가 UNVERIFIED로 표기. 리더가 사용자에게 해당 환경 요건 안내. Phase는 부분 승인 가능 여부 사용자에게 질의. |
| 기획서와 구현 간 모순 | 모순이 작으면 기획서 수정을 planner에게 요청하고 그대로 진행. 크면 PLAN으로 롤백. |
| 사용자 파괴적 액션 미승인 | 커밋·푸시·배포 보류. 구현·검증 산출물만 워크트리에 유지. |

## 테스트 시나리오

### 정상 흐름
1. 사용자가 "Phase 0 시작" 요청.
2. 리더가 `docs/specs/phase-0-scaffolding.md` 경로 결정, Task 4개 생성.
3. PLAN 팀 구성 → planner 초안 → plan-reviewer 1차 REQUEST_CHANGES (체크리스트 검증 방법 부족) → planner 수정 → plan-reviewer APPROVE.
4. BUILD 팀 구성 → go-implementer 스캐폴딩 → evaluator 단위테스트 전무(Phase 0은 코드 최소)지만 빌드·이식성 grep 통과 → APPROVE.
5. 리더가 사용자에게 완료 보고 + 커밋 승인 요청.
6. 승인 후 go-implementer가 체크리스트별 커밋 수행.

### 에러 흐름: 체크리스트 검증 불가
1. PLAN 종료 후 BUILD 진행 중, evaluator가 "F-3의 검증 방법이 실행 불가능 — '적절히 응답한다'로 모호"라고 지적.
2. go-implementer가 planner에게 `SendMessage`로 보정 요청.
3. planner가 기획서 수정 → plan-reviewer에게 재리뷰 요청 → APPROVE.
4. go-implementer가 보정된 검증 방법에 맞춰 부족한 부분 구현 → 재검증 → APPROVE.
5. 리더가 사용자에게 "Phase 중 기획서 재리뷰 발생" 보고.

## 사용 시 주의

- **팀 동시 활성 1개 원칙**: PLAN 팀과 BUILD 팀을 동시에 띄우지 않는다. PLAN 해체 후 BUILD 생성.
- **단일 Phase 원칙**: 한 번에 하나의 Phase만 진행. Phase N+1을 병행하지 않는다 (기획서 간 의존성 때문).
- **파괴적 액션 승인 규칙 엄수**: 커밋·푸시·배포는 반드시 사용자 승인 후.
- **모든 Agent 호출에 `model: "opus"` 명시**.
