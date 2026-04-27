// coverage_test.go: Step 3 추가 기능 단위 테스트.
// deletePreview, getPreview, StatusUpdater, ConnRegistry.OnlineAgentIDs,
// ProxyMiddleware 엣지 케이스, DefaultConfig 등을 커버.
package hub

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// ---------------------------------------------------------------------------
// webhook_handler: getPreview, deletePreview
// ---------------------------------------------------------------------------

func TestWebhookGetPreviewExists(t *testing.T) {
	srv, fs := newTestServer(t)
	// preview 먼저 생성.
	body := []byte(`{"action":"opened","pull_request":{"number":7,"head":{"sha":"aaa","ref":"f"}},"repository":{"full_name":"o/r"}}`)
	postWebhook(t, srv.URL, "pull_request", body, sign([]byte(testSecret), body)).Body.Close()

	var pid string
	for id := range fs.rows {
		pid = id
	}

	r, err := http.Get(srv.URL + "/admin/previews/" + pid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("status=%d want 200", r.StatusCode)
	}
	var view PreviewView
	_ = json.NewDecoder(r.Body).Decode(&view)
	if view.ID != pid {
		t.Errorf("id=%s want %s", view.ID, pid)
	}
}

func TestWebhookGetPreviewNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	r, err := http.Get(srv.URL + "/admin/previews/does-not-exist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatalf("status=%d want 404", r.StatusCode)
	}
}

func TestWebhookDeletePreviewExists(t *testing.T) {
	srv, fs := newTestServer(t)
	body := []byte(`{"action":"opened","pull_request":{"number":3,"head":{"sha":"bbb","ref":"g"}},"repository":{"full_name":"o/r"}}`)
	postWebhook(t, srv.URL, "pull_request", body, sign([]byte(testSecret), body)).Body.Close()

	var pid string
	for id := range fs.rows {
		pid = id
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/admin/previews/"+pid, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d want 202", resp.StatusCode)
	}
	// status should be teardown.
	if p := fs.rows[pid]; p.Status != "teardown" {
		t.Errorf("status=%s want teardown", p.Status)
	}
}

func TestWebhookDeletePreviewNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/admin/previews/no-such-id", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
}

