// admin_ui_test.go: Phase 3 §6 F-S1-7 — TestAdminPreviewsListFilter (status/repo
// 필터가 HTML 응답에 row 포함/제외 정확히 결정).
package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// adminUIFakeAgentStore — 최소 구현 (List 만 사용).
type adminUIFakeAgentStore struct {
	mu     sync.Mutex
	items  map[string]*store.Agent
	bcRaw  map[string]string
	bcPort map[string]int
}

func newAdminUIAgentStore() *adminUIFakeAgentStore {
	return &adminUIFakeAgentStore{
		items:  map[string]*store.Agent{},
		bcRaw:  map[string]string{},
		bcPort: map[string]int{},
	}
}

func (m *adminUIFakeAgentStore) Create(_ context.Context, a store.Agent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items {
		if it.Name == a.Name {
			return store.ErrDuplicate
		}
	}
	cp := a
	m.items[a.ID] = &cp
	return nil
}
func (m *adminUIFakeAgentStore) GetByName(_ context.Context, n string) (*store.Agent, error) {
	return nil, store.ErrNotFound
}
func (m *adminUIFakeAgentStore) GetByID(_ context.Context, id string) (*store.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.items[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, store.ErrNotFound
}
func (m *adminUIFakeAgentStore) List(_ context.Context) ([]store.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Agent, 0, len(m.items))
	for _, a := range m.items {
		out = append(out, *a)
	}
	return out, nil
}
func (m *adminUIFakeAgentStore) UpdateStatus(_ context.Context, id, s string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.items[id]; ok {
		a.Status = s
	}
	return nil
}
func (m *adminUIFakeAgentStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.items, id)
	delete(m.bcRaw, id)
	delete(m.bcPort, id)
	return nil
}

// Phase 4 stub.
func (m *adminUIFakeAgentStore) GetBuildConfig(_ context.Context, id string) ([]string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return nil, 0, store.ErrNotFound
	}
	raw := m.bcRaw[id]
	port := m.bcPort[id]
	cmds := []string{}
	if raw != "" {
		for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			cmds = append(cmds, line)
		}
	}
	return cmds, port, nil
}

func (m *adminUIFakeAgentStore) SaveBuildConfig(_ context.Context, id, raw string, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return store.ErrNotFound
	}
	m.bcRaw[id] = raw
	m.bcPort[id] = port
	return nil
}

// adminUIFakePreviewStore — 최소 in-memory 구현 (ListAll/GetByID/UpdateStatus/ListByAgent/ListPreviewEvents).
type adminUIFakePreviewStore struct {
	mu     sync.Mutex
	rows   map[string]*store.Preview
	events map[string][]store.PreviewEvent
}

func newAdminUIPreviewStore() *adminUIFakePreviewStore {
	return &adminUIFakePreviewStore{
		rows:   map[string]*store.Preview{},
		events: map[string][]store.PreviewEvent{},
	}
}

func (f *adminUIFakePreviewStore) put(p store.Preview) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := p
	f.rows[p.ID] = &cp
}

