# Phase 12 — UX Consistency (카테고리 A: 시각적 일관성)

## 1. Phase 개요

`docs/specs/ux-improvement-plan.md` §2 카테고리 A 의 3개 항목(A-1 nav active highlight / A-2 status badge 통일 / A-3 버튼 클래스 표준화)을 구현해 Admin UI 의 시각적 일관성을 개선한다. 본 Phase 는 **기능 추가가 아닌 표시 일관성 정리**에 한정되며, 핸들러 로직·DB·메시지 프로토콜은 일절 건드리지 않는다.

끝났을 때의 상태:
- 모든 페이지의 nav 에서 현재 페이지 항목이 굵게/색으로 강조되고 `aria-current="page"` 속성을 가진다.
- preview status (`queued`/`assigned`/`building`/`running`/`teardown`/`done`/`failed`) 의 시각 표현이 dashboard / previews 목록 / preview_detail 세 페이지에서 동일하다 (`{{statusBadge .Status}}` template helper 단일 진입점).
- 버튼·링크-as-button 의 클래스가 의도(primary / secondary outline / contrast) 기반 3 분류로 통일되어 혼재된 `outline`, `secondary outline`, 인라인 `class=""` 가 정리된다.
- `go build ./...` 통과, 기존 단위테스트 전부 통과, 페이지 렌더링이 깨지지 않음.

## 2. 범위와 비범위

**범위**
- `internal/hub/views/layout.gohtml` — nav 에 현재 경로 비교 로직 추가 (A-1).
- `internal/hub/admin_ui.go` — `mustParsePages` 가 layout 파싱 시 `template.FuncMap` 등록 (`statusBadge`, `navActive` 등 helper). 모든 view struct 의 공통 필드로 `RequestPath string` 추가 + 각 핸들러에서 채우기 (A-1, A-2 의존).
- `internal/hub/views/dashboard.gohtml` — Status breakdown 테이블의 status 컬럼 헤더는 그대로, **본문 셀 텍스트 자체는 숫자**이므로 본 Phase 에서 badge 가 들어가는 곳은 status 컬럼 헤더 또는 별도 status 표시 위치가 아니다 — dashboard 의 status 표현은 status 이름 헤더만 존재. **dashboard.gohtml 은 A-2 적용 대상에서 사실상 빈손**(헤더 텍스트만 있음, 동적 status 출력 없음). 명시적으로 §3 결정 4 에서 다룬다.
- `internal/hub/views/previews.gohtml` — Status 컬럼의 `{{.Status}}` → `{{statusBadge .Status}}` (A-2).
- `internal/hub/views/preview_detail.gohtml` — Status `<dd>` 안의 if/else 분기 색상 처리를 helper 호출로 대체 (A-2).
- `internal/hub/views/agents.gohtml`, `agent_detail.gohtml`, `preview_detail.gohtml`, `repos.gohtml`, `repo_secrets.gohtml`, `test_build.gohtml`, `previews.gohtml`, `settings.gohtml`, `token.gohtml` — 버튼 클래스 표준화 (A-3).

**비범위**
- 기능 추가, 핸들러 로직 변경, DB 스키마/쿼리 변경.
- 새 단위테스트 추가 (helper 의 순수성은 §6 의 수동 검증으로 충분 — 본 Phase 는 표시 변경에 한정). **단, NF-2 의 helper escape 테스트 1건은 예외로 추가** (보안 회귀 방지용 — 자세한 정의는 §7 NF-2 참조).
- A-1/A-2/A-3 외 카테고리 A-4 (`<html lang="en">` → `lang="ko"`) 및 카테고리 B/C/D/E/F.
- `agents.gohtml` 의 **agent Status 컬럼**(online/offline) 은 별도 도메인이라 `statusBadge` helper 를 적용하지 않는다.
- Pico CSS 버전·CDN URL 변경.
- token.gohtml 의 step-card / os-tabs 등 기존 inline `<style>` 블록 재구조화.
- 모바일 레이아웃 / dropdown(`<details>`) 도입 (D-4 의 영역).
- breadcrumb 도입 (D-5).

## 3. 설계 결정 및 근거

### 결정 1: Helper 는 `template.FuncMap` 에 등록한다 (`mustParsePages` 내부에서 일괄)
- **결정**: `mustParsePages` 가 layout 을 파싱할 때 `template.New(p).Funcs(adminFuncs())` 형태로 FuncMap 을 주입한다. `adminFuncs()` 는 `template.FuncMap{"statusBadge": statusBadge, "navActive": navActive}` 를 반환하는 내부 함수.
- **근거**: 모든 페이지가 layout 을 공유하므로 한 곳에서 등록하면 모든 페이지에서 호출 가능. helper 는 순수함수(입력만으로 출력 결정)라 핸들러별 주입이 불필요.
- **버려진 대안 1**: 각 페이지 파싱 시 `Funcs` 호출 → 중복. layout 파싱 단계에서 등록하면 후속 `t.Parse(pageBytes)` 가 같은 트리를 공유하므로 한 번이면 충분.
- **버려진 대안 2**: 핸들러에서 매 요청마다 `template.New(...).Funcs(...)` → 시작 시 1회 파싱(결정 5 in admin_ui.go) 원칙 위배.
- **되돌릴 때 비용**: 작다 (`adminFuncs()` 함수 1개 제거 + Funcs 호출 1줄 제거).

