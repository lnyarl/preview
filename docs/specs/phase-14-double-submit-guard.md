# Phase 14 — Double-Submit Guard (카테고리 C-3: form submit 시 버튼 disabled + 텍스트 변경)

## 1. Phase 개요

`docs/specs/ux-improvement-plan.md` §2 카테고리 C 의 **C-3 만** 구현한다. 사용자가 Admin UI 의 form 버튼을 더블 클릭하거나 양손으로 Enter 를 두 번 친 경우 동일 POST 가 두 번 전송되어 (예: agent 등록/삭제, preview rebuild, repo secrets 저장 등) 의도치 않은 중복 작업이 발생하는 사고를 막는다. 본 Phase 는 클라이언트 측 가드만 도입하며, 서버측 idempotency 토큰·DB UNIQUE 제약 등은 도입하지 않는다.

해결 방식: `views/layout.gohtml` 의 `<body>` 끝에 **글로벌 inline `<script>` 1개**를 추가해 document-level `submit` 이벤트를 listen 한다. 핸들러는 `e.defaultPrevented` 가 `false` 일 때만, 해당 form 안의 모든 `button[type=submit]` (또는 type 미지정 button) 을 `setTimeout(0)` 으로 disabled 처리하고 텍스트를 `처리 중...` 으로 바꾼다.

끝났을 때의 상태:
- `internal/hub/views/layout.gohtml` 의 `</body>` 직전에 약 10~15 줄짜리 inline `<script>` 블록이 1개 존재한다.
- 모든 페이지(layout 을 상속하는 10개 콘텐츠 템플릿)의 form 이 submit 되는 순간, 그 form 안의 submit 버튼이 disabled 되고 라벨이 `처리 중...` 으로 바뀐다.
- Phase 13 이 추가한 3개 confirm form (Delete / Teardown All / Stop) 에서 사용자가 `취소` 를 누르면 disabled 가 적용되지 **않는다**.
- 정상 클릭 1회 → 동일 form 재클릭 시 두 번째 클릭은 button disabled 라 무반응.
- `go build ./...`, `go test ./internal/hub/...` 모두 통과. 기존 페이지 응답 substring 단언 무회귀.

## 2. 범위와 비범위

**범위**
- `internal/hub/views/layout.gohtml` 1파일에 inline `<script>` 블록 1개 추가.
- 본 스크립트 1개로 layout 을 상속하는 모든 페이지(`dashboard`, `agents`, `agent_detail`, `previews`, `preview_detail`, `repos`, `repo_secrets`, `settings`, `token`, `test_build`)의 모든 form 에 동시 적용. 실제 form 보유 페이지는 5개(agents 3 + preview_detail 2 + previews 1 + repo_secrets 1 + test_build 1 = 총 8개 form). 나머지 5개 페이지(agent_detail / dashboard / repos / settings / token)에는 form 이 없으나 layout 공통 스크립트라 자동 적용 대기.
- POST/GET form 양쪽 모두 동일 적용 — 사용자 요청 "POST/GET 일관성" (§3 결정 4 참조).

**비범위**
- C-1 (`preview_detail` auto-refresh `<meta http-equiv="refresh">`) — 별도 Phase.
- C-2 (`?flash=stopped` 같은 success flash 메시지 표시) — 별도 Phase.
- C-4 (`?msg=...` URL-encoded conflict 메시지 디코드 후 alert article 화) — 별도 Phase.
- 서버측 idempotency: DB UNIQUE 제약 / submit 토큰 / 멱등키 — 본 Phase 는 클라이언트 가드만.
- Phase 13 의 confirm() 메시지 변경 또는 confirm 패턴 자체의 리팩터.
- 외부 JS 라이브러리 도입 (jQuery / Alpine / htmx 등). 순수 vanilla DOM API 만 사용.
- 별도 `*.js` 정적 파일 도입. Phase 12 결정 6 (별도 CSS 파일 미도입) 과 동일한 입장으로 inline 유지.
- form 의 GET/POST 분기 처리. 같은 핸들러로 동작.
- 비활성화된 버튼의 텍스트 복원 로직. Admin UI 는 모든 form 이 redirect 응답을 돌려주고 페이지가 재로드되므로 복원이 의미 없음 (§3 결정 6).
- Re-run/Stop 버튼 자체에 본래 적용되어 있던 조건부 `disabled` 속성 (`{{if not .RebuildEnabled}}` 등) 의 동작 변경. 본 Phase 의 disabled 는 submit 직후의 일시 disable 이며 기존 로직과 직교.
- form 안에 submit 버튼이 1개도 없는 케이스 (현재 코드베이스에 없음 — 모든 form 이 submit 버튼 보유. 후속 페이지 추가 시 가드 로직이 무동작이라 안전).

## 3. 설계 결정 및 근거

### 결정 1: 글로벌 document-level `submit` 리스너 (방법 A) 채택, 인라인 onsubmit 추가 (방법 B) 기각

