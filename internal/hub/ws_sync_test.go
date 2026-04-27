// ws_sync_test.go: Phase 3 HELLO 동기화 단위 테스트 (§5-3).
//   - TestSyncOnHelloLegacyAgent: running_previews 키 부재 → 비교 SKIP + WARN.
//   - TestSyncOnHelloOrphanContainer: Agent − DB → SendTeardown 호출.
//   - TestSyncOnHelloLostContainer: DB − Agent → UpdateStatus(*→failed) 호출.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// syncFakePreviewStore — HELLO sync 테스트용.
type syncFakePreviewStore struct {
	mu                 sync.Mutex
	listByAgentCalls   int
	listByAgentReturn  []store.Preview
	updateStatusCalls  int
	updateStatusFromTo []string // "from→to" 기록
	updateStatusErr    error
}

func (f *syncFakePreviewStore) Upsert(_ context.Context, _ store.Preview) (bool, *store.Preview, error) {
	return false, nil, errors.New("not used")
}
func (f *syncFakePreviewStore) GetByID(_ context.Context, _ string) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *syncFakePreviewStore) FindByHost(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *syncFakePreviewStore) ListQueuedForCandidates(_ context.Context) ([]store.Preview, error) {
	return nil, nil
}
func (f *syncFakePreviewStore) Claim(_ context.Context, _ []string, _ string, _ time.Time) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *syncFakePreviewStore) UpdateStatus(_ context.Context, id, fromStatus, toStatus, _ string, _ time.Time, _ store.PreviewFields) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateStatusCalls++
	f.updateStatusFromTo = append(f.updateStatusFromTo, fromStatus+"→"+toStatus+":"+id)
	return f.updateStatusErr
}
func (f *syncFakePreviewStore) ListRunningByAgent(_ context.Context, _ string) ([]store.Preview, error) {
	return nil, nil
}
func (f *syncFakePreviewStore) ListStaleAssigned(_ context.Context, _ time.Time) ([]store.Preview, error) {
	return nil, nil
}
func (f *syncFakePreviewStore) ListByAgent(_ context.Context, _ string, _ []string) ([]store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listByAgentCalls++
	out := make([]store.Preview, len(f.listByAgentReturn))
	copy(out, f.listByAgentReturn)
	return out, nil
}
func (f *syncFakePreviewStore) ListAll(_ context.Context) ([]store.Preview, error) {
	return nil, nil
}
func (f *syncFakePreviewStore) ListPreviewEvents(_ context.Context, _ string, _, _ int) ([]store.PreviewEvent, error) {
	return nil, nil
}
func (f *syncFakePreviewStore) GetActiveByRepoAndPR(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *syncFakePreviewStore) FindAdhocByBranch(_ context.Context, _, _ string) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *syncFakePreviewStore) ListRepos(_ context.Context) ([]string, error) { return nil, nil }

// fakeTeardownSender 는 SendTeardown 호출을 capture.
type fakeTeardownSender struct {
	mu       sync.Mutex
	calls    int
	lastID   string
	lastAgnt string
}

func (s *fakeTeardownSender) SendTeardown(_ context.Context, agentID, previewID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastAgnt = agentID
	s.lastID = previewID
	return nil
}

func newSyncTestHandler(ps *syncFakePreviewStore, ts *fakeTeardownSender, logBuf *bytes.Buffer) *WSHandler {
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := &WSHandler{
		Logger:         logger,
		PreviewStore:   ps,
		TeardownSender: ts,
	}
	return h
}

func TestSyncOnHelloLegacyAgent(t *testing.T) {
	ps := &syncFakePreviewStore{}
	ts := &fakeTeardownSender{}
	var logs bytes.Buffer
	h := newSyncTestHandler(ps, ts, &logs)

	// running_previews 키 부재 — 레거시 Agent.
	raw := json.RawMessage(`{"version":"v1","labels":{"env":"a"}}`)
	h.syncOnHello(context.Background(), "a1", protocol.HelloData{Version: "v1"}, raw)

	if ps.listByAgentCalls != 0 {
		t.Errorf("ListByAgent called %d times; want 0 (legacy SKIP)", ps.listByAgentCalls)
	}
	if !strings.Contains(logs.String(), "reconcile_hello_legacy_agent") {
		t.Errorf("expected 'reconcile_hello_legacy_agent' in logs:\n%s", logs.String())
	}
	if ts.calls != 0 {
		t.Errorf("SendTeardown called %d times; want 0", ts.calls)
	}
}

