// reconciler_test.go: tick 동작 검증 (stale → queued).
// internal/hub 에서는 sqlite 직접 import 가 금지되므로 (.golangci depguard),
// in-memory fake PreviewStore 로 단위 테스트를 구성한다.
package hub

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

// reconcilerFakeStore 는 reconciler 단위 테스트용 in-memory PreviewStore.
type reconcilerFakeStore struct {
	mu   sync.Mutex
	rows map[string]*store.Preview // id → preview
}

func newReconcilerFakeStore() *reconcilerFakeStore {
	return &reconcilerFakeStore{rows: map[string]*store.Preview{}}
}

func (f *reconcilerFakeStore) put(p store.Preview) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := p
	f.rows[p.ID] = &cp
}

func (f *reconcilerFakeStore) GetByID(_ context.Context, id string) (*store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (f *reconcilerFakeStore) ListStaleAssigned(_ context.Context, staleAfter time.Time) ([]store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Preview
	for _, p := range f.rows {
		if p.Status == "assigned" && p.UpdatedAt.Before(staleAfter) {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *reconcilerFakeStore) ListAll(_ context.Context) ([]store.Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Preview, 0, len(f.rows))
	for _, p := range f.rows {
		out = append(out, *p)
	}
	return out, nil
}

func (f *reconcilerFakeStore) UpdateStatus(_ context.Context, id, fromStatus, toStatus, _ string, now time.Time, _ store.PreviewFields) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return store.ErrNotFound
	}
	if fromStatus != "" && r.Status != fromStatus {
		return store.ErrStaleState
	}
	r.Status = toStatus
	r.UpdatedAt = now
	return nil
}

// 미사용 메서드 — 본 테스트에서 호출되지 않음.
func (f *reconcilerFakeStore) Upsert(_ context.Context, _ store.Preview) (bool, *store.Preview, error) {
	panic("not impl")
}
func (f *reconcilerFakeStore) FindByHost(_ context.Context, _ string, _ int) (*store.Preview, error) {
	panic("not impl")
}
func (f *reconcilerFakeStore) ListQueuedForCandidates(_ context.Context) ([]store.Preview, error) {
	panic("not impl")
}
func (f *reconcilerFakeStore) Claim(_ context.Context, _ []string, _ string, _ time.Time) (*store.Preview, error) {
	panic("not impl")
}
func (f *reconcilerFakeStore) ListRunningByAgent(_ context.Context, _ string) ([]store.Preview, error) {
	panic("not impl")
}
func (f *reconcilerFakeStore) ListByAgent(_ context.Context, _ string, _ []string) ([]store.Preview, error) {
	panic("not impl")
}
func (f *reconcilerFakeStore) GetActiveByRepoAndPR(_ context.Context, _ string, _ int) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *reconcilerFakeStore) FindAdhocByBranch(_ context.Context, _, _ string) (*store.Preview, error) {
	return nil, store.ErrNotFound
}
func (f *reconcilerFakeStore) ListRepos(_ context.Context) ([]string, error) { return nil, nil }
func (f *reconcilerFakeStore) ListPreviewEvents(_ context.Context, _ string, _, _ int) ([]store.PreviewEvent, error) {
	return nil, nil
}

func TestReconcilerStaleAssignedRequeued(t *testing.T) {
	fs := newReconcilerFakeStore()
	reg := NewConnRegistry()
	rc := NewReconciler(fs, reg, slog.Default())

	ctx := context.Background()
	now := time.Now().UTC()

	// stale: assigned 이고 updated_at 이 10분 전.
	stale := store.Preview{
		ID:        "stale-id",
		Status:    "assigned",
		PrNumber:  1,
		UpdatedAt: now.Add(-10 * time.Minute),
	}
	fs.put(stale)
	// fresh: assigned 이고 updated_at 이 1분 전 (5m 임계 미만 → fresh).
	fresh := store.Preview{
		ID:        "fresh-id",
		Status:    "assigned",
		PrNumber:  2,
		UpdatedAt: now.Add(-1 * time.Minute),
	}
	fs.put(fresh)

	rc.tick(ctx, 5*time.Minute)

	// stale → queued.
	got, err := fs.GetByID(ctx, "stale-id")
	if err != nil {
		t.Fatalf("GetByID stale: %v", err)
	}
	if got.Status != "queued" {
		t.Errorf("stale: want status=queued, got %s", got.Status)
	}

	// fresh → still assigned.
	gotFresh, err := fs.GetByID(ctx, "fresh-id")
	if err != nil {
		t.Fatalf("GetByID fresh: %v", err)
	}
	if gotFresh.Status != "assigned" {
		t.Errorf("fresh: want status=assigned, got %s", gotFresh.Status)
	}
}

func TestReconcilerOrphanRunningCounted(t *testing.T) {
	fs := newReconcilerFakeStore()
	reg := NewConnRegistry()
	rc := NewReconciler(fs, reg, slog.Default())

	now := time.Now().UTC()
	offlineAgent := "offline-agent"

	// running preview, agent offline (registry 에 없음).
	fs.put(store.Preview{
		ID:              "orphan-id",
		Status:          "running",
		PrNumber:        1,
		AssignedAgentID: &offlineAgent,
		UpdatedAt:       now,
	})

	// tick 만 호출 — 핵심 검증은 status 가 변경되지 않음 (보존).
	rc.tick(context.Background(), 5*time.Minute)

	got, err := fs.GetByID(context.Background(), "orphan-id")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("orphan running 보존 실패: status=%s want running", got.Status)
	}
}
