# Phase 15 — UX Navigation (카테고리 D: 네비게이션 / 동선)

## 1. Phase 개요

`docs/specs/ux-improvement-plan.md` §2 카테고리 D 의 5개 항목(D-1 preview detail Repo cross-link, D-2 preview detail Agent cross-link, D-3 agent detail 의 Active Previews 섹션 + Test Build 링크, D-4 agents 테이블 actions 의 dropdown 묶기, D-5 detail 페이지 breadcrumb)을 한 Phase 로 묶어 구현한다. 본 Phase 는 **새 기능 추가가 아닌 페이지 간 동선 정리** 에 한정되며, 핸들러는 단 한 곳(`agentDetail`)에서 `PreviewStore.ListByAgent` 를 추가 호출하고 view struct 에 필드 2개(`previewDetailView.AgentID`, `agentDetailView.ActivePreviews`)를 더할 뿐 DB 스키마/메시지 프로토콜/인증/세션 로직은 일절 건드리지 않는다.

끝났을 때의 상태:

- preview_detail 의 Repo / Agent 행이 클릭 가능한 링크로 바뀌어, 같은 repo 의 preview 목록(`/admin/previews?repo=...`) 또는 해당 agent detail (`/admin/agents/{id}`) 로 한 번에 이동할 수 있다.
- agent_detail 페이지에 "이 Agent 에 할당된 Active Previews" 섹션이 보이고 (assigned/building/running 상태의 preview 행), 페이지 상단에 Test Build 링크가 있다.
- agents 목록의 actions 컬럼이 `<details>`-기반 dropdown 으로 접혀 좁은 화면에서도 1행에 들어간다.
- preview_detail / agent_detail 페이지 상단에 breadcrumb (`Previews › PR #42 — owner/repo`, `Agents › agent-home`) 이 표시된다.
- `go build ./...` 통과, 기존 단위테스트 전부 통과, 모든 페이지가 200 으로 응답한다.

## 2. 범위와 비범위

**범위**

- `internal/hub/admin_ui.go`
  - `previewDetailView` 에 `AgentID string` 필드 1개 추가.
  - `previewDetail` 핸들러에서 `view.AgentID = *p.AssignedAgentID` (포인터 nil 시 `""`) 채움.
  - `agentDetailView` 에 `ActivePreviews []previewRow` 필드 1개 추가.
  - `agentDetail` 핸들러에서 `PreviewStore.ListByAgent(ctx, id, []string{"assigned","building","running"})` 호출 → `previewRow` 로 변환 → view 에 주입. 실패 시 WARN 로그 + 빈 목록으로 계속 (Phase 9/10 의 soft-fail 패턴).
- `internal/hub/views/preview_detail.gohtml`
  - Repo `<dd>` → `<dd><a href="/admin/previews?repo={{.Preview.RepoFullName}}">{{.Preview.RepoFullName}}</a></dd>` (D-1).
  - Agent `<dd>` → `AgentID` 비어있지 않을 때 `<a href="/admin/agents/{{.AgentID}}">{{.AgentLine}}</a>` 출력, 비어있으면 기존처럼 `{{.AgentLine}}` plain 텍스트 (D-2).
  - 페이지 상단(`<hgroup>` 위)에 breadcrumb `<nav aria-label="breadcrumb">` 추가 (D-5).
- `internal/hub/views/agent_detail.gohtml`
  - 상단 metadata 섹션 위에 Test Build 링크 (`<a href="/admin/agents/{{.AgentID}}/test-build" role="button" class="secondary outline">Test Build</a>`) 추가 (D-3).
  - Metadata 섹션 아래에 Active Previews 섹션 추가: `{{if .ActivePreviews}}<table>...</table>{{else}}<p><em>이 Agent 에 할당된 진행 중인 preview 가 없습니다.</em></p>{{end}}` (D-3).
  - 페이지 상단에 breadcrumb (D-5).
- `internal/hub/views/agents.gohtml`
  - actions `<td>` 내부 4개 요소(Configure 링크, Test Build 링크, Teardown All form, Delete form)를 `<details><summary>Actions</summary>...</details>` 로 감싸고 내부는 `flex-direction:column` 으로 세로 정렬 (D-4).

**비범위**

- 기능 추가, 핸들러 추가, DB 스키마/쿼리 변경, 메시지 프로토콜 변경.
- E-1 (상대시간 표시), E-2/E-3 (대시보드 카드), E-4 (timeline 시각화), E-5 (agent_detail 의 Run 카운트/Health) — 별도 Phase.
- F-1/F-2/F-3 (폼 사용성).
- breadcrumb 을 layout.gohtml 의 공통 컴포넌트로 추출 — 본 Phase 는 페이지별 직접 작성 (결정 5).
- agents 목록 외 페이지의 dropdown 도입 (현재 `<details>` 가 필요한 위치는 agents 1곳).
- ActivePreviews 의 페이지네이션·정렬 옵션 (현재는 status filter 와 결과 LIMIT 50 으로 충분 — 결정 4).
- D-3 의 ActivePreviews 안에서 status badge 외 추가 메타데이터 (commit sha, last event 등).
- 모바일 layout 의 dropdown 을 onclick JS 로 제어 (`<details>` 의 native toggle 만 사용).

## 3. 설계 결정 및 근거

### 결정 1: D-2 의 Agent 링크 분기는 `AgentID` (string) 1개 필드로 판단

