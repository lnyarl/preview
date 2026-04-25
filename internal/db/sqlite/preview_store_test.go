package sqlitestore

import (
	"context"
	"path/filepath"
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
		Labels:       map[string]string{"env": "home"},
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
	evs, err := s.ListPreviewEvents(ctx, p.ID)
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
		Labels:       map[string]string{},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, _, err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert(new): %v", err)
	}
	// 같은 (repo, pr) 두 번째 호출: sha 변경.
	p2 := p
	p2.ID = uuid.NewString() // INSERT 가 시도하지만 ON CONFLICT 로 UPDATE 됨; 기존 row id 유지.
	p2.CommitSha = "def456"
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

	// 기존 row 의 commit_sha 가 갱신되었는지 확인.
	got, err := s.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.CommitSha != "def456" {
		t.Fatalf("sha=%s want def456", got.CommitSha)
	}
	if got.Status != "queued" {
		t.Fatalf("status=%s want queued (Upsert never changes status)", got.Status)
	}

	// 룰 R2: status 변경 없음 → 이벤트는 신규 INSERT 시점 1건만.
	evs, err := s.ListPreviewEvents(ctx, p.ID)
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
		Labels:       map[string]string{},
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
	evs, err := s.ListPreviewEvents(ctx, id)
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
		CommitSha: "abc", Labels: map[string]string{}, CreatedAt: now, UpdatedAt: now,
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
	for i := 1; i <= 3; i++ {
		p := store.Preview{
			ID: uuid.NewString(), RepoFullName: "acme/web", PrNumber: i,
			CommitSha: "abc", Labels: map[string]string{}, CreatedAt: now, UpdatedAt: now,
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

func TestPreviewStoreStubMethodsReturnNotImplemented(t *testing.T) {
	s := newTestPreviewStore(t)
	ctx := context.Background()
	if _, err := s.FindByHost(ctx, "x", 1); err != store.ErrNotImplementedStep1 {
		t.Fatalf("FindByHost err=%v", err)
	}
	if _, err := s.ListQueuedForCandidates(ctx); err != store.ErrNotImplementedStep1 {
		t.Fatalf("ListQueuedForCandidates err=%v", err)
	}
	if _, err := s.Claim(ctx, []string{"x"}, "a", time.Now()); err != store.ErrNotImplementedStep1 {
		t.Fatalf("Claim err=%v", err)
	}
	if _, err := s.ListRunningByAgent(ctx, "a"); err != store.ErrNotImplementedStep1 {
		t.Fatalf("ListRunningByAgent err=%v", err)
	}
	if _, err := s.ListStaleAssigned(ctx, time.Now()); err != store.ErrNotImplementedStep1 {
		t.Fatalf("ListStaleAssigned err=%v", err)
	}
	if _, err := s.ListByAgent(ctx, "a", []string{"queued"}); err != store.ErrNotImplementedStep1 {
		t.Fatalf("ListByAgent err=%v", err)
	}
}