### 결정 2: `statusBadge` 의 시그니처 — `func(status string) template.HTML`
- **결정**: 입력은 status 문자열, 출력은 `template.HTML` (auto-escape 우회). 매핑 테이블은 함수 내부 `switch` 또는 `map[string]struct{...}`. 알 수 없는 status 입력 시 — fallback 으로 plain `<span>{{html-escaped status}}</span>` 반환 (panic 금지, 보안상 escape 는 보장).
- **근거**: html/template 의 auto-escape 가 `<span>` 자체를 escape 하지 않게 하려면 `template.HTML` 반환이 필수. 함수가 자체적으로 에스케이프(`html.EscapeString`)를 보장하면 XSS 위험 없음 — status 는 DB 의 enum 값이지만 미래에 다른 값이 들어올 수 있으므로 안전망.
- **버려진 대안 1**: `func(status string) string` + 템플릿에서 `{{statusBadge .Status | safe}}` 식 추가 helper → helper 2개 필요, 호출 측 부담 증가.
- **버려진 대안 2**: 페이지마다 if/else 유지 → A-2 의 목적(중복 제거) 자체 부정.
- **되돌릴 때 비용**: 작다. helper 본문을 페이지 if/else 로 inline 화.

### 결정 3: `navActive` helper — `func(currentPath, linkPath string) template.HTMLAttr`
- **결정**: 두 문자열 비교 후 일치 시 ` aria-current="page" class="nav-active" style="font-weight:700; color:var(--pico-primary)"`, 불일치 시 빈 문자열을 `template.HTMLAttr` 로 반환. 매칭 규칙은 **모든 nav 항목에 동일하게** 다음 한 줄을 적용:
  ```
  match := currentPath == linkPath || strings.HasPrefix(currentPath, linkPath+"/")
  ```
  즉 정확 일치이거나 `linkPath/` 로 시작할 때만 active. 단순 `strings.HasPrefix(currentPath, linkPath)` 는 `/admin/agents-history` 같은 경계 누수 위험이 있으므로 위 규칙을 강제한다. 이 규칙으로 `/admin` 도 정확 일치만 Dashboard active 가 되며 (`/admin/agents` 는 `/admin/` 으로 시작 ≠ `/admin` 이므로 Dashboard active 아님), `/admin/agents/{id}` 는 `/admin/agents/` 로 시작하므로 Agents active 가 된다.
- **근거**: 단일 매칭 규칙으로 nav 항목별 분기 없이 helper 한 줄에서 처리. `linkPath+"/"` 접미사 보장이 prefix 경계 누수를 막는 핵심 트릭.
- **버려진 대안 1**: 핸들러에서 active 키 결정 후 view struct 에 넣기 → 페이지마다 동일 로직 반복. 공통 base view 도입은 §3 결정 5 와 충돌.
- **버려진 대안 2**: layout 안에 if 체인 직접 작성 → if 5단 분기로 가독성 저하.
- **버려진 대안 3**: nav 항목별로 다른 매칭 규칙 (Dashboard 만 정확 일치, 나머지는 단순 prefix) → `linkPath+"/"` 트릭 1줄로 통일 가능하므로 분기 불필요.
- **되돌릴 때 비용**: 작다.

### 결정 4: Dashboard 의 Status breakdown 은 A-2 적용 대상이 아니다 (현재 구조상)
- **결정**: `dashboard.gohtml` 의 Status breakdown 테이블은 status 이름이 컬럼 헤더(`<th>queued</th>` 등)이고 본문은 카운트 숫자(`<td>{{.StatusCounts.queued}}</td>`)다. status 자체가 동적 표시 되는 위치가 아니므로 본 Phase 에서는 dashboard 를 수정하지 않는다. 향후 E-2 (클릭 가능한 카드) Phase 에서 status 텍스트가 동적으로 들어가는 시점에 helper 적용.
- **근거**: 사용자 요청문이 dashboard 를 적용 대상으로 명시했으나, 실제 코드를 보면 status 의 _이름 자체_ 가 정적 헤더라 helper 를 적용할 곳이 없다. 카운트 숫자에 색을 입히는 것은 별도 디자인 결정이라 본 Phase 의 범위와 다르다.
- **명시적 확인 절차**: `dashboard.gohtml` 본문에 `{{statusBadge` 가 0회 등장. 검증자가 "왜 dashboard 가 변경되지 않았나" 질문 시 본 결정과 §6 F-2-d 검증 항목으로 답변.
- **버려진 대안**: `<th><span class="status-pill ...">queued</span></th>` 식으로 헤더 자체에 색을 넣기 → status 카운트 숫자가 강조되어야 정보가 전달되는데, 헤더 색상이 더 강하면 시각 우선순위가 역전된다. 또한 상태 이름이 정적이라 helper 의 가치가 없다.
- **되돌릴 때 비용**: 0 (변경 없음).