- **결정**: `previewDetailView` 에 `AgentID string` 필드 1개를 추가하고, 핸들러에서 `p.AssignedAgentID` (포인터) 가 nil 이면 `""`, 아니면 `*p.AssignedAgentID` 로 채운다. 템플릿은 `{{if .AgentID}}<a href="/admin/agents/{{.AgentID}}">{{.AgentLine}}</a>{{else}}{{.AgentLine}}{{end}}` 형태로 분기.
- **근거**: 기존 `AgentLine` 은 사람 표시용 문자열(예: `agent-home (10.0.0.5:8080)` 또는 `-`)이라 직접 ID 추출이 어렵다. 별도 `AgentID` 를 명시적으로 노출하면 (1) 핸들러는 nil 체크 한 곳에서 빈 문자열을 넣고, (2) 템플릿은 빈 문자열 = 링크 없음 의 한 가지 규칙으로 끝난다. `AgentLine` 자체를 link 텍스트로 재사용하므로 표시 변경 없음.
- **버려진 대안 1**: `AgentLine` 안에 `<a>` 태그를 핸들러에서 직접 끼워 넣기 → `template.HTML` 반환이 필요하고 escape 책임이 핸들러로 넘어옴. XSS 위험 증가.
- **버려진 대안 2**: 템플릿에서 `AgentLine` 을 정규식으로 파싱해 ID 분리 → 비효율적이고 깨지기 쉽다.
- **버려진 대안 3**: `AgentLine` 을 구조체 (`AgentLine struct { ID, Display string }`) 로 바꾸기 → 기존 AgentLine 사용처(template) 가 string 가정이라 영향 큼. 필드 1개 추가가 더 작은 변경.
- **되돌릴 때 비용**: 작다. struct 필드 1개 + 핸들러 1줄 + 템플릿 if/else 제거.

### 결정 2: D-3 의 ActivePreviews 는 `previewRow` 재사용

- **결정**: `agentDetailView.ActivePreviews []previewRow` 로 정의해 `previewsList` 가 만드는 것과 동일한 `previewRow` 구조체를 재사용한다. 템플릿에서도 `previews.gohtml` 의 행 렌더 패턴(PR 링크, statusBadge, branch, urls)을 그대로 따른다.
- **근거**: 같은 도메인(preview 목록 한 행)의 두 표시 위치가 동일한 구조체를 공유하는 것이 맞다. 별도 경량 struct (`agentPreviewRow`) 를 만들면 필드 누락/스키마 드리프트가 발생할 위험. previewRow 는 이미 모든 필요 필드를 갖고 있다(`ID, PrNumber, RepoFullName, Status, Branch, AgentLabel, UpdatedString, PreviewURLs, IsAdhoc`).
- **버려진 대안 1**: `agentPreviewRow struct{ ID, PrNumber int, Status, Branch string }` 신설 → 필드 4개 짜리지만 향후 확장 시 두 곳을 동시에 수정해야 함. 기술 부채.
- **버려진 대안 2**: `[]store.Preview` 를 그대로 view 에 주입 → 템플릿이 `*string` 포인터·JSON 문자열 등 raw 도메인 타입을 다뤄야 함. 본 프로젝트 컨벤션(view struct 는 표시용 평면 타입) 위배.
- **되돌릴 때 비용**: 작다 (필드 1개 + 변환 루프 1개 제거).

### 결정 3: D-3 의 ListByAgent 실패는 soft-fail (WARN + 빈 목록)

- **결정**: `agentDetail` 핸들러가 `PreviewStore.ListByAgent` 를 호출할 때 에러 발생 시 `h.Logger.Warn("admin_ui_agent_active_previews_failed", "agent_id", id, "err", err.Error())` 로그 후 `view.ActivePreviews = nil` 로 계속 렌더. agent metadata 자체는 페이지 본문에 살아 있어야 하기 때문.
- **근거**: agent_detail 의 핵심 콘텐츠는 agent 자체(이름/상태/labels). ActivePreviews 는 부가 정보이므로 DB 일시 오류로 페이지 전체가 500 이 되는 것은 과도하다. Phase 9 의 `ListPreviewEvents` 실패가 동일 패턴(WARN + nil) 이고, Phase 10 의 secret 로딩 실패도 같은 방식. 일관성.
- **버려진 대안 1**: 500 반환 → agent 이름조차 못 보게 되어 운영 흐름 차단.
- **버려진 대안 2**: 빈 목록 + `<article role="alert">` 으로 사용자에게 표시 → 부가 정보 실패에 대한 시끄러운 알림. WARN 로그로 운영자만 인지하면 충분.
- **되돌릴 때 비용**: 작다 (분기 1개 변경).

### 결정 4: D-3 의 ActivePreviews 결과는 메모리에서 최대 50건으로 자른다

- **결정**: `ListByAgent` 는 LIMIT 없이 모든 매칭 row 를 반환하지만 (현재 sqlc 쿼리 — assigned/building/running 합쳐 동시 수십~수백개가 정상 한계), agent_detail 의 표시는 최대 50개로 자른다. 50 초과 시 `<p><em>최근 50개만 표시 (총 N개)</em></p>` 형태의 hint 1줄. 정렬은 핸들러에서 `UpdatedAt DESC` 로 명시.
- **근거**: 농경기 폭주 시 한 agent 가 수백개 preview 를 동시 들고 있을 수 있다. 모든 행을 렌더하면 페이지 응답이 수 MB 수준이 되어 모바일 / 느린 네트워크에서 timeout 위험. 50 은 1 viewport 분량의 약 2~3 배로, 운영 시 "지금 뭐가 돌고 있는가" 를 보기에 충분.
- **버려진 대안 1**: store 레벨에서 LIMIT 추가 → `ListByAgent` 의 다른 호출자(reconciler, dispatcher)에 영향. 시그니처 변경 비용 큼. 본 Phase 가 표시 한정이라는 원칙 위배.
- **버려진 대안 2**: 핸들러에서 LIMIT 없이 전부 렌더 → 응답 폭주 위험.
- **버려진 대안 3**: 페이지네이션 도입 → 본 Phase 의 비범위.
- **되돌릴 때 비용**: 작다 (slice 자르기 1줄 제거).

