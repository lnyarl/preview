package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// ---------- in-memory AgentStore for tests ----------

type memStore struct {
	mu     sync.Mutex
	items  map[string]*store.Agent
	bcRaw  map[string]string
	bcPort map[string]int
}

func newMemStore() *memStore {
	return &memStore{
		items:  map[string]*store.Agent{},
		bcRaw:  map[string]string{},
		bcPort: map[string]int{},
	}
}

func (m *memStore) Create(_ context.Context, a store.Agent) error {
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
func (m *memStore) GetByName(_ context.Context, name string) (*store.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items {
		if it.Name == name {
			cp := *it
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}
func (m *memStore) GetByID(_ context.Context, id string) (*store.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.items[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, store.ErrNotFound
}
func (m *memStore) List(_ context.Context) ([]store.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Agent, 0, len(m.items))
	for _, a := range m.items {
		out = append(out, *a)
	}
	return out, nil
}
func (m *memStore) UpdateStatus(_ context.Context, id, status string, lastSeen time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.items[id]; ok {
		a.Status = status
		ls := lastSeen
		a.LastSeenAt = &ls
		return nil
	}
	return store.ErrNotFound
}
func (m *memStore) Delete(_ context.Context, id string) error {
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
func (m *memStore) GetBuildConfig(_ context.Context, id string) ([]string, int, error) {
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

func (m *memStore) SaveBuildConfig(_ context.Context, id, raw string, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return store.ErrNotFound
	}
	m.bcRaw[id] = raw
	m.bcPort[id] = port
	return nil
}

// ---------- shared test harness ----------

func newTestHandler(t *testing.T) (*WSHandler, *memStore, string, string) {
	t.Helper()
	s := newMemStore()
	tg := token.NewGenerator(4) // fast bcrypt for tests.
	logger := slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))

	raw, hash, err := tg.Generate()
	if err != nil {
		t.Fatalf("token.Generate: %v", err)
	}
	id := uuid.NewString()
	if err := s.Create(context.Background(), store.Agent{
		ID:        id,
		Name:      "test-agent",
		TokenHash: hash,
		Status:    "offline",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	reg := NewConnRegistry()
	wsh := NewWSHandler(s, reg, logger)
	return wsh, s, id, raw
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func startTestServer(t *testing.T, wsh *WSHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	wsh.Register(mux)
	return httptest.NewServer(mux)
}

func dialWS(t *testing.T, srv *httptest.Server, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/ws"
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
}

func writeEnv(t *testing.T, conn *websocket.Conn, typ string, data any) {
	t.Helper()
	env, _ := protocol.NewEnvelope(typ, data)
	b, _ := json.Marshal(env)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write %s: %v", typ, err)
	}
}

func readEnv(t *testing.T, conn *websocket.Conn) protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return env
}

// ---------- tests ----------

func TestWSHandshakeRejectsNoAuth(t *testing.T) {
	wsh, _, _, _ := newTestHandler(t)
	srv := startTestServer(t, wsh)
	defer srv.Close()

	_, resp, err := dialWS(t, srv, "")
	if err == nil {
		t.Fatal("expected dial failure without auth")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%v want 401", respStatus(resp))
	}
}

func TestWSHandshakeRejectsBadToken(t *testing.T) {
	wsh, _, _, _ := newTestHandler(t)
	srv := startTestServer(t, wsh)
	defer srv.Close()

	_, resp, err := dialWS(t, srv, "agt_totallywrongtokenvalue000000000000000000")
	if err == nil {
		t.Fatal("expected dial failure with bad token")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%v want 401", respStatus(resp))
	}
}

func TestWSHandshakeSuccess(t *testing.T) {
	wsh, s, id, raw := newTestHandler(t)
	srv := startTestServer(t, wsh)
	defer srv.Close()

	conn, _, err := dialWS(t, srv, raw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

	writeEnv(t, conn, protocol.TypeHello, protocol.HelloData{Version: protocol.ProtoVersion})
	env := readEnv(t, conn)
	if env.Type != protocol.TypeWelcome {
		t.Fatalf("got type=%s want WELCOME", env.Type)
	}
	// Phase 4: WELCOME 직후 AGENT_CONFIG 1회 송신 (§4-6).
	cfgEnv := readEnv(t, conn)
	if cfgEnv.Type != protocol.TypeAgentConfig {
		t.Fatalf("expected AGENT_CONFIG after WELCOME, got %s", cfgEnv.Type)
	}
	var cfg protocol.AgentConfigData
	if err := cfgEnv.Decode(&cfg); err != nil {
		t.Fatalf("decode AGENT_CONFIG: %v", err)
	}
	// 신규 등록 agent → DB sentinel ([], 0).
	if len(cfg.RunCommands) != 0 {
		t.Fatalf("expected empty RunCommands, got %v", cfg.RunCommands)
	}
	if cfg.ContainerPort != 0 {
		t.Fatalf("expected ContainerPort=0, got %d", cfg.ContainerPort)
	}

	// Status should be online shortly.
	waitFor(t, 2*time.Second, func() bool {
		a, err := s.GetByID(context.Background(), id)
		return err == nil && a.Status == "online"
	})
}

// TestWSAgentConfigSentWithStoredValues — Phase 4 F-20: 저장된 값이 있는 Agent 에 대한
// AGENT_CONFIG payload 확인.
func TestWSAgentConfigSentWithStoredValues(t *testing.T) {
	wsh, s, id, raw := newTestHandler(t)
	// 저장 값 주입.
	if err := s.SaveBuildConfig(context.Background(), id, "npm ci\nnpm run build", 3000); err != nil {
		t.Fatalf("SaveBuildConfig: %v", err)
	}
	srv := startTestServer(t, wsh)
	defer srv.Close()

	conn, _, err := dialWS(t, srv, raw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

	writeEnv(t, conn, protocol.TypeHello, protocol.HelloData{Version: protocol.ProtoVersion})
	if env := readEnv(t, conn); env.Type != protocol.TypeWelcome {
		t.Fatalf("welcome missing: %s", env.Type)
	}
	cfgEnv := readEnv(t, conn)
	if cfgEnv.Type != protocol.TypeAgentConfig {
		t.Fatalf("expected AGENT_CONFIG, got %s", cfgEnv.Type)
	}
	var cfg protocol.AgentConfigData
	if err := cfgEnv.Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cfg.RunCommands) != 2 || cfg.RunCommands[0] != "npm ci" || cfg.RunCommands[1] != "npm run build" {
		t.Fatalf("RunCommands=%v", cfg.RunCommands)
	}
	if cfg.ContainerPort != 3000 {
		t.Fatalf("ContainerPort=%d want 3000", cfg.ContainerPort)
	}
}

func TestWSHandshakeVersionMismatch(t *testing.T) {
	wsh, _, _, raw := newTestHandler(t)
	srv := startTestServer(t, wsh)
	defer srv.Close()

	conn, _, err := dialWS(t, srv, raw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()

	writeEnv(t, conn, protocol.TypeHello, protocol.HelloData{Version: "v2"})

	_, _, err = conn.Read(context.Background())
	if err == nil {
		t.Fatal("expected close after version mismatch")
	}
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("want CloseError, got %T %v", err, err)
	}
	if closeErr.Code != websocket.StatusCode(4001) {
		t.Fatalf("close code=%d want 4001", closeErr.Code)
	}
}

func TestWSDuplicateConnection(t *testing.T) {
	wsh, _, _, raw := newTestHandler(t)
	srv := startTestServer(t, wsh)
	defer srv.Close()

	// First connection.
	conn1, _, err := dialWS(t, srv, raw)
	if err != nil {
		t.Fatalf("dial1: %v", err)
	}
	defer func() { _ = conn1.Close(websocket.StatusNormalClosure, "done") }()
	writeEnv(t, conn1, protocol.TypeHello, protocol.HelloData{Version: protocol.ProtoVersion})
	if env := readEnv(t, conn1); env.Type != protocol.TypeWelcome {
		t.Fatalf("conn1 welcome missing: %s", env.Type)
	}
	// Phase 4: drain AGENT_CONFIG.
	if env := readEnv(t, conn1); env.Type != protocol.TypeAgentConfig {
		t.Fatalf("conn1 expected AGENT_CONFIG, got %s", env.Type)
	}

	// Second connection, same token.
	conn2, _, err := dialWS(t, srv, raw)
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	defer func() { _ = conn2.Close(websocket.StatusNormalClosure, "done") }()
	writeEnv(t, conn2, protocol.TypeHello, protocol.HelloData{Version: protocol.ProtoVersion})

	_, _, err = conn2.Read(context.Background())
	if err == nil {
		t.Fatal("expected close on duplicate connection")
	}
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("want CloseError, got %T %v", err, err)
	}
	if closeErr.Code != websocket.StatusCode(4003) {
		t.Fatalf("close code=%d want 4003", closeErr.Code)
	}
}

func TestWSGracefulShutdown(t *testing.T) {
	wsh, _, _, raw := newTestHandler(t)
	mux := http.NewServeMux()
	wsh.Register(mux)
	srv := httptest.NewServer(mux)

	conn, _, err := dialWS(t, srv, raw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()
	writeEnv(t, conn, protocol.TypeHello, protocol.HelloData{Version: protocol.ProtoVersion})
	if env := readEnv(t, conn); env.Type != protocol.TypeWelcome {
		t.Fatalf("welcome missing: %s", env.Type)
	}
	// Phase 4: drain AGENT_CONFIG.
	if env := readEnv(t, conn); env.Type != protocol.TypeAgentConfig {
		t.Fatalf("expected AGENT_CONFIG, got %s", env.Type)
	}

	// Server graceful shutdown uses the registry from the handler.
	// wsh has no direct access; we can invoke Registry.closeAll directly here
	// because this test simulates Server.shutdown.
	wsh.Registry.closeAll(websocket.StatusGoingAway, "going away")

	_, _, err = conn.Read(context.Background())
	if err == nil {
		t.Fatal("expected close frame")
	}
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("want CloseError got %T %v", err, err)
	}
	if closeErr.Code != websocket.StatusGoingAway {
		t.Fatalf("close code=%d want 1001", closeErr.Code)
	}

	srv.Close()
}

// ---------- helpers ----------

func waitFor(t *testing.T, d time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met in %v", d)
}

func respStatus(r *http.Response) any {
	if r == nil {
		return "<nil>"
	}
	return r.StatusCode
}
