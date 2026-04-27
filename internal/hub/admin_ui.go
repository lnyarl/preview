// 이 파일의 책임:
//   - SSR Admin UI 핸들러 (Phase 3 §5-4, §5-5).
//   - Phase 3 페이지: dashboard, agents, token, previews, preview_detail (+ rebuild POST).
//   - Phase 4 페이지: agent_detail (build config 폼) + POST /admin/agents/{id}/config.
//   - html/template 표준 라이브러리만 사용 (결정 1) + embed.FS (결정 5) + Pico CDN (결정 2/17).
//   - 시작 시 1회 template.Must(template.ParseFS(...)) 로 fail-fast.
//
// 참고: docs/specs/phase-3-admin-ui-and-mvp.md §5-1, §5-4, §5-5,
//
//	docs/specs/phase-4-agent-build-config.md §4-3, §4-4, §4-5.
package hub

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/store"
)

//go:embed views/*.gohtml
var viewsFS embed.FS

// AdminUIHandler 는 /admin/* SSR 핸들러 묶음.
type AdminUIHandler struct {
	AgentStore       store.AgentStore
	PreviewStore     store.PreviewStore
	RepoSecrets      store.RepoSecretStore // Phase 10 (옵션 — nil 이면 /admin/repos 에 secret 카운트 0).
	TokenGen         *token.Generator
	Logger           *slog.Logger
	Registry         *ConnRegistry  // 옵션 — online 카운트 정확도 향상.
	TeardownSender   TeardownSender // 옵션 — agent teardown WS 메시지 송신.
	Dispatcher       *Dispatcher    // 옵션 — rebuild/test-build 후 즉시 dispatch 트리거.
	AgentDownloadURL string         // 빈 값이면 소스 빌드 안내, 설정 시 다운로드 링크 표시.

	cfg Config // 설정 페이지 표시용 (웹훅 시크릿 등).

	tmpls map[string]*template.Template
	now   func() time.Time
}

// NewAdminUIHandler 는 AdminUIHandler 를 조립하고 모든 템플릿을 시작 시 1회 파싱한다.
// 파싱 실패는 panic (fail-fast on startup, 결정 5).
func NewAdminUIHandler(as store.AgentStore, ps store.PreviewStore, tg *token.Generator, logger *slog.Logger) *AdminUIHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &AdminUIHandler{
		AgentStore:   as,
		PreviewStore: ps,
		TokenGen:     tg,
		Logger:       logger,
		now:          func() time.Time { return time.Now().UTC() },
	}
	h.tmpls = mustParsePages([]string{
		"dashboard.gohtml",
		"agents.gohtml",
		"token.gohtml",
		"previews.gohtml",
		"preview_detail.gohtml",
		"agent_detail.gohtml",
		"settings.gohtml",
		"test_build.gohtml",
		"repos.gohtml",
		"repo_secrets.gohtml",
	})
	return h
}

// SetRepoSecrets 는 Phase 10 의 RepoSecretStore 를 주입한다 (옵션).
// nil 일 때 /admin/repos 인덱스가 secret 카운트를 0 으로 표시하고 dispatcher hydration 도 skip.
func (h *AdminUIHandler) SetRepoSecrets(rs store.RepoSecretStore) { h.RepoSecrets = rs }

// SetRegistry 는 online 여부 카운트 보강용 ConnRegistry 를 주입한다 (옵션).
func (h *AdminUIHandler) SetRegistry(reg *ConnRegistry) { h.Registry = reg }

// SetTeardownSender 는 JOB_TEARDOWN WS 메시지 송신자를 주입한다 (옵션).
func (h *AdminUIHandler) SetTeardownSender(s TeardownSender) { h.TeardownSender = s }

// SetDispatcher 는 rebuild/test-build 후 즉시 dispatch 트리거용 Dispatcher 를 주입한다 (옵션).
func (h *AdminUIHandler) SetDispatcher(d *Dispatcher) { h.Dispatcher = d }

// SetConfig 는 설정 페이지 표시에 필요한 Hub Config 를 주입한다.
func (h *AdminUIHandler) SetConfig(cfg Config) { h.cfg = cfg }

// Register 는 mux 에 SSR 라우트를 등록한다.
// 이미 AdminHandler/WebhookHandler 가 점유한 라우트와의 충돌을 피하기 위해
// 신규 SSR 라우트만 등록 + 기존 GET/POST /admin/agents 와 /admin/previews* 는
// AdminHandler / WebhookHandler 가 Accept-header 분기로 SSR 응답 가능하게 처리.
func (h *AdminUIHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", h.dashboard)
	mux.HandleFunc("GET /admin/settings", h.settings)
	mux.HandleFunc("GET /admin/agents/token", h.agentToken)
	mux.HandleFunc("POST /admin/agents/{id}/delete", h.agentDelete)
	mux.HandleFunc("POST /admin/previews/{id}/rebuild", h.previewRebuild)
	mux.HandleFunc("POST /admin/previews/{id}/stop", h.previewStop)
	// Phase 4: agent detail (build config 폼) + 저장 POST.
	// {id} 가 "token" 등 다른 정적 prefix 와 충돌하지 않도록 GET /admin/agents/token 보다
	// 뒤에 등록되어도 net/http mux 는 longest-prefix + literal-match 우선이므로 안전.
	mux.HandleFunc("GET /admin/agents/{id}", h.agentDetail)
	mux.HandleFunc("GET /admin/agents/{id}/test-build", h.testBuildForm)
	mux.HandleFunc("POST /admin/agents/{id}/test-build", h.testBuildSubmit)
	mux.HandleFunc("POST /admin/agents/{id}/teardowns", h.agentTeardowns)
	// Phase 10: repo secrets.
	mux.HandleFunc("GET /admin/repos", h.reposIndex)
	mux.HandleFunc("GET /admin/repos/{owner}/{repo}/secrets", h.repoSecretsGet)
	mux.HandleFunc("POST /admin/repos/{owner}/{repo}/secrets", h.repoSecretsPost)
}