### 결정 5: D-5 의 breadcrumb 은 페이지별 직접 작성 (helper 없음)

- **결정**: breadcrumb 마크업을 각 detail 페이지의 `{{define "content"}}` 시작부에 `<nav aria-label="breadcrumb"><ul><li><a href="/admin/previews">Previews</a></li><li>PR #{{.Preview.PrNumber}} — {{.Preview.RepoFullName}}</li></ul></nav>` 형태로 직접 작성한다. helper 함수나 layout 의 공통 partial 은 도입하지 않는다.
- **근거**: detail 페이지가 2곳(preview, agent)뿐이고 각 페이지가 알아야 할 정보(`PrNumber`, `RepoFullName`, `Name`)가 다르다. helper 도입은 (1) `RequestPath` 만으로는 데이터 부족 → 별도 인자 필요, (2) `template.HTML` 반환으로 escape 책임 이동, (3) 후속 변경 시 helper 시그니처 변경이 모든 페이지에 파급. 두 페이지에 직접 적는 비용이 더 작다.
- **근거 2**: Pico CSS v2 가 `nav[aria-label="breadcrumb"] ul` 셀렉터를 자동 스타일링하므로 별도 CSS 불필요.
- **버려진 대안 1**: `breadcrumb` template helper (`func breadcrumb(items []breadcrumbItem) template.HTML`) → struct 정의 + escape 책임 + 호출 측 매번 slice 만들기. 비용/이득 나쁨.
- **버려진 대안 2**: layout.gohtml 의 `<header>` 영역에 `{{block "breadcrumb" .}}{{end}}` 추가 → 모든 페이지가 정의해야 하는 부담. 현재 비-detail 페이지는 breadcrumb 이 의미 없음.
- **버려진 대안 3**: agents 목록 / previews 목록에도 breadcrumb 추가 → 의미 없음 (top-level nav 에 이미 동일 항목 있음). UX 노이즈.
- **되돌릴 때 비용**: 작다 (각 페이지에서 nav 1블록 제거).

### 결정 6: D-4 의 dropdown 은 native `<details>` 만 사용

- **결정**: `<td><details><summary>Actions</summary><div style="display:flex; flex-direction:column; gap:0.4em; margin-top:0.4em;">...</div></details></td>` 구조. JavaScript 없음. summary 클릭으로 toggle, 다른 곳 클릭으로 자동 닫힘은 구현하지 않음.
- **근거**: 사용자 요청에 명시된 제약(JS 없이 native HTML). Pico CSS v2 가 `<details>` 의 회전 화살표·padding 을 자동 스타일링한다. 현재 4개 actions 가 좁은 화면에서 wrap 되어 행 높이가 들쑥날쑥한 문제를 해결하면서, 평소엔 1줄에 깔끔히 들어간다.
- **버려진 대안 1**: `<select>` + form action 동적 변경 → JS 필수. 비범위.
- **버려진 대안 2**: 모바일 미디어쿼리로만 dropdown 화 → desktop 에서도 시각 노이즈가 줄어드는 본 작업의 의도와 충돌.
- **버려진 대안 3**: clickaway 스크립트 추가 → 본 프로젝트는 JS 도입 의 명확한 정책 부재. native HTML 우선.
- **되돌릴 때 비용**: 작다 (`<details>` wrapper 제거 + `<td>` 의 flex-direction 복귀).

### 결정 7: D-3 의 Test Build 링크 위치는 metadata 위 actions 행

- **결정**: agent_detail 의 `<hgroup>` 다음, metadata 섹션 위에 actions 행을 추가하고 거기에 Test Build 링크를 둔다. 마크업: `<p><a href="/admin/agents/{{.AgentID}}/test-build" role="button" class="secondary outline">Test Build</a></p>`. 기존 `<p><a href="/admin/agents">&larr; Back to agents</a></p>` 라인을 breadcrumb 으로 대체하고, 그 자리에 Test Build 가 들어감. 즉 라인 1개를 두 번 갈음(breadcrumb 으로 한 번, actions 로 한 번).
- **근거**: agent detail 의 주요 액션이 1개뿐(향후 Configure 가 같은 페이지로 흡수됨)이라 별도 actions 박스를 만들 필요 없음. Pico 의 `role="button"` 으로 시각적 일관성 확보.
- **버려진 대안 1**: ActivePreviews 섹션 안의 우상단에 button → ActivePreviews 가 비어있을 때 button 도 사라짐. 사용성 저하.
- **버려진 대안 2**: `<hgroup>` 옆에 floating button → CSS 복잡, 모바일에서 깨짐.
- **되돌릴 때 비용**: 작다.

### 결정 8: D-1 / D-2 / D-5 / D-3 / D-4 모두 `RequestPath`-기반 nav active 와 무관

- **결정**: 본 Phase 의 모든 변경은 Phase 12 에서 도입된 `RequestPath`/`navActive`/`statusBadge` 를 그대로 사용한다 (재정의 없음). 새 helper 도입 없음.
- **근거**: 본 Phase 는 페이지 간 동선 추가에 한정. 시각 표현은 Phase 12 의 결과를 신뢰.
- **되돌릴 때 비용**: 0.

## 4. 아키텍처 / 구조

### 변경 디렉토리 트리 (변경 파일만)