func TestSyncOnHelloOrphanContainer(t *testing.T) {
	// DB: agent A 의 preview {p1, status=running} 만 등록.
	ps := &syncFakePreviewStore{
		listByAgentReturn: []store.Preview{
			{ID: "p1", Status: "running"},
		},
	}
	ts := &fakeTeardownSender{}
	var logs bytes.Buffer
	h := newSyncTestHandler(ps, ts, &logs)

	// HELLO RunningPreviews=[p1, p_orphan] — p_orphan 은 DB 에 없음.
	raw := json.RawMessage(`{"version":"v1","running_previews":["p1","p_orphan"]}`)
	hello := protocol.HelloData{Version: "v1", RunningPreviews: []string{"p1", "p_orphan"}}
	h.syncOnHello(context.Background(), "agentA", hello, raw)

	if ts.calls != 1 {
		t.Fatalf("SendTeardown calls=%d want 1", ts.calls)
	}
	if ts.lastID != "p_orphan" || ts.lastAgnt != "agentA" {
		t.Errorf("got SendTeardown(%s,%s); want (agentA,p_orphan)", ts.lastAgnt, ts.lastID)
	}
	if !strings.Contains(logs.String(), "reconcile_hello_orphan_container") {
		t.Errorf("expected reconcile_hello_orphan_container in logs:\n%s", logs.String())
	}
	// p1 는 일치 항목이므로 UpdateStatus(running→failed) 호출되면 안 됨.
	if ps.updateStatusCalls != 0 {
		t.Errorf("UpdateStatus called %d times for matched item; want 0 (transitions=%v)",
			ps.updateStatusCalls, ps.updateStatusFromTo)
	}
}

func TestSyncOnHelloLostContainer(t *testing.T) {
	// DB: agent A 의 preview {p1, p2} (둘 다 running).
	ps := &syncFakePreviewStore{
		listByAgentReturn: []store.Preview{
			{ID: "p1", Status: "running"},
			{ID: "p2", Status: "running"},
		},
	}
	ts := &fakeTeardownSender{}
	var logs bytes.Buffer
	h := newSyncTestHandler(ps, ts, &logs)

	// HELLO RunningPreviews=[p1] — p2 가 lost.
	raw := json.RawMessage(`{"version":"v1","running_previews":["p1"]}`)
	hello := protocol.HelloData{Version: "v1", RunningPreviews: []string{"p1"}}
	h.syncOnHello(context.Background(), "agentA", hello, raw)

	if ps.updateStatusCalls != 1 {
		t.Fatalf("UpdateStatus calls=%d want 1; transitions=%v",
			ps.updateStatusCalls, ps.updateStatusFromTo)
	}
	if !strings.Contains(ps.updateStatusFromTo[0], "running→failed:p2") {
		t.Errorf("transition=%q want running→failed:p2", ps.updateStatusFromTo[0])
	}
	if !strings.Contains(logs.String(), "reconcile_hello_lost_container") {
		t.Errorf("expected reconcile_hello_lost_container in logs:\n%s", logs.String())
	}
	// SendTeardown 호출은 없어야 함 — orphan 없음.
	if ts.calls != 0 {
		t.Errorf("SendTeardown calls=%d want 0", ts.calls)
	}
}

// TestSyncOnHelloErrStaleStateIgnored: UpdateStatus 가 ErrStaleState 반환해도
// 패닉 없고 다음 항목 처리 진행.
func TestSyncOnHelloErrStaleStateIgnored(t *testing.T) {
	ps := &syncFakePreviewStore{
		listByAgentReturn: []store.Preview{{ID: "p1", Status: "running"}},
		updateStatusErr:   store.ErrStaleState,
	}
	ts := &fakeTeardownSender{}
	var logs bytes.Buffer
	h := newSyncTestHandler(ps, ts, &logs)

	raw := json.RawMessage(`{"version":"v1","running_previews":[]}`)
	hello := protocol.HelloData{Version: "v1", RunningPreviews: []string{}}
	h.syncOnHello(context.Background(), "agentA", hello, raw)

	// ErrStaleState 무시 → reconcile_hello_lost_failed 로그 없어야 함.
	if strings.Contains(logs.String(), "reconcile_hello_lost_failed") {
		t.Errorf("ErrStaleState should be silently ignored; logs=%s", logs.String())
	}
}

// TestHasRunningPreviewsKey 유틸 검증.
func TestHasRunningPreviewsKey(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{}`, false},
		{`{"version":"v1"}`, false},
		{`{"version":"v1","running_previews":[]}`, true},
		{`{"version":"v1","running_previews":["p1"]}`, true},
		{`{"version":"v1","labels":{"running_previews":"x"}}`, false}, // nested 만 있음 → top-level 부재
	}
	for _, tc := range cases {
		got := hasRunningPreviewsKey(json.RawMessage(tc.raw))
		if got != tc.want {
			t.Errorf("hasRunningPreviewsKey(%s) = %v; want %v", tc.raw, got, tc.want)
		}
	}
}