### 결정 5: View struct 공통 필드 vs 핸들러별 구조체에 추가
- **결정**: 공통 base view struct 를 새로 도입하지 **않고**, 기존 각 view struct(`dashboardView`, `agentsView`, `previewsView`, `previewDetailView`, `agentDetailView`, `tokenView`, `testBuildView`, `settingsView`, `reposIndexView`, `repoSecretsView`) 모두에 `RequestPath string` 필드를 1개씩 추가한다. 각 핸들러에서 `view.RequestPath = r.URL.Path` 한 줄 채움.
- **근거**: Go 의 embedded struct 도입은 모든 사이트(템플릿 + 핸들러)에 영향 — 비용/이득 비율이 나쁘다. 1줄 반복 × 10 핸들러는 가독성 손실이 크지 않고, 각 view struct 의 책임이 명확히 유지됨.
- **버려진 대안 1**: `type baseView struct { RequestPath string }` + 각 view 에 `baseView` 임베드 → 임베드 + initializer 변경, fake/test 영향 점검 필요. 가치 대비 비용 큼.
- **버려진 대안 2**: `renderHTML` 안에서 `r` 를 받아 자동 주입 → 현재 `renderHTML(w, status, page, data)` 시그니처가 `data any` 라 reflect 로 필드 set 해야 함. 마법 코드.
- **되돌릴 때 비용**: 10 줄 (`view.RequestPath = r.URL.Path` 줄과 struct field) 제거.

### 결정 6: Pico CSS 토큰 우선, 부족 시 inline style 의 CSS 변수 사용
- **결정**: Pico v2 의 의미적 클래스 (`<mark>`, `class="contrast"`, `class="secondary"`) 를 우선 사용. 매핑 불가능한 색상은 `style="color:var(--pico-color-XXX-500)"` 인라인 스타일을 helper 가 직접 출력. 새로운 CSS 파일은 만들지 않는다.
- **근거**: Pico v2 가 prefers-color-scheme 자동 처리를 하므로 CSS 변수를 사용하면 다크/라이트 모두 호환. 별도 CSS 파일 도입은 Phase 3 결정(외부 CSS 미사용)과 충돌.
- **버려진 대안 1**: `internal/hub/views/admin.css` 새 파일 + `<link>` 추가 → embed.FS 변경, MIME-type 라우트 추가 등 표면적 증가.
- **버려진 대안 2**: `<style>` 블록을 layout 에 직접 추가 → CSS 의 cascade 가 페이지별 inline style 과 충돌할 위험.
- **되돌릴 때 비용**: 작다.

## 4. 아키텍처/구조

### 변경 디렉토리 트리 (변경 파일만)

```
internal/hub/admin_ui.go                         (+ adminFuncs/statusBadge/navActive 함수, mustParsePages 의 Funcs 호출 1줄, 각 view struct +1 필드, 각 핸들러 +1 줄)
internal/hub/views/layout.gohtml                 (nav 의 5개 <li> 에 navActive helper 호출)
internal/hub/views/previews.gohtml               (Status 컬럼 helper 호출)
internal/hub/views/preview_detail.gohtml         (Status <dd> 의 if/else 제거 + helper 호출, 버튼 클래스 정리)
internal/hub/views/agents.gohtml                 (버튼 클래스 정리)
internal/hub/views/agent_detail.gohtml           (버튼 클래스 정리 — 현재는 버튼이 거의 없으나 향후 합류분 대비)
internal/hub/views/repos.gohtml                  (Edit secrets 링크 클래스 정리)
internal/hub/views/repo_secrets.gohtml           (Save 버튼 클래스 정리)
internal/hub/views/test_build.gohtml             (Trigger Build / Cancel 클래스 정리 — 이미 표준에 가까움)
internal/hub/views/settings.gohtml               (Copy / Reveal 버튼 클래스 정리)
internal/hub/views/token.gohtml                  (마지막 "Agent 목록으로" 링크 클래스 정리)
```

dashboard.gohtml — 결정 4 에 따라 변경 없음.

### 호출 흐름 (모든 페이지 공통)

```
HTTP request → handler →
  view.RequestPath = r.URL.Path 채움 →
  renderHTML(w, status, page, view) →
  ExecuteTemplate("layout", view) →
    layout.gohtml 의 nav 가 {{navActive .RequestPath "/admin"}} 등 호출 →
    {{template "content" .}} 가 페이지 본문 실행 →
      previews.gohtml 본문에서 {{statusBadge .Status}} 호출 →
      preview_detail.gohtml 의 Status dd 에서 {{statusBadge .Preview.Status}} 호출
```

## 5. 인터페이스 계약

### 새 helper 함수 (admin_ui.go 의 mustParsePages 함수 근처에 정의)

