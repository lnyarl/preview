# Phase 13 — Destructive Guards (카테고리 B-1/B-2/B-3: 위험 액션 confirm)

## 1. Phase 개요

`docs/specs/ux-improvement-plan.md` §2 카테고리 B 의 3개 항목(B-1 Agent Delete / B-2 Teardown All / B-3 Preview Stop)을 구현해 위험 액션의 오클릭 사고를 방지한다. 본 Phase 는 **클라이언트 측 `confirm()` 다이얼로그 1개씩을 form 의 `onsubmit` 속성으로 추가**하는 것이 전부이며, 핸들러·DB·메시지 프로토콜·CSS 는 일절 건드리지 않는다.

끝났을 때의 상태:
- `agents.gohtml` 의 `Delete` 버튼이 들어 있는 form 은 submit 시 `이 Agent를 삭제하시겠습니까? 진행 중인 preview가 있다면 모두 정리됩니다.` 다이얼로그를 띄우고, 사용자가 취소하면 form 이 제출되지 않는다.
- `agents.gohtml` 의 `Teardown All` 버튼이 들어 있는 form 은 submit 시 `이 Agent의 모든 실행 중 preview를 종료하시겠습니까?` 다이얼로그를 띄운다.
- `preview_detail.gohtml` 의 `Stop` 버튼이 들어 있는 form 은 submit 시 `실행 중인 preview를 종료하시겠습니까?` 다이얼로그를 띄운다.
- `go build ./...` 통과, 기존 단위테스트 전부 통과, 페이지 렌더링이 깨지지 않음.

## 2. 범위와 비범위

**범위**
- `internal/hub/views/agents.gohtml` — 2개 form 태그(`/admin/agents/{ID}/teardowns`, `/admin/agents/{ID}/delete`)에 각각 `onsubmit` 속성 1개 추가.
- `internal/hub/views/preview_detail.gohtml` — Stop form 태그(`/admin/previews/{ID}/stop`)에 `onsubmit` 속성 1개 추가.

**비범위**
- B-4 (`repo_secrets` 의 "전체 키 비우면 삭제됨" 사전 안내) — 별도 Phase.
- Re-run 버튼에 confirm 추가 — 가벼운 액션이라 사용자 요청에서 명시적으로 제외.
- 핸들러 측 추가 검증(서버 측 이중 확인) — 본 Phase 는 클라이언트 confirm 만 도입한다. 핸들러는 이미 권한·존재성 검증을 수행하고 있고, confirm 의 책임은 "오클릭 방지" 이지 "보안 강화" 가 아니다.
- JS 라이브러리 도입, 모달 다이얼로그, 토스트 — 브라우저 native `confirm()` 만 사용.
- 단위테스트 추가 — 단순 attribute 추가는 grep 으로 검증 가능.
- `Delete` 버튼이 별도 모달을 열어 "agent 이름을 입력하세요" 식으로 강제하는 패턴 — 과도. native `confirm()` 1개로 충분.
- 카테고리 A/C/D/E/F 와 B-4.

## 3. 설계 결정 및 근거

### 결정 1: `onsubmit` 속성을 `<form>` 에 추가한다 (버튼의 `onclick` 이 아니라)
- **결정**: 3개 form 모두 `<form ... onsubmit="return confirm('...')">` 형태로 form 태그에 속성을 추가한다. 버튼의 `onclick` 은 사용하지 않는다.
- **근거**:
  - `onsubmit` 은 form 제출 진입점 1곳에서 한 번만 막으면 되므로 버튼이 여러 개여도(현재는 단일이지만) 누락 위험이 없다.
  - `onsubmit` 핸들러가 `false` 를 반환하면 브라우저가 form submission 을 취소한다 (HTML5 표준). `onclick` 은 버튼의 default action(`type=submit` 의 form 제출) 을 막으려면 `event.preventDefault()` 또는 `return false` + 버튼이 `type=submit` 일 때만 동작 — 미묘한 차이로 디버깅 비용이 크다.
  - 사용자 요청문이 명시적으로 `<form ... onsubmit=...>` 형태를 지정.
