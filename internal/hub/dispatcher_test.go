package hub

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

// dispFakePreviewStore 는 PreviewStore 의 in-memory mock.
// Step 2 dispatcher 단위 테스트에 사용. 다른 메서드는 호출되지 않음.
type dispFakePreviewStore struct {
	mu             sync.Mutex
	candidates     []store.Preview
	claimErr       error
	claimedAgentID string
	claimedIDs     []string
	claimReturn    *store.Preview
}

func (f *dispFakePreviewStore) Upsert(ctx context.Context, p store.Preview) (bool, *store.Preview, error) {
	return false, nil, errors.New("not used")
}
func (f *dispFakePreviewStore) GetByID(ctx context.Context, id string) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *dispFakePreviewStore) FindByHost(ctx context.Context, repo string, pr int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *dispFakePreviewStore) ListQueuedForCandidates(ctx context.Context) ([]store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Preview, len(f.candidates))
	copy(out, f.candidates)
	return out, nil
}
func (f *dispFakePreviewStore) Claim(ctx context.Context, ids []string, agentID string, now time.Time) (*store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimedAgentID = agentID
	f.claimedIDs = ids
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	return f.claimReturn, nil
}
func (f *dispFakePreviewStore) UpdateStatus(ctx context.Context, id, from, to, msg string, now time.Time, fields store.PreviewFields) error {
	return nil
}
func (f *dispFakePreviewStore) ListRunningByAgent(ctx context.Context, agentID string) ([]store.Preview, error) {
	return nil, nil
}
func (f *dispFakePreviewStore) ListStaleAssigned(ctx context.Context, t time.Time) ([]store.Preview, error) {
	return nil, nil
}
func (f *dispFakePreviewStore) ListByAgent(ctx context.Context, agentID string, statuses []string) ([]store.Preview, error) {
	return nil, nil
}
func (f *dispFakePreviewStore) ListAll(ctx context.Context) ([]store.Preview, error) { return nil, nil }
func (f *dispFakePreviewStore) ListPreviewEvents(ctx context.Context, previewID string, limit, offset int) ([]store.PreviewEvent, error) {
	return nil, nil
}
func (f *dispFakePreviewStore) GetActiveByRepoAndPR(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *dispFakePreviewStore) FindAdhocByBranch(_ context.Context, _, _ string) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *dispFakePreviewStore) ListRepos(_ context.Context) ([]string, error) { return nil, nil }

// fakeAgentStore — GetByID 만 사용.
type fakeAgentStore struct {
	agent *store.Agent
	err   error
}

func (f *fakeAgentStore) Create(ctx context.Context, a store.Agent) error { return nil }
func (f *fakeAgentStore) GetByName(ctx context.Context, n string) (*store.Agent, error) {
	return nil, store.ErrNotFound
}
func (f *fakeAgentStore) GetByID(ctx context.Context, id string) (*store.Agent, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.agent, nil
}
func (f *fakeAgentStore) List(ctx context.Context) ([]store.Agent, error) { return nil, nil }
func (f *fakeAgentStore) UpdateStatus(ctx context.Context, id, status string, t time.Time) error {
	return nil
}
func (f *fakeAgentStore) Delete(ctx context.Context, id string) error { return nil }

// fakeSender 는 SendJobAssign 호출을 capture (Phase 10: env 인자도 capture).
type fakeSender struct {
	mu          sync.Mutex
	calls       int
	lastAgentID string
	lastPreview store.Preview
	lastEnv     map[string]string
	err         error
}

func (f *fakeSender) SendJobAssign(ctx context.Context, agentID string, p store.Preview, env map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastAgentID = agentID
	f.lastPreview = p
	f.lastEnv = env
	return f.err
}

func newTestDispatcher(t *testing.T, ps store.PreviewStore, as store.AgentStore, sender JobSender) *Dispatcher {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
	d := NewDispatcher(as, ps, nil, sender, logger) // Phase 10: rs=nil → hydration skip 기본 동작
	d.now = func() time.Time { return time.Unix(0, 0) }
	return d
}