```go
// adminFuncs 는 모든 admin 페이지가 공유하는 template helper 셋을 반환한다.
// mustParsePages 의 layout 파싱 단계에서 Funcs() 로 등록된다.
func adminFuncs() template.FuncMap {
    return template.FuncMap{
        "statusBadge": statusBadge,
        "navActive":   navActive,
    }
}

// statusBadge 는 preview status 문자열을 색·라운드를 가진 <span> 으로 변환해 반환한다.
// 매핑: queued/assigned=gray, building=blue, running=green(<mark>), teardown=orange,
//       done=muted, failed=red. 알 수 없는 값은 escape 후 plain <span>.
func statusBadge(status string) template.HTML {
    // 구현 가이드 (코드 단계에서 작성):
    //   - status 별 (color, useMark) 결정
    //   - useMark==true (running) → <mark>{{status}}</mark>
    //   - 그 외 → <span class="status-pill" style="...">{{status}}</span>
    //   - 알 수 없는 status → <span>{{html.EscapeString(status)}}</span>
    //   - 출력 HTML 은 모두 status 를 html.EscapeString 으로 escape 후 삽입.
}

// navActive 는 nav 링크의 active 여부를 결정해 aria-current 속성과 강조 클래스/스타일을
// 합친 attribute 문자열을 반환한다. 매칭 규칙(모든 nav 항목 공통):
//   match := currentPath == linkPath || strings.HasPrefix(currentPath, linkPath+"/")
// 즉 정확 일치이거나 `linkPath/` 로 시작할 때만 active. (단순 HasPrefix 는
// `/admin/agents-history` 같은 경계 누수 위험이 있어 `linkPath+"/"` 접미사 강제.)
//
// 출력 형태 (단일):
//   일치 시:  ` aria-current="page" class="nav-active" style="font-weight:700; color:var(--pico-primary)"`
//   불일치:   `` (빈 문자열)
//
// `class="nav-active"` 는 본 Phase 시점에서 시각에 영향 없는 dead identifier 지만
// 후속 Phase 의 별도 CSS 도입 시 selector hook 으로 둔다 (결정 9).
func navActive(currentPath, linkPath string) template.HTMLAttr {
    // 구현 가이드: html.EscapeString 불필요 (입력은 코드 상수 + URL.Path).
    //   strings 비교 후 정적 문자열만 반환하므로 attribute 컨텍스트 안전.
}

// statusBadge 의 출력은 inline element (`<mark>` 또는 `<span>`) 로 본문 컨텍스트
// (td/dd) 전용. 헤더(`<th>`) 컨텍스트에서는 mark 의 background 가 헤더 굵기와
// 시각 충돌하므로 적용하지 말 것 — 후속 Phase 의 dashboard 적용 시 별도 디자인
// 결정 필요 (결정 4 참조).
```

### `mustParsePages` 의 변경 1줄

```go
// 변경 전:
t := template.Must(template.New(p).Parse(string(layoutBytes)))

// 변경 후:
t := template.Must(template.New(p).Funcs(adminFuncs()).Parse(string(layoutBytes)))
```

### 출력 HTML 예시

| status     | 출력 HTML 예시 |
|------------|----------------|
| `queued`   | `<span class="status-pill" style="display:inline-block; padding:0.1em 0.55em; border-radius:10px; font-size:0.85em; background:var(--pico-secondary-background); color:var(--pico-secondary)">queued</span>` |
| `assigned` | (queued 와 동일 색 — gray) |
| `building` | `<span class="status-pill" style="...background:var(--pico-primary-background); color:var(--pico-primary)">building</span>` |
| `running`  | `<mark>running</mark>` (Pico 의 mark 토큰 — green 계열) |
| `teardown` | `<span class="status-pill" style="...background:#fff3cd; color:#856404">teardown</span>` (orange — Pico 토큰에 직접 매칭이 없어 inline 색) |
| `done`     | `<span class="status-pill" style="...color:var(--pico-muted-color)">done</span>` |
| `failed`   | `<span class="status-pill" style="...background:#f8d7da; color:var(--pico-color-red-500)">failed</span>` |

> 정확한 색 값과 패딩은 구현 단계에서 시각 확인 후 미세조정 가능. 본 표는 helper 동작의 검증 기준.

### nav active 출력 예시 (layout.gohtml)

```gohtml
{{/* 변경 전 */}}
<li><a href="/admin">Dashboard</a></li>
<li><a href="/admin/agents">Agents</a></li>

{{/* 변경 후 */}}
<li><a href="/admin"{{navActive .RequestPath "/admin"}}>Dashboard</a></li>
<li><a href="/admin/agents"{{navActive .RequestPath "/admin/agents"}}>Agents</a></li>
{{/* Previews / Repos / Settings 동일 패턴 */}}
```

active 시 렌더 결과 (helper 가 반환하는 attribute 문자열 단일 형태):
```html
<li><a href="/admin/agents" aria-current="page" class="nav-active" style="font-weight:700; color:var(--pico-primary)">Agents</a></li>
```

helper 는 일치 시 항상 다음 한 줄을 반환한다 (불일치 시 빈 문자열):
```
 aria-current="page" class="nav-active" style="font-weight:700; color:var(--pico-primary)"
```

시각 강조는 inline `style` 로 해결하고 (결정 6 — 별도 CSS 미도입), `class="nav-active"` 는 후속 Phase 의 selector hook 으로 같이 둔다 (결정 9).

### A-3 버튼 클래스 매핑 테이블 (현재 → 새 표준)