- **결정**: `layout.gohtml` 1곳에 `document.addEventListener('submit', handler, false)` 를 추가하는 글로벌 위임 패턴을 사용한다. 각 form 에 `onsubmit` 또는 `onclick` 을 따로 붙이지 않는다.
- **근거**:
  - 현재 코드베이스에 form 이 8개 (agents 3 + preview_detail 2 + previews 1 + repo_secrets 1 + test_build 1 — 위 §2 범위 참조). 인라인 패턴은 8곳을 수정해야 하고 후속 PR 마다 누락 위험 발생.
  - Phase 13 이 이미 `onsubmit="return confirm('...')"` 를 3개 form 에 박아둠. 인라인 onsubmit 추가는 같은 attribute 에서 confirm 과 disabled 를 한 줄에 혼합해야 하며, 짧은 attribute string 안에 두 개 책임이 섞여 가독성·유지보수성이 나쁘다.
  - 글로벌 리스너는 `e.defaultPrevented` 를 검사함으로써 confirm 취소를 자동으로 회피한다 (§3 결정 3). Phase 13 의 onsubmit 와 충돌하지 않음을 표준 이벤트 모델로 보장 가능.
  - layout 은 모든 페이지 공통이므로 신규 페이지/form 추가 시에도 자동으로 가드가 적용된다 — "잊고 안 붙임" 사고 0.
- **버려진 대안 1 (방법 B — 인라인 onsubmit)**: 8곳 수정 + 그 중 3곳은 confirm 과 chain 해야 함 (`onsubmit="return confirm(...) && (this.querySelector('button[type=submit]').disabled=true||true)"` 같은 한 줄 hack). 가독성·실수 위험 모두 큼.
- **버려진 대안 2 (별도 `static/js/guard.js` 파일)**: `<script src="...">` 추가는 정적파일 라우트·캐시 헤더·sub-resource integrity 등 부가 결정을 동반. Phase 12 결정 6 (별도 CSS 파일 미도입) 과 동일 논리로 거절. layout inline 으로 충분.
- **되돌릴 때 비용**: 매우 낮다. layout.gohtml 의 `<script>` 블록 한 덩어리만 제거.

### 결정 2: 스크립트 위치는 `</body>` 직전 (foot 위치)

- **결정**: layout 의 `<footer class="container"><small>Preview Hub MVP</small></footer>` 다음, `</body>` 바로 위에 `<script>` 블록 1개를 둔다. `<head>` 가 아니다.
- **근거**:
  - 본 스크립트는 document-level `submit` 이벤트를 listen 하는 단일 등록만 수행. 등록 시점에는 form 요소가 이미 파싱되어 있어야 한다 — 또는 등록만 하고 실제 form 조회는 이벤트 발생 시점에 한다. `</body>` 직전이라면 두 조건 모두 만족.
  - `<head>` 에 두면 `DOMContentLoaded` 이전에 실행되어 등록 자체는 가능하지만, 페이지 파싱 중 사용자가 (불가능하지만) 아주 빨리 클릭한 경우의 race 가 0 이 됨을 보장하려면 foot 위치가 단순. Pico CSS 자체가 외부 CDN 으로 `<head>` 에서 불려와 페이지 렌더가 거의 즉시 끝나므로 차이는 미미하지만, foot 위치가 표준 권장 패턴.
  - `settings.gohtml` 의 inline `<script>` 가 이미 페이지 본문 안 (즉 body 후반) 에 있고, `token.gohtml` 은 본문 안에 있다. layout 으로 일반화할 때도 동일한 위치 정책(foot 또는 본문 후반) 으로 통일하는 것이 자연스럽다.
- **버려진 대안 1 (`<head>`)**: `defer` / `DOMContentLoaded` 래핑이 필요 — 코드 양 1줄 증가, 실익 0.
- **버려진 대안 2 (각 페이지의 `{{define "content"}}` 끝)**: 페이지마다 중복. 본 Phase 의 핵심이 "한 곳에 박는다" 이므로 채택 불가.
- **되돌릴 때 비용**: 0.

### 결정 3: confirm 취소와의 상호작용 — `e.defaultPrevented` 검사

- **결정**: 핸들러는 다음 조건을 만족할 때만 disabled 처리를 수행한다:
  1. `e.defaultPrevented === false` (다른 핸들러가 아직 form 제출을 막지 않았다).
  2. `e.target` 이 `HTMLFormElement`.
- **근거**:
  - Phase 13 의 `onsubmit="return confirm('...')"` 는 HTML 의 legacy 패턴으로, `confirm()` 이 `false` 를 반환하면 onsubmit 핸들러가 `false` 를 반환하고 브라우저는 submit event 의 기본 동작(form 제출) 을 자동으로 취소한다. **이때 submit 이벤트 자체는 발화하며, `event.defaultPrevented` 가 `true` 로 설정된다** (HTML Living Standard 의 event handler processing algorithm 에 따라, onsubmit 핸들러가 false 를 반환하면 preventDefault() 가 호출되어 defaultPrevented 가 true 로 세팅됨).
  - onsubmit attribute 핸들러는 form 요소(target)에서 등록된 listener 와 동등하며 DOM 이벤트 흐름의 target phase 에 실행된다. 우리의 글로벌 listener 는 document(ancestor)의 bubble 단계에 등록되므로 target phase 보다 후행함이 DOM Events 사양으로 보장된다.
  - 우리의 글로벌 리스너는 캡처(capture=false → bubble) 단계에서 등록하면 onsubmit 의 inline 핸들러가 먼저 실행된 뒤 호출되므로, 그 시점에 `e.defaultPrevented` 를 검사하면 confirm 취소를 100% 감지 가능.
  - **검증 방법**: §6 F-3 의 수동 테스트 (Phase 13 의 Delete/Teardown All/Stop 버튼에서 `취소` 클릭 → 버튼이 여전히 활성·텍스트 그대로 인지 확인) + DOM 콘솔에서 `document.querySelector('form[action$="delete"] button').disabled === false` 단언.