// mustParsePages 는 layout.gohtml + 각 페이지를 합쳐 별도 *template.Template 로 만든다.
// 각 페이지는 자기 파일에 `{{define "content"}}...{{end}}` 를 가지므로 페이지마다 트리가 분리된다.
//
// Phase 12: layout 파싱 시 adminFuncs() 를 Funcs() 로 등록한다 — 모든 페이지가 layout
// 트리를 공유하므로 한 곳에서 등록하면 statusBadge / navActive helper 가 어디서든 호출 가능
// (phase-12-ux-consistency.md §3 결정 1).
func mustParsePages(pages []string) map[string]*template.Template {
	out := make(map[string]*template.Template, len(pages))
	layoutBytes, err := viewsFS.ReadFile("views/layout.gohtml")
	if err != nil {
		panic(fmt.Sprintf("admin_ui: read layout: %v", err))
	}
	for _, p := range pages {
		pageBytes, err := viewsFS.ReadFile("views/" + p)
		if err != nil {
			panic(fmt.Sprintf("admin_ui: read %s: %v", p, err))
		}
		// template.Must — fail-fast on parse error at startup (결정 5).
		t := template.Must(template.New(p).Funcs(adminFuncs()).Parse(string(layoutBytes)))
		t = template.Must(t.Parse(string(pageBytes)))
		out[p] = t
	}
	return out
}

// adminFuncs 는 모든 admin 페이지가 공유하는 template helper 셋을 반환한다.
// mustParsePages 의 layout 파싱 단계에서 Funcs() 로 등록된다
// (phase-12-ux-consistency.md §3 결정 1, §5).
func adminFuncs() template.FuncMap {
	return template.FuncMap{
		"statusBadge": statusBadge,
		"navActive":   navActive,
	}
}

// statusBadge 는 preview status 문자열을 색·라운드를 가진 inline element 로 변환해 반환한다.
// 매핑 (phase-12-ux-consistency.md §5 표):
//   - queued/assigned: gray (Pico secondary 토큰)
//   - building       : blue (Pico primary 토큰)
//   - running        : <mark>running</mark> (Pico mark — green 계열)
//   - teardown       : orange (#fff3cd / #856404 — Pico 토큰에 직접 매칭이 없어 inline 색)
//   - done           : muted gray
//   - failed         : red
//   - 그 외          : escape 후 plain <span> (XSS 안전망 — NF-2)
//
// 본 helper 는 입력 status 를 항상 html.EscapeString 로 escape 한 뒤 삽입하므로,
// template.HTML 반환은 안전하다.
func statusBadge(status string) template.HTML {
	const pillBase = "display:inline-block; padding:0.1em 0.55em; border-radius:10px; font-size:0.85em;"
	esc := html.EscapeString(status)
	switch status {
	case "queued", "assigned":
		return template.HTML(fmt.Sprintf(
			`<span class="status-pill" style="%s background:var(--pico-secondary-background); color:var(--pico-secondary)">%s</span>`,
			pillBase, esc))
	case "building":
		return template.HTML(fmt.Sprintf(
			`<span class="status-pill" style="%s background:var(--pico-primary-background); color:var(--pico-primary)">%s</span>`,
			pillBase, esc))
	case "running":
		return template.HTML("<mark>" + esc + "</mark>")
	case "teardown":
		return template.HTML(fmt.Sprintf(
			`<span class="status-pill" style="%s background:#fff3cd; color:#856404">%s</span>`,
			pillBase, esc))
	case "done":
		return template.HTML(fmt.Sprintf(
			`<span class="status-pill" style="%s color:var(--pico-muted-color)">%s</span>`,
			pillBase, esc))
	case "failed":
		return template.HTML(fmt.Sprintf(
			`<span class="status-pill" style="%s background:#f8d7da; color:var(--pico-color-red-500)">%s</span>`,
			pillBase, esc))
	default:
		// 알 수 없는 status — escape 후 plain <span> (NF-2 보안 안전망).
		return template.HTML("<span>" + esc + "</span>")
	}
}

// navActive 는 nav 링크의 active 여부를 결정해 aria-current 속성과 강조 클래스/스타일을
// 합친 attribute 문자열을 반환한다. 매칭 규칙:
//
//   - linkPath == "/admin"  : currentPath == "/admin" (정확 일치만). prefix 매칭을
//     쓰면 모든 /admin/* 경로에서 Dashboard 가 active 가 되어 두 nav 가 동시 강조됨.
//   - 그 외                 : currentPath == linkPath || HasPrefix(currentPath, linkPath+"/")
//     `linkPath+"/"` 접미사 강제로 `/admin/agents-history` 같은 경계 누수 차단.
//
// (phase-12-ux-consistency.md §3 결정 3.)
//
// `class="nav-active"` 는 본 Phase 시점에서 시각에 영향 없는 dead identifier 지만
// 후속 Phase 의 별도 CSS 도입 시 selector hook 으로 둔다 (결정 9).
//
// 입력은 코드 상수 + URL.Path 라 별도 escape 가 필요하지 않으며, 반환 문자열은 모두
// 정적 ASCII 이므로 attribute 컨텍스트 안전.
func navActive(currentPath, linkPath string) template.HTMLAttr {
	const activeAttr = ` aria-current="page" class="nav-active" style="font-weight:700; color:var(--pico-primary)"`
	if linkPath == "/admin" {
		if currentPath == "/admin" {
			return template.HTMLAttr(activeAttr)
		}
		return template.HTMLAttr("")
	}
	if currentPath == linkPath || strings.HasPrefix(currentPath, linkPath+"/") {
		return template.HTMLAttr(activeAttr)
	}
	return template.HTMLAttr("")
}

