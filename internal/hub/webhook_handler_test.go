package hub

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
	"sync"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

// fakePreviewStore 는 webhook 단위 테스트를 위한 in-memory PreviewStore.
// sqlite 의존을 회피해 hub 패키지 단위에서 webhook 동작을 검증한다.
type fakePreviewStore struct {
	mu      sync.Mutex
	rows    map[string]store.Preview // id → preview
	events  map[string][]eventEntry  // preview_id → events
	created []string                 // upsert created order
}

type eventEntry struct {
	From    *string
	To      string
	Message string
}

func newFakeStore() *fakePreviewStore {
	return &fakePreviewStore{
		rows:   map[string]store.Preview{},
		events: map[string][]eventEntry{},
	}
}

func (f *fakePreviewStore) Upsert(_ context.Context, p store.Preview) (bool, *store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.rows {
		if existing.RepoFullName == p.RepoFullName && existing.PrNumber == p.PrNumber {
			// UPDATE: 필드 갱신, status 변경 없음.
			prev := existing
			existing.CommitSha = p.CommitSha
			existing.Branch = p.Branch
			existing.Labels = p.Labels
			existing.UpdatedAt = p.UpdatedAt
			f.rows[existing.ID] = existing
			return false, &prev, nil
		}
	}
	// INSERT: status='queued' + event(NULL→queued).
	p.Status = "queued"
	f.rows[p.ID] = p
	f.created = append(f.created, p.ID)
	f.events[p.ID] = append(f.events[p.ID], eventEntry{From: nil, To: "queued"})
	return true, nil, nil
}

func (f *fakePreviewStore) GetByID(_ context.Context, id string) (*store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

func (f *fakePreviewStore) UpdateStatus(_ context.Context, id string, fromStatus, toStatus, message string, now time.Time, _ store.PreviewFields) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return store.ErrNotFound
	}
	if fromStatus != "" && r.Status != fromStatus {
		return store.ErrStaleState
	}
	from := r.Status
	r.Status = toStatus
	r.UpdatedAt = now
	f.rows[id] = r
	f.events[id] = append(f.events[id], eventEntry{From: &from, To: toStatus, Message: message})
	return nil
}

func (f *fakePreviewStore) ListAll(_ context.Context) ([]store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Preview, 0, len(f.rows))
	for _, p := range f.rows {
		out = append(out, p)
	}
	return out, nil
}

// 미구현 stub (Step 2/3).
func (f *fakePreviewStore) FindByHost(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}
func (f *fakePreviewStore) ListQueuedForCandidates(_ context.Context) ([]store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}
func (f *fakePreviewStore) Claim(_ context.Context, _ []string, _ string, _ time.Time) (*store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}
func (f *fakePreviewStore) ListRunningByAgent(_ context.Context, _ string) ([]store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}
func (f *fakePreviewStore) ListStaleAssigned(_ context.Context, _ time.Time) ([]store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}
func (f *fakePreviewStore) ListByAgent(_ context.Context, _ string, _ []string) ([]store.Preview, error) {
	return nil, store.ErrNotImplementedStep1
}
func (f *fakePreviewStore) ListPreviewEvents(_ context.Context, _ string, _, _ int) ([]store.PreviewEvent, error) {
	return nil, nil
}

// helpers --------------------------------------------------------------------

const testSecret = "test-secret"

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newTestServer(t *testing.T) (*httptest.Server, *fakePreviewStore) {
	t.Helper()
	s := newFakeStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewWebhookHandler(s, testSecret, logger)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, s
}