```
internal/hub/admin_ui.go
  ├ previewDetailView struct: + AgentID string
  ├ previewDetail handler: + view.AgentID = ...
  ├ agentDetailView struct: + ActivePreviews []previewRow
  └ agentDetail handler: + ListByAgent + 변환 루프 + soft-fail

internal/hub/views/preview_detail.gohtml
  ├ <hgroup> 위에 breadcrumb
  ├ Repo <dd> 의 텍스트 → <a href="/admin/previews?repo=...">
  └ Agent <dd> 의 텍스트 → AgentID 분기로 <a> 또는 plain

internal/hub/views/agent_detail.gohtml
  ├ <hgroup> 위에 breadcrumb (기존 "← Back to agents" 줄 제거)
  ├ <hgroup> 다음에 Test Build 링크
  └ Metadata 섹션 다음에 Active Previews 섹션

internal/hub/views/agents.gohtml
  └ Actions <td> 의 내용을 <details><summary>Actions</summary>...</details> 로 wrap
```

### 호출 흐름 (변경된 부분만)

```
GET /admin/previews/{id}
  → previewDetail handler:
    p = PreviewStore.GetByID
    events = ListPreviewEvents (기존)
    view.AgentID = (p.AssignedAgentID == nil ? "" : *p.AssignedAgentID)
    renderHTML preview_detail.gohtml
  → preview_detail.gohtml:
    breadcrumb (PrNumber, RepoFullName 사용)
    Repo dd: <a href="/admin/previews?repo={{.Preview.RepoFullName}}">
    Agent dd: {{if .AgentID}}<a href="/admin/agents/{{.AgentID}}">{{.AgentLine}}</a>{{else}}{{.AgentLine}}{{end}}

GET /admin/agents/{id}
  → agentDetail handler:
    a = AgentStore.GetByID
    previews, err = PreviewStore.ListByAgent(ctx, id, []string{"assigned","building","running"})
    if err: WARN; previews = nil
    sort previews by UpdatedAt DESC
    if len(previews) > 50: previews = previews[:50]; tooManyHint = "최근 50개만 표시 (총 N개)"
    view.ActivePreviews = convert(previews)
    renderHTML agent_detail.gohtml
  → agent_detail.gohtml:
    breadcrumb (Name 사용)
    Test Build 링크
    Metadata
    if .ActivePreviews: 표; else: empty hint
```

### 시퀀스 다이어그램 (D-3 의 신규 호출만)

```
Client          AdminUIHandler.agentDetail        AgentStore        PreviewStore
  |                       |                            |                  |
  |---GET /admin/agents/{id}-->                        |                  |
  |                       |---GetByID(id)------------->|                  |
  |                       |<--Agent-------------------|                  |
  |                       |---ListByAgent(id, [a,b,r])----------------->|
  |                       |<--[]Preview / err---------------------------|
  |                       | (err → WARN + nil)                          |
  |                       | sort, slice to 50                            |
  |                       | render agent_detail.gohtml                   |
  |<--HTML--------------- |                            |                  |
```

## 5. 인터페이스 계약

### previewDetailView 변경

```go
type previewDetailView struct {
    Title           string
    RequestPath     string
    Preview         previewDetailRow
    AgentLine       string
    AgentID         string            // [NEW] D-2: 빈 문자열이면 링크 미출력
    PreviewURLs     map[string]string
    Events          []eventRow
    OlderEvents     []eventRow
    RebuildEnabled  bool
    StopEnabled     bool
    ConflictMessage string
    Diagnosis       string
    BuildOutput     string
}
```

핸들러 변경 (1줄 추가):

```go
agentID := ""
if p.AssignedAgentID != nil {
    agentID = *p.AssignedAgentID
}
view := previewDetailView{
    // ... 기존 필드 ...
    AgentLine: agentLine,
    AgentID:   agentID,   // [NEW]
    // ...
}
```

### agentDetailView 변경

```go
type agentDetailView struct {
    Title           string
    RequestPath     string
    AgentID         string
    Name            string
    Status          string
    LabelsString    string
    LastSeenString  string
    CreatedString   string
    Error           string
    ActivePreviews  []previewRow  // [NEW] D-3: assigned/building/running 상태의 preview 행 (최대 50개)
    ActiveTotal     int           // [NEW] D-3: 50건 자르기 전의 원본 개수 (hint 표시용)
}
```

핸들러 변경:

```go
const activePreviewLimit = 50

view := agentDetailView{
    Title:         "Agent " + a.Name,
    RequestPath:   r.URL.Path,
    AgentID:       a.ID,
    Name:          a.Name,
    // ... 기존 필드 ...
}

if h.PreviewStore != nil {
    ps, err := h.PreviewStore.ListByAgent(r.Context(), id,
        []string{"assigned", "building", "running"})
    if err != nil {
        h.Logger.Warn("admin_ui_agent_active_previews_failed",
            "agent_id", id, "err", err.Error())
    } else {
        // newest first
        sort.Slice(ps, func(i, j int) bool {
            return ps[i].UpdatedAt.After(ps[j].UpdatedAt)
        })
        view.ActiveTotal = len(ps)
        if len(ps) > activePreviewLimit {
            ps = ps[:activePreviewLimit]
        }
        rows := make([]previewRow, 0, len(ps))
        for _, p := range ps {
            agentLabel := ""
            if p.AssignedAgentID != nil {
                agentLabel = *p.AssignedAgentID
            }
            var urls map[string]string
            if p.PreviewURLs != "" {
                _ = json.Unmarshal([]byte(p.PreviewURLs), &urls)
            }
            rows = append(rows, previewRow{
                ID:            p.ID,
                PrNumber:      p.PrNumber,
                RepoFullName:  p.RepoFullName,
                Status:        p.Status,
                Branch:        p.Branch,
                AgentLabel:    agentLabel,
                UpdatedString: p.UpdatedAt.UTC().Format(time.RFC3339),
                PreviewURLs:   urls,
                IsAdhoc:       p.IsAdhoc,
            })
        }
        view.ActivePreviews = rows
    }
}
```

### 템플릿 변경 — preview_detail.gohtml

breadcrumb 추가 (D-5):