| 파일 | 위치 (대략) | 버튼/링크 텍스트 | 의도 분류 | 현재 클래스 | 새 클래스 |
|------|------------|----------------|-----------|------------|----------|
| `agents.gohtml` | 행별 actions | `Configure` (link-as-button) | secondary | `outline` | `secondary outline` |
| `agents.gohtml` | 행별 actions | `Test Build` (link) | secondary | `outline` | `secondary outline` |
| `agents.gohtml` | 행별 actions | `Teardown All` | contrast (파괴) | `outline` | `contrast` |
| `agents.gohtml` | 행별 actions | `Delete` | contrast (파괴) | `secondary outline` | `contrast` |
| `agents.gohtml` | Add Agent | `Submit` | primary | `(없음)` | `(없음 — 기본 유지)` |
| `agent_detail.gohtml` | (현재 버튼 없음) | — | — | — | — |
| `preview_detail.gohtml` | actions | `Re-run` | primary | `(없음)` | `(없음 — 기본 유지)` |
| `preview_detail.gohtml` | actions | `Stop` | contrast (파괴) | `secondary` | `contrast` |
| `repos.gohtml` | 행 action | `Edit secrets` (link) | secondary | `outline` | `secondary outline` |
| `repo_secrets.gohtml` | form 하단 | `Save` | primary | `(없음)` | `(없음 — 기본 유지)` |
| `test_build.gohtml` | form actions | `Trigger Build` | primary | `(없음)` | `(없음 — 기본 유지)` |
| `test_build.gohtml` | form actions | `Cancel` (link) | secondary | `secondary outline` | `secondary outline` (변경 없음) |
| `previews.gohtml` | filter | `Apply` | primary | `(없음)` | `(없음 — 기본 유지)` |
| `settings.gohtml` | webhook url | `Copy` | secondary | `outline` | `secondary outline` |
| `settings.gohtml` | webhook secret | `Reveal` | secondary | `outline` | `secondary outline` |
| `token.gohtml` | step 0/1/2 | `Copy` (×N) | secondary | `copy-btn` (custom) | `copy-btn` 유지 (token.gohtml 의 다듬어진 디자인을 깨지 않음 — 결정 7 참조) |
| `token.gohtml` | 하단 | `← Agent 목록으로` (link) | secondary | `secondary` | `secondary outline` |

### 결정 7 (보강): token.gohtml 의 `copy-btn` 은 표준 적용 제외
- **결정**: token.gohtml 의 inline `<style>` 블록이 정의한 `.copy-btn` 클래스는 absolute positioning 과 backdrop-filter 등 페이지 고유 디자인이라, secondary outline 으로 일괄 치환하면 layout 이 깨진다. 본 Phase 에서 그대로 유지. 단, token.gohtml 의 `<a class='secondary'>← Agent 목록으로</a>` 는 페이지 고유 디자인이 아닌 일반 nav-back 링크이므로 §5 매핑 테이블대로 `secondary outline` 으로 변경한다 (`copy-btn` 만 보존, 그 외 일반 링크는 표준 적용).
- **근거**: A-3 의 목표는 의도 표현 통일이지 디자인 회귀가 아니다. token.gohtml 은 이미 다듬어져 있다 (ux-improvement-plan.md §1 참고).
- **검증**: token.gohtml 의 시각 회귀 없음 (수동 확인 — F-3-c).

### 결정 9: `nav-active` 클래스는 의도적 미사용 hook
- **결정**: 본 Phase 는 시각 강조를 inline `style` 로만 처리하고, `class="nav-active"` 는 후속 Phase 에서 별도 CSS 도입 시 selector hook 으로 같이 둔다. 본 Phase 시점에서는 시각에 영향 없는 dead identifier — 결정 6 (별도 CSS 미도입) 과 의도적으로 공존.
- **근거**: A-2/A-3 진행 후 후속 카테고리 D-5 (breadcrumb) 또는 별도 admin.css 도입 시 selector 가 즉시 필요. 그때 helper 본문 변경 없이 CSS 만 추가하면 된다.
- **버려진 대안**: class 자체를 제거 → 후속 Phase 에서 helper 출력 변경이 필요해지므로 결합도 증가.
- **되돌릴 때 비용**: helper 출력에서 `class="nav-active"` 토큰 제거 1줄.

## 6. 기능 요구사항 체크리스트