func postWebhook(t *testing.T, url, event string, body []byte, sig string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-GitHub-Event", event)
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

// tests ----------------------------------------------------------------------

func TestWebhookMissingSignature(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := postWebhook(t, srv.URL, "pull_request", []byte("{}"), "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["error"] != "missing_signature" {
		t.Fatalf("error=%q want missing_signature", body["error"])
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := postWebhook(t, srv.URL, "pull_request", []byte("{}"), "sha256=deadbeef")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["error"] != "invalid_signature" {
		t.Fatalf("error=%q want invalid_signature", body["error"])
	}
}

func TestWebhookOpenedNew(t *testing.T) {
	srv, fs := newTestServer(t)
	body := []byte(`{"action":"opened","pull_request":{"number":42,"head":{"sha":"abc123","ref":"feature/x"}},"repository":{"full_name":"acme/web"}}`)
	sig := sign([]byte(testSecret), body)
	resp := postWebhook(t, srv.URL, "pull_request", body, sig)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d want 202", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["status"] != "queued" {
		t.Fatalf("status=%v want queued", out["status"])
	}
	// row 1건 + event 1건(NULL→queued).
	if len(fs.rows) != 1 {
		t.Fatalf("rows=%d want 1", len(fs.rows))
	}
	for _, ev := range fs.events {
		if len(ev) != 1 || ev[0].To != "queued" || ev[0].From != nil {
			t.Fatalf("events=%+v", ev)
		}
	}
}

func TestWebhookSynchronizeUpdatesShaSameStatus(t *testing.T) {
	srv, fs := newTestServer(t)
	// 1) opened
	body1 := []byte(`{"action":"opened","pull_request":{"number":42,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp1 := postWebhook(t, srv.URL, "pull_request", body1, sign([]byte(testSecret), body1))
	resp1.Body.Close()
	// 2) synchronize with new sha
	body2 := []byte(`{"action":"synchronize","pull_request":{"number":42,"head":{"sha":"def","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp2 := postWebhook(t, srv.URL, "pull_request", body2, sign([]byte(testSecret), body2))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("synchronize status=%d", resp2.StatusCode)
	}
	if len(fs.rows) != 1 {
		t.Fatalf("rows=%d want 1 (upsert)", len(fs.rows))
	}
	for _, p := range fs.rows {
		if p.CommitSha != "def" {
			t.Fatalf("sha=%s want def", p.CommitSha)
		}
		if p.Status != "queued" {
			t.Fatalf("status=%s want queued", p.Status)
		}
	}
	// 룰 R2: synchronize(같은 status) → event 추가 없음. Initial open 1건만.
	for _, ev := range fs.events {
		if len(ev) != 1 {
			t.Fatalf("events=%d want 1 (no synchronize event)", len(ev))
		}
	}
}

func TestWebhookReopenedFromDone(t *testing.T) {
	srv, fs := newTestServer(t)
	// 1) opened (creates row, status=queued).
	body1 := []byte(`{"action":"opened","pull_request":{"number":42,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp1 := postWebhook(t, srv.URL, "pull_request", body1, sign([]byte(testSecret), body1))
	resp1.Body.Close()
	// 2) 강제로 done 으로 만든다.
	var pid string
	for id := range fs.rows {
		pid = id
	}
	r := fs.rows[pid]
	r.Status = "done"
	fs.rows[pid] = r
	// 3) opened again → 핸들러가 prev.Status=done 보고 UpdateStatus(done→queued) 호출.
	body3 := []byte(`{"action":"opened","pull_request":{"number":42,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp3 := postWebhook(t, srv.URL, "pull_request", body3, sign([]byte(testSecret), body3))
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp3.StatusCode)
	}
	if fs.rows[pid].Status != "queued" {
		t.Fatalf("status=%s want queued", fs.rows[pid].Status)
	}
	// events 마지막 항목 = (done, queued).
	evs := fs.events[pid]
	last := evs[len(evs)-1]
	if last.From == nil || *last.From != "done" || last.To != "queued" {
		t.Fatalf("last event=%+v want done→queued", last)
	}
}

func TestWebhookClosedTransitionsToTeardown(t *testing.T) {
	srv, fs := newTestServer(t)
	body1 := []byte(`{"action":"opened","pull_request":{"number":42,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp1 := postWebhook(t, srv.URL, "pull_request", body1, sign([]byte(testSecret), body1))
	resp1.Body.Close()
	body2 := []byte(`{"action":"closed","pull_request":{"number":42,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp2 := postWebhook(t, srv.URL, "pull_request", body2, sign([]byte(testSecret), body2))
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp2.StatusCode)
	}
	for _, p := range fs.rows {
		if p.Status != "teardown" {
			t.Fatalf("status=%s want teardown", p.Status)
		}
	}
}

func TestWebhookClosedNoExistingPreview(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{"action":"closed","pull_request":{"number":99,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp := postWebhook(t, srv.URL, "pull_request", body, sign([]byte(testSecret), body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["ignored"] != true {
		t.Fatalf("ignored=%v", out["ignored"])
	}
}

func TestWebhookNonPullRequestEventIgnored(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{"hello":"world"}`)
	resp := postWebhook(t, srv.URL, "push", body, sign([]byte(testSecret), body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["ignored"] != true {
		t.Fatalf("ignored=%v", out["ignored"])
	}
}

func TestWebhookUnknownActionIgnored(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{"action":"labeled","pull_request":{"number":1,"head":{"sha":"abc","ref":"x"}},"repository":{"full_name":"acme/web"}}`)
	resp := postWebhook(t, srv.URL, "pull_request", body, sign([]byte(testSecret), body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
}

func TestWebhookInvalidJSON(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{not json`)
	resp := postWebhook(t, srv.URL, "pull_request", body, sign([]byte(testSecret), body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
}

func TestConfigValidateMissingSecret(t *testing.T) {
	cfg := Config{}
	if err := cfg.Validate(); err != ErrWebhookSecretMissing {
		t.Fatalf("err=%v want ErrWebhookSecretMissing", err)
	}
	cfg.WebhookSecret = "x"
	// ADMIN_PASSWORD 도 필수 — 없으면 ErrAdminPasswordMissing.
	if err := cfg.Validate(); err != ErrAdminPasswordMissing {
		t.Fatalf("err=%v want ErrAdminPasswordMissing", err)
	}
	cfg.AdminPassword = "secret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("err=%v want nil", err)
	}
}

func TestAdminPreviewsList(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{"action":"opened","pull_request":{"number":1,"head":{"sha":"abc","ref":"x"}},"repository":{"full_name":"acme/web"}}`)
	resp := postWebhook(t, srv.URL, "pull_request", body, sign([]byte(testSecret), body))
	resp.Body.Close()
	r, err := http.Get(srv.URL + "/admin/previews")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", r.StatusCode)
	}
	var views []PreviewView
	_ = json.NewDecoder(r.Body).Decode(&views)
	if len(views) != 1 {
		t.Fatalf("views=%d want 1", len(views))
	}
	if views[0].PrNumber != 1 {
		t.Fatalf("pr=%d", views[0].PrNumber)
	}
}