- **버려진 대안 1**: 버튼의 `onclick="return confirm(...)"` → form 의 다른 진입(키보드 Enter 제출)에서 트리거 안 될 위험. 표준 분기 1곳 차단이 안전.
- **버려진 대안 2**: 외부 JS 파일에서 `addEventListener('submit', ...)` 로 wrap → JS 파일 신규 도입은 비범위. 또한 단순 confirm 1줄에 비해 과한 구조.
- **되돌릴 때 비용**: 매우 작다 (3줄 attribute 제거).

### 결정 2: 메시지 문자열은 single quote 로 감싸고 attribute 는 double quote 로 감싼다
- **결정**: 형태는 사용자 요청문과 동일하게 `onsubmit="return confirm('...한국어 메시지...')"`. 메시지 본문에는 작은따옴표(`'`)를 사용하지 않는다.
- **근거**:
  - 3개 메시지 모두 본문에 작은따옴표 문자를 포함하지 않는다 (확인됨).
    - `이 Agent를 삭제하시겠습니까? 진행 중인 preview가 있다면 모두 정리됩니다.` — `'` 없음.
    - `이 Agent의 모든 실행 중 preview를 종료하시겠습니까?` — `'` 없음.
    - `실행 중인 preview를 종료하시겠습니까?` — `'` 없음.
  - 따라서 single quote 이스케이프(`\'`) 가 불필요. attribute 의 double quote 역시 본문에 없다.
  - 한국어 음절(예: `습`, `까`)은 UTF-8 multi-byte 이지만 quote/HTML 메타 문자가 아니므로 attribute context 에서 그대로 안전. 페이지 자체의 `<meta charset>` 또는 `Content-Type` 은 기존 `text/html; charset=utf-8` 응답으로 이미 제공됨.
- **버려진 대안**: backslash 이스케이프(`\&#39;` 등) 를 미리 박아두기 → 불필요한 가독성 저하. 메시지를 변경할 때만 점검하면 된다.
- **되돌릴 때 비용**: 0 (미래에 메시지에 `'` 가 들어가면 그때 `&#39;` 로 표기).

### 결정 3: html/template 의 auto-escape 회피를 위한 트릭 없이, 정적 attribute 그대로 둔다
- **결정**: `onsubmit="..."` 안의 메시지는 템플릿 변수(`{{...}}`)가 아닌 **정적 리터럴**이다. 따라서 html/template 이 attribute context 에서 escape 를 적용해도 변경 대상이 없다 (정적 한국어/공백/구두점만 있음). 별도 `template.JS`/`template.HTMLAttr` 캐스팅이나 helper 가 불필요.
- **근거**: html/template 은 변수 보간 시점에만 context-aware escape 를 한다. 리터럴 텍스트는 그대로 출력된다. 본 Phase 의 메시지 3개는 모두 변수 없이 페이지에 박혀 있으므로 escape 위험이 0.
  - 만약 미래에 동적 변수(예: agent name) 가 메시지에 들어간다면, 그때 별도 결정이 필요하다 (`template.JSEscapeString` helper 등). 본 Phase 의 비범위.
- **버려진 대안**: helper 로 `confirmAttr "이 Agent를 ..."` 식으로 추출 → 1회 사용에 helper 도입은 과한 구조. 후속 Phase 에서 메시지가 동적이 되면 그때 도입.
- **되돌릴 때 비용**: 0 (미래에 helper 도입 시 attribute → helper 호출 1줄 치환).

## 4. 아키텍처/구조

### 변경 디렉토리 트리

```
internal/hub/views/agents.gohtml          (+ onsubmit attribute × 2)
internal/hub/views/preview_detail.gohtml  (+ onsubmit attribute × 1)
```

코드(.go) 변경 0건. 템플릿 외 파일 변경 0건.

### 변경 라인 단위 (정확한 위치)

| 파일 | 변경 전 라인 (현재 파일 기준) | 변경 후 |
|---|---|---|
| `agents.gohtml:36` | `<form method="POST" action="/admin/agents/{{.ID}}/teardowns" style="display:inline">` | `<form method="POST" action="/admin/agents/{{.ID}}/teardowns" style="display:inline" onsubmit="return confirm('이 Agent의 모든 실행 중 preview를 종료하시겠습니까?')">` |
| `agents.gohtml:39` | `<form method="POST" action="/admin/agents/{{.ID}}/delete" style="display:inline">` | `<form method="POST" action="/admin/agents/{{.ID}}/delete" style="display:inline" onsubmit="return confirm('이 Agent를 삭제하시겠습니까? 진행 중인 preview가 있다면 모두 정리됩니다.')">` |
| `preview_detail.gohtml:54` | `<form method="POST" action="/admin/previews/{{.Preview.ID}}/stop">` | `<form method="POST" action="/admin/previews/{{.Preview.ID}}/stop" onsubmit="return confirm('실행 중인 preview를 종료하시겠습니까?')">` |

