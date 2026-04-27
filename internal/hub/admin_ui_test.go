// admin_ui_test.go: Phase 3 §6 F-S1-7 — TestAdminPreviewsListFilter (status/repo
// 필터가 HTML 응답에 row 포함/제외 정확히 결정).
package hub

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/store"
)

// adminUIFakeAgentStore — 최소 구현 (List 만 사용).
type adminUIFakeAgentStore struct {
	mu    sync.Mutex
	items map[string]*store.Agent
}

func newAdminUIAgentStore() *adminUIFakeAgentStore {
	return &adminUIFakeAgentStore{
		items: map[string]*store.Agent{},
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
	return nil
}

// adminUIFakePreviewStore — 최소 in-memory 구현 (ListAll/GetByID/UpdateStatus/ListByAgent/ListPreviewEvents).
//
// Phase 11 추가:
//   - lastUpdateFields: UpdateStatus 의 마지막 호출 PreviewFields 를 capture (F-9 검증용).
//   - lastUpdateTo: UpdateStatus 의 마지막 toStatus (분기 진입 검증용).
//   - updateStatusErr: 다음 UpdateStatus 호출이 반환할 에러 강제 주입 (ErrStaleState 시뮬용, F-6).
//   - updateStatusCalls: UpdateStatus 호출 횟수 카운터.
type adminUIFakePreviewStore struct {
	mu                sync.Mutex
	rows              map[string]*store.Preview
	events            map[string][]store.PreviewEvent
	lastUpdateFields  store.PreviewFields
	lastUpdateTo      string
	updateStatusErr   error
	updateStatusCalls int
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

// Upsert: Phase 9 자연키 (repo, sha) 기반 in-memory 구현 + IsAdhoc 보존.
// sha 가 빈 문자열이면 항상 신규 INSERT (NULL UNIQUE 미발동 시뮬레이션).
func (f *adminUIFakePreviewStore) Upsert(_ context.Context, p store.Preview) (bool, *store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.CommitSha != "" {
		for _, existing := range f.rows {
			if existing.RepoFullName == p.RepoFullName && existing.CommitSha == p.CommitSha {
				prev := *existing
				existing.Branch = p.Branch
				existing.Labels = p.Labels
				existing.RepoCloneURL = p.RepoCloneURL
				existing.UpdatedAt = p.UpdatedAt
				return false, &prev, nil
			}
		}
	}
	p.Status = "queued"
	cp := p
	f.rows[p.ID] = &cp
	return true, nil, nil
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
func (f *adminUIFakePreviewStore) UpdateStatus(_ context.Context, id, _, toStatus, _ string, now time.Time, fields store.PreviewFields) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateStatusCalls++
	f.lastUpdateFields = fields
	f.lastUpdateTo = toStatus
	if f.updateStatusErr != nil {
		err := f.updateStatusErr
		f.updateStatusErr = nil // one-shot
		return err
	}
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

// Phase 9: GetActiveByRepoAndPR — admin UI 테스트는 webhook 분기를 다루지 않으므로 stub.
func (f *adminUIFakePreviewStore) GetActiveByRepoAndPR(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}

// Phase 11: FindAdhocByBranch — testBuildSubmit dedup 분기 테스트가 자체 fake 로
// override 하지 않는 한 기본 stub 은 ErrNotFound 반환 (= dedup miss → 기존 신규 INSERT 경로).
func (f *adminUIFakePreviewStore) FindAdhocByBranch(_ context.Context, repoFullName, branch string) (*store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *store.Preview
	for _, p := range f.rows {
		if !p.IsAdhoc || p.RepoFullName != repoFullName || p.Branch != branch {
			continue
		}
		if best == nil || p.CreatedAt.After(best.CreatedAt) {
			cp := *p
			best = &cp
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return best, nil
}

// Phase 10: ListRepos stub — admin UI 테스트는 /admin/repos 인덱스 union 동작을 별도로 검증.
func (f *adminUIFakePreviewStore) ListRepos(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]struct{}{}
	out := []string{}
	for _, p := range f.rows {
		if p.RepoFullName == "" {
			continue
		}
		if _, ok := seen[p.RepoFullName]; ok {
			continue
		}
		seen[p.RepoFullName] = struct{}{}
		out = append(out, p.RepoFullName)
	}
	sort.Strings(out)
	return out, nil
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

// TestAdminAgentDetailRenders — agent detail 페이지가 200 OK + 기본 메타데이터 표시.
func TestAdminAgentDetailRenders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "agent-home", Status: "online",
		Labels: []string{"home"}, CreatedAt: time.Now().UTC(),
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
		"home",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body", want)
		}
	}
}

// TestAdminAgentDetail404 — 존재하지 않는 agent → 404.
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

// TestAdminAgentsListHasDetailLink — agents 목록 행 Name 이 detail 링크.
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

// TestAdminTestBuildSubmitDifferentBranchesCreateRowsAllAdhoc (F-10):
// Admin Test Build 두 번, 같은 repo + 다른 branch → row 2개, 모두 IsAdhoc=true.
//
// Phase 11 변경: 기존 동명 테스트는 "같은 branch + 다른 sha" 였으나, Phase 11 의
// (repo, branch) dedup 으로는 같은 branch 두 번째 호출이 dedup HIT 으로 처리되므로
// 새 row 가 생기지 않는다. 다른 branch 로 분기해 신규 row 가 생기는 케이스를 검증.
func TestAdminTestBuildSubmitDifferentBranchesCreateRowsAllAdhoc(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "agent-1", Status: "online", CreatedAt: time.Now().UTC(),
	})

	for i, br := range []string{"main", "feature/x"} {
		form := "repo_clone_url=https://github.com/acme/web.git&branch=" + br + "&commit_sha=sha-" + br
		req := httptest.NewRequest("POST", "/admin/agents/a1/test-build", strings.NewReader(form))
		req.SetPathValue("id", "a1")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ui.testBuildSubmit(rr, req)
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("submit[%d] status=%d want 303 (branch=%s)", i, rr.Code, br)
		}
	}

	all, _ := ps.ListAll(context.Background())
	if len(all) != 2 {
		t.Fatalf("rows=%d want 2 (different branch → distinct rows)", len(all))
	}
	for _, p := range all {
		if !p.IsAdhoc {
			t.Errorf("row %s IsAdhoc=false want true (Test Build)", p.ID)
		}
		if p.PrNumber != 0 {
			t.Errorf("row %s PrNumber=%d want 0", p.ID, p.PrNumber)
		}
	}
}