```gohtml
{{define "content"}}
<nav aria-label="breadcrumb">
  <ul>
    <li><a href="/admin/previews">Previews</a></li>
    <li>{{if eq .Preview.PrNumber 0}}Test Build — {{.Preview.RepoFullName}}{{else}}PR #{{.Preview.PrNumber}} — {{.Preview.RepoFullName}}{{end}}</li>
  </ul>
</nav>

<hgroup>
  <h1>{{.Title}}</h1>
  ...
```

Repo 행 (D-1):

```gohtml
<dt>Repo</dt>
<dd><a href="/admin/previews?repo={{.Preview.RepoFullName}}">{{.Preview.RepoFullName}}</a></dd>
```

Agent 행 (D-2):

```gohtml
<dt>Agent</dt>
<dd>{{if .AgentID}}<a href="/admin/agents/{{.AgentID}}">{{.AgentLine}}</a>{{else}}{{.AgentLine}}{{end}}</dd>
```

### 템플릿 변경 — agent_detail.gohtml

전체 재구성 (변경 후 모습):

```gohtml
{{define "content"}}
<nav aria-label="breadcrumb">
  <ul>
    <li><a href="/admin/agents">Agents</a></li>
    <li>{{.Name}}</li>
  </ul>
</nav>

<hgroup>
  <h1>Agent: {{.Name}}</h1>
  <h2>Status: {{.Status}}{{if .LastSeenString}} · Last seen: {{.LastSeenString}}{{end}}</h2>
</hgroup>

<p>
  <a href="/admin/agents/{{.AgentID}}/test-build" role="button" class="secondary outline">Test Build</a>
</p>

{{if .Error}}
  <article role="alert">{{.Error}}</article>
{{end}}

<section>
  <h3>Metadata</h3>
  <dl>
    <dt>ID</dt><dd><code>{{.AgentID}}</code></dd>
    <dt>Labels</dt><dd>{{if .LabelsString}}{{.LabelsString}}{{else}}<em>none</em>{{end}}</dd>
    <dt>Created</dt><dd>{{.CreatedString}}</dd>
  </dl>
</section>

<section>
  <h3>Active Previews</h3>
  {{if .ActivePreviews}}
    <table>
      <thead>
        <tr>
          <th scope="col">PR</th>
          <th scope="col">Repo</th>
          <th scope="col">Status</th>
          <th scope="col">Branch</th>
          <th scope="col">URLs</th>
          <th scope="col">Updated</th>
        </tr>
      </thead>
      <tbody>
      {{range .ActivePreviews}}
        <tr>
          <td>
            <a href="/admin/previews/{{.ID}}">#{{.PrNumber}}</a>
            {{if .IsAdhoc}}<span class="badge" data-source="adhoc" style="margin-left:0.4em; padding:0.1em 0.45em; font-size:0.75em; border:1px solid var(--pico-secondary); border-radius:3px; color:var(--pico-secondary);">Adhoc</span>{{end}}
          </td>
          <td>{{.RepoFullName}}</td>
          <td>{{statusBadge .Status}}</td>
          <td>{{.Branch}}</td>
          <td>{{if .PreviewURLs}}{{range $name, $url := .PreviewURLs}}<a href="{{$url}}">{{$name}}</a> {{end}}{{else}}<em>—</em>{{end}}</td>
          <td>{{.UpdatedString}}</td>
        </tr>
      {{end}}
      </tbody>
    </table>
    {{if gt .ActiveTotal (len .ActivePreviews)}}
      <p><small><em>최근 {{len .ActivePreviews}}개만 표시 (총 {{.ActiveTotal}}개)</em></small></p>
    {{end}}
  {{else}}
    <p><em>이 Agent 에 할당된 진행 중인 preview 가 없습니다.</em></p>
  {{end}}
</section>
{{end}}
```

기존의 `<p><a href="/admin/agents">&larr; Back to agents</a></p>` 라인은 breadcrumb 으로 대체되어 제거된다.

### 템플릿 변경 — agents.gohtml (Actions 컬럼)

변경 전:

```gohtml
<td style="display: flex; flex-direction: row; flex-wrap: nowrap; gap: 8px;">
  <a href="/admin/agents/{{.ID}}" role="button" class="secondary outline" style="height: fit-content;">Configure</a>
  <a href="/admin/agents/{{.ID}}/test-build" role="button" class="secondary outline" style="height: fit-content;">Test Build</a>
  <form method="POST" action="/admin/agents/{{.ID}}/teardowns" style="display:inline" onsubmit="...">
    <button type="submit" class="contrast">Teardown All</button>
  </form>
  <form method="POST" action="/admin/agents/{{.ID}}/delete" style="display:inline" onsubmit="...">
    <button type="submit" class="contrast">Delete</button>
  </form>
</td>
```

변경 후:

```gohtml
<td>
  <details>
    <summary>Actions</summary>
    <div style="display:flex; flex-direction:column; gap:0.4em; margin-top:0.4em;">
      <a href="/admin/agents/{{.ID}}" role="button" class="secondary outline">Configure</a>
      <a href="/admin/agents/{{.ID}}/test-build" role="button" class="secondary outline">Test Build</a>
      <form method="POST" action="/admin/agents/{{.ID}}/teardowns" onsubmit="return confirm('이 Agent의 모든 실행 중 preview를 종료하시겠습니까?')">
        <button type="submit" class="contrast" style="width:100%;">Teardown All</button>
      </form>
      <form method="POST" action="/admin/agents/{{.ID}}/delete" onsubmit="return confirm('이 Agent를 삭제하시겠습니까? 진행 중인 preview가 있다면 모두 정리됩니다.')">
        <button type="submit" class="contrast" style="width:100%;">Delete</button>
      </form>
    </div>
  </details>
</td>
```