(라인 번호는 본 기획서 작성 시점의 파일 상태 기준. 구현 시점에 다른 PR 의 머지로 ±1~2 라인 이동 가능 — 위치 식별은 form 의 `action` 속성으로 확정.)

### 호출 흐름 (런타임 동작)

```
사용자 클릭 (Delete/Teardown All/Stop) 또는 Enter 제출
  → 브라우저가 form submit 이벤트 발화
  → onsubmit 핸들러 실행: confirm("...메시지...")
      → 사용자가 OK → confirm 반환 true → onsubmit 반환 true → form 제출 진행
      → 사용자가 Cancel → confirm 반환 false → onsubmit 반환 false → form 제출 취소
```

JS 비활성 환경 (브라우저 설정/스크립트 차단):
- `onsubmit` 이 미실행 → form 이 그대로 제출됨 → 핸들러가 정상 처리. (리스크 R-1 참조 — 본 Phase 는 이를 수용한다.)

## 5. 인터페이스 계약

### form 태그 attribute 변경 명세 (양식)

```
<form method="POST" action="{{server-route}}" [style="display:inline"] onsubmit="return confirm('<message>')">
```

- `<message>` 는 다음 3개 중 하나 (정적 한국어 리터럴, single quote/double quote/backslash 미포함):
  - 삭제: `이 Agent를 삭제하시겠습니까? 진행 중인 preview가 있다면 모두 정리됩니다.`
  - Teardown: `이 Agent의 모든 실행 중 preview를 종료하시겠습니까?`
  - Stop: `실행 중인 preview를 종료하시겠습니까?`
- `onsubmit` 속성의 위치는 form 태그 내 마지막 attribute (`style` 뒤). 기존 attribute 순서는 변경하지 않는다.

### 핸들러 동작 변화: 없음

- POST `/admin/agents/{id}/delete` — 변화 없음.
- POST `/admin/agents/{id}/teardowns` — 변화 없음.
- POST `/admin/previews/{id}/stop` — 변화 없음.

### 응답 HTML body 안 substring 검증 기준

- `agents.gohtml` 렌더 응답에 `confirm('이 Agent를 삭제하시겠습니까?` 와 `confirm('이 Agent의 모든 실행 중 preview를 종료하시겠습니까?` 가 각각 1회 이상 등장.
- `preview_detail.gohtml` 렌더 응답에 `confirm('실행 중인 preview를 종료하시겠습니까?` 가 1회 이상 등장.

## 6. 기능 요구사항 체크리스트

- [ ] **F-1 (agents.gohtml — Delete confirm)**: `agents.gohtml` 의 `Delete` form (action `/admin/agents/{{.ID}}/delete`) 태그에 `onsubmit="return confirm('이 Agent를 삭제하시겠습니까? 진행 중인 preview가 있다면 모두 정리됩니다.')"` 속성이 정확히 추가되어 있다. — 검증:
    - `grep -n "/admin/agents/{{.ID}}/delete" internal/hub/views/agents.gohtml` 매칭 라인이 `onsubmit="return confirm('이 Agent를 삭제하시겠습니까?` substring 을 포함.
    - 동일 라인에 `진행 중인 preview가 있다면 모두 정리됩니다.')"` substring 포함.