// renderHTML 은 layout 템플릿을 실행해 응답에 쓴다.
func (h *AdminUIHandler) renderHTML(w http.ResponseWriter, status int, page string, data any) {
	t, ok := h.tmpls[page]
	if !ok {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		h.Logger.Warn("admin_ui_render_failed", "page", page, "err", err.Error())
	}
}

// ---------- Dashboard ----------

type dashboardView struct {
	Title           string
	RequestPath     string // Phase 12: layout nav active 매칭용 (r.URL.Path).
	TotalAgents     int
	OnlineAgents    int
	RunningPreviews int
	QueuedPreviews  int
	StatusCounts    map[string]int
}

func (h *AdminUIHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	agents, err := h.AgentStore.List(r.Context())
	if err != nil {
		h.Logger.Error("admin_ui_dashboard_agents_failed", "err", err.Error())
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	previews, err := h.PreviewStore.ListAll(r.Context())
	if err != nil {
		h.Logger.Error("admin_ui_dashboard_previews_failed", "err", err.Error())
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	online := 0
	if h.Registry != nil {
		online = len(h.Registry.OnlineAgentIDs())
	}
	if online == 0 {
		// fall back to agents.status == "online"
		for _, a := range agents {
			if a.Status == "online" {
				online++
			}
		}
	}
	counts := map[string]int{
		"queued": 0, "assigned": 0, "building": 0, "running": 0,
		"teardown": 0, "done": 0, "failed": 0,
	}
	for _, p := range previews {
		counts[p.Status]++
	}
	view := dashboardView{
		Title:           "Dashboard",
		TotalAgents:     len(agents),
		OnlineAgents:    online,
		RunningPreviews: counts["running"],
		QueuedPreviews:  counts["queued"],
		StatusCounts:    counts,
	}
	view.RequestPath = r.URL.Path
	h.renderHTML(w, http.StatusOK, "dashboard.gohtml", view)
}

// ---------- Agents list ----------

type agentRow struct {
	ID             string
	Name           string
	Status         string
	LabelsString   string
	RunningCount   int
	LastSeenString string
}

type agentsView struct {
	Title       string
	RequestPath string // Phase 12: layout nav active 매칭용 (r.URL.Path).
	Agents      []agentRow
	Error       string
}

// AgentsList 는 GET /admin/agents 의 SSR 핸들러. AdminHandler 가 Accept-header 분기에서
// 이 메서드를 호출한다.
func (h *AdminUIHandler) AgentsList(w http.ResponseWriter, r *http.Request) {
	h.agentsList(w, r)
}

func (h *AdminUIHandler) agentsList(w http.ResponseWriter, r *http.Request) {
	agents, err := h.AgentStore.List(r.Context())
	if err != nil {
		h.renderHTML(w, http.StatusInternalServerError, "agents.gohtml",
			agentsView{Title: "Agents", RequestPath: r.URL.Path, Error: err.Error()})
		return
	}
	rows := make([]agentRow, 0, len(agents))
	for _, a := range agents {
		runningCount := 0
		if h.PreviewStore != nil {
			ps, perr := h.PreviewStore.ListByAgent(r.Context(), a.ID,
				[]string{"assigned", "building", "running"})
			if perr == nil {
				runningCount = len(ps)
			}
		}
		last := ""
		if a.LastSeenAt != nil {
			last = a.LastSeenAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, agentRow{
			ID:             a.ID,
			Name:           a.Name,
			Status:         a.Status,
			LabelsString:   labelsToString(a.Labels),
			RunningCount:   runningCount,
			LastSeenString: last,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	h.renderHTML(w, http.StatusOK, "agents.gohtml",
		agentsView{Title: "Agents", RequestPath: r.URL.Path, Agents: rows})
}

// labelsToString 은 라벨 슬라이스를 ", " 로 join 한 표시용 문자열로 변환한다.
// 빈 슬라이스 / nil 모두 "" 를 돌려준다.
func labelsToString(labels []string) string {
	return strings.Join(labels, ", ")
}

// parseLabelsForm 은 "a, b, c" 형태의 form 값을 라벨 슬라이스로 변환한다.
// 빈 토큰은 제거하고 trim 한 결과만 반환한다. 빈 입력은 nil 슬라이스 (caller 가 정규화).
func parseLabelsForm(raw string) []string {
	var result []string
	for _, part := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(part); v != "" {
			result = append(result, v)
		}
	}
	return result
}

// CreateAgentForm 는 SSR 폼 POST 처리. AdminHandler 가 Accept-header 분기에서 호출.
func (h *AdminUIHandler) CreateAgentForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if !nameRE.MatchString(name) {
		http.Error(w, "invalid name (must match ^[a-zA-Z0-9_-]{1,64}$)", http.StatusBadRequest)
		return
	}
	labels := parseLabelsForm(r.FormValue("labels"))
	raw, hash, err := h.TokenGen.Generate()
	if err != nil {
		h.Logger.Error("admin_ui_token_generate_failed", "err", err.Error())
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	a := store.Agent{
		ID:        uuid.NewString(),
		Name:      name,
		TokenHash: hash,
		Labels:    labels,
		Status:    "offline",
		CreatedAt: h.now(),
	}
	if err := h.AgentStore.Create(r.Context(), a); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			http.Error(w, "agent name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("admin_ui_agent_create_failed", "err", err.Error())
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.Logger.Info("agent_registered", "agent_id", a.ID, "name", a.Name)
	q := url.Values{}
	q.Set("name", a.Name)
	q.Set("t", raw)
	http.Redirect(w, r, "/admin/agents/token?"+q.Encode(), http.StatusSeeOther)
}

// ---------- Token display ----------

type tokenView struct {
	Title            string
	RequestPath      string // Phase 12: layout nav active 매칭용 (r.URL.Path).
	Name             string
	Token            string
	HubHost          string // e.g. "localhost:3000" — Agent 실행 명령에 사용
	AgentDownloadURL string // 빈 값이면 소스 빌드 안내 표시
}

func (h *AdminUIHandler) agentToken(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	tok := r.URL.Query().Get("t")
	if tok == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	// r.Host 는 브라우저가 Hub 에 접속한 주소 — Agent 실행 명령 생성에 사용.
	host := r.Host
	if host == "" {
		host = "localhost:3000"
	}
	h.renderHTML(w, http.StatusOK, "token.gohtml",
		tokenView{Title: "Agent Setup", RequestPath: r.URL.Path,
			Name: name, Token: tok, HubHost: host,
			AgentDownloadURL: h.AgentDownloadURL})
}

// ---------- Agent delete (SSR) ----------

func (h *AdminUIHandler) agentDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "agent id required", http.StatusNotFound)
		return
	}
	if err := h.AgentStore.Delete(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("admin_ui_agent_delete_failed", "err", err.Error(), "agent_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.Logger.Info("agent_deleted", "agent_id", id)
	http.Redirect(w, r, "/admin/agents", http.StatusSeeOther)
}

// ---------- Test Build ----------

type testBuildView struct {
	Title       string
	RequestPath string // Phase 12: layout nav active 매칭용 (r.URL.Path).
	AgentID     string
	AgentName   string
	Error       string
}

func (h *AdminUIHandler) testBuildForm(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "agent id required", http.StatusNotFound)
		return
	}
	a, err := h.AgentStore.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	h.renderHTML(w, http.StatusOK, "test_build.gohtml",
		testBuildView{Title: "Test Build", RequestPath: r.URL.Path,
			AgentID: id, AgentName: a.Name})
}

// testBuildSubmit 은 Admin Test Build 폼 POST 핸들러.
//
// Phase 11 dedup 시퀀스 (§4 의사코드):
//
//  1. FindAdhocByBranch(ctx, repo, branch) lookup.
//  2. dedup HIT (err == nil):
//     - active = {queued, assigned, building, running, teardown} → noop redirect.
//     - terminal = {done, failed} → 6 필드 reset + UpdateStatus(→queued).
//     ErrStaleState 시 다른 경로가 이미 처리한 것으로 보고 redirect (dispatcher 트리거 생략).
//     성공 시 triggerDispatch + redirect.
//  3. dedup MISS (ErrNotFound) → 기존 Upsert 경로. !created 는 비범위(§2) — slog.Error+500.
//  4. 그 외 err → 500.
//
// 결정 4: re-queue 시 fields.CommitSha = nil. store 의 nil 가드가 SQL 갱신을 건너뛰므로
// 기존 sha 가 자연 보존된다. 폼의 commit_sha 입력은 dedup hit 경로에서 무시된다.
func (h *AdminUIHandler) testBuildSubmit(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	repoCloneURL := strings.TrimSpace(r.FormValue("repo_clone_url"))
	branch := strings.TrimSpace(r.FormValue("branch"))
	commitSha := strings.TrimSpace(r.FormValue("commit_sha"))

	repoFullName, err := repoFullNameFromURL(repoCloneURL)
	if err != nil {
		a, _ := h.AgentStore.GetByID(r.Context(), id)
		name := id
		if a != nil {
			name = a.Name
		}
		h.renderHTML(w, http.StatusBadRequest, "test_build.gohtml",
			testBuildView{Title: "Test Build", RequestPath: r.URL.Path,
				AgentID: id, AgentName: name,
				Error: "invalid repo URL: " + err.Error()})
		return
	}

	// (1) dedup lookup — (repo, branch, is_adhoc=1) 가장 최근 row.
	prev, ferr := h.PreviewStore.FindAdhocByBranch(r.Context(), repoFullName, branch)
	switch {
	case ferr == nil:
		// (2) dedup HIT.
		switch prev.Status {
		case "queued", "assigned", "building", "running", "teardown":
			// active → noop redirect. dispatcher 호출 없음 (F-11).
			h.Logger.Info("test_build_dedup_active", "agent_id", id,
				"repo", repoFullName, "branch", branch, "preview_id", prev.ID,
				"status", prev.Status)
			http.Redirect(w, r, "/admin/previews/"+prev.ID, http.StatusSeeOther)
			return
		case "done", "failed":
			// terminal → 6 필드 reset + UpdateStatus(→queued).
			// CommitSha 는 nil 로 두어 store 의 nil 가드가 SQL 갱신을 건너뛰게 한다 (결정 4).
			emptyContainer := ""
			emptyHost := ""
			zeroPort := 0
			emptyURL := ""
			emptyErr := ""
			emptyAssign := ""
			fields := store.PreviewFields{
				ContainerID:     &emptyContainer,
				AgentHost:       &emptyHost,
				AgentPort:       &zeroPort,
				PreviewURLs:     &emptyURL,
				ErrorMessage:    &emptyErr,
				AssignedAgentID: &emptyAssign,
				// CommitSha: nil — 기존 sha 보존 (결정 4).
			}
			uerr := h.PreviewStore.UpdateStatus(r.Context(), prev.ID, prev.Status, "queued",
				"re-queued by test build", h.now(), fields)
			if errors.Is(uerr, store.ErrStaleState) {
				// 다른 경로가 이미 상태를 바꿨다 → 그 경로의 dispatcher 트리거에 일임 (F-6).
				h.Logger.Info("test_build_dedup_requeued", "agent_id", id,
					"repo", repoFullName, "branch", branch, "preview_id", prev.ID,
					"stale_state", true)
				http.Redirect(w, r, "/admin/previews/"+prev.ID, http.StatusSeeOther)
				return
			}
			if uerr != nil {
				h.Logger.Error("admin_ui_test_build_requeue_failed",
					"err", uerr.Error(), "preview_id", prev.ID)
				http.Error(w, "internal", http.StatusInternalServerError)
				return
			}
			h.Logger.Info("test_build_dedup_requeued", "agent_id", id,
				"repo", repoFullName, "branch", branch, "preview_id", prev.ID)
			h.triggerDispatch(r.Context())
			http.Redirect(w, r, "/admin/previews/"+prev.ID, http.StatusSeeOther)
			return
		default:
			// 알 수 없는 상태값 — 보수적으로 redirect.
			h.Logger.Warn("admin_ui_test_build_unknown_status",
				"preview_id", prev.ID, "status", prev.Status)
			http.Redirect(w, r, "/admin/previews/"+prev.ID, http.StatusSeeOther)
			return
		}
	case errors.Is(ferr, store.ErrNotFound):
		// (3) dedup MISS — 신규 Upsert 경로.
	default:
		// (4) 그 외 lookup 에러.
		h.Logger.Error("admin_ui_test_build_dedup_lookup_failed",
			"err", ferr.Error(), "repo", repoFullName, "branch", branch)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	now := h.now()
	p := store.Preview{
		ID:           uuid.NewString(),
		RepoFullName: repoFullName,
		PrNumber:     0,
		CommitSha:    commitSha,
		Branch:       branch,
		RepoCloneURL: repoCloneURL,
		Labels:       []string{},
		IsAdhoc:      true, // Phase 9: Admin Test Build 진입점은 항상 IsAdhoc=true.
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	created, _, err := h.PreviewStore.Upsert(r.Context(), p)
	if err != nil {
		h.Logger.Error("admin_ui_test_build_upsert_failed", "err", err.Error())
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !created {
		// Phase 11 §3 결정 5 / §2 비범위: cross-branch sha 충돌 (같은 sha 가 다른 branch/PR
		// 로 진입) 케이스. 본 Phase 는 비범위로 선언하고 보호 차원의 500 으로 처리한다.
		// 후속 Phase 에서 정식 정책 도입 예정.
		h.Logger.Error("admin_ui_test_build_unexpected_existing",
			"repo", repoFullName, "branch", branch, "commit_sha", commitSha,
			"preview_id", p.ID)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.Logger.Info("test_build_triggered", "agent_id", id,
		"repo", repoFullName, "branch", branch, "preview_id", p.ID, "is_adhoc", true)
	h.triggerDispatch(r.Context())
	http.Redirect(w, r, "/admin/previews/"+p.ID, http.StatusSeeOther)
}

// repoFullNameFromURL 은 git clone URL 에서 "owner/repo" 를 추출한다.
// e.g. https://github.com/owner/repo.git → "owner/repo"
func repoFullNameFromURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("expected owner/repo path in URL, got %q", u.Path)
	}
	return parts[0] + "/" + parts[1], nil
}

// ---------- Agent teardowns ----------

func (h *AdminUIHandler) agentTeardowns(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "agent id required", http.StatusNotFound)
		return
	}
	previews, err := h.PreviewStore.ListByAgent(r.Context(), id,
		[]string{"assigned", "building", "running"})
	if err != nil {
		h.Logger.Error("admin_ui_teardowns_list_failed", "err", err.Error(), "agent_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	now := h.now()
	count := 0
	for _, p := range previews {
		if err := h.PreviewStore.UpdateStatus(r.Context(), p.ID, p.Status, "teardown",
			"manual teardown via admin UI", now, store.PreviewFields{}); err != nil {
			h.Logger.Warn("admin_ui_teardown_update_failed",
				"preview_id", p.ID, "err", err.Error())
			continue
		}
		if h.TeardownSender != nil {
			if err := h.TeardownSender.SendTeardown(r.Context(), id, p.ID); err != nil {
				h.Logger.Warn("admin_ui_teardown_send_failed",
					"preview_id", p.ID, "err", err.Error())
			}
		}
		count++
	}
	h.Logger.Info("agent_teardowns", "agent_id", id, "count", count)
	http.Redirect(w, r, "/admin/agents", http.StatusSeeOther)
}

// ---------- Previews list ----------

type previewRow struct {
	ID            string
	PrNumber      int
	RepoFullName  string
	Status        string
	Branch        string
	AgentLabel    string
	UpdatedString string
	PreviewURLs   map[string]string // Phase 6: service → URL
	IsAdhoc       bool              // Phase 9: Adhoc badge 렌더용
}

type previewsFilter struct {
	Repo   string
	Status string
}

type previewsView struct {
	Title       string
	RequestPath string // Phase 12: layout nav active 매칭용 (r.URL.Path).
	Previews    []previewRow
	Filter      previewsFilter
}

// PreviewsList 는 GET /admin/previews 의 SSR 핸들러. WebhookHandler 가 Accept-header
// 분기에서 호출한다.
func (h *AdminUIHandler) PreviewsList(w http.ResponseWriter, r *http.Request) {
	h.previewsList(w, r)
}

func (h *AdminUIHandler) previewsList(w http.ResponseWriter, r *http.Request) {
	all, err := h.PreviewStore.ListAll(r.Context())
	if err != nil {
		h.Logger.Error("admin_ui_previews_list_failed", "err", err.Error())
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	repoFilter := strings.TrimSpace(r.URL.Query().Get("repo"))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))

	rows := make([]previewRow, 0, len(all))
	for _, p := range all {
		if repoFilter != "" && p.RepoFullName != repoFilter {
			continue
		}
		if statusFilter != "" && p.Status != statusFilter {
			continue
		}
		agentLabel := ""
		if p.AssignedAgentID != nil {
			agentLabel = *p.AssignedAgentID
		}
		var previewURLs map[string]string
		if p.PreviewURLs != "" {
			_ = json.Unmarshal([]byte(p.PreviewURLs), &previewURLs)
		}
		rows = append(rows, previewRow{
			ID:            p.ID,
			PrNumber:      p.PrNumber,
			RepoFullName:  p.RepoFullName,
			Status:        p.Status,
			Branch:        p.Branch,
			AgentLabel:    agentLabel,
			UpdatedString: p.UpdatedAt.UTC().Format(time.RFC3339),
			PreviewURLs:   previewURLs,
			IsAdhoc:       p.IsAdhoc,
		})
	}
	h.renderHTML(w, http.StatusOK, "previews.gohtml", previewsView{
		Title:       "Previews",
		RequestPath: r.URL.Path,
		Previews:    rows,
		Filter:      previewsFilter{Repo: repoFilter, Status: statusFilter},
	})
}

// ---------- Preview detail ----------

type eventRow struct {
	TimeString string
	FromString string
	ToStatus   string
	Message    string
}

type previewDetailRow struct {
	ID           string
	PrNumber     int
	RepoFullName string
	CommitSha    string
	Branch       string
	Status       string
	ErrorMessage string
	IsAdhoc      bool // Phase 9: Source 행("Adhoc (manual)" vs "Webhook") 표시용
}

type previewDetailView struct {
	Title       string
	RequestPath string // Phase 12: layout nav active 매칭용 (r.URL.Path).
	Preview     previewDetailRow
	AgentLine   string
	// AgentID 는 Phase 15 D-2 의 Agent cross-link 분기용. 비어있으면 plain text,
	// 채워져 있으면 /admin/agents/{AgentID} 로 가는 <a> 가 출력된다.
	AgentID         string
	PreviewURLs     map[string]string // Phase 6: service → URL (parsed from JSON)
	Events          []eventRow        // 최근 N개 (newest first)
	OlderEvents     []eventRow        // 그 이전 이벤트들 (collapsed, newest first)
	RebuildEnabled  bool
	StopEnabled     bool
	ConflictMessage string
	Diagnosis       string // 사람이 읽을 수 있는 실패 원인 요약
	BuildOutput     string // docker/compose 실행 출력 (error_message 의 "(output: ...)" 부분)
}

// extractBuildOutput 은 execRunner.Run 이 만드는
// "<cmd>: exit status N (output: <text>)" 형태의 에러 문자열에서
// 실제 커맨드 출력 부분만 추출한다.
func extractBuildOutput(msg string) string {
	const marker = "(output: "
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	out := strings.TrimSuffix(msg[i+len(marker):], ")")
	return strings.TrimSpace(out)
}

// diagnoseBuildError 는 raw 에러 메시지를 분석해 원인을 한 문장으로 반환한다.
func diagnoseBuildError(msg string) string {
	switch {
	case strings.Contains(msg, "repo_url_missing"):
		return "저장소 URL이 설정되지 않았습니다."
	case strings.Contains(msg, "repocache_ensure") && strings.Contains(msg, "exit status 128"):
		return "저장소를 clone할 수 없습니다. URL이 올바른지, private 저장소라면 GITHUB_TOKEN(또는 --github-token)이 필요합니다."
	case strings.Contains(msg, "repocache_checkout") && strings.Contains(msg, "fetch:"):
		return "저장소 fetch 실패. private 저장소라면 GITHUB_TOKEN(또는 --github-token)을 설정하세요."
	case strings.Contains(msg, "resolve ref") && strings.Contains(msg, "exit status 128"):
		// "resolve ref \"origin/main\": ..."에서 브랜치명 추출
		branch := "지정한 브랜치"
		if i := strings.Index(msg, "resolve ref \""); i >= 0 {
			rest := msg[i+len("resolve ref \""):]
			if j := strings.Index(rest, "\""); j >= 0 {
				branch = `"` + rest[:j] + `"`
			}
		}
		return fmt.Sprintf("브랜치 %s를 찾을 수 없습니다. 저장소의 브랜치 이름을 확인하세요.", branch)
	case strings.Contains(msg, "not found after fetch"):
		return "지정한 커밋 SHA가 저장소에 없습니다. SHA가 올바른지 확인하세요."
	case strings.Contains(msg, "preview_config_load"), strings.Contains(msg, ".preview.yml"):
		return ".preview.yml 파일을 찾을 수 없거나 형식이 잘못됐습니다."
	case strings.Contains(msg, "docker build"), strings.Contains(msg, "handleDockerfile"):
		return "Docker 이미지 빌드에 실패했습니다. Dockerfile을 확인하세요."
	case strings.Contains(msg, "compose") && strings.Contains(msg, "exit status"):
		return "docker compose up에 실패했습니다. docker-compose.yml을 확인하세요."
	case msg == "":
		return ""
	default:
		return "빌드 중 오류가 발생했습니다."
	}
}

// PreviewDetail 는 GET /admin/previews/{id} SSR 핸들러. WebhookHandler 가 Accept 분기에서 호출.
func (h *AdminUIHandler) PreviewDetail(w http.ResponseWriter, r *http.Request) {
	h.previewDetail(w, r)
}

func (h *AdminUIHandler) previewDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "preview id required", http.StatusNotFound)
		return
	}
	p, err := h.PreviewStore.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "preview not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("admin_ui_preview_detail_failed", "err", err.Error(), "preview_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	events, err := h.PreviewStore.ListPreviewEvents(r.Context(), id, 50, 0)
	if err != nil {
		h.Logger.Warn("admin_ui_preview_events_failed", "err", err.Error(), "preview_id", id)
		events = nil
	}
	// 역순(최신 먼저)으로 변환
	rows := make([]eventRow, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		from := "NULL"
		if e.FromStatus != nil {
			from = *e.FromStatus
		}
		rows = append(rows, eventRow{
			TimeString: e.CreatedAt.UTC().Format(time.RFC3339),
			FromString: from,
			ToStatus:   e.ToStatus,
			Message:    e.Message,
		})
	}
	// 최근 5개는 바로 표시, 나머지는 접힌 상태
	const recentEventCount = 5
	recentRows := rows
	var olderRows []eventRow
	if len(rows) > recentEventCount {
		recentRows = rows[:recentEventCount]
		olderRows = rows[recentEventCount:]
	}
	agentLine := "-"
	agentID := ""
	if p.AssignedAgentID != nil {
		agentID = *p.AssignedAgentID
		agentLine = *p.AssignedAgentID
		if p.AgentHost != nil && p.AgentPort != nil {
			agentLine = fmt.Sprintf("%s (%s:%d)", *p.AssignedAgentID, *p.AgentHost, *p.AgentPort)
		}
	}
	// Phase 6: preview_urls JSON 문자열 → map[string]string 파싱.
	var previewURLs map[string]string
	if p.PreviewURLs != "" {
		_ = json.Unmarshal([]byte(p.PreviewURLs), &previewURLs)
	}
	rebuildEnabled := p.Status == "done" || p.Status == "failed"
	stopEnabled := p.Status == "running"
	conflict := r.URL.Query().Get("msg")
	errMsg := ""
	if p.ErrorMessage != nil {
		errMsg = *p.ErrorMessage
	}
	title := fmt.Sprintf("PR #%d — %s", p.PrNumber, p.RepoFullName)
	if p.PrNumber == 0 {
		title = fmt.Sprintf("Test Build — %s", p.RepoFullName)
	}
	view := previewDetailView{
		Title:       title,
		RequestPath: r.URL.Path,
		Preview: previewDetailRow{
			ID:           p.ID,
			PrNumber:     p.PrNumber,
			RepoFullName: p.RepoFullName,
			CommitSha:    p.CommitSha,
			Branch:       p.Branch,
			Status:       p.Status,
			ErrorMessage: errMsg,
			IsAdhoc:      p.IsAdhoc,
		},
		AgentLine:       agentLine,
		AgentID:         agentID,
		PreviewURLs:     previewURLs,
		Events:          recentRows,
		OlderEvents:     olderRows,
		RebuildEnabled:  rebuildEnabled,
		StopEnabled:     stopEnabled,
		ConflictMessage: conflict,
		Diagnosis:       diagnoseBuildError(errMsg),
		BuildOutput:     extractBuildOutput(errMsg),
	}
	h.renderHTML(w, http.StatusOK, "preview_detail.gohtml", view)
}

