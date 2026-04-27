package sqlitestore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/store"
)

func newTestPreviewStore(t *testing.T) *PreviewStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	dsn := "sqlite://" + path
	if _, err := MigrateUp(dsn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	ctx := context.Background()
	db, err := OpenURL(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenURL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewPreviewStore(db)
}

// seedAgent 는 Claim 의 FK 제약을 만족시키기 위해 더미 agent row 를 INSERT.
// 다중 host 에 대비해 이름 충돌 시 무시.
func seedAgent(t *testing.T, ps *PreviewStore, id string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := ps.db.ExecContext(context.Background(),
		`INSERT INTO agents (id, name, token_hash, labels, status, created_at) VALUES (?, ?, '', '{}', 'offline', ?)`,
		id, "agent-"+id, now)
	if err != nil {
		t.Fatalf("seedAgent %s: %v", id, err)
	}
}

func TestPreviewStoreUpsertNewInsertsEvent(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := store.Preview{
		ID:           uuid.NewString(),
		RepoFullName: "acme/web",
		PrNumber:     42,
		CommitSha:    "abc123",
		Branch:       "feature/x",
		Labels:       []string{"home"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	created, prev, err := s.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !created {
		t.Fatalf("created=false want true")
	}
	if prev != nil {
		t.Fatalf("prev=%+v want nil", prev)
	}

	got, err := s.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "queued" {
		t.Fatalf("status=%s want queued", got.Status)
	}
	if got.CommitSha != "abc123" {
		t.Fatalf("sha=%s", got.CommitSha)
	}

	// 룰 R1: 신규 INSERT → event 1건 (NULL → queued).
	evs, err := s.ListPreviewEventsRaw(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListPreviewEvents: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events=%d want 1", len(evs))
	}
	if evs[0].FromStatus != nil {
		t.Fatalf("from=%v want nil", evs[0].FromStatus)
	}
	if evs[0].ToStatus != "queued" {
		t.Fatalf("to=%s want queued", evs[0].ToStatus)
	}
}

// TestPreviewStoreUpsertExistingUpdatesNoEvent: Phase 9 자연키가 (repo, sha) 로 변경되었으므로
// "같은 sha 두 번 Upsert → 두 번째는 UPDATE" 시나리오로 재작성. 같은 (repo, sha) 두 번째 호출은
// branch / labels / repo_clone_url 만 갱신하고 status / event 는 변경 없음 (R2).
func TestPreviewStoreUpsertExistingUpdatesNoEvent(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := store.Preview{
		ID:           uuid.NewString(),
		RepoFullName: "acme/web",
		PrNumber:     42,
		CommitSha:    "abc123",
		Branch:       "feature/x",
		Labels:       []string{},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, _, err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert(new): %v", err)
	}
	// 같은 (repo, sha) 두 번째 호출 — branch 만 변경 (멱등 + UPDATE 분기).
	p2 := p
	p2.ID = uuid.NewString() // INSERT 가 시도하지만 ON CONFLICT 로 기존 row UPDATE; 기존 ID 유지.
	p2.Branch = "feature/x-renamed"
	p2.UpdatedAt = now.Add(time.Second)
	created, prev, err := s.Upsert(ctx, p2)
	if err != nil {
		t.Fatalf("Upsert(update): %v", err)
	}
	if created {
		t.Fatalf("created=true want false")
	}
	if prev == nil {
		t.Fatalf("prev=nil want existing row")
	}
	if prev.Status != "queued" {
		t.Fatalf("prev.status=%s", prev.Status)
	}

	// 기존 row 의 branch 가 갱신되었는지 확인 (sha 는 키이므로 그대로).
	got, err := s.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.CommitSha != "abc123" {
		t.Fatalf("sha=%s want abc123 (key unchanged)", got.CommitSha)
	}
	if got.Branch != "feature/x-renamed" {
		t.Fatalf("branch=%s want feature/x-renamed", got.Branch)
	}
	if got.Status != "queued" {
		t.Fatalf("status=%s want queued (Upsert never changes status)", got.Status)
	}

	// 룰 R2: status 변경 없음 → 이벤트는 신규 INSERT 시점 1건만.
	evs, err := s.ListPreviewEventsRaw(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListPreviewEvents: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events=%d want 1 (no event from Upsert update)", len(evs))
	}
}

func TestPreviewStoreUpdateStatusInsertsEvent(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := uuid.NewString()
	p := store.Preview{
		ID:           id,
		RepoFullName: "acme/web",
		PrNumber:     7,
		CommitSha:    "abc",
		Labels: []string{},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, _, err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	errMsg := "closed_via_webhook"
	if err := s.UpdateStatus(ctx, id, "", "teardown", errMsg, now.Add(time.Second), store.PreviewFields{ErrorMessage: &errMsg}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "teardown" {
		t.Fatalf("status=%s want teardown", got.Status)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("error_message=%v", got.ErrorMessage)
	}
	// 룰 R1: 신규 1건(insert) + UpdateStatus 1건 = 2건.
	evs, err := s.ListPreviewEventsRaw(ctx, id)
	if err != nil {
		t.Fatalf("ListPreviewEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("events=%d want 2", len(evs))
	}
	last := evs[len(evs)-1]
	if last.FromStatus == nil || *last.FromStatus != "queued" {
		t.Fatalf("last.from=%v want queued", last.FromStatus)
	}
	if last.ToStatus != "teardown" {
		t.Fatalf("last.to=%s want teardown", last.ToStatus)
	}
}

func TestPreviewStoreUpdateStatusCASStaleState(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := uuid.NewString()
	p := store.Preview{
		ID: id, RepoFullName: "acme/web", PrNumber: 1,
		CommitSha: "abc", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// fromStatus="running" 인데 실제는 "queued" → ErrStaleState.
	err := s.UpdateStatus(ctx, id, "running", "done", "", now, store.PreviewFields{})
	if err != store.ErrStaleState {
		t.Fatalf("err=%v want ErrStaleState", err)
	}
}

func TestPreviewStoreListAll(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// Phase 9: 자연키가 (repo, sha) 이므로 각 row 마다 unique sha 사용.
	for i := 1; i <= 3; i++ {
		p := store.Preview{
			ID: uuid.NewString(), RepoFullName: "acme/web", PrNumber: i,
			CommitSha: fmt.Sprintf("sha-%d", i), Labels: []string{}, CreatedAt: now, UpdatedAt: now,
		}
		if _, _, err := s.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	list, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len=%d want 3", len(list))
	}
}

func TestPreviewStoreGetByIDNotFound(t *testing.T) {
	s := newTestPreviewStore(t)
	if _, err := s.GetByID(context.Background(), "no-such"); err != store.ErrNotFound {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
}

func TestPreviewStoreListQueuedForCandidates(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 3개 INSERT, 가운데 1개를 UpdateStatus 로 teardown 으로 옮김 → 큐에 2개 남음.
	// Phase 9: 자연키가 (repo, sha) 이므로 각 row 마다 unique sha.
	ids := []string{}
	for i := 1; i <= 3; i++ {
		id := uuid.NewString()
		ids = append(ids, id)
		p := store.Preview{
			ID: id, RepoFullName: "acme/web", PrNumber: i,
			CommitSha: fmt.Sprintf("sha-%d", i), Labels: []string{}, CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if _, _, err := s.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	if err := s.UpdateStatus(ctx, ids[1], "", "teardown", "", now, store.PreviewFields{}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := s.ListQueuedForCandidates(ctx)
	if err != nil {
		t.Fatalf("ListQueuedForCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	// ASC by created_at: ids[0] 가 첫번째.
	if got[0].ID != ids[0] {
		t.Fatalf("first=%s want %s", got[0].ID, ids[0])
	}
	if got[1].ID != ids[2] {
		t.Fatalf("second=%s want %s", got[1].ID, ids[2])
	}
}

func TestPreviewStoreClaimSuccess(t *testing.T) {
	s := newTestPreviewStore(t)
	seedAgent(t, s, "agent-1")
	seedAgent(t, s, "agent-2")
	ctx := context.Background()
	now := time.Now().UTC()
	id := uuid.NewString()
	p := store.Preview{
		ID: id, RepoFullName: "acme/web", PrNumber: 1,
		CommitSha: "abc", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Claim(ctx, []string{id}, "agent-1", now.Add(time.Second))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got.Status != "assigned" {
		t.Fatalf("status=%s want assigned", got.Status)
	}
	if got.AssignedAgentID == nil || *got.AssignedAgentID != "agent-1" {
		t.Fatalf("agent_id=%v", got.AssignedAgentID)
	}
	// 두 번째 Claim 은 ErrNotFound (이미 assigned).
	_, err = s.Claim(ctx, []string{id}, "agent-2", now.Add(2*time.Second))
	if err != store.ErrNotFound {
		t.Fatalf("second Claim err=%v want ErrNotFound", err)
	}
}

func TestPreviewStoreClaimEmptyCandidates(t *testing.T) {
	s := newTestPreviewStore(t)
	_, err := s.Claim(context.Background(), nil, "agent-1", time.Now())
	if err != store.ErrNotFound {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
}

// TestClaimPreviewRace 는 50 goroutine 이 동시에 단일 candidate 를 Claim 시도해도
// 정확히 1개만 성공함을 검증한다 (NF-Test-Race-1, F-S2-2).
func TestClaimPreviewRace(t *testing.T) {
	s := newTestPreviewStore(t)
	const N = 50
	for i := 0; i < N; i++ {
		seedAgent(t, s, fmt.Sprintf("agent-%d", i))
	}
	ctx := context.Background()
	now := time.Now().UTC()
	id := uuid.NewString()
	p := store.Preview{
		ID: id, RepoFullName: "acme/web", PrNumber: 1,
		CommitSha: "abc", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	successes := make(chan bool, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Claim(ctx, []string{id}, fmt.Sprintf("agent-%d", i), time.Now())
			successes <- err == nil
		}(i)
	}
	wg.Wait()
	close(successes)
	count := 0
	for ok := range successes {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("success count=%d want 1", count)
	}
}

// TestClaimPreviewMultiCandidateRace 는 10 candidate × 10 goroutine 시 정확히
// 10 unique 점유, 중복 0 임을 검증한다 (NF-Test-Race-2, F-S2-2-b).
func TestClaimPreviewMultiCandidateRace(t *testing.T) {
	s := newTestPreviewStore(t)
	const M = 10
	for i := 0; i < M; i++ {
		seedAgent(t, s, fmt.Sprintf("agent-%d", i))
	}
	ctx := context.Background()
	now := time.Now().UTC()
	ids := []string{}
	for i := 0; i < M; i++ {
		id := uuid.NewString()
		ids = append(ids, id)
		p := store.Preview{
			ID: id, RepoFullName: "acme/web", PrNumber: i + 1,
			CommitSha: fmt.Sprintf("multi-sha-%d", i), Labels: []string{}, CreatedAt: now.Add(time.Duration(i) * time.Millisecond), UpdatedAt: now,
		}
		if _, _, err := s.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	type res struct {
		id  string
		err error
	}
	results := make(chan res, M)
	var wg sync.WaitGroup
	for i := 0; i < M; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := s.Claim(ctx, ids, fmt.Sprintf("agent-%d", i), time.Now())
			if err == nil {
				results <- res{id: p.ID}
			} else {
				results <- res{err: err}
			}
		}(i)
	}
	wg.Wait()
	close(results)
	seen := map[string]int{}
	successes := 0
	for r := range results {
		if r.err == nil {
			successes++
			seen[r.id]++
		}
	}
	if successes != M {
		t.Fatalf("successes=%d want %d", successes, M)
	}
	if len(seen) != M {
		t.Fatalf("unique ids=%d want %d", len(seen), M)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("id=%s claimed %d times (want 1)", id, c)
		}
	}
}

func TestPreviewStoreUpdateStatusFields(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := uuid.NewString()
	p := store.Preview{
		ID: id, RepoFullName: "acme/web", PrNumber: 1,
		CommitSha: "abc", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	host := "127.0.0.1"
	port := 12345
	cid := "container-abc"
	if err := s.UpdateStatus(ctx, id, "", "running", "", now.Add(time.Second), store.PreviewFields{
		ContainerID: &cid,
		AgentHost:   &host,
		AgentPort:   &port,
	}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "running" || got.AgentHost == nil || *got.AgentHost != host ||
		got.AgentPort == nil || *got.AgentPort != port || got.ContainerID == nil || *got.ContainerID != cid {
		t.Fatalf("fields not persisted: %+v", got)
	}
	// 두 번째 호출에서 fields nil → 기존 값 보존.
	if err := s.UpdateStatus(ctx, id, "running", "teardown", "", now.Add(2*time.Second), store.PreviewFields{}); err != nil {
		t.Fatalf("UpdateStatus 2: %v", err)
	}
	got2, _ := s.GetByID(ctx, id)
	if got2.AgentHost == nil || *got2.AgentHost != host {
		t.Fatalf("host not preserved: %v", got2.AgentHost)
	}
}

func TestPreviewStoreResetAllAssigned(t *testing.T) {
	s := newTestPreviewStore(t)
	seedAgent(t, s, "agent-1")
	seedAgent(t, s, "agent-2")
	ctx := context.Background()
	now := time.Now().UTC()
	// 2개 assigned + 1개 queued 상태 만들기. Phase 9: unique sha per row.
	ids := []string{}
	for i := 0; i < 3; i++ {
		id := uuid.NewString()
		ids = append(ids, id)
		p := store.Preview{
			ID: id, RepoFullName: "acme/web", PrNumber: i + 1,
			CommitSha: fmt.Sprintf("sha-r-%d", i), Labels: []string{}, CreatedAt: now, UpdatedAt: now,
		}
		if _, _, err := s.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	if _, err := s.Claim(ctx, []string{ids[0]}, "agent-1", now); err != nil {
		t.Fatalf("Claim 1: %v", err)
	}
	if _, err := s.Claim(ctx, []string{ids[1]}, "agent-2", now); err != nil {
		t.Fatalf("Claim 2: %v", err)
	}
	n, err := s.ResetAllAssigned(ctx)
	if err != nil {
		t.Fatalf("ResetAllAssigned: %v", err)
	}
	if n != 2 {
		t.Fatalf("n=%d want 2", n)
	}
	for _, id := range ids[:2] {
		got, _ := s.GetByID(ctx, id)
		if got.Status != "queued" {
			t.Fatalf("id=%s status=%s want queued", id, got.Status)
		}
		// 이벤트 마지막은 assigned → queued.
		evs, _ := s.ListPreviewEventsRaw(ctx, id)
		last := evs[len(evs)-1]
		if last.FromStatus == nil || *last.FromStatus != "assigned" || last.ToStatus != "queued" {
			t.Fatalf("id=%s last event=%+v", id, last)
		}
	}
}

// TestPreviewStoreStep3Lookups 는 Step 3 에서 stub 을 대체한 4 개 메서드의
// happy-path 동작을 확인한다. (이전 step 의 stub 검증 테스트는 본 Step 에서 제거.)
func TestPreviewStoreStep3Lookups(t *testing.T) {
	s := newTestPreviewStore(t)
	seedAgent(t, s, "agent-1")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// (1) PR=1: queued 로 시작.
	id1 := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: id1, RepoFullName: "acme/web", PrNumber: 1,
		CommitSha: "aa", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert(1): %v", err)
	}
	if _, err := s.Claim(ctx, []string{id1}, "agent-1", now); err != nil {
		t.Fatalf("Claim(1): %v", err)
	}
	// stale 임계 통과를 위해 updated_at 을 10분 전으로 강제.
	stale := now.Add(-10 * time.Minute).Format(iso8601)
	if _, err := s.db.ExecContext(ctx, `UPDATE previews SET updated_at = ? WHERE id = ?`, stale, id1); err != nil {
		t.Fatalf("force stale: %v", err)
	}

	// FindByHost: PR 1 → id1 매칭.
	got, err := s.FindByHost(ctx, "acme/web", 1)
	if err != nil {
		t.Fatalf("FindByHost: %v", err)
	}
	if got.ID != id1 {
		t.Fatalf("FindByHost id=%s want %s", got.ID, id1)
	}
	if _, err := s.FindByHost(ctx, "acme/web", 999); err != store.ErrNotFound {
		t.Fatalf("FindByHost(missing) err=%v want ErrNotFound", err)
	}

	// ListStaleAssigned: 5분 임계로 id1 매칭.
	stales, err := s.ListStaleAssigned(ctx, now.Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("ListStaleAssigned: %v", err)
	}
	if len(stales) != 1 || stales[0].ID != id1 {
		t.Fatalf("ListStaleAssigned=%+v want [%s]", stales, id1)
	}

	// (2) PR=2: running + agent-1 할당.
	id2 := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: id2, RepoFullName: "acme/web", PrNumber: 2,
		CommitSha: "bb", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert(2): %v", err)
	}
	if _, err := s.Claim(ctx, []string{id2}, "agent-1", now); err != nil {
		t.Fatalf("Claim(2): %v", err)
	}
	agentID := "agent-1"
	if err := s.UpdateStatus(ctx, id2, "assigned", "running", "", now,
		store.PreviewFields{AssignedAgentID: &agentID}); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}

	running, err := s.ListRunningByAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListRunningByAgent: %v", err)
	}
	if len(running) != 1 || running[0].ID != id2 {
		t.Fatalf("ListRunningByAgent=%+v want [%s]", running, id2)
	}

	// ListByAgent("agent-1", [assigned, running]) → id1 + id2 (둘 다 매칭).
	all, err := s.ListByAgent(ctx, "agent-1", []string{"assigned", "running"})
	if err != nil {
		t.Fatalf("ListByAgent: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListByAgent len=%d want 2", len(all))
	}

	// 빈 statuses → nil.
	empty, err := s.ListByAgent(ctx, "agent-1", nil)
	if err != nil {
		t.Fatalf("ListByAgent(empty): %v", err)
	}
	if empty != nil {
		t.Fatalf("ListByAgent(empty)=%+v want nil", empty)
	}
}

// TestPreviewStoreUpsertSameShaIdempotent (F-1, F-8 일부): 같은 (repo, sha) 두 번 →
// row 1개. ON CONFLICT 가 발동해 두 번째 호출은 UPDATE.
func TestPreviewStoreUpsertSameShaIdempotent(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		p := store.Preview{
			ID: uuid.NewString(), RepoFullName: "acme/web", PrNumber: 7,
			CommitSha: "shared-sha", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
		}
		if _, _, err := s.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert(%d): %v", i, err)
		}
	}
	list, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("rows=%d want 1 (idempotent on same sha)", len(list))
	}
}

// TestPreviewStoreUpsertDifferentShaInsertsRows (F-1, F-7): 같은 (repo, pr) 다른 sha →
// row 2 개. UNIQUE (repo, sha) 자연키 변경 검증.
func TestPreviewStoreUpsertDifferentShaInsertsRows(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i, sha := range []string{"sha-A", "sha-B"} {
		p := store.Preview{
			ID: uuid.NewString(), RepoFullName: "acme/web", PrNumber: 7,
			CommitSha: sha, Labels: []string{}, CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now,
		}
		if _, _, err := s.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert(%s): %v", sha, err)
		}
	}
	list, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("rows=%d want 2 (different sha → distinct rows)", len(list))
	}
}

// TestPreviewStoreUpsertEmptyShaInsertsMultipleRows (F-9): sha="" (NULL) 두 번 → row 2개.
// SQLite NULL UNIQUE 미발동 규칙 검증.
func TestPreviewStoreUpsertEmptyShaInsertsMultipleRows(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		p := store.Preview{
			ID: uuid.NewString(), RepoFullName: "acme/web", PrNumber: 7,
			CommitSha: "", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
		}
		if _, _, err := s.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert(%d): %v", i, err)
		}
	}
	list, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("rows=%d want 2 (empty sha → NULL → no UNIQUE conflict)", len(list))
	}
}

// TestPreviewStoreUpsertIsAdhocFlagPreserved (F-2, F-3, F-5): IsAdhoc 필드 INSERT/READ.
func TestPreviewStoreUpsertIsAdhocFlagPreserved(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Adhoc=true row.
	idAdhoc := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: idAdhoc, RepoFullName: "x/y", PrNumber: 0,
		CommitSha: "adhoc-sha", Labels: []string{}, IsAdhoc: true,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert(adhoc): %v", err)
	}
	got, err := s.GetByID(ctx, idAdhoc)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.IsAdhoc {
		t.Errorf("IsAdhoc=false, want true (Adhoc row)")
	}

	// Default(false) row.
	idWebhook := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: idWebhook, RepoFullName: "x/y", PrNumber: 1,
		CommitSha: "webhook-sha", Labels: []string{},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert(webhook): %v", err)
	}
	got2, err := s.GetByID(ctx, idWebhook)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got2.IsAdhoc {
		t.Errorf("IsAdhoc=true, want false (default)")
	}
}

// TestPreviewStoreUpdateStatusFillsCommitShaWhenNull (F-4, F-22): NULL sha row 에
// fields.CommitSha=&"abc" → row.CommitSha == "abc". 같은 BeginTx 안에서 SELECT → COALESCE
// 채움 시퀀스를 행동으로 검증.
func TestPreviewStoreUpdateStatusFillsCommitShaWhenNull(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: id, RepoFullName: "acme/web", PrNumber: 1,
		CommitSha: "", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	sha := "ff00aa"
	if err := s.UpdateStatus(ctx, id, "", "building", "", now.Add(time.Second),
		store.PreviewFields{CommitSha: &sha}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ := s.GetByID(ctx, id)
	if got.CommitSha != "ff00aa" {
		t.Fatalf("sha=%q want ff00aa", got.CommitSha)
	}
}

// TestPreviewStoreUpdateStatusErrShaConflictRejectsDifferentSha (F-4, F-22): 이미 채워진 sha
// row 에 다른 sha 보고 → ErrShaConflict + row.CommitSha 보존.
func TestPreviewStoreUpdateStatusErrShaConflictRejectsDifferentSha(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: id, RepoFullName: "acme/web", PrNumber: 1,
		CommitSha: "abc", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	other := "def"
	err := s.UpdateStatus(ctx, id, "", "building", "", now.Add(time.Second),
		store.PreviewFields{CommitSha: &other})
	if !errors.Is(err, store.ErrShaConflict) {
		t.Fatalf("err=%v want ErrShaConflict", err)
	}
	got, _ := s.GetByID(ctx, id)
	if got.CommitSha != "abc" {
		t.Fatalf("sha=%q want abc (preserved)", got.CommitSha)
	}
}

// TestPreviewStoreUpdateStatusSameShaIsHarmless (F-22): 이미 같은 sha 인 row 에 같은 sha
// 보고 → 통과. (idempotent 보고 시나리오 — Agent 가 building 송신 두 번 또는 webhook race)
func TestPreviewStoreUpdateStatusSameShaIsHarmless(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: id, RepoFullName: "acme/web", PrNumber: 1,
		CommitSha: "abc", Labels: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	same := "abc"
	if err := s.UpdateStatus(ctx, id, "", "building", "", now.Add(time.Second),
		store.PreviewFields{CommitSha: &same}); err != nil {
		t.Fatalf("UpdateStatus(same sha): %v", err)
	}
	got, _ := s.GetByID(ctx, id)
	if got.CommitSha != "abc" {
		t.Fatalf("sha=%q want abc", got.CommitSha)
	}
	if got.Status != "building" {
		t.Fatalf("status=%s want building", got.Status)
	}
}

// TestPreviewStoreGetActiveByRepoAndPR (F-6): status ∈ {queued, assigned, building, running}
// 만 매칭. done 만 있으면 ErrNotFound, queued+done 섞이면 queued 반환.
func TestPreviewStoreGetActiveByRepoAndPR(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// (1) done row 만 있음 → ErrNotFound.
	doneID := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: doneID, RepoFullName: "acme/web", PrNumber: 5,
		CommitSha: "done-sha", Labels: []string{},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Upsert(done): %v", err)
	}
	if err := s.UpdateStatus(ctx, doneID, "", "done", "", now.Add(time.Second), store.PreviewFields{}); err != nil {
		t.Fatalf("UpdateStatus(done): %v", err)
	}
	if _, err := s.GetActiveByRepoAndPR(ctx, "acme/web", 5); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound (only done rows)", err)
	}

	// (2) 같은 (repo, pr) 에 building row 추가 → building 반환.
	buildID := uuid.NewString()
	if _, _, err := s.Upsert(ctx, store.Preview{
		ID: buildID, RepoFullName: "acme/web", PrNumber: 5,
		CommitSha: "build-sha", Labels: []string{},
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("Upsert(build): %v", err)
	}
	if err := s.UpdateStatus(ctx, buildID, "queued", "building", "", now.Add(3*time.Second), store.PreviewFields{}); err != nil {
		t.Fatalf("UpdateStatus(build): %v", err)
	}
	got, err := s.GetActiveByRepoAndPR(ctx, "acme/web", 5)
	if err != nil {
		t.Fatalf("GetActiveByRepoAndPR: %v", err)
	}
	if got.ID != buildID {
		t.Fatalf("id=%s want %s (building row)", got.ID, buildID)
	}
}