- [ ] **F-1 (helper 등록)**: `adminFuncs()` 함수가 `internal/hub/admin_ui.go` 에 존재하고 `template.FuncMap{"statusBadge": ..., "navActive": ...}` 두 키를 반환한다. — 검증: `grep -n "adminFuncs" internal/hub/admin_ui.go` 1회 이상 매칭, 함수 본문에 두 키 존재.
- [ ] **F-2 (statusBadge)**:
  - [ ] **F-2-a**: `statusBadge` 가 7개 status 입력에 대해 §5 표의 출력을 반환한다. — 검증: 작은 main 테스트나 임시 `go run` 스니펫으로 `statusBadge("running")` 호출 → `<mark>running</mark>` 정확히 일치 확인 (또는 helper 를 직접 호출하는 핸들러를 통한 응답 본문 검증).
  - [ ] **F-2-b**: 알 수 없는 status (예: `"oops<script>"`) 입력 시 출력 HTML 안에 `<script>` 가 raw 로 들어가지 않는다 (escape 보장). — 검증: `statusBadge("oops<script>")` 결과 문자열에 `&lt;script&gt;` 포함, `<script>` raw 미포함.
  - [ ] **F-2-c**: `previews.gohtml` 의 Status 컬럼 셀이 `{{statusBadge .Status}}` 1개 호출만 가진다 (`{{.Status}}` 직접 출력 제거). — 검증: `grep "{{.Status}}" internal/hub/views/previews.gohtml` 0회, `grep "statusBadge" internal/hub/views/previews.gohtml` 1회 이상.
  - [ ] **F-2-d**: `preview_detail.gohtml` 의 Status `<dd>` 안의 if/else if 분기 4개가 제거되고 `{{statusBadge .Preview.Status}}` 1줄로 대체. **단순 `grep "eq .Preview.Status"` 는 line 11 의 `{{if and (eq .Preview.Status "failed") .Diagnosis}}` 와 충돌하므로 Status `<dd>` 블록 내부에 한정**. — 검증:
      - 블록 한정 grep: `awk '/<dt>Status<\/dt>/,/<\/dd>/' internal/hub/views/preview_detail.gohtml | grep -c 'eq .Preview.Status'` 결과 0회.
      - helper 호출 1회 확인: `grep -c 'statusBadge .Preview.Status' internal/hub/views/preview_detail.gohtml` 결과 ≥ 1.
  - [ ] **F-2-e**: dashboard.gohtml 은 변경되지 않는다 (결정 4). — 검증: `git diff internal/hub/views/dashboard.gohtml` 빈 결과.
- [ ] **F-3 (nav active)**:
  - [ ] **F-3-a**: `layout.gohtml` 의 nav `<li>` 5개(`Dashboard`/`Agents`/`Previews`/`Repos`/`Settings`)가 모두 `{{navActive .RequestPath ...}}` 호출을 포함한다. — 검증: `grep -c "navActive" internal/hub/views/layout.gohtml` 결과 ≥ 5.
  - [ ] **F-3-b**: 모든 view struct 가 `RequestPath string` 필드를 가지고, 모든 페이지 핸들러가 view 초기화 직후 `view.RequestPath = r.URL.Path` 를 호출한다. — 검증 (magic number 사용 금지):
      1. 아래 §6 의 **핸들러 함수 표** 에 정의된 모든 view struct 정의에 `RequestPath string` 필드 1줄 존재 (struct 정의별 grep, 표의 view struct 칼럼 기준).
      2. 표의 모든 페이지 핸들러 함수에서 view 초기화 직후 `view.RequestPath = r.URL.Path` 호출 (handler 함수별로 함수 본문 grep). redirect-only 핸들러 (`testBuildSubmit`, `repoSecretsPost` 의 redirect 분기 등 — view 가 없는 경로) 는 채울 필요 없음.
  - [ ] **F-3-c**: `/admin/agents/{id}` 에 GET 요청 시 응답 HTML 의 Agents `<a>` 태그가 active 형태를 가진다. — 검증: `curl -s http://localhost:.../admin/agents/<id>` 응답에서:
      - `aria-current="page"` substring 1회 이상.
      - `style="font-weight:700` substring 1회 이상 (helper 가 반환한 단일 attribute 문자열이 실제로 출력되었음을 확정).
      - Dashboard `<a>` 태그에는 `aria-current` 미포함.
  - [ ] **F-3-d**: `/admin` 정확 일치만 Dashboard 를 active 로 하고 `/admin/agents` 에서는 Dashboard 가 active 아니다. — 검증: `curl -s .../admin/agents` 응답에서 Dashboard 의 `<a>` 에 `aria-current` 미포함.

#### 핸들러 함수 표 (F-3-b 검증 기준)

| 핸들러 함수 | view struct | path |
|---|---|---|
| `dashboard` | `dashboardView` | `GET /admin` |
| `agentsList` (`/admin/agents` GET) | `agentsView` | `GET /admin/agents` |
| `agentDetail` | `agentDetailView` | `GET /admin/agents/{id}` |
| `agentToken` | `tokenView` | `GET /admin/agents/token` |
| `testBuildForm` | `testBuildView` | `GET /admin/agents/{id}/test-build` |
| `testBuildSubmit` | (redirect — view 없음) | `POST /admin/agents/{id}/test-build` |
| `previewsList` | `previewsView` | `GET /admin/previews` |
| `previewDetail` | `previewDetailView` | `GET /admin/previews/{id}` |
| `reposIndex` | `reposIndexView` | `GET /admin/repos` |
| `repoSecretsGet` | `repoSecretsView` | `GET /admin/repos/{owner}/{repo}/secrets` |
| `repoSecretsPost` | `repoSecretsView` (re-render) | `POST .../secrets` |
| `settings` | `settingsView` | `GET /admin/settings` |