// seedAdhocRow 는 testBuildSubmit dedup 테스트의 사전 row 시딩 헬퍼.
// status 외 필드는 합리적인 기본값으로 세팅한다.
func seedAdhocRow(ps *adminUIFakePreviewStore, id, repo, branch, status string, createdAt time.Time) {
	host := "10.0.0.1"
	port := 5000
	cid := "old-container"
	urls := `{"web":"http://old"}`
	ps.put(store.Preview{
		ID: id, RepoFullName: repo, Branch: branch,
		PrNumber: 0, CommitSha: "old-sha",
		Status: status, IsAdhoc: true,
		AgentHost: &host, AgentPort: &port, ContainerID: &cid,
		PreviewURLs: urls, Labels: []string{},
		CreatedAt: createdAt, UpdatedAt: createdAt,
	})
}

// TestTestBuildSubmit_DedupActiveNoop (F-8, F-11): active 상태 row 존재 →
// redirect, DB 변경 없음(상태/UpdateStatus 호출 0).
func TestTestBuildSubmit_DedupActiveNoop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "agent-1", Status: "online", CreatedAt: time.Now().UTC(),
	})
	now := time.Now().UTC()
	seedAdhocRow(ps, "p-active", "acme/web", "main", "running", now)

	form := "repo_clone_url=https://github.com/acme/web.git&branch=main&commit_sha=new-sha"
	req := httptest.NewRequest("POST", "/admin/agents/a1/test-build", strings.NewReader(form))
	req.SetPathValue("id", "a1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.testBuildSubmit(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/admin/previews/p-active" {
		t.Errorf("Location=%q want /admin/previews/p-active", loc)
	}
	all, _ := ps.ListAll(context.Background())
	if len(all) != 1 {
		t.Errorf("rows=%d want 1 (no new row on dedup hit + active)", len(all))
	}
	if got := ps.rows["p-active"].Status; got != "running" {
		t.Errorf("status=%q want running (no change on active dedup hit)", got)
	}
	if ps.updateStatusCalls != 0 {
		t.Errorf("UpdateStatus called %d times; want 0 (active dedup must be noop)", ps.updateStatusCalls)
	}
}

