//go:build e2e

// 이 파일의 책임:
//   - Hub 를 httptest.Server 위에 in-process 기동하는 헬퍼.
//   - daemon.go 의 wiring 을 그대로 복제 (Phase 6: ProxyMiddleware 제거됨).
//   - 인메모리 SQLite 는 동일 DSN 으로 sql.DB 와 migrator 가 두 번 열어야 하므로,
//     파일 기반 임시 DB 를 t.TempDir() 아래에 두고 마이그레이션을 적용한다.
//   - 공용 HTTP 헬퍼(createAgent / saveRunConfig / sendWebhook / poll*) 제공.
//
// e2e 빌드 태그로 격리. tests/e2e 는 wiring 패키지로서 internal/* 직접 import 가 허용된다
// (daemon.go 와 동일 예외).
package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlitestore "github.com/lnyarl/preview/internal/db/sqlite"
	"github.com/lnyarl/preview/internal/hub"
	"github.com/lnyarl/preview/internal/hub/token"
)

// hubHarness 는 e2e 테스트가 Hub 와 통신하는 표면.
// URL 은 http://127.0.0.1:PORT, WSURL 은 ws://127.0.0.1:PORT/agent/ws 형식.
type hubHarness struct {
	URL    string
	WSURL  string
	secret string
	server *httptest.Server
}

// startHub 은 daemon.go 와 동일한 wiring 으로 Hub 를 in-process 기동한다.
// webhookSecret 은 /webhooks/github HMAC 비교에 사용되며 빈 값 금지 (cfg.Validate).
func startHub(t *testing.T, webhookSecret string) *hubHarness {
	t.Helper()

	// 파일 기반 임시 DB. 인메모리(":memory:") 는 동일 DSN 으로 두 번 Open 하면
	// 별도 DB 가 되어 MigrateUp 결과가 hub 의 sql.DB 에 보이지 않는다.
	dbPath := filepath.Join(t.TempDir(), "hub.db")
	dsn := "sqlite://" + dbPath

	if _, err := sqlitestore.MigrateUp(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	db, err := sqlitestore.OpenURL(ctx, dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var logOut io.Writer = io.Discard
	if testing.Verbose() {
		logOut = testWriter{t: t, prefix: "[hub] "}
	}
	logger := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug}))

	agentStore := sqlitestore.NewAgentStore(db)
	previewStore := sqlitestore.NewPreviewStore(db)

	if _, err := agentStore.ResetAllOnline(ctx); err != nil {
		t.Fatalf("reset online: %v", err)
	}
	if _, err := previewStore.ResetAllAssigned(ctx); err != nil {
		t.Fatalf("reset assigned: %v", err)
	}

	tg := token.NewGenerator(4) // bcrypt cost 4 (테스트 가속).
	reg := hub.NewConnRegistry()
	admin := hub.NewAdminHandler(agentStore, tg, logger)
	ws := hub.NewWSHandler(agentStore, reg, logger)
	webhook := hub.NewWebhookHandler(previewStore, webhookSecret, logger)
	adminUI := hub.NewAdminUIHandler(agentStore, previewStore, tg, logger)
	adminUI.SetRegistry(reg)
	admin.SetUI(adminUI)
	webhook.SetUI(adminUI)
	ws.SetPreviewStore(previewStore)

	jobSender := hub.NewWSJobSender(reg)
	dispatcher := hub.NewDispatcher(agentStore, previewStore, jobSender, logger)
	statusUpdater := hub.NewStatusUpdater(previewStore, logger)
	ws.SetReady(dispatcher)
	ws.SetStatusUpdate(statusUpdater)
	ws.SetTeardownSender(jobSender)
	webhook.SetTeardownSender(jobSender)

	// reconciler 빠른 주기 — stale assigned/orphan 검사가 테스트 흐름을 방해하지 않도록 짧게.
	reconciler := hub.NewReconciler(previewStore, reg, logger)
	reconciler.Start(ctx, 100*time.Millisecond, 1*time.Hour) // staleAfter 큰 값으로 사실상 비활성.

	cfg := hub.Config{
		Addr:               "", // httptest 가 할당.
		DatabaseURL:        dsn,
		BcryptCost:         4,
		LogLevel:           "debug",
		WebhookSecret:      webhookSecret,
		PreviewBaseDomain:  "localhost",
		ReconcileInterval:  100 * time.Millisecond,
		StaleAssignedAfter: 1 * time.Hour,
		AdminPassword:      "", // 인증 비활성.
	}
	srv := hub.NewServer(cfg, admin, ws, webhook, reg, logger, adminUI)

	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	wsURL := strings.Replace(hs.URL, "http://", "ws://", 1) + "/agent/ws"
	return &hubHarness{
		URL:    hs.URL,
		WSURL:  wsURL,
		secret: webhookSecret,
		server: hs,
	}
}