func (f *adminUIFakePreviewStore) Upsert(_ context.Context, _ store.Preview) (bool, *store.Preview, error) {
	return false, nil, errors.New("not used")
}
func (f *adminUIFakePreviewStore) GetByID(_ context.Context, id string) (*store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.rows[id]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, store.ErrNotFound
}
func (f *adminUIFakePreviewStore) FindByHost(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *adminUIFakePreviewStore) ListQueuedForCandidates(_ context.Context) ([]store.Preview, error) {
	return nil, nil
}
func (f *adminUIFakePreviewStore) Claim(_ context.Context, _ []string, _ string, _ time.Time) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *adminUIFakePreviewStore) UpdateStatus(_ context.Context, id, _, toStatus, _ string, now time.Time, _ store.PreviewFields) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.rows[id]; ok {
		p.Status = toStatus
		p.UpdatedAt = now
		return nil
	}
	return store.ErrNotFound
}
func (f *adminUIFakePreviewStore) ListRunningByAgent(_ context.Context, _ string) ([]store.Preview, error) {
	return nil, nil
}
func (f *adminUIFakePreviewStore) ListStaleAssigned(_ context.Context, _ time.Time) ([]store.Preview, error) {
	return nil, nil
}
func (f *adminUIFakePreviewStore) ListByAgent(_ context.Context, agentID string, statuses []string) ([]store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	allowed := map[string]bool{}
	for _, s := range statuses {
		allowed[s] = true
	}
	out := []store.Preview{}
	for _, p := range f.rows {
		if p.AssignedAgentID != nil && *p.AssignedAgentID == agentID && allowed[p.Status] {
			out = append(out, *p)
		}
	}
	return out, nil
}
func (f *adminUIFakePreviewStore) ListAll(_ context.Context) ([]store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Preview, 0, len(f.rows))
	for _, p := range f.rows {
		out = append(out, *p)
	}
	return out, nil
}
func (f *adminUIFakePreviewStore) ListPreviewEvents(_ context.Context, previewID string, limit, offset int) ([]store.PreviewEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.events[previewID]
	if offset >= len(all) {
		return nil, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return append([]store.PreviewEvent(nil), all[offset:end]...), nil
}

func newAdminUIHandler() *AdminUIHandler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tg := token.NewGenerator(4)
	return NewAdminUIHandler(newAdminUIAgentStore(), newAdminUIPreviewStore(), tg, logger)
}

func TestAdminPreviewsListFilter(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	now := time.Now().UTC()
	ps.put(store.Preview{
		ID: "p-running", RepoFullName: "acme/web", PrNumber: 1, Status: "running",
		Branch: "feat/run", CreatedAt: now, UpdatedAt: now,
	})
	ps.put(store.Preview{
		ID: "p-done", RepoFullName: "acme/web", PrNumber: 2, Status: "done",
		Branch: "feat/done", CreatedAt: now, UpdatedAt: now,
	})

	// status=running 필터.
	req := httptest.NewRequest("GET", "/admin/previews?status=running", nil)
	rr := httptest.NewRecorder()
	ui.PreviewsList(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "feat/run") {
		t.Errorf("status=running filter missing feat/run row:\n%s", body)
	}
	if strings.Contains(body, "feat/done") {
		t.Errorf("status=running filter should NOT include feat/done")
	}

	// status=done 필터.
	req2 := httptest.NewRequest("GET", "/admin/previews?status=done", nil)
	rr2 := httptest.NewRecorder()
	ui.PreviewsList(rr2, req2)
	body2 := rr2.Body.String()
	if !strings.Contains(body2, "feat/done") {
		t.Errorf("status=done filter missing feat/done row:\n%s", body2)
	}
	if strings.Contains(body2, "feat/run") {
		t.Errorf("status=done filter should NOT include feat/run")
	}

	// repo=other 필터 — 매칭 없음.
	req3 := httptest.NewRequest("GET", "/admin/previews?repo=zzz/none", nil)
	rr3 := httptest.NewRecorder()
	ui.PreviewsList(rr3, req3)
	body3 := rr3.Body.String()
	if strings.Contains(body3, "feat/run") || strings.Contains(body3, "feat/done") {
		t.Errorf("repo=zzz/none should match nothing:\n%s", body3)
	}
	if !strings.Contains(body3, "No previews match") {
		t.Errorf("expected empty-state message; got:\n%s", body3)
	}
}