func TestDispatcherOnReadyMatchesAndClaims(t *testing.T) {
	want := store.Preview{ID: "p2", RepoFullName: "acme/web", PrNumber: 2, Labels: []string{"test"}}
	ps := &dispFakePreviewStore{
		candidates: []store.Preview{
			{ID: "p1", Labels: []string{"prod"}},
			want,
			{ID: "p3", Labels: []string{"test"}},
		},
		claimReturn: &want,
	}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1", Labels: []string{"test"}}}
	sender := &fakeSender{}
	d := newTestDispatcher(t, ps, as, sender)

	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady: %v", err)
	}
	if sender.calls != 1 {
		t.Fatalf("sender calls=%d", sender.calls)
	}
	if sender.lastAgentID != "a1" || sender.lastPreview.ID != "p2" {
		t.Fatalf("got=%+v", sender.lastPreview)
	}
	// 매칭된 IDs 만 candidate 로 전달되었는지 (env=test 인 p2, p3).
	if len(ps.claimedIDs) != 2 {
		t.Fatalf("claimedIDs=%v want 2 entries", ps.claimedIDs)
	}
}

func TestDispatcherOnReadyNoMatch(t *testing.T) {
	ps := &dispFakePreviewStore{
		candidates: []store.Preview{
			{ID: "p1", Labels: []string{"prod"}},
		},
	}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1", Labels: []string{"test"}}}
	sender := &fakeSender{}
	d := newTestDispatcher(t, ps, as, sender)
	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady: %v", err)
	}
	if sender.calls != 0 {
		t.Fatalf("expected no JOB_ASSIGN; calls=%d", sender.calls)
	}
}

func TestDispatcherOnReadyEmptyQueue(t *testing.T) {
	ps := &dispFakePreviewStore{candidates: nil}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1"}}
	sender := &fakeSender{}
	d := newTestDispatcher(t, ps, as, sender)
	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady: %v", err)
	}
	if sender.calls != 0 {
		t.Fatalf("calls=%d", sender.calls)
	}
}

func TestDispatcherOnReadyClaimNotFoundIsNoOp(t *testing.T) {
	ps := &dispFakePreviewStore{
		candidates: []store.Preview{{ID: "p1", Labels: []string{}}},
		claimErr:   store.ErrNotFound,
	}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1"}}
	sender := &fakeSender{}
	d := newTestDispatcher(t, ps, as, sender)
	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady should swallow ErrNotFound: %v", err)
	}
	if sender.calls != 0 {
		t.Fatalf("calls=%d (no JOB_ASSIGN on Claim ErrNotFound)", sender.calls)
	}
}

func TestDispatcherOnReadyAgentNotFound(t *testing.T) {
	ps := &dispFakePreviewStore{}
	as := &fakeAgentStore{err: store.ErrNotFound}
	sender := &fakeSender{}
	d := newTestDispatcher(t, ps, as, sender)
	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady should swallow ErrNotFound: %v", err)
	}
}

func TestDispatcherOnReadyAgentStoreError(t *testing.T) {
	ps := &dispFakePreviewStore{}
	as := &fakeAgentStore{err: errors.New("db down")}
	sender := &fakeSender{}
	d := newTestDispatcher(t, ps, as, sender)
	if err := d.OnReady(context.Background(), "a1"); err == nil {
		t.Fatalf("expected error")
	}
}

// raceFakeStore 는 Phase 3 race 테스트용. ListQueuedForCandidates 는 항상 같은
// candidate 1건을 반환하지만 Claim 은 sync.Once 로 정확히 1번만 성공한다.
type raceFakeStore struct {
	mu          sync.Mutex
	claimed     int32
	candidate   store.Preview
	claimReturn store.Preview
}

func (f *raceFakeStore) Upsert(_ context.Context, _ store.Preview) (bool, *store.Preview, error) {
	return false, nil, errors.New("not used")
}
func (f *raceFakeStore) GetByID(_ context.Context, _ string) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *raceFakeStore) FindByHost(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *raceFakeStore) ListQueuedForCandidates(_ context.Context) ([]store.Preview, error) {
	return []store.Preview{f.candidate}, nil
}
func (f *raceFakeStore) Claim(_ context.Context, _ []string, _ string, _ time.Time) (*store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed > 0 {
		return nil, store.ErrNotFound
	}
	f.claimed++
	cp := f.claimReturn
	return &cp, nil
}
func (f *raceFakeStore) UpdateStatus(_ context.Context, _, _, _, _ string, _ time.Time, _ store.PreviewFields) error {
	return nil
}
func (f *raceFakeStore) ListRunningByAgent(_ context.Context, _ string) ([]store.Preview, error) {
	return nil, nil
}
func (f *raceFakeStore) ListStaleAssigned(_ context.Context, _ time.Time) ([]store.Preview, error) {
	return nil, nil
}
func (f *raceFakeStore) ListByAgent(_ context.Context, _ string, _ []string) ([]store.Preview, error) {
	return nil, nil
}
func (f *raceFakeStore) ListAll(_ context.Context) ([]store.Preview, error) { return nil, nil }
func (f *raceFakeStore) ListPreviewEvents(_ context.Context, _ string, _, _ int) ([]store.PreviewEvent, error) {
	return nil, nil
}
func (f *raceFakeStore) GetActiveByRepoAndPR(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *raceFakeStore) FindAdhocByBranch(_ context.Context, _, _ string) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *raceFakeStore) ListRepos(_ context.Context) ([]string, error) { return nil, nil }