// ---------- Preview rebuild ----------

func (h *AdminUIHandler) previewRebuild(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "preview id required", http.StatusNotFound)
		return
	}
	p, err := h.PreviewStore.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "preview not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("admin_ui_rebuild_get_failed", "err", err.Error(), "preview_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	if !(p.Status == "done" || p.Status == "failed") {
		if wantsJSON {
			writeError(w, http.StatusConflict, "already_in_flight",
				fmt.Sprintf("preview status is %s; rebuild allowed only from done/failed", p.Status))
			return
		}
		http.Redirect(w, r,
			fmt.Sprintf("/admin/previews/%s?msg=rebuild+requires+done+or+failed+status", id),
			http.StatusSeeOther)
		return
	}
	now := h.now()
	emptyAssign := ""
	emptyContainer := ""
	emptyHost := ""
	zeroPort := 0
	emptyURL := ""
	emptyErr := ""
	// Phase 6: PublicURL → PreviewURLs (NOT NULL 'TEXT', default '').
	// rebuild 시 이전 preview_urls 도 함께 초기화한다.
	fields := store.PreviewFields{
		AssignedAgentID: &emptyAssign,
		ContainerID:     &emptyContainer,
		AgentHost:       &emptyHost,
		AgentPort:       &zeroPort,
		PreviewURLs:     &emptyURL,
		ErrorMessage:    &emptyErr,
	}
	if err := h.PreviewStore.UpdateStatus(r.Context(), id, p.Status, "queued",
		"rebuild requested via admin UI", now, fields); err != nil {
		h.Logger.Error("admin_ui_rebuild_update_failed", "err", err.Error(), "preview_id", id)
		if wantsJSON {
			writeError(w, http.StatusInternalServerError, "internal", "rebuild failed")
			return
		}
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.Logger.Info("preview_rebuild_requested", "preview_id", id)
	h.triggerDispatch(r.Context())
	if wantsJSON {
		writeJSON(w, http.StatusOK, map[string]any{"preview_id": id, "status": "queued"})
		return
	}
	http.Redirect(w, r, "/admin/previews/"+id, http.StatusSeeOther)
}