func TestAdminDashboardRenders(t *testing.T) {
	ui := newAdminUIHandler()
	req := httptest.NewRequest("GET", "/admin", nil)
	rr := httptest.NewRecorder()
	ui.dashboard(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	body := rr.Body.String()
	wantLines := []string{
		"<title>Dashboard — Preview Hub</title>",
		"Total Agents",
		"Online Agents",
		"Status breakdown",
		"https://cdn.jsdelivr.net/npm/@picocss/pico@2/css/pico.min.css",
	}
	for _, w := range wantLines {
		if !strings.Contains(body, w) {
			t.Errorf("dashboard missing %q:\n%s", w, body)
		}
	}
}

func TestAdminAgentsListEmpty(t *testing.T) {
	ui := newAdminUIHandler()
	req := httptest.NewRequest("GET", "/admin/agents", nil)
	rr := httptest.NewRecorder()
	ui.AgentsList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "No agents yet") {
		t.Errorf("expected 'No agents yet' placeholder:\n%s", body)
	}
	if !strings.Contains(body, "Add Agent") {
		t.Errorf("expected 'Add Agent' form heading:\n%s", body)
	}
}

func TestAdminAgentsCreateFormRedirect(t *testing.T) {
	ui := newAdminUIHandler()
	req := httptest.NewRequest("POST", "/admin/agents", strings.NewReader("name=home&labels=env=home"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.CreateAgentForm(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/agents/token?") {
		t.Errorf("Location=%q want prefix /admin/agents/token?", loc)
	}
	if !strings.Contains(loc, "name=home") || !strings.Contains(loc, "t=") {
		t.Errorf("Location should contain name + t params: %s", loc)
	}
}

func TestAdminAgentsCreateFormInvalidName(t *testing.T) {
	ui := newAdminUIHandler()
	req := httptest.NewRequest("POST", "/admin/agents", strings.NewReader("name=&labels="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.CreateAgentForm(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rr.Code)
	}
}

func TestAdminPreviewRebuildConflict(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	now := time.Now().UTC()
	ps.put(store.Preview{
		ID: "p-running", RepoFullName: "x/y", PrNumber: 1, Status: "running",
		CreatedAt: now, UpdatedAt: now,
	})
	// JSON Accept → 409.
	req := httptest.NewRequest("POST", "/admin/previews/p-running/rebuild", nil)
	req.SetPathValue("id", "p-running")
	req.Header.Set("Accept", "application/json")
	rr := httptest.NewRecorder()
	ui.previewRebuild(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 (in-flight)", rr.Code)
	}
}

func TestAdminPreviewRebuildSuccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	now := time.Now().UTC()
	ps.put(store.Preview{
		ID: "p-done", RepoFullName: "x/y", PrNumber: 2, Status: "done",
		CreatedAt: now, UpdatedAt: now,
	})
	req := httptest.NewRequest("POST", "/admin/previews/p-done/rebuild", nil)
	req.SetPathValue("id", "p-done")
	rr := httptest.NewRecorder()
	ui.previewRebuild(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status=%d want 303", rr.Code)
	}
	if got := ps.rows["p-done"].Status; got != "queued" {
		t.Errorf("status=%s want queued after rebuild", got)
	}
}

func TestAdminAgentDeleteRedirect(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{ID: "a1", Name: "ag", Status: "offline", CreatedAt: time.Now()})
	req := httptest.NewRequest("POST", "/admin/agents/a1/delete", nil)
	req.SetPathValue("id", "a1")
	rr := httptest.NewRecorder()
	ui.agentDelete(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status=%d want 303", rr.Code)
	}
	if rr.Header().Get("Location") != "/admin/agents" {
		t.Errorf("Location=%q want /admin/agents", rr.Header().Get("Location"))
	}
}

// fakeAgentConfigSender — admin UI 의 jobSender mock.
type fakeAgentConfigSender struct {
	calls   int
	agentID string
	cfg     protocol.AgentConfigData
	err     error
}

func (f *fakeAgentConfigSender) SendAgentConfig(_ context.Context, agentID string, cfg protocol.AgentConfigData) error {
	f.calls++
	f.agentID = agentID
	f.cfg = cfg
	return f.err
}

// TestAdminAgentDetailRenders — F-8 / F-10: agent detail 페이지가 200 OK + 폼 + 환경변수 표.
func TestAdminAgentDetailRenders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "agent-home", Status: "online",
		Labels: map[string]string{"env": "home"}, CreatedAt: time.Now().UTC(),
	})

	req := httptest.NewRequest("GET", "/admin/agents/a1", nil)
	req.SetPathValue("id", "a1")
	rr := httptest.NewRecorder()
	ui.agentDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"agent-home",
		`name="run_commands"`,
		`name="container_port"`,
		`placeholder="80"`,
		"$PREVIEW_ID",
		"$PREVIEW_SHA",
		"$PREVIEW_BRANCH",
		"$PORT",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body", want)
		}
	}
	// $PREVIEW_IMAGE 는 docker 가정 제거와 함께 사라져야 한다.
	if strings.Contains(body, "$PREVIEW_IMAGE") {
		t.Errorf("body should not contain $PREVIEW_IMAGE anymore:\n%s", body)
	}
}