- **버려진 대안 1**: `setTimeout(0)` 안에서 `e.defaultPrevented` 재검사. → 이미 본 결정이 동기 시점에 e.defaultPrevented 를 검사하고 setTimeout(0) 안에서는 disable 만 수행하므로, setTimeout 안에서의 재검사는 중복이며 추가 가치 0. 결론: 동기 1회 검사로 충분.
- **버려진 대안 2**: 캡처(capture=true) 단계 등록 → confirm 보다 먼저 실행되어 disabled 가 confirm 다이얼로그 띄우는 동안 적용됨. 사용자가 취소한 경우 버튼이 영구 disabled 로 남는 사고. 절대 채택 금지.
- **되돌릴 때 비용**: 0 (한 줄 if 검사 제거).

### 결정 4: `setTimeout(0)` 으로 disabled 적용 (동기 disabled 회피)

- **결정**: 핸들러는 `setTimeout(function(){ btn.disabled = true; btn.textContent = '처리 중...'; }, 0)` 형태로 다음 tick 에서 disabled 를 적용한다. 동기적으로 핸들러 본체에서 `btn.disabled = true` 하지 않는다.
- **근거**:
  - 일부 브라우저 (특히 구버전 Safari, 일부 Webkit) 는 submit 이벤트 핸들러 도중 button 이 disabled 되면 form data 직렬화 단계에서 해당 버튼의 name/value 가 누락되거나, 더 심하게는 form 제출 자체를 취소하는 동작이 보고된 바 있다. 사용자 요청문에서도 "동기 disabled 는 일부 브라우저에서 form submit 자체를 차단함" 이라는 사실을 명시.
  - `setTimeout(0)` 은 현재 macrotask 가 끝난 뒤 실행되며, 그 시점에는 브라우저가 form 의 직렬화·POST 전송을 이미 시작했으므로 disabled 가 전송에 영향을 주지 않는다.
  - 실제 근거는 (a) Safari/구 WebKit 에서 submit 이벤트 핸들러 도중 동기 disable 시 form data 직렬화에서 해당 버튼이 누락되거나 제출 자체가 취소되는 동작 보고가 있음, (b) HTML Living Standard 상 submit event 처리는 현재 task 종료 후 form submission algorithm 으로 이어지므로 setTimeout(0) macrotask 는 직렬화/POST 송신 이후 실행됨이 보장.
- **버려진 대안**: `requestAnimationFrame` 사용 → tick 보다 살짝 늦어 사용자 시각적 피드백이 한 프레임 지연. UX 차이는 미미하지만 `setTimeout(0)` 이 표준 관용구.
- **되돌릴 때 비용**: 0.

### 결정 5: GET form 에도 동일 적용 (POST/GET 일관성)

- **결정**: `previews.gohtml` 의 filter `Apply` 버튼(`<form method="GET" action="/admin/previews">`) 을 포함해 모든 form 에 동일 가드를 적용. method 분기를 두지 않는다.
- **근거**:
  - 사용자 요청문 명시: "POST/GET 일관성".
  - GET 의 더블 클릭은 서버측 부작용이 없지만 사용자 입장에서는 "두 번째 클릭에서 버튼이 살아있는데 왜 무반응?" 같은 불일관 UX 가 발생 가능. 일관 처리가 멘탈 모델 단순.
  - filter Apply 의 두 번 제출은 같은 URL 두 번 GET — 부작용은 없지만 가드 적용으로 사용자에게 "처리 중" 피드백이 0.x 초 노출되는 것은 무해.
  - GET 분기를 두면 코드 1~2줄 증가 + 분기 조건 검토 비용. 통합 처리가 단순.
  - GET filter 의 응답은 보통 ms 단위라 자기-교정 워크플로우(빠른 재제출) 는 사실상 차단되지 않으나, 비-redirect 미래 케이스(예: AJAX/SPA 화) 에서는 §3 결정 6 의 dataset.origText 기반 복원 로직과 함께 재검토 필요.
- **버려진 대안**: `if (e.target.method.toLowerCase() === 'get') return;` 으로 GET 만 제외. 채택 안 함 (위 근거).
- **되돌릴 때 비용**: 1줄 추가로 가능 (`if (e.target.method && e.target.method.toLowerCase() === 'get') return;`).

### 결정 6: 텍스트 복원 미구현 (redirect 가정)

- **결정**: 핸들러는 disabled + 텍스트 변경만 수행하고, **원래 텍스트 복원 로직은 구현하지 않는다.** 단, 미래에 SPA-style 페이지 미전환 케이스가 생길 때를 대비해 원래 텍스트는 `btn.dataset.origText` 에 저장만 해둔다 (저장 비용 0, 후속 활용 가능).
- **근거**:
  - 현재 hub 의 모든 form POST 핸들러는 `Location: /admin/...` 또는 `Location: ?msg=...` 형태의 303/302 redirect 응답을 돌려주며, 브라우저가 즉시 새 페이지로 이동 → 원래 페이지의 DOM 은 GC 됨. 텍스트 복원 코드는 사용되는 일이 없다.
  - GET form 의 filter Apply 도 동일 페이지로 이동하지만 새 GET 요청이라 페이지가 재렌더 → 새 button 이 새로 그려짐.
  - `btn.dataset.origText` 저장만 해두는 것은 저장 비용 0 이고, 후속 phase 에서 비-redirect 케이스(예: 모달 form, AJAX) 가 추가되면 복원 로직 1줄 추가로 동작.