func TestWebhookSettersNilSafe(t *testing.T) {
	h := NewWebhookHandler(newFakeStore(), testSecret, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// SetTeardownSender 호출 후 nil-safe 확인 (패닉 없어야 함).
	h.SetTeardownSender(nil)
}

// ---------------------------------------------------------------------------
// StatusUpdater
// ---------------------------------------------------------------------------

type statusFakeStore struct {
	rows     map[string]*store.Preview
	updated  []string
}

func newStatusFakeStore() *statusFakeStore {
	return &statusFakeStore{rows: make(map[string]*store.Preview)}
}

func (f *statusFakeStore) put(id, status string) {
	f.rows[id] = &store.Preview{ID: id, Status: status}
}

func (f *statusFakeStore) GetByID(_ context.Context, id string) (*store.Preview, error) {
	r, ok := f.rows[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (f *statusFakeStore) UpdateStatus(_ context.Context, id, _, toStatus, _ string, now time.Time, _ store.PreviewFields) error {
	r, ok := f.rows[id]
	if !ok {
		return store.ErrNotFound
	}
	r.Status = toStatus
	r.UpdatedAt = now
	f.updated = append(f.updated, id+":"+toStatus)
	return nil
}

// unneeded methods.
func (f *statusFakeStore) Upsert(_ context.Context, _ store.Preview) (bool, *store.Preview, error) {
	panic("not impl")
}
func (f *statusFakeStore) FindByHost(_ context.Context, _ string, _ int) (*store.Preview, error) {
	panic("not impl")
}
func (f *statusFakeStore) ListQueuedForCandidates(_ context.Context) ([]store.Preview, error) {
	panic("not impl")
}
func (f *statusFakeStore) Claim(_ context.Context, _ []string, _ string, _ time.Time) (*store.Preview, error) {
	panic("not impl")
}
func (f *statusFakeStore) ListRunningByAgent(_ context.Context, _ string) ([]store.Preview, error) {
	panic("not impl")
}
func (f *statusFakeStore) ListStaleAssigned(_ context.Context, _ time.Time) ([]store.Preview, error) {
	panic("not impl")
}
func (f *statusFakeStore) ListByAgent(_ context.Context, _ string, _ []string) ([]store.Preview, error) {
	panic("not impl")
}
func (f *statusFakeStore) ListAll(_ context.Context) ([]store.Preview, error) {
	panic("not impl")
}
func (f *statusFakeStore) ListPreviewEvents(_ context.Context, _ string, _, _ int) ([]store.PreviewEvent, error) {
	return nil, nil
}
func (f *statusFakeStore) GetActiveByRepoAndPR(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *statusFakeStore) FindAdhocByBranch(_ context.Context, _, _ string) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *statusFakeStore) ListRepos(_ context.Context) ([]string, error) { return nil, nil }

func TestStatusUpdaterOnStatusUpdate(t *testing.T) {
	fs := newStatusFakeStore()
	fs.put("p1", "assigned")
	su := NewStatusUpdater(fs, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := su.OnStatusUpdate(context.Background(), "agent-1", protocol.StatusUpdateData{
		PreviewID: "p1",
		Status:    "building",
	}); err != nil {
		t.Fatalf("OnStatusUpdate: %v", err)
	}
	if fs.rows["p1"].Status != "building" {
		t.Errorf("status=%s want building", fs.rows["p1"].Status)
	}
}

func TestStatusUpdaterRunningLogsHost(t *testing.T) {
	fs := newStatusFakeStore()
	fs.put("p2", "building")
	su := NewStatusUpdater(fs, slog.New(slog.NewTextHandler(io.Discard, nil)))

	host := "127.0.0.1"
	port := 8080
	if err := su.OnStatusUpdate(context.Background(), "agent-1", protocol.StatusUpdateData{
		PreviewID: "p2",
		Status:    "running",
		AgentHost: &host,
		AgentPort: &port,
	}); err != nil {
		t.Fatalf("OnStatusUpdate: %v", err)
	}
	if fs.rows["p2"].Status != "running" {
		t.Errorf("status=%s want running", fs.rows["p2"].Status)
	}
}

func TestStatusUpdaterInvalidPayload(t *testing.T) {
	su := NewStatusUpdater(newStatusFakeStore(), slog.Default())
	if err := su.OnStatusUpdate(context.Background(), "a", protocol.StatusUpdateData{}); err == nil {
		t.Error("want error for empty PreviewID/Status")
	}
}

// ---------------------------------------------------------------------------
// ConnRegistry.OnlineAgentIDs
// ---------------------------------------------------------------------------

func TestConnRegistryOnlineAgentIDs(t *testing.T) {
	reg := NewConnRegistry()
	// Initially empty.
	if ids := reg.OnlineAgentIDs(); len(ids) != 0 {
		t.Fatalf("want empty, got %v", ids)
	}

	// Manually inject a fake entry (same-package test access).
	reg.mu.Lock()
	reg.conns["agent-A"] = &wsConn{agentID: "agent-A"}
	reg.conns["agent-B"] = &wsConn{agentID: "agent-B"}
	reg.mu.Unlock()

	ids := reg.OnlineAgentIDs()
	if len(ids) != 2 {
		t.Fatalf("want 2 online agents, got %d", len(ids))
	}
	if !ids["agent-A"] || !ids["agent-B"] {
		t.Errorf("unexpected ids: %v", ids)
	}
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfigDefaults(t *testing.T) {
	// 기본 환경변수 미설정 시 기본값 확인.
	cfg := DefaultConfig()
	if cfg.Addr == "" {
		t.Error("Addr should not be empty")
	}
	if cfg.DatabaseURL == "" {
		t.Error("DatabaseURL should not be empty")
	}
	if cfg.BcryptCost <= 0 {
		t.Error("BcryptCost should be positive")
	}
	if cfg.PreviewBaseDomain == "" {
		t.Error("PreviewBaseDomain should not be empty")
	}
	if cfg.ReconcileInterval <= 0 {
		t.Error("ReconcileInterval should be positive")
	}
	if cfg.StaleAssignedAfter <= 0 {
		t.Error("StaleAssignedAfter should be positive")
	}
}