`style="display:inline"` 은 column 정렬과 충돌하므로 제거. button 의 `width:100%` 로 form 가로폭을 따라가게 한다 (form 이 inline 이 아닌 block 이 되면서 자식 button 도 같이 늘어남).

## 6. 기능 요구사항 체크리스트

- [ ] **F-1 (D-1: Repo cross-link)**: `preview_detail.gohtml` 의 Repo `<dd>` 안에 `<a href="/admin/previews?repo={{.Preview.RepoFullName}}">` 패턴이 정확히 1회 등장. — 검증: `grep -c 'href="/admin/previews?repo={{.Preview.RepoFullName}}"' internal/hub/views/preview_detail.gohtml` 결과 = 1.
- [ ] **F-2 (D-1: 클릭 동작)**: `/admin/previews/{id}` 응답 HTML 의 Repo 셀에 `href="/admin/previews?repo=<repo>"` substring 포함. 클릭 시 `/admin/previews?repo=...` 로 이동하고 결과에 동일 repo 의 row 만 표시된다. — 검증: 수동 — 브라우저에서 클릭 + 결과 페이지의 Filter Repo 입력란에 값이 채워져 있는지 확인. 자동: `curl` 응답 grep.
- [ ] **F-3 (D-2: Agent 링크 분기 — assigned 인 경우)**: `AssignedAgentID` 가 nil 이 아닌 preview 의 detail 응답 HTML 안에 `<a href="/admin/agents/<agent-id>">` 패턴 1회 등장. — 검증: 수동(테스트 fixture) 또는 단위테스트에서 `view.AgentID = "a-1"` 로 렌더한 결과에 `href="/admin/agents/a-1"` 포함.
- [ ] **F-4 (D-2: Agent 링크 분기 — unassigned 인 경우)**: `AssignedAgentID` 가 nil 인 preview 의 detail 응답 HTML 안에 Agent 셀에 `<a href="/admin/agents/` substring 미포함, `<dd>-</dd>` (또는 `<dd>{{.AgentLine}}</dd>` plain 텍스트) 형태. — 검증: 수동 — `AssignedAgentID == nil` 인 queued preview 의 detail 응답 grep. 자동: 위 단위테스트에서 `AgentID = ""` 케이스 추가.
- [ ] **F-5 (D-2: previewDetailView.AgentID 필드 존재)**: `internal/hub/admin_ui.go` 의 `previewDetailView` struct 정의에 `AgentID string` 필드 1줄 존재. — 검증: `grep -E '^\s*AgentID\s+string' internal/hub/admin_ui.go` 결과 ≥ 1, 그리고 `previewDetail` 핸들러에서 `view.AgentID =` 또는 `AgentID:` 1회 이상 등장.
- [ ] **F-6 (D-3: agentDetailView.ActivePreviews 필드 존재)**: `internal/hub/admin_ui.go` 의 `agentDetailView` struct 정의에 `ActivePreviews []previewRow` 와 `ActiveTotal int` 필드가 존재. — 검증: grep.
- [ ] **F-7 (D-3: ListByAgent 호출)**: `agentDetail` 핸들러 함수 본문에 `h.PreviewStore.ListByAgent(r.Context(), id, []string{"assigned", "building", "running"})` 패턴 호출 1회 존재. — 검증: `grep -nE 'ListByAgent\(r.Context\(\), id, \[\]string\{"assigned", "building", "running"\}\)' internal/hub/admin_ui.go` 결과 ≥ 1, 위치가 `func (h *AdminUIHandler) agentDetail` 함수 본문.
- [ ] **F-8 (D-3: soft-fail 로그)**: `ListByAgent` 가 에러를 반환할 때 `h.Logger.Warn("admin_ui_agent_active_previews_failed", "agent_id", id, "err", err.Error())` 호출 후 nil 로 계속. — 검증: 단위테스트 — `agentDetail` 호출 시 `PreviewStore.ListByAgent` 가 임의 에러를 리턴하도록 fake 를 설정 → 응답이 200, 본문에 `이 Agent 에 할당된 진행 중인 preview 가 없습니다.` substring 포함.
- [ ] **F-9 (D-3: ActivePreviews 정상 케이스)**: agent 가 assigned/building/running 인 preview 3개를 들고 있을 때 agent_detail 응답에 3개 row 가 `<table>` 안에 표시된다. — 검증: 단위테스트 (fake store 에 3개 preview seed) — 응답 HTML 의 Active Previews 섹션 안에 `<a href="/admin/previews/<id>">` 3회 등장.
- [ ] **F-10 (D-3: 50개 자르기 hint)**: agent 가 51개 이상 active preview 를 들고 있을 때 응답 본문에 `최근 50개만 표시 (총 N개)` substring 1회. — 검증: 수동 — fake store 에 60개 seed 후 응답 grep. (자동화는 선택)
- [ ] **F-11 (D-3: Test Build 링크)**: agent_detail 응답에 `<a href="/admin/agents/<id>/test-build" role="button"` 패턴 1회 등장. — 검증: `grep` 또는 단위테스트.
- [ ] **F-12 (D-4: details wrapper)**: `agents.gohtml` 의 actions `<td>` 안에 `<details>` 와 `<summary>Actions</summary>` 가 정확히 1회 등장. — 검증: `grep -c '<summary>Actions</summary>' internal/hub/views/agents.gohtml` 결과 = 1.
- [ ] **F-13 (D-4: 4개 actions 모두 details 내부)**: 변경 후 `agents.gohtml` 안의 `Configure`/`Test Build`/`Teardown All`/`Delete` 4개 텍스트가 모두 `<details>...</details>` 블록 안에 있다. — 검증: 수동 — 브라우저에서 details 닫힌 상태에서 4개 텍스트 미표시, 펼치면 모두 표시.
- [ ] **F-14 (D-4: column 레이아웃)**: `<details>` 내부 wrapping `<div>` 의 style 에 `flex-direction:column` 포함. — 검증: `grep "flex-direction:column" internal/hub/views/agents.gohtml` 결과 ≥ 1.
- [ ] **F-15 (D-4: confirm 보존)**: 변경 후에도 Teardown All / Delete 의 `onsubmit="return confirm(...)"` 호출이 보존된다 (Phase 13 의 보호 유지). — 검증: `grep -c 'onsubmit="return confirm' internal/hub/views/agents.gohtml` 결과 = 2.
- [ ] **F-16 (D-5: preview_detail breadcrumb)**: `preview_detail.gohtml` 의 `{{define "content"}}` 직후에 `<nav aria-label="breadcrumb">` 1회 등장. — 검증: `grep -c '<nav aria-label="breadcrumb">' internal/hub/views/preview_detail.gohtml` 결과 = 1.
- [ ] **F-17 (D-5: preview_detail breadcrumb 콘텐츠)**: breadcrumb 안에 `<a href="/admin/previews">Previews</a>` 와 `PR #{{.Preview.PrNumber}} — {{.Preview.RepoFullName}}` (또는 `Test Build — {{.Preview.RepoFullName}}` for adhoc with PrNumber=0) 가 모두 존재. — 검증: grep.
- [ ] **F-18 (D-5: agent_detail breadcrumb)**: `agent_detail.gohtml` 에 `<nav aria-label="breadcrumb">` 1회 등장 + 내부에 `<a href="/admin/agents">Agents</a>` 와 `{{.Name}}` 둘 다 존재. — 검증: grep.
- [ ] **F-19 (D-5: 기존 "← Back to agents" 제거)**: agent_detail.gohtml 에서 `Back to agents` substring 미등장 (breadcrumb 으로 대체). — 검증: `grep -c 'Back to agents' internal/hub/views/agent_detail.gohtml` 결과 = 0.
- [ ] **F-20 (페이지 렌더 무회귀)**: `go build ./...` + `go test ./internal/hub/...` 둘 다 통과. 모든 detail 페이지가 200 응답. — 검증: 기존 `admin_ui_test.go` 의 모든 통과 케이스 그대로 통과.