- **버려진 대안 1**: `try/catch` 로 `unload` 까지 감싸고 fallback 복원 → 과한 구조. 현재 redirect 보장이 깨질 일이 없음.
- **버려진 대안 2**: 복원 로직 자체를 작성 + `setTimeout(5000)` 후 자동 복원 → "5초 안에 응답이 안 오면 사용자가 다시 누를 수 있게" 라는 의도이지만, 그 경우 더블 POST 가능성이 다시 열림. 현재 hub 응답은 ms 단위라 실용적 가치 없음.
- **되돌릴 때 비용**: dataset 보존 덕에 후속 phase 에서 복원 로직 1줄 추가로 가능.

### 결정 7: 비활성화 후 라벨 = `처리 중...` (한국어, 정적 문자열)

- **결정**: 변경 후 텍스트는 한국어 정적 문자열 `처리 중...` 으로 통일. 버튼별 다른 라벨(`Submitting...`, `Deleting...`, `Stopping...` 등) 은 사용하지 않는다.
- **근거**:
  - Phase 12 (UX 일관성) 와 Phase 13 (한국어 confirm 메시지) 의 콘텐츠 언어 정책이 "운영자 화면은 한국어 통일" — 이미 page 곳곳이 한국어. 영어 라벨은 일관성 손상.
  - 버튼별 동적 라벨은 핸들러가 form 의 의도(rebuild/stop/delete/...) 를 구분해야 하는데 form action url 파싱 + 매핑 테이블이 필요. 단순 통일 라벨이 코드 단순.
  - `처리 중...` 은 모든 form 에 의미상 적용 가능 (filter Apply 의 GET 도 "처리 중" 으로 자연스러움 — 0.5초도 안 보이지만).
- **버려진 대안 1**: 영어 `Working…`. 한국어 정책 위배.
- **버려진 대안 2**: 빈 문자열로 비우고 spinner gif. 추가 자산 도입, 비범위.
- **되돌릴 때 비용**: 1줄 (스크립트의 한 문자열 리터럴 변경).

### 결정 8: 가드 적용 대상 = `button[type=submit]` + `type` 미지정 button

- **결정**: 핸들러는 `form.querySelectorAll('button[type=submit], button:not([type])')` 의 결과 모두에 적용. `<input type="submit">` 도 포함 (`form.querySelectorAll('input[type=submit]')`).
- **근거**:
  - HTML 표준에서 `<button>` 의 default type 은 `submit`. 코드베이스 grep 결과 모든 `<button>` 이 `type="submit"` 을 명시적으로 가지고 있어서 단순 selector `button[type=submit]` 으로도 충분하지만, 미래 페이지 추가 시 `type` 미지정 button 이 등장할 수 있으므로 양쪽 selector 를 모두 포함.
  - `<input type="submit">` 은 현재 코드베이스에 없지만 (확인됨: agents.gohtml/preview_detail.gohtml/etc 모두 `<button>` 사용), 표준 form submit 패턴이므로 미래를 대비해 selector 에 포함.
  - `<button type="button">` 은 form 제출에 무관하므로 제외 (이미 selector 에서 자동 제외됨).
- **버려진 대안**: `form.querySelector('[type=submit]')` 로 1개만 → form 안에 submit 버튼이 여러 개 있는 미래 케이스 (예: "저장" 과 "삭제" 같은 form) 에서 한쪽만 disabled 되는 사고. 모두 disabled 가 안전.
- **되돌릴 때 비용**: selector 1개 줄이는 것으로 가능.

## 4. 아키텍처/구조

### 변경 디렉토리 트리

```
internal/hub/views/layout.gohtml   (+ <script> 블록 1개, 약 10~15 줄)
```

코드(.go) 변경 0건. 다른 .gohtml 변경 0건. 정적 파일 신규 0개.

### 변경 라인 단위 (정확한 위치)

| 파일 | 변경 전 | 변경 후 |
|---|---|---|
| `layout.gohtml:28~30` | `<footer class="container"><small>Preview Hub MVP</small></footer>`<br>`</body>`<br>`</html>{{end}}` | `<footer class="container"><small>Preview Hub MVP</small></footer>`<br>`<script>` (블록 시작)<br>`...10~15 줄 본문...`<br>`</script>`<br>`</body>`<br>`</html>{{end}}` |

(라인 번호는 본 기획서 작성 시점의 layout.gohtml 기준. 구현 시점에 다른 PR 의 머지로 ±1~2 라인 이동 가능 — 위치 식별은 `</body>` 직전이라는 기준으로 확정.)

### 정확한 스크립트 본문 (검토 가능한 형태)

```html
<script>
(function () {
  document.addEventListener('submit', function (e) {
    if (e.defaultPrevented) return;
    var form = e.target;
    if (!form || form.tagName !== 'FORM') return;
    var btns = form.querySelectorAll('button[type=submit], button:not([type]), input[type=submit]');
    setTimeout(function () {
      for (var i = 0; i < btns.length; i++) {
        var b = btns[i];
        if (b.disabled) continue;
        if (b.dataset.origText === undefined) b.dataset.origText = b.textContent;
        b.disabled = true;
        if ('value' in b && b.tagName === 'INPUT') b.value = '처리 중...';
        else b.textContent = '처리 중...';
      }
    }, 0);
  }, false);
})();
</script>
```

