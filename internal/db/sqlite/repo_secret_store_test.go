// 이 파일의 책임:
//   - sqlite.RepoSecretStore 의 단위 테스트 (Phase 10 F-3 ~ F-8b).
//   - newTestRepoSecretStore: 임시 sqlite DB + migrations + Queries 조립.
//   - lifecycle: Upsert 신규 → List → Upsert 갱신 → Delete → DeleteAllFor.
//   - lowercase 정규화 (결정 15 / F-8b): "Foo/Bar" 입력 → "foo/bar" 로 저장 + 조회.
//   - ListRepos: 두 repo 등록 후 정렬 반환.
package sqlitestore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

func newTestRepoSecretStore(t *testing.T) (*RepoSecretStore, *PreviewStore) {
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
	return NewRepoSecretStore(New(db)), NewPreviewStore(db)
}

func TestRepoSecretUpsertList(t *testing.T) {
	s, _ := newTestRepoSecretStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// F-3: 신규 INSERT.
	if err := s.Upsert(ctx, store.RepoSecret{RepoFullName: "acme/web", Key: "API_KEY", Value: "v1", UpdatedAt: now}); err != nil {
		t.Fatalf("Upsert new: %v", err)
	}
	rows, err := s.List(ctx, "acme/web")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "API_KEY" || rows[0].Value != "v1" {
		t.Fatalf("after first Upsert rows=%+v", rows)
	}
	if rows[0].UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt zero after Upsert")
	}

	// F-4: 같은 (repo, key) 두 번 → 1행 + value 갱신 + updated_at 갱신.
	later := now.Add(2 * time.Second)
	if err := s.Upsert(ctx, store.RepoSecret{RepoFullName: "acme/web", Key: "API_KEY", Value: "v2", UpdatedAt: later}); err != nil {
		t.Fatalf("Upsert repeat: %v", err)
	}
	rows, err = s.List(ctx, "acme/web")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != "v2" {
		t.Fatalf("after second Upsert rows=%+v", rows)
	}
	if !rows[0].UpdatedAt.After(now) {
		t.Fatalf("UpdatedAt not advanced; first=%s second=%s", now, rows[0].UpdatedAt)
	}
}

func TestRepoSecretListSortAndScope(t *testing.T) {
	s, _ := newTestRepoSecretStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// F-6: List(repo) 가 key ASC 정렬 + 다른 repo 의 row 미포함.
	for _, kv := range []store.RepoSecret{
		{RepoFullName: "acme/web", Key: "B", Value: "b", UpdatedAt: now},
		{RepoFullName: "acme/web", Key: "A", Value: "a", UpdatedAt: now},
		{RepoFullName: "acme/web", Key: "C", Value: "c", UpdatedAt: now},
		{RepoFullName: "other/repo", Key: "X", Value: "x", UpdatedAt: now},
	} {
		if err := s.Upsert(ctx, kv); err != nil {
			t.Fatalf("seed Upsert: %v", err)
		}
	}
	rows, err := s.List(ctx, "acme/web")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len=%d want 3 (other/repo not leaking)", len(rows))
	}
	want := []string{"A", "B", "C"}
	for i, r := range rows {
		if r.Key != want[i] {
			t.Fatalf("rows[%d].Key=%q want %q", i, r.Key, want[i])
		}
	}
}