## 7. 비기능 요구사항 체크리스트

- [ ] **NF-1 (이식성)**: 새 코드가 OS 의존 패키지(syscall, os/exec 등) import 안 함. — 검증: `grep -n 'syscall\|os/exec' internal/hub/admin_ui.go` 결과에 새 라인 없음.
- [ ] **NF-2 (XSS — Repo 링크)**: `RepoFullName` 이 `acme/web` 같은 정상 값일 때만 사용되지만, 이론적으로 `<` 등이 포함된 데이터가 들어가도 `href` 와 텍스트 콘텐츠 양쪽이 html/template auto-escape 로 안전. `template.HTML` 반환 helper 를 본 Phase 에서 신규 추가하지 않으므로 escape 우회 위험 없음. — 검증: `grep -n 'template.HTML\|template.HTMLAttr' internal/hub/admin_ui.go` 의 결과가 Phase 12 에서 추가된 helper 외에 새 항목 없음.
- [ ] **NF-3 (XSS — Agent ID)**: `AgentID` 는 UUID(`uuid.NewString()`) 산출이므로 항상 alphanumeric+`-`. 이론적 escape 는 html/template 가 보장. — 검증: 코드 리뷰. `view.AgentID` 가 escape 안 된 상태로 `template.HTML` 에 들어가는 경로 없음.
- [ ] **NF-4 (관측성)**: `agentDetail` 의 ListByAgent 실패 시 정확히 `slog.Warn` 1회 호출되고 메시지 키는 `admin_ui_agent_active_previews_failed`. 그 외 새 로그 없음. — 검증: `grep -n 'admin_ui_agent_active_previews_failed' internal/hub/admin_ui.go` 결과 = 1.
- [ ] **NF-5 (성능 — 응답 크기)**: ActivePreviews 가 50 row 로 제한되므로 agent_detail 응답 본문은 worst-case 약 50 × 한 row HTML(~600B) ≈ 30KB 미만. — 검증: 코드 리뷰 + 수동 — 50건 seed 한 응답 크기 측정.
- [ ] **NF-6 (성능 — DB 쿼리 추가)**: agent_detail 페이지가 1회 추가 DB 호출(`ListByAgent`) 을 발생시킨다. assigned_agent_id 인덱스가 이미 있으므로 응답 시간 영향은 ms 수준. — 검증: 코드 리뷰 (Phase 1 의 인덱스 정의 재확인 — `idx_previews_assigned_agent_id`).
- [ ] **NF-7 (Pico 테마 호환)**: `<details>`, `<summary>`, `<nav aria-label="breadcrumb">` 모두 Pico v2 가 기본 스타일링하는 selector. 별도 CSS 도입 없음. — 검증: 다크/라이트 모드 양쪽에서 시각 깨짐 없음 (devtools 의 prefers-color-scheme 토글).
- [ ] **NF-8 (template parse 영향 없음)**: `mustParsePages` 가 시작 시 panic 없이 모든 페이지(10개) 파싱. 새 helper 없으므로 FuncMap 변경 없음. — 검증: 테스트 `TestNewAdminUIHandler*` (있다면) 또는 `go test -run TestAdminUI` 통과.
- [ ] **NF-9 (HTML 검증)**: 변경된 페이지의 출력이 W3C HTML 검증을 통과하거나, 적어도 unclosed tag / mismatched element 가 없다. `<details>` 안의 `<form>` 도 valid HTML. — 검증: 수동 — `view-source:` 후 W3C validator 또는 `tidy -e` 실행.

## 8. 리스크와 완화책