// previewStop 은 POST /admin/previews/{id}/stop 핸들러.
// running 상태의 preview 를 teardown 으로 전이하고 agent 에 JOB_TEARDOWN 을 송신한다.
func (h *AdminUIHandler) previewStop(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "preview id required", http.StatusNotFound)
		return
	}
	p, err := h.PreviewStore.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "preview not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("admin_ui_stop_get_failed", "err", err.Error(), "preview_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if p.Status != "running" {
		http.Redirect(w, r,
			fmt.Sprintf("/admin/previews/%s?msg=stop+requires+running+status", id),
			http.StatusSeeOther)
		return
	}
	if err := h.PreviewStore.UpdateStatus(r.Context(), id, p.Status, "teardown",
		"stopped via admin UI", h.now(), store.PreviewFields{}); err != nil {
		h.Logger.Error("admin_ui_stop_update_failed", "err", err.Error(), "preview_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if h.TeardownSender != nil && p.AssignedAgentID != nil {
		if err := h.TeardownSender.SendTeardown(r.Context(), *p.AssignedAgentID, id); err != nil {
			h.Logger.Warn("admin_ui_stop_teardown_send_failed", "preview_id", id, "err", err.Error())
		}
	}
	h.Logger.Info("preview_stop_requested", "preview_id", id)
	http.Redirect(w, r, "/admin/previews/"+id, http.StatusSeeOther)
}

