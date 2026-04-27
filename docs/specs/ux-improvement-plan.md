# UX / 사용성 개선 계획

본 문서는 새 기능 추가 없이 기존 Admin UI 의 사용성을 개선하기 위한 작업 목록이다.
범위는 `internal/hub/views/*.gohtml` 와 그를 렌더링하는 `internal/hub/admin_ui*.go` 핸들러로 한정한다.

## 1. 현재 UX 약점 요약

- 페이지마다 디자인 톤이 다름 (`token.gohtml` 만 매우 잘 다듬어져 있고 나머지는 빈약)
- 위험 액션(`Delete` / `Teardown All`) 에 confirm 없음
- 빌드 중 페이지를 봐도 자동 갱신 없어 F5 반복
- 상태(`running` / `failed` / ...) 가 텍스트만 — 시각적 스캔이 어려움
- 페이지 간 이동 동선이 막혀있음 (예: `agent_detail` → 그 agent 의 preview 목록)

## 2. 개선 카테고리 6 + To-do

### A. 시각적 일관성 (Consistency)

| # | 작업 | 효과 |
|---|---|---|
| A-1 | nav 현재 페이지 highlight (request path 비교 후 `aria-current="page"`) | 위치 인지 |
| A-2 | **status badge 컴포넌트** — 색+라운드를 가진 span 을 template helper(`{{statusBadge .Status}}`) 로 통일. dashboard / previews 목록 / preview detail 모두 동일 규칙 (queued=gray, building=blue, running=green-mark, failed=red, done=muted) | 시각 스캔 |
| A-3 | 버튼 클래스 표준 정리 — primary(주 액션) / secondary outline(취소·복사) / contrast(파괴적 액션). 현재 `outline`, `secondary outline`, 인라인 `class=""` 혼재 | 의도 명확 |
| A-4 | `<html lang="en">` → `lang="ko"` (콘텐츠가 한국어) | 접근성 |

### B. 위험 액션 보호 (Destructive guards)

| # | 작업 | 효과 |
|---|---|---|
| B-1 | Agent **Delete** 버튼: `onsubmit="return confirm('이 Agent를 삭제하시겠습니까? 진행 중인 preview 가 있다면 모두 정리됩니다.')"` | 사고 방지 |
| B-2 | **Teardown All** 버튼: `confirm('이 Agent 의 모든 실행 중 preview 를 종료하시겠습니까?')` | 사고 방지 |
| B-3 | Preview detail 의 **Stop**: `confirm('실행 중인 preview 를 종료하시겠습니까?')` | 일관성 |
| B-4 | `repo_secrets` 의 "전체 키 비우면 삭제됨" 동작에 사전 안내 (이미 doc 에는 있음, 폼에 alert 로 노출) | 가시성 |

### C. 상태 피드백 (Feedback)

| # | 작업 | 효과 |
|---|---|---|
| C-1 | preview_detail 에서 status 가 `queued` / `assigned` / `building` / `teardown` 이면 `<meta http-equiv="refresh" content="5">` (5초 자동 새로고침). running/done/failed 면 새로고침 없음 | 빌드 모니터링 |
| C-2 | Re-run / Stop 후 redirect 시 query 에 `?flash=stopped` 등 success flash. 페이지에서 한 번 보여주고 사라짐 | 액션 확인 |
| C-3 | form submit 시 버튼 disabled + 텍스트 변경 — 더블 클릭 방지 | 안정성 |
| C-4 | `?msg=...` 쿼리의 conflict 메시지를 `+` / URL-encoded 그대로 보여주지 말고 디코드 후 alert article 로 표시 | 가독성 |

### D. 네비게이션 / 동선 (Navigation)

| # | 작업 | 효과 |
|---|---|---|
| D-1 | preview_detail 의 **Repo** 행을 `<a href="/admin/previews?repo={{.RepoFullName}}">` 로 — 같은 repo 묶어 보기 | 탐색 |
| D-2 | preview_detail 의 **Agent** 행을 `<a href="/admin/agents/{{ID}}">` 로 | 탐색 |
| D-3 | agent_detail 에 **이 Agent 에 할당된 Preview 목록** 섹션 + Test Build 버튼 | 동선 |
| D-4 | agents 테이블의 actions 를 **dropdown(`<details>`)** 으로 묶기 — 좁은 화면 깨짐 해결 + 시각 노이즈 감소 | 모바일 |
| D-5 | 모든 detail 페이지에 **breadcrumb** (Agents › agent-home, Previews › #42) | 위치 인지 |

### E. 정보 밀도 (Information density)

| # | 작업 | 효과 |
|---|---|---|
| E-1 | previews 목록의 **Updated** 컬럼: 절대시간 → "2분 전" 같은 상대시간 + tooltip(절대시간) | 스캔 |
| E-2 | dashboard 의 status 1행 테이블 → **클릭 가능한 카드**(`<a href="/admin/previews?status=failed">7</a>`) | 진입점 |
| E-3 | dashboard 에 **최근 실패 5개** 작은 리스트 추가 (failed 상태로 ListAll 의 head 5건) | 운영성 |
| E-4 | preview_detail 의 **Timeline** 을 테이블 → 좌측 점/선이 있는 vertical timeline (CSS only) | 가독성 |
| E-5 | agent_detail 의 metadata 빈약 → **Run 카운트, 최근 빌드 5개, Health(last_seen 기반 색상) 추가** | 가치 |

### F. 폼 사용성 (Forms)

| # | 작업 | 효과 |
|---|---|---|
| F-1 | previews filter: **Reset 링크** 추가 (`<a href="/admin/previews">초기화</a>`) | 편의 |
| F-2 | test_build: 라벨/hint 한국어 통일 (현재 영어 + 한국어 혼재) + Cancel 링크가 항상 `/admin/agents` 로 가는데 referrer 활용해 agent_detail 로 돌아가게 | 일관성 |
| F-3 | repo_secrets textarea: rows=14 고정 → `rows="auto"` 또는 시각적 line counter | 편의 |

## 3. 권장 우선순위 (Sprint 단위)

### 1차 — Quick wins (1~2 커밋, 큰 체감)
- B-1, B-2, B-3 (confirm 추가)
- A-1 (nav active)
- A-4 (`lang=ko`)
- C-1 (auto-refresh)
- F-1 (filter reset)

### 2차 — 일관성 (3~5 커밋)
- A-2 (status badge 통일) — template helper 1개 만들고 4곳 적용
- C-2 + C-3 (flash + double-click 방지)
- D-1, D-2 (cross-link)

### 3차 — 정보 추가 (5~8 커밋)
- D-3, E-5 (agent_detail 강화)
- E-1, E-2, E-3 (dashboard / 목록 정보 밀도)
- D-5 (breadcrumb)
- E-4 (timeline 시각화)

### 4차 — 마감 (선택)
- D-4 (모바일 dropdown)
- F-2, F-3 (폼 통일)

## 4. 비범위

- WebSocket 기반 실시간 상태 갱신 (새 기능에 가까움 — 새 phase 로 분리)
- Dark mode toggle (pico CSS 가 prefers-color-scheme 자동 처리하므로 별도 구현 불필요)
- 검색 / 정렬의 백엔드 측 구현 변경
- 기능 자체의 추가 (본 계획은 기존 기능의 사용감 개선에 한정)

## 5. 다음 단계

각 차수마다 별도 phase 기획서 (`phase-12-...md`) 로 분리해 planner → plan-reviewer → 구현 워크플로우를 따른다.  
1차만 묶어서 한 PR 로 가는 것을 추천 — 체감 효과가 가장 크고 위험은 가장 낮다.