func TestRepoSecretDelete(t *testing.T) {
	s, _ := newTestRepoSecretStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, kv := range []store.RepoSecret{
		{RepoFullName: "acme/web", Key: "A", Value: "a", UpdatedAt: now},
		{RepoFullName: "acme/web", Key: "B", Value: "b", UpdatedAt: now},
	} {
		_ = s.Upsert(ctx, kv)
	}
	if err := s.Delete(ctx, "acme/web", "A"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows, _ := s.List(ctx, "acme/web")
	if len(rows) != 1 || rows[0].Key != "B" {
		t.Fatalf("after Delete rows=%+v", rows)
	}
	// 미존재 row Delete 는 no-op (에러 아님).
	if err := s.Delete(ctx, "acme/web", "NOPE"); err != nil {
		t.Fatalf("Delete nonexistent should be no-op: %v", err)
	}
}

func TestRepoSecretDeleteAllFor(t *testing.T) {
	s, _ := newTestRepoSecretStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = s.Upsert(ctx, store.RepoSecret{RepoFullName: "acme/web", Key: "A", Value: "a", UpdatedAt: now})
	_ = s.Upsert(ctx, store.RepoSecret{RepoFullName: "acme/web", Key: "B", Value: "b", UpdatedAt: now})
	_ = s.Upsert(ctx, store.RepoSecret{RepoFullName: "other/repo", Key: "X", Value: "x", UpdatedAt: now})

	if err := s.DeleteAllFor(ctx, "acme/web"); err != nil {
		t.Fatalf("DeleteAllFor: %v", err)
	}
	rows, _ := s.List(ctx, "acme/web")
	if len(rows) != 0 {
		t.Fatalf("rows should be empty after DeleteAllFor; got=%+v", rows)
	}
	other, _ := s.List(ctx, "other/repo")
	if len(other) != 1 {
		t.Fatalf("other repo row should be preserved; got=%+v", other)
	}
}

func TestRepoSecretListRepos(t *testing.T) {
	s, _ := newTestRepoSecretStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// F-7: distinct repo_full_name 정렬 반환.
	_ = s.Upsert(ctx, store.RepoSecret{RepoFullName: "z/last", Key: "A", Value: "1", UpdatedAt: now})
	_ = s.Upsert(ctx, store.RepoSecret{RepoFullName: "z/last", Key: "B", Value: "2", UpdatedAt: now})
	_ = s.Upsert(ctx, store.RepoSecret{RepoFullName: "a/first", Key: "X", Value: "x", UpdatedAt: now})

	repos, err := s.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("repos=%v want 2 distinct", repos)
	}
	if repos[0] != "a/first" || repos[1] != "z/last" {
		t.Fatalf("not sorted: %v", repos)
	}
}

func TestRepoSecretLowercaseNormalization(t *testing.T) {
	// F-8b / 결정 15: 입력 "Foo/Bar" 는 "foo/bar" 로 저장되어 같은 row 로 매칭.
	s, _ := newTestRepoSecretStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.Upsert(ctx, store.RepoSecret{RepoFullName: "Foo/Bar", Key: "API_KEY", Value: "v1", UpdatedAt: now}); err != nil {
		t.Fatalf("Upsert mixed-case: %v", err)
	}
	// 이미 lowercase 로 조회.
	rows, err := s.List(ctx, "foo/bar")
	if err != nil {
		t.Fatalf("List lowercase: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != "v1" {
		t.Fatalf("lookup lowercase fail rows=%+v", rows)
	}
	if rows[0].RepoFullName != "foo/bar" {
		t.Fatalf("stored RepoFullName=%q want lowercase", rows[0].RepoFullName)
	}
	// 같은 mixed-case 두 번째 Upsert 는 같은 row 갱신.
	later := now.Add(time.Second)
	if err := s.Upsert(ctx, store.RepoSecret{RepoFullName: "FoO/BAR", Key: "API_KEY", Value: "v2", UpdatedAt: later}); err != nil {
		t.Fatalf("Upsert second mixed-case: %v", err)
	}
	rows, _ = s.List(ctx, "FOO/BAR")
	if len(rows) != 1 || rows[0].Value != "v2" {
		t.Fatalf("after second Upsert rows=%+v", rows)
	}

	// Delete / DeleteAllFor 도 lowercase 정규화.
	if err := s.Delete(ctx, "FOO/BAR", "API_KEY"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows, _ = s.List(ctx, "foo/bar")
	if len(rows) != 0 {
		t.Fatalf("Delete should remove row; got=%+v", rows)
	}
}

func TestPreviewStoreListRepos(t *testing.T) {
	// F-7b: PreviewStore.ListRepos 가 distinct repo_full_name 을 정렬 반환.
	_, ps := newTestRepoSecretStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, p := range []store.Preview{
		{ID: "id-1", RepoFullName: "z/last", PrNumber: 1, Branch: "m", CreatedAt: now, UpdatedAt: now},
		{ID: "id-2", RepoFullName: "z/last", PrNumber: 2, Branch: "m", CreatedAt: now, UpdatedAt: now},
		{ID: "id-3", RepoFullName: "a/first", PrNumber: 3, Branch: "m", CreatedAt: now, UpdatedAt: now},
	} {
		if _, _, err := ps.Upsert(ctx, p); err != nil {
			t.Fatalf("seed Upsert: %v", err)
		}
	}
	repos, err := ps.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 || repos[0] != "a/first" || repos[1] != "z/last" {
		t.Fatalf("repos=%v want [a/first z/last]", repos)
	}
}