// triggerDispatch 는 현재 online 인 모든 agent 에 대해 dispatcher.OnReady 를 호출해
// 방금 queued 된 preview 가 즉시 할당되도록 한다.
// Dispatcher 또는 Registry 가 nil 이면 no-op.
func (h *AdminUIHandler) triggerDispatch(ctx context.Context) {
	if h.Dispatcher == nil || h.Registry == nil {
		return
	}
	for agentID := range h.Registry.OnlineAgentIDs() {
		if err := h.Dispatcher.OnReady(ctx, agentID); err != nil {
			h.Logger.Warn("admin_ui_trigger_dispatch_failed", "agent_id", agentID, "err", err.Error())
		}
	}
}

// ---------- Agent detail ----------

// agentDetailView 는 GET /admin/agents/{id} 의 렌더 모델.
type agentDetailView struct {
	Title          string
	RequestPath    string // Phase 12: layout nav active 매칭용 (r.URL.Path).
	AgentID        string
	Name           string
	Status         string
	LabelsString   string
	LastSeenString string
	CreatedString  string
	Error          string
	// ActivePreviews 는 Phase 15 D-3 — 이 Agent 에 할당된 assigned/building/running
	// 상태의 preview 목록 (UpdatedAt DESC, 최대 50개).
	ActivePreviews []previewRow
	// ActiveTotal 은 50건 자르기 전의 원본 개수 (hint 표시용).
	ActiveTotal int
}