// TestDispatcherClaimRace: 50 goroutine, 1 queued preview, 정확히 1 SendJobAssign.
func TestDispatcherClaimRace(t *testing.T) {
	want := store.Preview{ID: "p1", RepoFullName: "o/r", PrNumber: 1}
	ps := &raceFakeStore{candidate: want, claimReturn: want}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1"}}
	sender := &fakeSender{}
	d := newTestDispatcher(t, ps, as, sender)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = d.OnReady(context.Background(), "a1")
		}()
	}
	wg.Wait()

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.calls != 1 {
		t.Fatalf("SendJobAssign calls=%d want 1", sender.calls)
	}
}

// fakeRepoSecretStore 는 Phase 10 hydration 테스트용 in-memory mock.
// rows 는 (repo → key → value) 형식. err 가 non-nil 이면 List 가 항상 그 에러 반환.
type fakeRepoSecretStore struct {
	mu       sync.Mutex
	rows     map[string]map[string]string
	err      error
	listN    int    // List 호출 횟수
	lastRepo string // 마지막 List 의 repoFullName 인자
}

func newFakeRepoSecretStore() *fakeRepoSecretStore {
	return &fakeRepoSecretStore{rows: map[string]map[string]string{}}
}

func (f *fakeRepoSecretStore) List(_ context.Context, repoFullName string) ([]store.RepoSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listN++
	f.lastRepo = repoFullName
	if f.err != nil {
		return nil, f.err
	}
	kv := f.rows[repoFullName]
	out := make([]store.RepoSecret, 0, len(kv))
	for k, v := range kv {
		out = append(out, store.RepoSecret{RepoFullName: repoFullName, Key: k, Value: v})
	}
	return out, nil
}

func (f *fakeRepoSecretStore) Upsert(_ context.Context, s store.RepoSecret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows[s.RepoFullName] == nil {
		f.rows[s.RepoFullName] = map[string]string{}
	}
	f.rows[s.RepoFullName][s.Key] = s.Value
	return nil
}

func (f *fakeRepoSecretStore) Delete(_ context.Context, repo, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kv := f.rows[repo]; kv != nil {
		delete(kv, key)
	}
	return nil
}

func (f *fakeRepoSecretStore) DeleteAllFor(_ context.Context, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, repo)
	return nil
}

func (f *fakeRepoSecretStore) ListRepos(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.rows))
	for k := range f.rows {
		out = append(out, k)
	}
	return out, nil
}

// TestDispatcherOnReadyHydratesBuildEnv (F-20): hydrate 성공 → Sender 가 env 수신.
func TestDispatcherOnReadyHydratesBuildEnv(t *testing.T) {
	want := store.Preview{ID: "p2", RepoFullName: "acme/web", PrNumber: 2, Labels: []string{"test"}}
	ps := &dispFakePreviewStore{
		candidates:  []store.Preview{want},
		claimReturn: &want,
	}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1", Labels: []string{"test"}}}
	sender := &fakeSender{}
	rs := newFakeRepoSecretStore()
	rs.rows["acme/web"] = map[string]string{
		"DATABASE_URL": "postgres://x",
		"API_KEY":      "k1",
	}
	logger := slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
	d := NewDispatcher(as, ps, rs, sender, logger)

	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady: %v", err)
	}
	if sender.calls != 1 {
		t.Fatalf("sender.calls=%d want 1", sender.calls)
	}
	if rs.listN != 1 {
		t.Fatalf("RepoSecrets.List call count=%d want 1 (R-4 — claimed 1건당 1쿼리)", rs.listN)
	}
	if rs.lastRepo != "acme/web" {
		t.Fatalf("List repo=%q want acme/web", rs.lastRepo)
	}
	if len(sender.lastEnv) != 2 {
		t.Fatalf("env len=%d want 2: %+v", len(sender.lastEnv), sender.lastEnv)
	}
	if sender.lastEnv["DATABASE_URL"] != "postgres://x" {
		t.Fatalf("DATABASE_URL value mismatch: %q", sender.lastEnv["DATABASE_URL"])
	}
	if sender.lastEnv["API_KEY"] != "k1" {
		t.Fatalf("API_KEY value mismatch: %q", sender.lastEnv["API_KEY"])
	}
}