// createAgent 는 POST /admin/agents (JSON) 로 agent 를 생성하고 토큰을 반환한다.
func (h *hubHarness) createAgent(t *testing.T, name string) string {
	t.Helper()
	body := map[string]any{"name": name, "labels": []string{}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, h.URL+"/admin/agents", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("createAgent post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("createAgent: status=%d body=%s", resp.StatusCode, string(body))
	}
	var out struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("createAgent decode: %v", err)
	}
	if out.Token == "" {
		t.Fatalf("createAgent: empty token")
	}
	return out.Token
}

// agentID 는 GET /admin/agents 응답에서 name 에 해당하는 ID 를 반환한다.
func (h *hubHarness) agentID(t *testing.T, name string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.URL+"/admin/agents", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agentID get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agentID: status=%d", resp.StatusCode)
	}
	var list []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("agentID decode: %v", err)
	}
	for _, a := range list {
		if a.Name == name {
			return a.ID
		}
	}
	t.Fatalf("agentID: agent %q not found in %d entries", name, len(list))
	return ""
}

// signWebhook 은 GitHub webhook X-Hub-Signature-256 값을 만든다.
func signWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// sendWebhook 은 GitHub pull_request 이벤트를 흉내내어 /webhooks/github 로 POST 한다.
// action: "opened" | "synchronize" | "reopened" | "closed".
func (h *hubHarness) sendWebhook(t *testing.T, action string, prNumber int, sha, branch, repo string) {
	t.Helper()
	payload := map[string]any{
		"action": action,
		"number": prNumber,
		"pull_request": map[string]any{
			"number": prNumber,
			"head": map[string]any{
				"sha": sha,
				"ref": branch,
			},
			"labels": []any{},
		},
		"repository": map[string]any{
			"full_name": repo,
		},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, h.URL+"/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", signWebhook(h.secret, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sendWebhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("sendWebhook %s pr=%d: status=%d body=%s", action, prNumber, resp.StatusCode, string(b))
	}
	io.Copy(io.Discard, resp.Body)
}

// previewView 는 GET /admin/previews/{id} 응답의 부분 DTO. 필요한 필드만 선언.
type previewView struct {
	ID              string  `json:"id"`
	RepoFullName    string  `json:"repo_full_name"`
	PrNumber        int     `json:"pr_number"`
	Status          string  `json:"status"`
	AssignedAgentID *string `json:"assigned_agent_id"`
	AgentHost       *string `json:"agent_host"`
	AgentPort       *int    `json:"agent_port"`
}

// listPreviews 는 GET /admin/previews 결과를 디코드한다.
func (h *hubHarness) listPreviews(t *testing.T) []previewView {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.URL+"/admin/previews", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("listPreviews: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listPreviews: status=%d", resp.StatusCode)
	}
	var list []previewView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("listPreviews decode: %v", err)
	}
	return list
}

// getPreview 는 (prNumber, repo) 로 preview 1건을 찾는다. 미발견은 nil.
func (h *hubHarness) getPreview(t *testing.T, prNumber int, repo string) *previewView {
	t.Helper()
	for _, p := range h.listPreviews(t) {
		if p.PrNumber == prNumber && p.RepoFullName == repo {
			pp := p
			return &pp
		}
	}
	return nil
}

// pollAgentStatus 는 want 상태가 될 때까지 GET /admin/agents 를 폴링한다.
// timeout 초과 시 t.Fatalf.
func (h *hubHarness) pollAgentStatus(t *testing.T, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, h.URL+"/admin/agents", nil)
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			var list []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
			for _, a := range list {
				if a.Name == name {
					last = a.Status
					if a.Status == want {
						return
					}
				}
			}
		} else if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pollAgentStatus(%s): want=%q last=%q timeout=%s", name, want, last, timeout)
}

// pollPreviewStatus 는 want 상태가 될 때까지 listPreviews 를 폴링한다.
// 도달한 preview 의 ID 를 반환한다. timeout 초과 시 t.Fatalf.
func (h *hubHarness) pollPreviewStatus(t *testing.T, prNumber int, repo, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if p := h.getPreview(t, prNumber, repo); p != nil {
			last = p.Status
			if p.Status == want {
				return p.ID
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pollPreviewStatus(pr=%d repo=%s): want=%q last=%q timeout=%s", prNumber, repo, want, last, timeout)
	return ""
}

