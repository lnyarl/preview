// admin_handler_test.go: AdminHandler 단위 테스트 — /health, /admin/agents CRUD.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
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

// adminFakeAgentStore 는 AdminHandler 단위 테스트용 in-memory AgentStore.
type adminFakeAgentStore struct {
	mu     sync.Mutex
	rows   map[string]store.Agent     // id → agent
	byName map[string]string          // name → id
	// Phase 4: build config storage. agentID → (raw, port).
	bcRaw  map[string]string
	bcPort map[string]int
}

func newFakeAgentStore() *adminFakeAgentStore {
	return &adminFakeAgentStore{
		rows:   make(map[string]store.Agent),
		byName: make(map[string]string),
		bcRaw:  make(map[string]string),
		bcPort: make(map[string]int),
	}
}

func (f *adminFakeAgentStore) Create(_ context.Context, a store.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.byName[a.Name]; exists {
		return store.ErrDuplicate
	}
	f.rows[a.ID] = a
	f.byName[a.Name] = a.ID
	return nil
}

func (f *adminFakeAgentStore) GetByName(_ context.Context, name string) (*store.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byName[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	a := f.rows[id]
	return &a, nil
}

func (f *adminFakeAgentStore) GetByID(_ context.Context, id string) (*store.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.rows[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &a, nil
}

func (f *adminFakeAgentStore) List(_ context.Context) ([]store.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Agent, 0, len(f.rows))
	for _, a := range f.rows {
		out = append(out, a)
	}
	return out, nil
}

func (f *adminFakeAgentStore) UpdateStatus(_ context.Context, id, status string, lastSeenAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.rows[id]
	if !ok {
		return store.ErrNotFound
	}
	a.Status = status
	a.LastSeenAt = &lastSeenAt
	f.rows[id] = a
	return nil
}

func (f *adminFakeAgentStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.rows[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(f.byName, a.Name)
	delete(f.rows, id)
	delete(f.bcRaw, id)
	delete(f.bcPort, id)
	return nil
}

// Phase 4 stub: GetBuildConfig / SaveBuildConfig.
func (f *adminFakeAgentStore) GetBuildConfig(_ context.Context, id string) ([]string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[id]; !ok {
		return nil, 0, store.ErrNotFound
	}
	raw := f.bcRaw[id]
	port := f.bcPort[id]
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

func (f *adminFakeAgentStore) SaveBuildConfig(_ context.Context, id, raw string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[id]; !ok {
		return store.ErrNotFound
	}
	f.bcRaw[id] = raw
	f.bcPort[id] = port
	return nil
}

// helpers

func newAdminTestServer(t *testing.T) (*httptest.Server, *adminFakeAgentStore) {
	t.Helper()
	as := newFakeAgentStore()
	tg := token.NewGenerator(4) // cost 4 for speed
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewAdminHandler(as, tg, logger)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, as
}

// tests

func TestAdminHealth(t *testing.T) {
	srv, _ := newAdminTestServer(t)
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("body=%v", body)
	}
}

func TestAdminCreateAgent(t *testing.T) {
	srv, as := newAdminTestServer(t)
	payload := []byte(`{"name":"test-agent","labels":["local"]}`)
	resp, err := http.Post(srv.URL+"/admin/agents", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d want 201", resp.StatusCode)
	}
	var out createAgentResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Name != "test-agent" {
		t.Errorf("name=%s want test-agent", out.Name)
	}
	if out.Token == "" {
		t.Error("token should not be empty")
	}
	if len(as.rows) != 1 {
		t.Errorf("rows=%d want 1", len(as.rows))
	}
}

func TestAdminCreateAgentInvalidName(t *testing.T) {
	srv, _ := newAdminTestServer(t)
	payload := []byte(`{"name":"bad name!"}`)
	resp, err := http.Post(srv.URL+"/admin/agents", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
}

func TestAdminCreateAgentDuplicate(t *testing.T) {
	srv, _ := newAdminTestServer(t)
	payload := []byte(`{"name":"dup"}`)
	http.Post(srv.URL+"/admin/agents", "application/json", bytes.NewReader(payload))
	resp, err := http.Post(srv.URL+"/admin/agents", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", resp.StatusCode)
	}
}

func TestAdminListAgents(t *testing.T) {
	srv, _ := newAdminTestServer(t)
	http.Post(srv.URL+"/admin/agents", "application/json", bytes.NewReader([]byte(`{"name":"a1"}`)))
	http.Post(srv.URL+"/admin/agents", "application/json", bytes.NewReader([]byte(`{"name":"a2"}`)))

	resp, err := http.Get(srv.URL + "/admin/agents")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var views []AgentView
	_ = json.NewDecoder(resp.Body).Decode(&views)
	if len(views) != 2 {
		t.Errorf("views=%d want 2", len(views))
	}
}

func TestAdminDeleteAgentExists(t *testing.T) {
	srv, as := newAdminTestServer(t)
	resp, _ := http.Post(srv.URL+"/admin/agents", "application/json", bytes.NewReader([]byte(`{"name":"del-me"}`)))
	var cr createAgentResponse
	json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/admin/agents/"+cr.ID, nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d want 204", delResp.StatusCode)
	}
	if len(as.rows) != 0 {
		t.Errorf("rows=%d want 0", len(as.rows))
	}
}

func TestAdminDeleteAgentNotFound(t *testing.T) {
	srv, _ := newAdminTestServer(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/admin/agents/no-such", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
}

func TestToViewWithLastSeenAt(t *testing.T) {
	now := time.Now()
	a := store.Agent{
		ID:         "x",
		Name:       "n",
		Labels:     nil,
		Status:     "online",
		LastSeenAt: &now,
		CreatedAt:  now,
	}
	v := toView(a)
	if v.LastSeenAt == nil {
		t.Error("LastSeenAt should not be nil")
	}
}