// TestAdminAgentDetail404 — F-9: 존재하지 않는 agent → 404.
func TestAdminAgentDetail404(t *testing.T) {
	ui := newAdminUIHandler()
	req := httptest.NewRequest("GET", "/admin/agents/missing", nil)
	req.SetPathValue("id", "missing")
	rr := httptest.NewRecorder()
	ui.agentDetail(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rr.Code)
	}
}

// TestAdminAgentDetailSavedValues — F-11: 저장된 값이 textarea/input 에 표시된다.
func TestAdminAgentDetailSavedValues(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "ag", Status: "offline", CreatedAt: time.Now().UTC(),
	})
	_ = as.SaveBuildConfig(context.Background(), "a1", "npm ci\nnpm run build", 3000)

	req := httptest.NewRequest("GET", "/admin/agents/a1", nil)
	req.SetPathValue("id", "a1")
	rr := httptest.NewRecorder()
	ui.agentDetail(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "npm ci") || !strings.Contains(body, "npm run build") {
		t.Errorf("textarea missing saved commands. body:\n%s", body)
	}
	if !strings.Contains(body, `value="3000"`) {
		t.Errorf("port input missing value=3000. body:\n%s", body)
	}
}

// TestAdminAgentConfigSaveRedirect — F-12 / F-13 / F-22: 폼 저장 → 303 + DB 갱신.
// jobSender nil → ?msg=saved_offline.
func TestAdminAgentConfigSaveRedirect(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "ag", Status: "offline", CreatedAt: time.Now().UTC(),
	})

	form := strings.NewReader("run_commands=npm+ci%0Anpm+run+build&container_port=3000")
	req := httptest.NewRequest("POST", "/admin/agents/a1/config", form)
	req.SetPathValue("id", "a1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.agentConfigSave(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/agents/a1?") {
		t.Errorf("Location=%q want prefix /admin/agents/a1?", loc)
	}
	// jobSender 미주입 → saved_offline.
	if !strings.Contains(loc, "saved_offline") {
		t.Errorf("Location=%q want msg=saved_offline (no jobSender)", loc)
	}

	// DB 갱신 검증.
	cmds, port, err := as.GetBuildConfig(context.Background(), "a1")
	if err != nil {
		t.Fatalf("GetBuildConfig: %v", err)
	}
	if len(cmds) != 2 || cmds[0] != "npm ci" || cmds[1] != "npm run build" {
		t.Errorf("cmds=%v want [npm ci, npm run build]", cmds)
	}
	if port != 3000 {
		t.Errorf("port=%d want 3000", port)
	}
}