(redirect-only 핸들러는 view 가 없으므로 RequestPath 채울 필요 없음.)
- [ ] **F-4 (버튼 표준화)**: §5 매핑 테이블의 모든 행이 적용된다. 행별 검증:
  - [ ] **F-4-a**: `agents.gohtml` 의 `Delete` 버튼이 `class="contrast"` (정규식 `class="\s*contrast\s*"`). — 검증: grep.
  - [ ] **F-4-b**: `agents.gohtml` 의 `Teardown All` 버튼이 `class="contrast"`. — 검증: grep.
  - [ ] **F-4-c**: `preview_detail.gohtml` 의 `Stop` 버튼이 `class="contrast"`. — 검증: grep.
  - [ ] **F-4-d**: `agents.gohtml` 의 `Configure`, `Test Build` 링크와 `repos.gohtml` 의 `Edit secrets` 링크가 `class="secondary outline"`. — 검증: grep.
  - [ ] **F-4-e**: `settings.gohtml` 의 `Copy`, `Reveal` 버튼이 `class="secondary outline"`. — 검증: grep.
  - [ ] **F-4-f**: `token.gohtml` 의 하단 "Agent 목록으로" 링크가 `class="secondary outline"`. — 검증: grep.
  - [ ] **F-4-g**: token.gohtml 의 `copy-btn` 클래스는 변경되지 않는다 (결정 7). — 검증: `grep -c "copy-btn" internal/hub/views/token.gohtml` 결과가 변경 전과 동일.
  - [ ] **F-4-h**: primary 의도 버튼들(`Submit`/`Apply`/`Save`/`Re-run`/`Trigger Build`)에는 `class=` 속성이 명시되지 않는다 (Pico 의 기본 primary 스타일 사용). — 검증 (자동 grep):
      - `grep -E '<button[^>]*class=[^>]*>Re-run' internal/hub/views/preview_detail.gohtml` 결과 0회.
      - `grep -E '<button[^>]*class=[^>]*>Submit' internal/hub/views/agents.gohtml` 결과 0회.
      - `grep -E '<button[^>]*class=[^>]*>Apply' internal/hub/views/previews.gohtml` 결과 0회.
      - `grep -E '<button[^>]*class=[^>]*>Save' internal/hub/views/repo_secrets.gohtml` 결과 0회.
      - `grep -E '<button[^>]*class=[^>]*>Trigger Build' internal/hub/views/test_build.gohtml` 결과 0회.
- [ ] **F-5 (페이지 렌더 무회귀)**: `go build ./...` + `go test ./internal/hub/...` 둘 다 통과한다. 모든 페이지가 200 으로 응답하고 status code/Content-Type 동일. — 검증: 기존 test (`admin_ui_test.go`) 의 모든 통과 케이스 그대로 통과.

## 7. 비기능 요구사항 체크리스트

- [ ] **NF-1 (이식성)**: 새 코드가 OS 의존 패키지(syscall, os/exec 등)를 import 하지 않는다. — 검증: `grep -n "syscall\|os/exec" internal/hub/admin_ui.go` 결과에 새 라인 없음.
- [ ] **NF-2 (보안 — XSS)**: `statusBadge` 가 입력 status 를 항상 `html.EscapeString` 후 삽입한다. `template.HTML` 반환은 helper 본문이 escape 책임을 진다는 의미. — 검증: unexported `statusBadge` 의 XSS 회귀 방지를 위해 **`internal/hub/admin_ui_funcs_test.go` (또는 `admin_ui_test.go` 의 새 함수) 1건의 단위테스트를 허용** 한다. §2 비범위의 "새 단위테스트 추가" 는 *기능 동작 테스트* 에 한정되며, **보안 회귀 방지용 helper escape 테스트 1건은 예외**.
    - 테스트 내용: `statusBadge("oops<script>")` 호출 → 결과 string 에 raw `<script>` 미포함 + `&lt;script&gt;` 포함. (F-2-b 와 동일한 케이스를 단위테스트 1건으로 자동화.)
- [ ] **NF-3 (관측성)**: 본 Phase 는 로그를 추가하지 않는다 (표시 변경에 한정). — 검증: `git diff` 결과에 새 `slog.Info`/`slog.Error` 호출 없음.
- [ ] **NF-4 (성능)**: helper 호출은 페이지 렌더 1회당 statusBadge ≤ N(=previews 행 수, 최대 수백) 회 + navActive 5회. 함수 본문은 switch + string 비교만이므로 µs 수준. 추가 측정 불필요. — 검증: 코드 리뷰.
- [ ] **NF-5 (Pico 테마 호환)**: 모든 색상이 `var(--pico-...)` 또는 `<mark>`/`class="contrast"` 의미적 클래스로 표현되어 prefers-color-scheme 자동 전환 시 시각이 유지된다. teardown 의 inline `#fff3cd`/`#856404` 는 다크모드 대응이 약하지만, 같은 색을 light/dark 양쪽에서 관용적으로 쓰는 warning 색이라 수용. — 검증: 다크모드 시 teardown badge 가 식별 가능하게 보이는지 수동 확인 (Edge/Chrome devtools 의 prefers-color-scheme: dark 토글).
- [ ] **NF-6 (template parse 영향 없음)**: `mustParsePages` 가 시작 시 panic 없이 모든 페이지(10개)를 파싱한다. — 검증: 테스트 `TestNewAdminUIHandler*` (있다면) 또는 `go test -run TestAdminUI` 통과 — 기존 테스트가 NewAdminUIHandler 를 호출하므로 panic 시 즉시 fail.
    - 추가로 다음을 검증 ('parse OK 이지만 execute 시 function not defined' 위험 방지):
      - previews 페이지(`GET /admin/previews`) 응답 본문에 `class="status-pill"` 또는 `<mark>` substring 1회 이상 (statusBadge 가 실제 실행됐음을 증명).
      - preview_detail 페이지(`GET /admin/previews/{id}`) 응답 본문에 동일 substring 1회 이상.