- [ ] **F-2 (agents.gohtml — Teardown All confirm)**: `Teardown All` form (action `/admin/agents/{{.ID}}/teardowns`) 태그에 `onsubmit="return confirm('이 Agent의 모든 실행 중 preview를 종료하시겠습니까?')"` 가 정확히 추가. — 검증: `grep -n "/admin/agents/{{.ID}}/teardowns" internal/hub/views/agents.gohtml` 매칭 라인이 `onsubmit="return confirm('이 Agent의 모든 실행 중 preview를 종료하시겠습니까?')"` substring 포함.
- [ ] **F-3 (preview_detail.gohtml — Stop confirm)**: `Stop` form (action `/admin/previews/{{.Preview.ID}}/stop`) 태그에 `onsubmit="return confirm('실행 중인 preview를 종료하시겠습니까?')"` 가 정확히 추가. — 검증: `grep -n "/admin/previews/{{.Preview.ID}}/stop" internal/hub/views/preview_detail.gohtml` 매칭 라인이 해당 substring 포함.
- [ ] **F-4 (Re-run / Submit / 기타 form 무회귀)**: 본 Phase 가 추가하는 onsubmit 속성은 위 3개 form 만 대상. 다른 form (`Add Agent` Submit / `Re-run` / 등)에는 onsubmit 이 추가되지 않는다. — 검증:
    - `grep -c "onsubmit" internal/hub/views/agents.gohtml` 결과 = 2 (정확히 Teardown All + Delete).
    - `grep -c "onsubmit" internal/hub/views/preview_detail.gohtml` 결과 = 1 (정확히 Stop).
    - `grep -rn "onsubmit" internal/hub/views/` 결과의 다른 .gohtml 파일에 새 onsubmit 없음 (변경 전 0개 → 변경 후도 해당 파일들은 0개 유지).
- [ ] **F-5 (브라우저 동작 — 수동 확인 1회)**: 로컬 hub 를 띄운 뒤 `/admin/agents` 에서 `Delete` 클릭 시 native confirm 다이얼로그가 뜨고, "취소" 선택 시 페이지 이동 없이 form 제출이 취소된다. `Teardown All`, `Stop` 도 동일. — 검증:
    - 수동: Edge/Chrome 에서 다이얼로그 텍스트 일치 확인 + 취소 시 URL 무변동 + 확인 시 정상 핸들러 진입(redirect).
    - 자동(선택): Playwright 스크립트로 `dialog` 이벤트 캡처 후 `dialog.message()` 검사 가능. 본 Phase 는 단순 attribute 추가라 자동 e2e 는 권장만 하고 강제하지 않는다.
- [ ] **F-6 (페이지 렌더 무회귀)**: `go build ./...` + `go test ./internal/hub/...` 둘 다 통과. agents/preview_detail 페이지가 200 응답하고 응답 본문에 위 3개 confirm substring 이 정확히 포함된다. — 검증: 기존 `admin_ui_test.go` 의 모든 통과 케이스 그대로 통과.

## 7. 비기능 요구사항 체크리스트