세부 사항:
- IIFE 로 스코프 격리 (`_revealed` 같은 글로벌 누출 방지 — `settings.gohtml` 의 기존 패턴은 누출이 있지만 본 Phase 의 스크립트는 layout 전역이므로 더 보수적으로 처리).
- `false` 는 capture=false (=bubble 단계). confirm 의 onsubmit 가 먼저 실행되어 `e.defaultPrevented` 를 세팅한 뒤 본 핸들러가 호출됨을 보장 (§3 결정 3).
- `b.dataset.origText === undefined` 체크는 두 번 트리거되는 edge 케이스(있을 리 없지만) 에서 원본 텍스트가 `처리 중...` 으로 덮이는 것을 방지.
- `<input type="submit">` 은 텍스트가 `value` 속성이므로 `tagName === 'INPUT'` 분기로 `value` 갱신.

### 호출 흐름 (런타임 동작)

#### Case A: confirm 없는 일반 form (예: Add Agent Submit, repo_secrets 저장)

```
사용자 클릭 → form submit 이벤트 발화
  → onsubmit 인라인 핸들러 없음 → defaultPrevented = false
  → 글로벌 리스너 진입: e.defaultPrevented = false 이므로 setTimeout(0) 등록
  → 브라우저가 form 직렬화 + POST 전송 시작
  → 다음 tick: 버튼 disabled = true, 텍스트 = "처리 중..."
  → 사용자가 빠르게 다시 클릭해도 disabled 라 무반응
  → 서버 응답 (redirect) 도착 → 페이지 재로드 → 새 버튼이 새로 그려짐
```

#### Case B: Phase 13 의 confirm form (Delete / Teardown All / Stop) — 사용자 OK

```
사용자 클릭 → submit 이벤트 발화
  → 인라인 onsubmit="return confirm('...')" 실행 → confirm() 다이얼로그
  → 사용자 OK → confirm 반환 true → onsubmit 반환 true → defaultPrevented = false
  → 글로벌 리스너 진입: defaultPrevented = false 이므로 setTimeout(0) 등록
  → 이후 흐름 Case A 와 동일
```

#### Case C: Phase 13 의 confirm form — 사용자 취소

```
사용자 클릭 → submit 이벤트 발화
  → 인라인 onsubmit 실행 → confirm() 반환 false → onsubmit 반환 false
  → 브라우저가 자동으로 e.preventDefault() 호출 → defaultPrevented = true
  → 글로벌 리스너 진입: defaultPrevented = true → 즉시 return
  → 버튼 disabled 적용되지 않음, 텍스트 그대로
  → 사용자가 다시 클릭하면 정상적으로 confirm 다이얼로그 재출현 가능
```

#### Case D: JavaScript 비활성 환경

```
스크립트 블록 자체가 실행되지 않음
  → submit 리스너 등록 안 됨
  → form 이 그대로 제출됨 (가드 없음)
```

→ R-1 참조. 본 Phase 는 이를 수용한다. Phase 13 R-1 과 동일 입장: Admin UI 는 운영자 전용이며 JS 활성 전제.

## 5. 인터페이스 계약

### layout.gohtml 변경 계약

`{{define "layout"}}` 블록의 `</body>` 직전, `<footer>` 다음 줄에 `<script>...</script>` 블록 1개를 삽입한다.

스크립트 외부 인터페이스:
- 입력: 페이지 안의 모든 `<form>` 요소의 `submit` 이벤트.
- 출력 (DOM mutation): 해당 form 안의 `button[type=submit]`/`button:not([type])`/`input[type=submit]` 의 `disabled` 속성을 `true` 로, 텍스트(또는 input value) 를 `처리 중...` 으로. `data-orig-text` 속성에 원본 텍스트 저장.
- 부작용: 글로벌 변수 누출 0 개 (IIFE).

### 핸들러/Go 코드 동작 변화: 없음

본 Phase 는 .go 파일을 변경하지 않는다. 모든 `/admin/...` POST/GET 핸들러는 변화 없음.

### Phase 13 onsubmit 와의 호환성 명세

| Phase 13 form | onsubmit 결과 | defaultPrevented | 본 Phase 동작 |
|---|---|---|---|
| Agent Delete (OK) | true | false | 가드 적용 (disabled + "처리 중...") |
| Agent Delete (Cancel) | false | true | 가드 미적용 (버튼 그대로) |
| Teardown All (OK) | true | false | 가드 적용 |
| Teardown All (Cancel) | false | true | 가드 미적용 |
| Preview Stop (OK) | true | false | 가드 적용 |
| Preview Stop (Cancel) | false | true | 가드 미적용 |

### 응답 HTML body 안 substring 검증 기준

- 모든 페이지 응답 본문에 `처리 중...` 문자열이 정확히 1회 등장 (script 안 리터럴).
- 모든 페이지 응답 본문에 `addEventListener('submit'` substring 1회 등장.
- 모든 페이지 응답 본문에 `e.defaultPrevented` substring 1회 등장.

## 6. 기능 요구사항 체크리스트

- [ ] **F-1 (스크립트 블록 존재)**: `internal/hub/views/layout.gohtml` 의 `</body>` 직전, `<footer>` 다음 위치에 `<script>` 블록 1개가 정확히 추가되어 있다. — 검증:
    - `grep -n "<script>" internal/hub/views/layout.gohtml` 결과 정확히 1줄.
    - 해당 라인 번호가 `<footer>` 라인 번호 보다 크고 `</body>` 라인 번호 보다 작다.
