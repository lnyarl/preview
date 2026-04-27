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
	// Phase 9: 자연키가 (repo_full_name, commit_sha). sha=="" 는 NULL 로 매핑되어
	// UNIQUE 미발동 → 매번 신규 INSERT.
	if p.CommitSha != "" {
		for _, existing := range f.rows {
			if existing.RepoFullName == p.RepoFullName && existing.CommitSha == p.CommitSha {
				// UPDATE: branch/labels/repo_clone_url/updated_at 만 갱신. status / is_adhoc 보존.
				prev := existing
				existing.Branch = p.Branch
				existing.Labels = p.Labels
				existing.RepoCloneURL = p.RepoCloneURL
				existing.UpdatedAt = p.UpdatedAt
				f.rows[existing.ID] = existing
				return false, &prev, nil
			}
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

func (f *fakePreviewStore) UpdateStatus(_ context.Context, id string, fromStatus, toStatus, message string, now time.Time, fields store.PreviewFields) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return store.ErrNotFound
	}
	if fromStatus != "" && r.Status != fromStatus {
		return store.ErrStaleState
	}
	// Phase 9: ErrShaConflict 검사 — 이미 채워진 sha 와 다른 값이면 거부 (NULL 일 때만 채움).
	if fields.CommitSha != nil && r.CommitSha != "" && r.CommitSha != *fields.CommitSha {
		return store.ErrShaConflict
	}
	from := r.Status
	r.Status = toStatus
	r.UpdatedAt = now
	if fields.CommitSha != nil && r.CommitSha == "" {
		r.CommitSha = *fields.CommitSha
	}
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
func (f *fakePreviewStore) GetActiveByRepoAndPR(_ context.Context, repoFullName string, prNumber int) (*store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 가장 최근 created_at active row.
	var best *store.Preview
	for _, p := range f.rows {
		if p.RepoFullName != repoFullName || p.PrNumber != prNumber {
			continue
		}
		switch p.Status {
		case "queued", "assigned", "building", "running":
		default:
			continue
		}
		if best == nil || p.CreatedAt.After(best.CreatedAt) {
			cp := p
			best = &cp
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return best, nil
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

// Phase 9 Decision Matrix Case B (F-12): synchronize 로 새 sha 도착 시 기존 active row
// teardown + 신규 INSERT. Phase 8 까지의 "같은 row sha 만 갱신" 동작은 폐기.
func TestWebhookSynchronizeNewShaTeardownAndInsert(t *testing.T) {
	srv, fs := newTestServer(t)
	// 1) opened (sha=abc)
	body1 := []byte(`{"action":"opened","pull_request":{"number":42,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp1 := postWebhook(t, srv.URL, "pull_request", body1, sign([]byte(testSecret), body1))
	resp1.Body.Close()
	// 2) synchronize with new sha (Case B)
	body2 := []byte(`{"action":"synchronize","pull_request":{"number":42,"head":{"sha":"def","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	resp2 := postWebhook(t, srv.URL, "pull_request", body2, sign([]byte(testSecret), body2))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("synchronize status=%d", resp2.StatusCode)
	}
	if len(fs.rows) != 2 {
		t.Fatalf("rows=%d want 2 (Case B: teardown old + insert new)", len(fs.rows))
	}
	// status 분포: 기존 teardown + 신규 queued.
	statusCounts := map[string]int{}
	shaSet := map[string]bool{}
	for _, p := range fs.rows {
		statusCounts[p.Status]++
		shaSet[p.CommitSha] = true
	}
	if statusCounts["teardown"] != 1 {
		t.Errorf("teardown rows=%d want 1: %+v", statusCounts["teardown"], statusCounts)
	}
	if statusCounts["queued"] != 1 {
		t.Errorf("queued rows=%d want 1: %+v", statusCounts["queued"], statusCounts)
	}
	if !shaSet["abc"] || !shaSet["def"] {
		t.Errorf("expected both sha abc and def: %v", shaSet)
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

// fakeWHTeardownSender 는 webhook handler 테스트 전용 SendTeardown 캡처.
type fakeWHTeardownSender struct {
	mu    sync.Mutex
	calls int
}

func (s *fakeWHTeardownSender) SendTeardown(_ context.Context, _ string, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return nil
}

// newTestServerWithTeardown 은 newTestServer 와 동일하지만 fakeWHTeardownSender 를 주입한다.
func newTestServerWithTeardown(t *testing.T) (*httptest.Server, *fakePreviewStore, *fakeWHTeardownSender) {
	t.Helper()
	s := newFakeStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewWebhookHandler(s, testSecret, logger)
	ts := &fakeWHTeardownSender{}
	h.SetTeardownSender(ts)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, s, ts
}

// TestWebhookSynchronizeCaseA (F-12a): 같은 sha 두 번 synchronize → row 1개 idempotent,
// teardown 호출 0회.
func TestWebhookSynchronizeCaseA(t *testing.T) {
	srv, fs, ts := newTestServerWithTeardown(t)
	body := []byte(`{"action":"synchronize","pull_request":{"number":42,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	for i := 0; i < 2; i++ {
		r := postWebhook(t, srv.URL, "pull_request", body, sign([]byte(testSecret), body))
		r.Body.Close()
	}
	if len(fs.rows) != 1 {
		t.Fatalf("rows=%d want 1 (Case A: idempotent same sha)", len(fs.rows))
	}
	if ts.calls != 0 {
		t.Fatalf("teardown calls=%d want 0", ts.calls)
	}
}

// TestWebhookSynchronizeCaseC (F-12b): active 없음 + 같은 sha 의 done row 존재 → reopen.
func TestWebhookSynchronizeCaseC(t *testing.T) {
	srv, fs, ts := newTestServerWithTeardown(t)
	// 1) opened (sha=X) → row 1, status=queued.
	body1 := []byte(`{"action":"opened","pull_request":{"number":42,"head":{"sha":"X","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	r1 := postWebhook(t, srv.URL, "pull_request", body1, sign([]byte(testSecret), body1))
	r1.Body.Close()
	// 2) 강제로 done 으로 만든다 (직접 수정).
	var pid string
	for id := range fs.rows {
		pid = id
	}
	row := fs.rows[pid]
	row.Status = "done"
	fs.rows[pid] = row
	// 3) synchronize(sha=X) → Case C reopen.
	body2 := []byte(`{"action":"synchronize","pull_request":{"number":42,"head":{"sha":"X","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	r2 := postWebhook(t, srv.URL, "pull_request", body2, sign([]byte(testSecret), body2))
	r2.Body.Close()
	if len(fs.rows) != 1 {
		t.Fatalf("rows=%d want 1 (Case C: same sha reopen)", len(fs.rows))
	}
	if fs.rows[pid].Status != "queued" {
		t.Fatalf("status=%s want queued (reopened)", fs.rows[pid].Status)
	}
	if ts.calls != 0 {
		t.Fatalf("teardown calls=%d want 0", ts.calls)
	}
	// reopen event message 확인.
	evs := fs.events[pid]
	last := evs[len(evs)-1]
	if last.Message != "reopened_by_synchronize" {
		t.Errorf("last.message=%q want reopened_by_synchronize", last.Message)
	}
}

// TestWebhookSynchronizeCaseD (F-12c): active 없음 + 새 sha → 신규 INSERT 만, teardown 0회.
func TestWebhookSynchronizeCaseD(t *testing.T) {
	srv, fs, ts := newTestServerWithTeardown(t)
	body := []byte(`{"action":"synchronize","pull_request":{"number":42,"head":{"sha":"new","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	r := postWebhook(t, srv.URL, "pull_request", body, sign([]byte(testSecret), body))
	r.Body.Close()
	if len(fs.rows) != 1 {
		t.Fatalf("rows=%d want 1 (Case D: insert)", len(fs.rows))
	}
	if ts.calls != 0 {
		t.Fatalf("teardown calls=%d want 0", ts.calls)
	}
}

// TestWebhookSynchronizeCaseBSendsTeardownAndAddsSupersedeEvent (F-12 보강): Case B 에서
// SendTeardown 호출 + 신규 row 의 첫 event 에 supersede message 포함.
func TestWebhookSynchronizeCaseBSendsTeardownAndAddsSupersedeEvent(t *testing.T) {
	srv, fs, ts := newTestServerWithTeardown(t)
	// 1) opened (sha=abc) — assigned_agent_id 가 없는 시작 상태이므로 이번 케이스에서는
	//    SendTeardown 이 호출되지 않는다(handleUpsert 의 nil 가드). 본 테스트는 supersede
	//    이벤트만 검증하고, SendTeardown 호출은 별도 시나리오에서 검증.
	body1 := []byte(`{"action":"opened","pull_request":{"number":7,"head":{"sha":"abc","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	r1 := postWebhook(t, srv.URL, "pull_request", body1, sign([]byte(testSecret), body1))
	r1.Body.Close()
	var oldID string
	for id := range fs.rows {
		oldID = id
	}
	// 2) synchronize(sha=def) → Case B (sha 다름).
	body2 := []byte(`{"action":"synchronize","pull_request":{"number":7,"head":{"sha":"def","ref":"f"}},"repository":{"full_name":"acme/web"}}`)
	r2 := postWebhook(t, srv.URL, "pull_request", body2, sign([]byte(testSecret), body2))
	r2.Body.Close()
	if len(fs.rows) != 2 {
		t.Fatalf("rows=%d want 2", len(fs.rows))
	}
	// 신규 row 의 events 에 supersede 메시지 존재.
	var newID string
	for id, p := range fs.rows {
		if p.CommitSha == "def" {
			newID = id
		}
	}
	if newID == "" {
		t.Fatal("new row not found (sha=def)")
	}
	found := false
	for _, ev := range fs.events[newID] {
		if ev.Message == "created_after_supersede_of="+oldID {
			found = true
		}
	}
	if !found {
		t.Errorf("supersede event missing in new row events: %+v", fs.events[newID])
	}
	// teardown 메시지 확인 (기존 row).
	teardownFound := false
	for _, ev := range fs.events[oldID] {
		if ev.To == "teardown" && ev.Message == "superseded_by_sha=def" {
			teardownFound = true
		}
	}
	if !teardownFound {
		t.Errorf("teardown event with superseded_by_sha message missing: %+v", fs.events[oldID])
	}
	_ = ts // SendTeardown 은 assigned_agent_id 가 없으면 호출되지 않으므로 본 테스트에서는 무관.
}
