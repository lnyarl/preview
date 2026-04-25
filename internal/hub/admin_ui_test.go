// admin_ui_test.go: Phase 3 §6 F-S1-7 — TestAdminPreviewsListFilter (status/repo
// 필터가 HTML 응답에 row 포함/제외 정확히 결정).
package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/store"
)

// adminUIFakeAgentStore — 최소 구현 (List 만 사용).
type adminUIFakeAgentStore struct{ mu sync.Mutex; items map[string]*store.Agent }

func newAdminUIAgentStore() *adminUIFakeAgentStore {
	return &adminUIFakeAgentStore{items: map[string]*store.Agent{}}
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