- [ ] **F-2 (스크립트 본문 핵심 substring)**: 스크립트 블록 본문이 다음 4개 substring 을 모두 포함한다. 정규식 메타문자(`.`, `(`, 한국어 등)가 섞여 있으므로 fixed-string 매칭(`-F`) 으로 검증한다. — 검증:
    - `grep -cF "addEventListener('submit'" internal/hub/views/layout.gohtml` = 1
    - `grep -cF "e.defaultPrevented" internal/hub/views/layout.gohtml` = 1
    - `grep -cF "setTimeout" internal/hub/views/layout.gohtml` = 1
    - `grep -cF '처리 중...' internal/hub/views/layout.gohtml` = 1
- [ ] **F-3 (confirm 취소 시 가드 미적용 — 수동)**: 로컬 hub 를 띄운 뒤 `/admin/agents` 에서 `Delete` 버튼 클릭 → confirm 다이얼로그에서 `취소` 선택 → 버튼이 여전히 활성 상태(`disabled` 속성 없음)이고 라벨이 `Delete` 그대로. `Teardown All` / `Stop` 버튼도 동일. — 검증:
    - 수동: 브라우저 DevTools 콘솔에서 confirm 취소 직후 `document.querySelector('form[action$="delete"] button').disabled` 가 `false` 인지 단언.
    - 또는 (선택) Playwright 스크립트로 `dialog.dismiss()` 후 `expect(page.locator(...).getAttribute('disabled')).toBeNull()`.
- [ ] **F-4 (정상 클릭 시 가드 적용 — 수동)**: 로컬 hub 의 `/admin/agents/new` 또는 `Add Agent` 폼에서 Submit 클릭 → 0.x 초 안에 버튼이 disabled 되고 라벨이 `처리 중...` 으로 바뀐다. 응답 redirect 도착 전까지 다시 클릭해도 무반응. — 검증:
    - 수동: DevTools Network 탭에서 의도적으로 응답을 slow 3G 로 throttle → 버튼 disabled + 라벨 변경을 시각 확인.
    - DOM 콘솔: `document.activeElement.disabled === true && document.activeElement.textContent === '처리 중...'`.
- [ ] **F-5 (더블 클릭 방지 — 수동)**: `Add Agent` 폼 Submit 버튼을 빠르게 2회 연속 클릭 → 서버 access log 에 `POST /admin/agents` 가 1건만 기록된다. — 검증:
    - 수동: hub 로그(`slog`) 에 `POST /admin/agents` 1줄만 등장.
    - 또는 DevTools Network 탭에서 `/admin/agents` POST 요청 1건만 보임.
- [ ] **F-6 (모든 페이지에 스크립트 적용 — substring 검증)**: layout 을 상속하는 모든 페이지(dashboard / agents / agent_detail / previews / preview_detail / repos / repo_secrets / settings / token / test_build) 응답 본문에 `처리 중...` substring 이 정확히 1회 등장. — 검증:
    - 자동: 기존 admin UI 테스트가 응답 본문을 캡처하는 패턴이 있다면 1줄 단언 추가 (`assert.Contains(body, "처리 중...")`). 본 Phase 는 substring 단언 1건을 dashboard 페이지에 추가해 layout 적용을 증명 (다른 페이지는 동일 layout 이라 자동 보장).
    - 수동: 각 페이지 view-source 에서 `처리 중...` 1회 등장 확인.
- [ ] **F-7 (GET form 에도 적용)**: `/admin/previews` 의 filter `Apply` 버튼 클릭 시 잠시 disabled + `처리 중...` 으로 바뀌었다가 페이지 재로드. — 검증:
    - 수동: filter 입력 후 Apply 클릭 → 시각 확인.
- [ ] **F-8 (페이지 렌더 무회귀)**: `go build ./...` + `go test ./internal/hub/...` 둘 다 통과. layout 을 상속하는 모든 페이지가 200 응답하고 기존 substring 단언 그대로 통과. — 검증:
    - `go build ./...` exit 0.
    - `go test ./internal/hub/... -count=1` 모두 PASS.

## 7. 비기능 요구사항 체크리스트