// TestTestBuildSubmit_DedupTerminalRequeue (F-9, F-11): done 상태 row 존재 →
// UpdateStatus(→queued) + 6 필드 명시 reset + CommitSha=nil + redirect.
func TestTestBuildSubmit_DedupTerminalRequeue(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "agent-1", Status: "online", CreatedAt: time.Now().UTC(),
	})
	now := time.Now().UTC()
	seedAdhocRow(ps, "p-done", "acme/web", "main", "done", now)

	form := "repo_clone_url=https://github.com/acme/web.git&branch=main&commit_sha=new-sha"
	req := httptest.NewRequest("POST", "/admin/agents/a1/test-build", strings.NewReader(form))
	req.SetPathValue("id", "a1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.testBuildSubmit(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/admin/previews/p-done" {
		t.Errorf("Location=%q want /admin/previews/p-done", loc)
	}
	all, _ := ps.ListAll(context.Background())
	if len(all) != 1 {
		t.Errorf("rows=%d want 1 (no new row on dedup hit + terminal)", len(all))
	}
	if got := ps.rows["p-done"].Status; got != "queued" {
		t.Errorf("status=%q want queued after re-queue", got)
	}
	if ps.updateStatusCalls != 1 {
		t.Errorf("UpdateStatus called %d times; want 1", ps.updateStatusCalls)
	}
	if ps.lastUpdateTo != "queued" {
		t.Errorf("toStatus=%q want queued", ps.lastUpdateTo)
	}

	// F-9: 6 필드가 ptr-to-zero 로 reset, CommitSha 는 nil.
	f := ps.lastUpdateFields
	if f.CommitSha != nil {
		t.Errorf("CommitSha=%v want nil (결정 4)", f.CommitSha)
	}
	checks := []struct {
		name string
		cond bool
	}{
		{"ContainerID", f.ContainerID != nil && *f.ContainerID == ""},
		{"AgentHost", f.AgentHost != nil && *f.AgentHost == ""},
		{"AgentPort", f.AgentPort != nil && *f.AgentPort == 0},
		{"PreviewURLs", f.PreviewURLs != nil && *f.PreviewURLs == ""},
		{"ErrorMessage", f.ErrorMessage != nil && *f.ErrorMessage == ""},
		{"AssignedAgentID", f.AssignedAgentID != nil && *f.AssignedAgentID == ""},
	}
	for _, c := range checks {
		if !c.cond {
			t.Errorf("PreviewFields.%s not reset to ptr(zero)", c.name)
		}
	}
}

// TestTestBuildSubmit_DedupTerminalErrStaleState (F-6): done 상태이나 UpdateStatus 가
// ErrStaleState 반환 → 303 redirect 유지, dispatcher 트리거 생략(다른 경로에 일임).
//
// 검증은 행동적 효과로 갈음한다: ErrStaleState 시 fake store 의 row 상태가 변하지 않고
// (one-shot err 가 fake 본체 mutation 전에 return 되도록 구현됨) 응답이 303 으로 떨어진다.
func TestTestBuildSubmit_DedupTerminalErrStaleState(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "agent-1", Status: "online", CreatedAt: time.Now().UTC(),
	})
	now := time.Now().UTC()
	seedAdhocRow(ps, "p-done", "acme/web", "main", "done", now)
	// 다음 UpdateStatus 호출이 ErrStaleState 를 반환하도록 강제.
	ps.updateStatusErr = store.ErrStaleState

	form := "repo_clone_url=https://github.com/acme/web.git&branch=main&commit_sha=new-sha"
	req := httptest.NewRequest("POST", "/admin/agents/a1/test-build", strings.NewReader(form))
	req.SetPathValue("id", "a1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.testBuildSubmit(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303 (ErrStaleState should not surface as 5xx)", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/admin/previews/p-done" {
		t.Errorf("Location=%q want /admin/previews/p-done", loc)
	}
	if ps.updateStatusCalls != 1 {
		t.Errorf("UpdateStatus called %d times; want 1 (one attempt then redirect)", ps.updateStatusCalls)
	}
	if got := ps.rows["p-done"].Status; got != "done" {
		t.Errorf("status=%q want done preserved (ErrStaleState leaves row untouched)", got)
	}
}

// TestTestBuildSubmit_NewPreview (F-7): FindAdhocByBranch ErrNotFound (빈 DB) →
// 신규 INSERT + redirect. 기존 동작 유지.
func TestTestBuildSubmit_NewPreview(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	as := newAdminUIAgentStore()
	ps := newAdminUIPreviewStore()
	tg := token.NewGenerator(4)
	ui := NewAdminUIHandler(as, ps, tg, logger)

	_ = as.Create(context.Background(), store.Agent{
		ID: "a1", Name: "agent-1", Status: "online", CreatedAt: time.Now().UTC(),
	})

	form := "repo_clone_url=https://github.com/acme/web.git&branch=main&commit_sha=fresh-sha"
	req := httptest.NewRequest("POST", "/admin/agents/a1/test-build", strings.NewReader(form))
	req.SetPathValue("id", "a1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	ui.testBuildSubmit(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status=%d want 303", rr.Code)
	}
	all, _ := ps.ListAll(context.Background())
	if len(all) != 1 {
		t.Fatalf("rows=%d want 1 (new INSERT)", len(all))
	}
	got := all[0]
	if !got.IsAdhoc {
		t.Errorf("IsAdhoc=false want true")
	}
	if got.RepoFullName != "acme/web" || got.Branch != "main" || got.CommitSha != "fresh-sha" {
		t.Errorf("row=%+v not as expected", got)
	}
	if !strings.HasPrefix(rr.Header().Get("Location"), "/admin/previews/") {
		t.Errorf("Location=%q want /admin/previews/...", rr.Header().Get("Location"))
	}
	if ps.updateStatusCalls != 0 {
		t.Errorf("UpdateStatus called %d times; want 0 (new preview path)", ps.updateStatusCalls)
	}
}
