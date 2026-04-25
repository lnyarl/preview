// 이 파일의 책임:
//   - SSR Admin UI 핸들러 (Phase 3 §5-4, §5-5).
//   - Phase 3 페이지: dashboard, agents, token, previews, preview_detail (+ rebuild POST).
//   - Phase 4 페이지: agent_detail (build config 폼) + POST /admin/agents/{id}/config.
//   - html/template 표준 라이브러리만 사용 (결정 1) + embed.FS (결정 5) + Pico CDN (결정 2/17).
//   - 시작 시 1회 template.Must(template.ParseFS(...)) 로 fail-fast.
//
// 참고: docs/specs/phase-3-admin-ui-and-mvp.md §5-1, §5-4, §5-5,
//       docs/specs/phase-4-agent-build-config.md §4-3, §4-4, §4-5.
package hub

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

//go:embed views/*.gohtml
var viewsFS embed.FS

// AgentConfigSender 는 Admin UI 가 폼 저장 직후 CONFIG_UPDATE 를 푸시하는 데 사용.
// 의존 역전을 위해 인터페이스로 노출 (테스트용 mock 주입 + 순환 의존 회피).
type AgentConfigSender interface {
	SendAgentConfig(ctx context.Context, agentID string, cfg protocol.AgentConfigData) error
}

// AdminUIHandler 는 /admin/* SSR 핸들러 묶음.
type AdminUIHandler struct {
	AgentStore       store.AgentStore
	PreviewStore     store.PreviewStore
	TokenGen         *token.Generator
	Logger           *slog.Logger
	Registry         *ConnRegistry // 옵션 — online 카운트 정확도 향상.
	AgentDownloadURL string        // 빈 값이면 소스 빌드 안내, 설정 시 다운로드 링크 표시.

	jobSender AgentConfigSender // Phase 4: CONFIG_UPDATE 푸시 (옵션).

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
	})
	return h
}

// SetRegistry 는 online 여부 카운트 보강용 ConnRegistry 를 주입한다 (옵션).
func (h *AdminUIHandler) SetRegistry(reg *ConnRegistry) { h.Registry = reg }

// SetJobSender 는 Phase 4: 폼 저장 시 즉시 CONFIG_UPDATE 푸시할 sender 를 주입한다.
// nil 이면 푸시는 시도되지 않으며 PushOutcome 은 "agent offline" 으로 보고된다.
func (h *AdminUIHandler) SetJobSender(s AgentConfigSender) { h.jobSender = s }

// Register 는 mux 에 SSR 라우트를 등록한다.
// 이미 AdminHandler/WebhookHandler 가 점유한 라우트와의 충돌을 피하기 위해
// 신규 SSR 라우트만 등록 + 기존 GET/POST /admin/agents 와 /admin/previews* 는
// AdminHandler / WebhookHandler 가 Accept-header 분기로 SSR 응답 가능하게 처리.
func (h *AdminUIHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", h.dashboard)
	mux.HandleFunc("GET /admin/agents/token", h.agentToken)
	mux.HandleFunc("POST /admin/agents/{id}/delete", h.agentDelete)
	mux.HandleFunc("POST /admin/previews/{id}/rebuild", h.previewRebuild)
	// Phase 4: agent detail (build config 폼) + 저장 POST.
	// {id} 가 "token" 등 다른 정적 prefix 와 충돌하지 않도록 GET /admin/agents/token 보다
	// 뒤에 등록되어도 net/http mux 는 longest-prefix + literal-match 우선이므로 안전.
	mux.HandleFunc("GET /admin/agents/{id}", h.agentDetail)
	mux.HandleFunc("POST /admin/agents/{id}/config", h.agentConfigSave)
}

// mustParsePages 는 layout.gohtml + 각 페이지를 합쳐 별도 *template.Template 로 만든다.
// 각 페이지는 자기 파일에 `{{define "content"}}...{{end}}` 를 가지므로 페이지마다 트리가 분리된다.
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
		t := template.Must(template.New(p).Parse(string(layoutBytes)))
		t = template.Must(t.Parse(string(pageBytes)))
		out[p] = t
	}
	return out
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
	Title  string
	Agents []agentRow
	Error  string
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
			agentsView{Title: "Agents", Error: err.Error()})
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
		agentsView{Title: "Agents", Agents: rows})
}