// TestDispatcherOnReadyHydrateError (F-21): List 에러 → WARN + 빈 env 로 dispatch 계속.
func TestDispatcherOnReadyHydrateError(t *testing.T) {
	want := store.Preview{ID: "p1", RepoFullName: "acme/web", PrNumber: 1}
	ps := &dispFakePreviewStore{
		candidates:  []store.Preview{want},
		claimReturn: &want,
	}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1"}}
	sender := &fakeSender{}
	rs := newFakeRepoSecretStore()
	rs.err = errors.New("db down")
	logger := slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
	d := NewDispatcher(as, ps, rs, sender, logger)

	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady should swallow hydrate error: %v", err)
	}
	if sender.calls != 1 {
		t.Fatalf("sender.calls=%d want 1 (dispatch must continue on hydrate err)", sender.calls)
	}
	if sender.lastEnv != nil {
		t.Fatalf("env should be nil on hydrate err; got=%+v", sender.lastEnv)
	}
}

// TestDispatcherOnReadyNilRepoSecretsSkipsHydration (F-21b): rs=nil → hydration 자체 skip.
func TestDispatcherOnReadyNilRepoSecretsSkipsHydration(t *testing.T) {
	want := store.Preview{ID: "p1", RepoFullName: "acme/web"}
	ps := &dispFakePreviewStore{
		candidates:  []store.Preview{want},
		claimReturn: &want,
	}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1"}}
	sender := &fakeSender{}
	logger := slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
	// 명시적으로 nil 주입.
	d := NewDispatcher(as, ps, nil, sender, logger)
	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady: %v", err)
	}
	if sender.calls != 1 {
		t.Fatalf("sender.calls=%d want 1", sender.calls)
	}
	if sender.lastEnv != nil {
		t.Fatalf("env should be nil with nil RepoSecrets; got=%+v", sender.lastEnv)
	}
}

// TestDispatcherOnReadyEmptySecretsResultsInNilEnv: 빈 row → nil env (omitempty 호환).
func TestDispatcherOnReadyEmptySecretsResultsInNilEnv(t *testing.T) {
	want := store.Preview{ID: "p1", RepoFullName: "acme/web"}
	ps := &dispFakePreviewStore{
		candidates:  []store.Preview{want},
		claimReturn: &want,
	}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1"}}
	sender := &fakeSender{}
	rs := newFakeRepoSecretStore() // rows 비어있음
	logger := slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
	d := NewDispatcher(as, ps, rs, sender, logger)

	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady: %v", err)
	}
	if sender.lastEnv != nil {
		t.Fatalf("empty rows should result in nil env (omitempty); got=%+v", sender.lastEnv)
	}
}

// TestDispatcherPause: Pause 후 OnReady → Claim 호출 0회 + SendJobAssign 0회.
func TestDispatcherPause(t *testing.T) {
	ps := &dispFakePreviewStore{
		candidates:  []store.Preview{{ID: "p1"}},
		claimReturn: &store.Preview{ID: "p1"},
	}
	as := &fakeAgentStore{agent: &store.Agent{ID: "a1"}}
	sender := &fakeSender{}
	d := newTestDispatcher(t, ps, as, sender)

	d.Pause()
	if !d.Paused() {
		t.Fatalf("Paused() = false; want true")
	}
	if err := d.OnReady(context.Background(), "a1"); err != nil {
		t.Fatalf("OnReady: %v", err)
	}
	if sender.calls != 0 {
		t.Fatalf("SendJobAssign calls=%d want 0 after Pause", sender.calls)
	}
	if len(ps.claimedIDs) != 0 {
		t.Fatalf("Claim called after Pause: %v", ps.claimedIDs)
	}
}