- [ ] **NF-1 (이식성)**: 본 Phase 의 변경은 layout.gohtml 1파일의 `<script>` 블록 추가뿐. Go 코드 0줄 변경, 새 의존성 0개, 외부 JS 라이브러리 0개. — 검증: `git diff --stat` 결과가 `internal/hub/views/layout.gohtml` 1줄만 표시.
- [ ] **NF-2 (보안 — XSS / attribute injection)**: 추가되는 `<script>` 블록은 정적 리터럴이며 `{{...}}` 템플릿 변수가 0개. 사용자 입력이 스크립트로 흘러들 경로 없음. — 검증: `grep '{{' internal/hub/views/layout.gohtml` 결과의 매칭 라인이 모두 `<script>` 블록 외부 (header/nav/title 영역).
- [ ] **NF-3 (글로벌 변수 누출 0)**: 스크립트가 IIFE 로 감싸져 있어 `window` 에 새 변수가 추가되지 않음. — 검증: 스크립트 본문이 `(function () { ... })();` 패턴.
- [ ] **NF-4 (관측성 영향 0)**: 본 Phase 는 서버 로그·메트릭을 추가/변경하지 않는다. 핸들러 진입은 평소와 동일. — 검증: `git diff` 결과에 `slog.*` 새 호출 없음.
- [ ] **NF-5 (성능 — 페이지 응답 크기)**: 추가 `<script>` 블록 약 600~700 byte (UTF-8). 모든 페이지에 동일 블록이 들어가지만 정적 리터럴이라 gzip 압축 후 ~250 byte. 무시 수준. — 검증: 코드 리뷰.
- [ ] **NF-6 (성능 — 클라이언트 실행 비용)**: 페이지 로드 당 `addEventListener` 호출 1회 + form submit 당 핸들러 호출 1회 (querySelectorAll + setTimeout 1회). 사람의 클릭 빈도 기준 무시 수준. — 검증: 코드 리뷰.
- [ ] **NF-7 (콘텐츠 언어 일관성)**: 라벨 변경 텍스트 `처리 중...` 은 한국어. Phase 12 / Phase 13 의 한국어 통일 정책과 정렬. — 검증: 스크립트 본문에 영문 라벨 리터럴 없음 (`Submitting`, `Working`, `Processing` 등).
- [ ] **NF-8 (Phase 13 호환성)**: Phase 13 이 추가한 onsubmit confirm 3개와 충돌하지 않음. 이는 §5 의 호환성 표 6행 모두를 §6 F-3 의 수동 테스트로 검증. — 검증: §6 F-3 의 3개 form × OK/Cancel = 6 케이스 수동 확인.
- [ ] **NF-9 (renderHTML substring 단언 — 선택)**: 기존 admin UI 테스트 인프라가 응답 본문을 캡처한다면 dashboard 1개 페이지에 `처리 중...` substring 단언 1건을 추가해 layout 적용을 증명. — 검증: 추가된 단언이 PASS.

## 8. 리스크와 완화책

- **R-1: JavaScript 비활성 환경에서 가드 미동작 → 더블 클릭으로 인한 중복 POST 발생**. 사용자가 브라우저 JS 를 끈 경우 또는 NoScript 같은 확장으로 차단한 경우, 스크립트 블록이 실행되지 않아 가드가 적용되지 않음.
  - **완화 방침**: **본 Phase 는 이 동작을 수용한다.** Phase 13 R-1 과 동일 근거:
    1. Admin UI 는 운영자 전용이며 JS 활성 환경 전제 (token.gohtml 의 Copy 버튼, settings.gohtml 의 toggleSecret 등 이미 JS 의존).
    2. C-3 의 책임은 "운영자의 의도치 않은 더블 클릭 사고 방지" 이지 "악의적 더블 POST 방지" 가 아니다. 후자는 서버측 idempotency 영역으로 §2 비범위.
    3. 추가 안전장치(서버측 submit 토큰, DB UNIQUE 제약 등) 는 별도 Phase 의 결정이며 본 Phase 의 범위 확장 명시 거절.
  - **수용 명시**: 본 결정을 §3 결정 1 보강 항목으로 기록. 후속 Phase 에서 서버측 idempotency 가 필요해지면 별도 결정 문서로 다룸.

- **R-2: 미래에 form 이 비-redirect 응답을 돌려주는 케이스가 추가되어 버튼이 영구 disabled 로 남음**. 예: AJAX form, 모달 안 form, 또는 핸들러가 200 + 같은 페이지 그대로 렌더하는 케이스.
  - **완화 방침**:
    1. §3 결정 6 에 따라 `data-orig-text` 가 이미 저장되어 있어, 후속 phase 에서 비-redirect 케이스 발견 시 핸들러에 1줄 복원 로직 추가로 즉시 대응 가능 (`btn.disabled = false; btn.textContent = btn.dataset.origText;`).
    2. 현재 코드베이스에 비-redirect form 응답이 0건 임을 §3 결정 6 의 근거에서 명시 — 본 Phase 는 redirect 가정을 명문화한 위에서 단순 구조 채택.
  - **수용 명시**: 후속 Phase (예: AJAX 도입 Phase) 의 작업 항목으로 인계.

- **R-3: 동기 disabled 가 일부 브라우저에서 form 제출 차단 — 만약 setTimeout(0) 이 충분히 늦지 않다면**. `setTimeout(0)` 의 콜백은 현재 task(submit 이벤트 dispatch + onsubmit 핸들러 실행) 가 종료된 뒤 macrotask 로 enqueue 된다. submit 이벤트의 task 가 종료되면 브라우저는 form submission algorithm 에 따라 직렬화·POST 전송을 진행하므로, 콜백 시점에는 송신이 이미 시작되어 있다. 그럼에도 브라우저 구현 차이로 form 직렬화 전에 disabled 가 적용될 가능성을 100% 배제할 수는 없음.
  - **완화 방침**:
    1. 현재 `setTimeout(0)` 은 W3C HTML5 의 macrotask 큐에 들어가며, submit 이벤트 핸들러가 끝난 뒤에야 실행됨이 명세로 보장됨. submit 이벤트 핸들러 종료 시점에는 브라우저가 form 직렬화를 이미 시작하므로 사실상 안전.
    2. §6 F-5 의 수동 테스트 (DevTools Network throttling 환경에서 더블 클릭 시도 + access log 1줄 확인) 가 실증 검증 역할.
    3. 만약 미래에 특정 브라우저(특히 모바일 Safari 구버전) 에서 문제 보고되면, `setTimeout(0)` 을 `setTimeout(50)` 으로 늘리는 것으로 즉시 우회 가능 — 코드 1글자 변경.
  - **수용 명시**: 본 결정의 잔여 위험은 현실적 무시 수준이며, 발생 시 핫픽스 비용 1줄.