- **R-1 (D-3 — agent_detail 페이지 무거워짐)**: agent 가 매우 많은 preview 를 들고 있을 때 페이지 응답 시간/크기 증가. 결정 4 의 50건 LIMIT 으로 완화하되, "총 N개" hint 가 운영자에게 가시화. 향후 페이지네이션 도입은 별도 Phase.
  - **완화**: `activePreviewLimit = 50` 상수로 한곳에 명시. 변경 시 한 줄 수정.

- **R-2 (D-3 — sort.Slice 안정성)**: 핸들러에서 `UpdatedAt` 으로 정렬하는데 동일 시각 row 가 있을 때 순서가 비결정적. 운영상 문제 없으나 테스트 픽스처에서 동일 시각 사용 시 flaky 가능.
  - **완화**: 단위테스트 fixture 의 UpdatedAt 을 1ms 단위로 다르게 set. 또는 `sort.SliceStable` 사용. 본 Phase 에서는 `sort.Slice` 로 충분(테스트만 주의).

- **R-3 (D-2 — AgentID 가 노출되어도 보안 영향 없는가)**: `AgentID` 는 UUID 라 enumeration 위험 없음. agent detail 페이지는 admin 인증 필요. 외부 노출 위험 없음.
  - **완화**: 코드 리뷰 시 admin 미들웨어 라우팅 확인 (`/admin/*` 가 기존 인증 미들웨어 적용되는지).

- **R-4 (D-4 — `<details>` 내 form 의 layout)**: form 이 block 으로 바뀌면서 button 이 column 의 100% 너비를 차지하지 않으면 시각적으로 어색. `width:100%` style 명시.
  - **완화**: §5 마크업 예시에 `style="width:100%"` 명시. 수동 시각 확인 (F-13/F-14 검증).

- **R-5 (D-5 — breadcrumb 의 PrNumber == 0 케이스)**: adhoc preview 는 `PrNumber == 0` 이라 breadcrumb 에 `PR #0` 이 표시되면 어색. `Test Build — owner/repo` 로 분기.
  - **완화**: 템플릿에서 `{{if eq .Preview.PrNumber 0}}Test Build — ...{{else}}PR #... — ...{{end}}` 분기. 기존 `previewDetail` 핸들러의 `Title` 분기와 동일 규칙(라인 919~921).

- **R-6 (D-1 — Repo cross-link 의 ?repo= 인코딩)**: `RepoFullName` 이 `acme/web` 처럼 `/` 를 포함하는데 `?repo=` 의 query value 로 그대로 들어가면 `/` 가 raw 로 전송됨. RFC 3986 상 query 안의 `/` 는 reserved 하지만 unreserved 와 호환되어 대부분 브라우저/서버가 허용. Go 의 `r.URL.Query().Get("repo")` 도 raw 로 받음. 단 `&`/`#`/공백 등이 들어갈 위험은 RepoFullName 데이터 모델상 (GitHub 의 `owner/repo` 규칙) 없음.
  - **완화**: 일반 GitHub repo 명은 alphanumeric + `-`/`_`/`.`/`/` 만 허용되므로 escape 불필요. 그러나 안전망을 위해 템플릿에서 `{{.Preview.RepoFullName | urlquery}}` 사용 가능. 본 Phase 는 raw 사용을 default 로 하고 (기존 previews.gohtml 의 filter 이 동일 방식), 회귀 발견 시 `urlquery` helper 적용.

- **R-7 (D-3 — previewRow 의 AgentLabel 가 항상 자기 자신)**: ActivePreviews 안의 row 는 모두 같은 agent (agent_detail 의 그 agent) 에 속하므로 AgentLabel 컬럼이 의미 없음. `previews.gohtml` 의 Agent 컬럼을 그대로 베끼면 시각 노이즈.
  - **완화**: §5 의 마크업 예시에서 Agent 컬럼을 의도적으로 생략. 표 헤더가 `PR/Repo/Status/Branch/URLs/Updated` 6개 (Agent 없음).

- **R-8 (D-4 — `<details>` 의 SEO/접근성)**: 스크린리더가 `<details>` 의 닫힌 상태에서 내부 link 를 읽지 않을 수 있음. 운영 페이지라 SEO 무관, 접근성은 키보드 사용자가 summary 에서 Enter 로 펼칠 수 있어 문제없음.
  - **완화**: `<summary>Actions</summary>` 텍스트가 명확. 추가 `aria-label` 불필요.

## 9. 다음 Phase 연결점

- 본 Phase 의 `AgentID` 노출은 후속 D-1' (preview detail 의 owner/repo 분리 클릭) / E-5 (agent_detail 의 metadata 강화) 에서 그대로 재사용.
- ActivePreviews 의 `previewRow` 재사용 패턴은 후속 E-3 (대시보드의 최근 실패 5개) 가 같은 구조로 갈 것 — `dashboardView.RecentFailures []previewRow`.
- breadcrumb 마크업 패턴은 향후 신설될 detail 페이지 (예: repo_detail) 에 직접 복사. 4번째 페이지가 추가될 때쯤 helper 추출을 재고 (현재는 2개라 helper 가 과잉).
- D-4 의 `<details>` 패턴은 향후 다른 행이 많은 테이블 (예: previews 목록의 row-level actions) 에서 동일하게 재사용 가능. 본 Phase 가 첫 사례.
- `activePreviewLimit = 50` 상수는 후속 Phase 의 ListByAgent paged API 도입 시 첫 페이지 size 의 default 값으로 승격.
- Phase 12 의 `RequestPath`/`navActive`/`statusBadge` 는 본 Phase 에서 그대로 사용되며 변경 없음. nav 의 active 강조는 `/admin/previews/{id}` → Previews active, `/admin/agents/{id}` → Agents active 로 자연스럽게 동작 (Phase 12 의 prefix 매칭 규칙).