// labelsToString converts labels map → "k=v,k2=v2" deterministic string.
func labelsToString(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

// parseLabelsForm parses "k=v,k2=v2" form value → map.
func parseLabelsForm(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.IndexByte(part, '=')
		if idx <= 0 {
			continue
		}
		out[part[:idx]] = part[idx+1:]
	}
	return out
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
	Title           string
	Name            string
	Token           string
	HubHost         string // e.g. "localhost:3000" — Agent 실행 명령에 사용
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
		tokenView{Title: "Agent Setup", Name: name, Token: tok, HubHost: host,
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

// ---------- Previews list ----------

type previewRow struct {
	ID            string
	PrNumber      int
	RepoFullName  string
	Status        string
	Branch        string
	AgentLabel    string
	UpdatedString string
}

type previewsFilter struct {
	Repo   string
	Status string
}

type previewsView struct {
	Title    string
	Previews []previewRow
	Filter   previewsFilter
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
		rows = append(rows, previewRow{
			ID:            p.ID,
			PrNumber:      p.PrNumber,
			RepoFullName:  p.RepoFullName,
			Status:        p.Status,
			Branch:        p.Branch,
			AgentLabel:    agentLabel,
			UpdatedString: p.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	h.renderHTML(w, http.StatusOK, "previews.gohtml", previewsView{
		Title:    "Previews",
		Previews: rows,
		Filter:   previewsFilter{Repo: repoFilter, Status: statusFilter},
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
}

type previewDetailView struct {
	Title           string
	Preview         previewDetailRow
	AgentLine       string
	PublicURL       string
	Events          []eventRow
	RebuildEnabled  bool
	ConflictMessage string
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
	rows := make([]eventRow, 0, len(events))
	for _, e := range events {
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
	agentLine := "-"
	if p.AssignedAgentID != nil {
		agentLine = *p.AssignedAgentID
		if p.AgentHost != nil && p.AgentPort != nil {
			agentLine = fmt.Sprintf("%s (%s:%d)", *p.AssignedAgentID, *p.AgentHost, *p.AgentPort)
		}
	}
	publicURL := ""
	if p.PublicURL != nil {
		publicURL = *p.PublicURL
	}
	rebuildEnabled := p.Status == "done" || p.Status == "failed"
	conflict := r.URL.Query().Get("msg")
	view := previewDetailView{
		Title: fmt.Sprintf("PR #%d", p.PrNumber),
		Preview: previewDetailRow{
			ID:           p.ID,
			PrNumber:     p.PrNumber,
			RepoFullName: p.RepoFullName,
			CommitSha:    p.CommitSha,
			Branch:       p.Branch,
			Status:       p.Status,
		},
		AgentLine:       agentLine,
		PublicURL:       publicURL,
		Events:          rows,
		RebuildEnabled:  rebuildEnabled,
		ConflictMessage: conflict,
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
	fields := store.PreviewFields{
		AssignedAgentID: &emptyAssign,
		ContainerID:     &emptyContainer,
		AgentHost:       &emptyHost,
		AgentPort:       &zeroPort,
		PublicURL:       &emptyURL,
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
	if wantsJSON {
		writeJSON(w, http.StatusOK, map[string]any{"preview_id": id, "status": "queued"})
		return
	}
	http.Redirect(w, r, "/admin/previews/"+id, http.StatusSeeOther)
}

// ---------- Agent detail (Phase 4) ----------

// agentDetailView 는 GET /admin/agents/{id} 의 렌더 모델.
type agentDetailView struct {
	Title           string
	AgentID         string
	Name            string
	Status          string
	LabelsString    string
	LastSeenString  string
	CreatedString   string
	RunCommandsText string // raw textarea 내용. NULL/empty 일 때 빈 문자열.
	ContainerPort   int    // 0 이면 빈 input(=기본값).
	SavedFlash      bool
	PushOutcome     string // "delivered" | "agent offline" | "delivery failed"
	Error           string
}

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
	cmds, port, err := h.AgentStore.GetBuildConfig(r.Context(), id)
	if err != nil {
		h.Logger.Warn("admin_ui_agent_config_get_failed", "err", err.Error(), "agent_id", id)
		cmds = []string{}
		port = 0
	}
	view := agentDetailView{
		Title:           "Agent " + a.Name,
		AgentID:         a.ID,
		Name:            a.Name,
		Status:          a.Status,
		LabelsString:    labelsToString(a.Labels),
		CreatedString:   a.CreatedAt.UTC().Format(time.RFC3339),
		RunCommandsText: strings.Join(cmds, "\n"),
		ContainerPort:   port,
	}
	if a.LastSeenAt != nil {
		view.LastSeenString = a.LastSeenAt.UTC().Format(time.RFC3339)
	}
	// 플래시 메시지 (?msg=).
	switch r.URL.Query().Get("msg") {
	case "saved":
		view.SavedFlash = true
		view.PushOutcome = "delivered"
	case "saved_offline":
		view.SavedFlash = true
		view.PushOutcome = "skipped (agent offline)"
	case "saved_push_failed":
		view.SavedFlash = true
		view.PushOutcome = "delivery failed"
	}
	h.renderHTML(w, http.StatusOK, "agent_detail.gohtml", view)
}

// agentConfigSave 는 POST /admin/agents/{id}/config 핸들러 (Phase 4 §4-6).
//
//  1. 폼 파싱: run_commands raw text + container_port int.
//  2. Normalize: port 1..65535 외 / 비숫자 / 빈 값 → 0 (sentinel).
//  3. SaveBuildConfig 호출 (rawCommands "" → NULL, port 0 → NULL).
//  4. jobSender 가 있고 agent 가 connected 이면 CONFIG_UPDATE 푸시.
//  5. 303 redirect 로 detail 페이지에 ?msg=... 첨부.
func (h *AdminUIHandler) agentConfigSave(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "agent id required", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	// agent 존재 확인 (404 우선).
	if _, err := h.AgentStore.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("admin_ui_agent_config_save_get_failed", "err", err.Error(), "agent_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	rawCommands := r.FormValue("run_commands")
	port := normalizeContainerPort(r.FormValue("container_port"))

	if err := h.AgentStore.SaveBuildConfig(r.Context(), id, rawCommands, port); err != nil {
		h.Logger.Error("admin_ui_agent_config_save_failed", "err", err.Error(), "agent_id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.Logger.Info("admin_ui_agent_config_saved",
		"agent_id", id, "raw_commands_len", len(rawCommands), "port", port)

	// 와이어 페이로드 구성: split + trim + drop empty.
	cmdLines := splitAndCleanLines(rawCommands)
	cfg := protocol.AgentConfigData{RunCommands: cmdLines, ContainerPort: port}

	msg := "saved"
	if h.jobSender == nil {
		msg = "saved_offline"
	} else {
		pushCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := h.jobSender.SendAgentConfig(pushCtx, id, cfg)
		cancel()
		switch {
		case err == nil:
			h.Logger.Info("admin_ui_agent_config_push_delivered", "agent_id", id)
		case strings.Contains(err.Error(), "not connected"):
			msg = "saved_offline"
			h.Logger.Info("admin_ui_agent_config_push_skipped",
				"agent_id", id, "reason", "agent_not_connected")
		default:
			msg = "saved_push_failed"
			h.Logger.Warn("admin_ui_agent_config_push_failed",
				"agent_id", id, "err", err.Error())
		}
	}

	q := url.Values{}
	q.Set("msg", msg)
	http.Redirect(w, r, "/admin/agents/"+id+"?"+q.Encode(), http.StatusSeeOther)
}

// normalizeContainerPort 는 폼 입력을 [1, 65535] 범위 정수로 정규화.
// 범위 밖 / 빈 값 / 비숫자 → 0 (sentinel = "기본값 적용").
// Phase 4 결정 12.
func normalizeContainerPort(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	if n < 1 || n > 65535 {
		return 0
	}
	return n
}

// splitAndCleanLines 는 textarea 내용을 \r?\n 기준 split → trim → drop empty.
// Phase 4 결정 13. SaveBuildConfig 가 raw 그대로 저장하므로,
// 이 함수의 결과는 wire 페이로드 구성 시점에서만 사용된다.
func splitAndCleanLines(s string) []string {
	if s == "" {
		return []string{}
	}
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	parts := strings.Split(normalized, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