// activePreviewLimit 은 agent_detail 의 Active Previews 표 최대 행수 (Phase 15 결정 4).
const activePreviewLimit = 50

// agentDetail 은 GET /admin/agents/{id} 핸들러.
func (h *AdminUIHandler) agentDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "agent id required", http.StatusNotFound)
		return
	}
	a, err := h.AgentStore.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("admin_ui_agent_detail_get_failed", "err", err.Error(), "agent_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	view := agentDetailView{
		Title:         "Agent " + a.Name,
		RequestPath:   r.URL.Path,
		AgentID:       a.ID,
		Name:          a.Name,
		Status:        a.Status,
		LabelsString:  labelsToString(a.Labels),
		CreatedString: a.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.LastSeenAt != nil {
		view.LastSeenString = a.LastSeenAt.UTC().Format(time.RFC3339)
	}
	// Phase 15 D-3: Active Previews 섹션 — assigned/building/running 상태의 preview 목록.
	// 실패 시 WARN + 빈 목록으로 계속 (Phase 9/10 의 soft-fail 패턴, 결정 3).
	if h.PreviewStore != nil {
		ps, perr := h.PreviewStore.ListByAgent(r.Context(), id,
			[]string{"assigned", "building", "running"})
		if perr != nil {
			h.Logger.Warn("admin_ui_agent_detail_previews_failed",
				"agent_id", id, "err", perr.Error())
		} else {
			// newest first (UpdatedAt DESC).
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
	h.renderHTML(w, http.StatusOK, "agent_detail.gohtml", view)
}

type settingsView struct {
	Title             string
	RequestPath       string // Phase 12: layout nav active 매칭용 (r.URL.Path).
	WebhookURL        string
	WebhookSecret     string
	PreviewBaseDomain string
	AgentDownloadURL  string
}

// settings 는 GET /admin/settings — Hub 설정 정보(웹훅 URL, 시크릿 등) 표시.
func (h *AdminUIHandler) settings(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	view := settingsView{
		Title:             "Settings",
		RequestPath:       r.URL.Path,
		WebhookURL:        scheme + "://" + host + "/webhooks/github",
		WebhookSecret:     h.cfg.WebhookSecret,
		PreviewBaseDomain: h.cfg.PreviewBaseDomain,
		AgentDownloadURL:  h.cfg.AgentDownloadURL,
	}
	h.renderHTML(w, http.StatusOK, "settings.gohtml", view)
}