// TestAdminAgentConfigSavePushDelivered — F-21: jobSender 가 호출되고 ?msg=saved.
func TestAdminAgentConfigSavePushDelivered(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)
	sender := &fakeAgentConfigSender{}
	ui.SetJobSender(sender)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "ag", Status: "online", CreatedAt: time.Now().UTC(),
	})

	form := strings.NewReader("run_commands=echo+hi&container_port=8080")
	req := httptest.NewRequest("POST", "/admin/agents/a1/config", form)
	req.SetPathValue("id", "a1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.agentConfigSave(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303", rr.Code)
	}
	if sender.calls != 1 {
		t.Fatalf("jobSender calls=%d want 1", sender.calls)
	}
	if sender.agentID != "a1" {
		t.Errorf("agentID=%q want a1", sender.agentID)
	}
	if len(sender.cfg.RunCommands) != 1 || sender.cfg.RunCommands[0] != "echo hi" {
		t.Errorf("cfg.RunCommands=%v", sender.cfg.RunCommands)
	}
	if sender.cfg.ContainerPort != 8080 {
		t.Errorf("cfg.ContainerPort=%d want 8080", sender.cfg.ContainerPort)
	}
	if !strings.Contains(rr.Header().Get("Location"), "msg=saved") {
		t.Errorf("Location=%q want msg=saved", rr.Header().Get("Location"))
	}
}

// TestAdminAgentConfigSavePushOffline — F-22: jobSender 가 not-connected 에러 → ?msg=saved_offline.
func TestAdminAgentConfigSavePushOffline(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)
	sender := &fakeAgentConfigSender{err: fmt.Errorf("ws_job_sender: agent a1 not connected")}
	ui.SetJobSender(sender)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "ag", Status: "offline", CreatedAt: time.Now().UTC(),
	})
	form := strings.NewReader("run_commands=&container_port=")
	req := httptest.NewRequest("POST", "/admin/agents/a1/config", form)
	req.SetPathValue("id", "a1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.agentConfigSave(rr, req)
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "saved_offline") {
		t.Errorf("Location=%q want saved_offline", loc)
	}
}

// TestAdminAgentConfigSavePushFailed — F-23: jobSender 가 일반 에러 → ?msg=saved_push_failed.
func TestAdminAgentConfigSavePushFailed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)
	sender := &fakeAgentConfigSender{err: fmt.Errorf("write tcp boom")}
	ui.SetJobSender(sender)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "ag", Status: "online", CreatedAt: time.Now().UTC(),
	})
	form := strings.NewReader("run_commands=:&container_port=80")
	req := httptest.NewRequest("POST", "/admin/agents/a1/config", form)
	req.SetPathValue("id", "a1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.agentConfigSave(rr, req)
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "saved_push_failed") {
		t.Errorf("Location=%q want saved_push_failed", loc)
	}
}

// TestNormalizeContainerPort — F-15: 1..65535 범위 외/비숫자 → 0.
func TestNormalizeContainerPort(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 0},
		{"-1", 0},
		{"0", 0},
		{"1", 1},
		{"80", 80},
		{"65535", 65535},
		{"65536", 0},
		{"  3000  ", 3000},
	}
	for _, tc := range cases {
		if got := normalizeContainerPort(tc.in); got != tc.want {
			t.Errorf("normalizeContainerPort(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}

// TestSplitAndCleanLines — Phase 4 결정 13.
func TestSplitAndCleanLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"a", []string{"a"}},
		{"a\nb", []string{"a", "b"}},
		{"a\n\nb\n", []string{"a", "b"}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"  a  \n  b  ", []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := splitAndCleanLines(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitAndCleanLines(%q) len=%d want %d (got %v)", tc.in, len(got), len(tc.want), got)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitAndCleanLines(%q)[%d]=%q want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// TestAdminAgentsListHasDetailLink — F-16: agents 목록 행 Name 이 detail 링크.
func TestAdminAgentsListHasDetailLink(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)
	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "agent-home", Status: "offline", CreatedAt: time.Now().UTC(),
	})
	req := httptest.NewRequest("GET", "/admin/agents", nil)
	rr := httptest.NewRecorder()
	ui.AgentsList(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, `href="/admin/agents/a1"`) {
		t.Errorf("expected detail link href=/admin/agents/a1 in body:\n%s", body)
	}
}