- [ ] **NF-1 (이식성)**: 본 Phase 의 변경은 .gohtml 파일 3개의 attribute 추가뿐. Go 코드 0줄 변경, 새 의존성 0개. — 검증: `git diff --stat` 결과가 두 파일(agents/preview_detail) 만 표시.
- [ ] **NF-2 (보안 — XSS / attribute injection)**: 추가되는 onsubmit 메시지는 정적 리터럴이며 변수 보간이 없다. html/template 이 attribute context 에서 escape 를 적용해도 변경 대상 없음. — 검증: `grep '{{' internal/hub/views/agents.gohtml | grep onsubmit` 결과 0회 (onsubmit 라인에 템플릿 변수 미포함). preview_detail 동일 grep 0회.
- [ ] **NF-3 (관측성)**: 본 Phase 는 로그/메트릭을 추가하지 않는다. 핸들러 진입은 confirm 통과 여부와 무관하게 기존 핸들러 로그 그대로. — 검증: `git diff` 결과에 새 `slog.*` 호출 없음.
- [ ] **NF-4 (성능)**: 추가 attribute 1개당 약 70~110 byte. 페이지 응답 크기 영향 무시 수준. — 검증: 코드 리뷰.
- [ ] **NF-5 (콘텐츠 언어 일관성)**: 메시지 3개 모두 한국어로, Phase 12 결정 6 (콘텐츠 언어 일관성) 과 정렬. — 검증: 메시지 본문에 영문 단어 없음 (예외: 식별자로 사용된 `Agent`, `preview` — 도메인 용어이며 화면 다른 곳에서도 동일하게 등장).
- [ ] **NF-6 (HTML escape 안전)**: 메시지 3개 모두 single quote/double quote/backslash 문자를 본문에 포함하지 않으므로 attribute context 에서 추가 escape 가 필요 없다. — 검증:
    - `grep -n "onsubmit" internal/hub/views/agents.gohtml internal/hub/views/preview_detail.gohtml` 결과의 모든 매칭 라인에서 메시지 부분(`confirm('` 과 `')` 사이)에 `'`, `"`, `\` 문자가 없다 (수동 1회).
- [ ] **NF-7 (renderHTML 응답 안 정확 substring 검증)**: 기존 admin UI 테스트 인프라가 있다면 응답 본문 substring 단언을 1건씩 추가해도 되지만, 본 Phase 는 비범위. grep 으로 충분. — 검증: §6 F-1/F-2/F-3 의 grep 으로 대체.

## 8. 리스크와 완화책

- **R-1: JavaScript 비활성 환경에서 confirm 미동작 → form 그대로 제출**. 사용자가 브라우저에서 JS 를 끈 경우 또는 NoScript 등 확장으로 차단한 경우, `onsubmit` 핸들러가 실행되지 않아 confirm 단계 없이 form 이 즉시 제출된다.
  - **완화 방침**: **본 Phase 는 이 동작을 수용한다.** 이유:
    1. Admin UI 는 운영자 전용이며 JS 활성 환경을 전제로 한다 (이미 token.gohtml 의 `Copy` 버튼 등이 JS 의존).
    2. confirm 의 책임은 "오클릭 방지" 이지 "보안 강화" 가 아니다 — 핸들러 측 이중 확인은 비범위(§2).
    3. 추가 안전장치(서버측 confirm token 등)는 과한 구조이며 사용자 요청에서 명시 제외.
  - **수용 명시**: 이 결정을 §3 결정 1 보강 항목으로 본 기획서에 기록함. 후속 Phase 에서 JS-less fallback 이 필요해지면 별도 결정 문서로 다룬다.

- **R-2: 메시지 문자열 변경 시 quote/escape 사고**. 미래 PR 에서 메시지에 작은따옴표(예: `Agent's preview`) 또는 줄바꿈을 추가하면 attribute 파싱이 깨질 수 있다.
  - **완화**: §3 결정 2 에 "메시지에 `'` 추가 시 `&#39;` 또는 `'` 로 표기" 를 명문화. 코드 리뷰 시 onsubmit attribute 변경은 grep 으로 즉시 확인 가능.

- **R-3: 다른 Stop/Delete form 이 미래에 추가될 때 onsubmit 누락**. 새 페이지에서 같은 의도의 form 을 만들 때 본 Phase 의 패턴을 적용하지 않으면 가드 누락.
  - **완화**: 본 기획서 §5 의 "form 태그 attribute 변경 명세" 표를 후속 PR 의 체크리스트로 인용. `class="contrast"` 가 적용된 form 은 onsubmit 이 함께 따라가야 한다는 기준을 후속 phase 에서 명문화 (Phase 12 의 contrast 분류 결과를 기준으로 grep 가능).

## 9. 다음 Phase 연결점

- **B-4 (repo_secrets 사전 안내)**: 별도 Phase 에서 다룬다. 본 Phase 의 `onsubmit` 패턴은 적용 가능하지만, repo_secrets 의 동작은 "전체 키 비우면 삭제" 라는 조건부이므로 confirm 만으로 부족하고 사전 안내 article 이 필요하다 (alert article + form 옆 안내 문구 조합). 본 Phase 의 패턴을 그대로 가져가지 않는다.
- **C-1 (auto-refresh)**: preview_detail 의 `<head>` 에 meta refresh 를 status 별로 추가하는 작업. 본 Phase 와 직교 — 같은 파일을 만지지만 attribute 위치가 다르므로 머지 충돌 위험은 작다.
- **C-3 (form submit 시 버튼 disabled)**: confirm 통과 후 추가로 더블클릭 방지를 도입하는 후속 Phase. 본 Phase 의 `onsubmit` 핸들러 본문을 `return confirm(...)` 에서 `if(!confirm(...))return false; this.querySelector('button[type=submit]').disabled=true;` 식으로 확장하게 된다 — 확장 지점이 같은 onsubmit 속성이므로 재작성 비용 낮음.
- **클라이언트 측 가드 패턴 표준화**: 본 Phase 의 결정 1 (`onsubmit` 1곳 차단) 은 후속 모든 위험 액션에 동일하게 적용된다. 코드 리뷰 가이드에 "form 의 위험도가 contrast 이상이면 onsubmit 필수" 를 추가할 시점은 본 Phase 머지 직후가 적절.