- **R-4: 스크립트 블록 안에서 한국어 문자열의 인코딩 문제**. layout.gohtml 은 UTF-8 로 저장되며, `Content-Type: text/html; charset=utf-8` 응답 헤더로 전송됨. 그러나 일부 환경에서 `<script>` 안의 non-ASCII 문자열이 깨질 가능성을 배제할 수 없음.
  - **완화 방침**:
    1. 기존 `settings.gohtml` 의 `<script>` 블록이 영어만 사용하지만, layout 의 `<header>`, agents.gohtml 등 본문에는 이미 한국어가 등장하고 정상 렌더되고 있음 — 인코딩은 문제 없음.
    2. `처리 중...` 의 UTF-8 byte sequence (`처` = `\xEC\xB2\x98`, ...) 는 JS 문자열 리터럴에서 정상 해석됨.
    3. §6 F-2 의 grep 검증이 byte 단위 substring 일치를 단언하므로 인코딩 손상은 즉시 검출됨.
  - **수용 명시**: 기존 한국어 페이지가 정상 동작하므로 추가 위험 없음. 만약 검출되면 `\u` escape (`처리 중...`) 로 즉시 우회 가능.

- **R-5: cross-form submit (`<button form="otherFormID">`) 미정의 동작**. HTML5 의 `button` 요소는 `form` attribute 로 자기 외부 form 을 제출할 수 있으나, 이 경우 `e.target` 이 외부 form 이 되며 본 핸들러의 `form.querySelectorAll('button[type=submit] ...')` 는 외부 form 안의 버튼만 찾으므로 트리거한 버튼 자체는 disabled 되지 않을 수 있음.
  - **완화 방침**: 본 코드베이스에 `<button form="...">` cross-form submit 사용처 0건임을 grep 으로 확인 (`grep -rE 'button[^>]+form=' internal/hub/views`). 본 Phase 비범위로 명시. 미래에 cross-form 패턴이 추가되면 핸들러를 `e.submitter` 기반으로 보강하는 별도 Phase 필요.
  - **수용 명시**: 미래 form 추가 시 주의.

- **R-6: 비동기 cancel 패턴의 onsubmit 미지원** (예: `onsubmit="event.preventDefault(); validateAsync().then(ok => ok && form.submit())"`). 본 Phase 의 `e.defaultPrevented` 동기 검사 모델은 onsubmit 가 동기적으로 preventDefault 를 호출하지 않고 비동기로 결정하는 패턴을 처리할 수 없음. 이런 form 은 가드가 적용되지 않거나 잘못된 시점에 disabled 됨.
  - **완화 방침**: 본 코드베이스에 비동기 cancel 패턴 0건 (Phase 13 의 confirm 3개는 모두 동기). 본 Phase 비범위로 명시. 미래에 AJAX 폼 검증 등 비동기 onsubmit 가 추가되면 별도 Phase 에서 핸들러 모델 재설계 필요.
  - **수용 명시**: 미래 form 추가 시 주의.

## 9. 다음 Phase 연결점

- **C-1 (preview_detail auto-refresh)**: 별도 Phase. preview_detail 의 `<head>` 에 `<meta http-equiv="refresh" content="5">` 를 status 별로 추가. 본 Phase 의 layout `<script>` 와는 위치(head vs body) 가 다르고 책임도 다르므로 머지 충돌 가능성 0.
- **C-2 (success flash 메시지)**: 별도 Phase. handler 의 redirect URL 에 `?flash=stopped` 를 추가하고 layout 또는 페이지에서 `<article role="alert">` 로 표시. 본 Phase 의 가드와 결합 시 더 매끄러운 UX (가드로 더블 클릭 방지 → 한 번의 요청 → flash 로 성공 확인).
- **C-4 (?msg= URL-decode)**: 별도 Phase. handler 또는 template helper 가 query 의 conflict 메시지를 디코드해 표시. 본 Phase 와 직교.
- **서버측 idempotency**: 본 Phase 의 클라이언트 가드는 1차 방어선. 후속 Phase 에서 DB UNIQUE 제약 (예: `previews(repo_full_name, pr_number)` 조합) 또는 submit token (CSRF 토큰과 결합) 도입 시 2차 방어선으로 보강. 본 Phase 의 `data-orig-text` 보존 + IIFE 격리 패턴이 후속 확장의 기반.
- **layout 안 글로벌 JS 자산이 커지면 별도 파일로 분리하는 시점**: 본 Phase 의 스크립트는 약 15줄. Phase 12 결정 6 (별도 CSS 파일 미도입 기준) 과 동일하게, 글로벌 JS 가 50줄을 넘기 전에는 inline 유지. 50줄 초과 시점에 `static/js/admin.js` 로 분리하는 별도 결정 Phase 를 둔다.
- **C-3 의 검증 자동화 강화**: 본 Phase 는 수동 검증 비중이 높음 (F-3, F-4, F-5, F-7). 후속 Phase 에서 Playwright e2e 가 도입되면 6 케이스 (Phase 13 의 3 form × OK/Cancel) 를 자동화하는 것이 자연스럽다 — 본 기획서의 §5 호환성 표를 그대로 테스트 케이스로 사용.