- [ ] **NF-7 (token.gohtml 영문 콘텐츠 처리)**: token.gohtml 의 영문 코드 블록(`curl ...`, PowerShell 명령)은 본 Phase 에서 그대로 유지된다 (lang 변경은 A-4 의 영역). — 검증: `git diff internal/hub/views/token.gohtml` 의 변경이 마지막 secondary 링크 1줄로 한정.

## 8. 리스크와 완화책

- **R-1: helper 등록 누락 시 모든 페이지 500**. `mustParsePages` 가 layout 파싱 시 `Funcs(adminFuncs())` 호출을 빠뜨리면 layout 이 `navActive`/`statusBadge` 를 미정의 함수로 보고 파싱 실패 → 시작 시 panic. 또는 layout 만 등록하고 page 파싱 단계에서 누락 시 일부 페이지만 깨질 수 있음 (실제로는 `template.Parse` 가 같은 트리에 누적되므로 layout 단계 1회면 충분 — 결정 1).
  - **완화**: `template.Must` 가 panic 으로 즉시 검출 (fail-fast). 추가로 NF-6 의 startup test 가 panic 을 catch.

- **R-2: `RequestPath` 누락**. 새 핸들러 추가 시 view struct 에 `RequestPath` 만 두고 핸들러에서 채우는 줄을 빠뜨리면 nav 가 항상 비활성. 빈 문자열은 매칭 안 되므로 시각 깨짐만 있고 panic 없음 — silent.
  - **완화**: 본 Phase 에서 모든 기존 핸들러에 일괄 적용. 후속 핸들러 추가 시 코드 리뷰 항목으로 명문화 (`docs/specs/phase-12-...md` 의 §6 F-3-b 를 향후 PR 체크리스트로 인용).

- **R-3: token.gohtml 의 `<style>` 안 셀렉터가 새 `class="active-nav"` 와 충돌**. token.gohtml 이 정의한 셀렉터는 `.step-card`, `.step-header`, `.os-tabs`, `.tab-bar`, `.copy-btn`, `.code-wrap`, `.arch-label` 로 nav 와 무관. 충돌 위험 없음. (확인 완료 — view 파일 읽음)
  - **완화**: 위 확인을 §3 결정 7 에 명시. helper 가 출력하는 `class="active-nav"` 가 token.gohtml 본문이 아닌 layout nav 에만 적용됨을 시각 확인.

- **R-4: 기존 `class="secondary"` (없는 outline) 가 contrast 로 바뀌면서 다른 페이지의 동일 텍스트 버튼이 잘못 매핑**될 위험. 예: `preview_detail.gohtml` Stop 버튼 `class="secondary"` → `class="contrast"`. 다른 곳에 의도 없이 동일 텍스트 버튼이 있다면 뜻이 달라짐.
  - **완화**: §5 매핑 테이블에 파일×버튼 텍스트 1:1 명시. grep `class="secondary"` 를 일괄 치환하지 않고 파일별로 정확한 라인을 Edit 한다.

## 9. 다음 Phase 연결점

- 본 Phase 에서 도입한 `adminFuncs()` 와 `RequestPath` 패턴은 후속 카테고리 (B/C/D/E/F) 에서도 재사용된다. 특히:
  - **B-1/B-2/B-3 (confirm 추가)**: contrast 클래스가 적용된 버튼만 대상으로 confirm 을 추가하는 식으로 본 Phase 의 분류가 입력값이 됨.
  - **C-1 (auto-refresh)**: preview_detail 의 `<head>` meta 를 status 에 따라 추가 — 본 Phase 의 status 분류 매핑이 그대로 재사용 가능 (active/non-terminal vs terminal).
  - **C-2 (flash msg)**: `RequestPath` 와 같은 패턴으로 view struct 에 `Flash string` 필드 추가.
  - **D-5 (breadcrumb)**: `RequestPath` 를 입력으로 받는 helper `breadcrumb` 추가 — 본 Phase 패턴 그대로.
- A-4 (`<html lang="ko">`) 는 본 Phase 와 같은 layout.gohtml 을 건드리므로 같은 PR 에 묶어 처리해도 무방. 다만 본 기획서는 A-4 를 비범위로 명시한다 — 분리 PR 권장.
- token.gohtml 의 inline `<style>` 블록을 `internal/hub/views/admin.css` 같은 별도 파일로 추출하는 리팩터링은 본 Phase 의 결정 6 과 정면 충돌하므로 별도 Phase (또는 별도 결정 문서) 가 필요하다.
