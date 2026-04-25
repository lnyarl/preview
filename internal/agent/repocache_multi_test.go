package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMultiRepoCacheGetOrCreateSameURL 는 같은 repoURL 이 동일 *RepoCache 를 반환함을 검증.
func TestMultiRepoCacheGetOrCreateSameURL(t *testing.T) {
	m := NewMultiRepoCache(t.TempDir(), nil)
	rc1 := m.getOrCreate("https://github.com/owner/repo")
	rc2 := m.getOrCreate("https://github.com/owner/repo")
	if rc1 != rc2 {
		t.Fatal("same repoURL should return same *RepoCache")
	}
}

// TestMultiRepoCacheGetOrCreateDifferentURLs 는 다른 repoURL 이 별도 *RepoCache 를 반환함을 검증.
func TestMultiRepoCacheGetOrCreateDifferentURLs(t *testing.T) {
	workDir := t.TempDir()
	m := NewMultiRepoCache(workDir, nil)
	rcA := m.getOrCreate("https://github.com/owner/repo-a")
	rcB := m.getOrCreate("https://github.com/owner/repo-b")
	if rcA == rcB {
		t.Fatal("different repoURLs should return different *RepoCache instances")
	}
	if rcA.RepoDir() == rcB.RepoDir() {
		t.Fatalf("repo dirs should differ: %s vs %s", rcA.RepoDir(), rcB.RepoDir())
	}
}

// TestMultiRepoCacheEnsureDelegate 는 Ensure 가 올바른 RepoCache 에 위임함을 검증.
func TestMultiRepoCacheEnsureDelegate(t *testing.T) {
	workDir := t.TempDir()
	m := NewMultiRepoCache(workDir, nil)
	repoURL := "https://github.com/owner/repo"

	rc := m.getOrCreate(repoURL)
	r := &fakeRunner{}
	rc.SetRunner(r)

	if err := m.Ensure(context.Background(), repoURL); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := r.countCalls("clone --bare"); got != 1 {
		t.Fatalf("clone calls=%d want 1", got)
	}
}

// TestMultiRepoCacheTwoReposEnsureIndependent 는 두 repoURL 이 서로 다른 경로에 clone 됨을 검증.
func TestMultiRepoCacheTwoReposEnsureIndependent(t *testing.T) {
	workDir := t.TempDir()
	m := NewMultiRepoCache(workDir, nil)

	repoA := "https://github.com/owner/repo-a"
	repoB := "https://github.com/owner/repo-b"

	rcA := m.getOrCreate(repoA)
	rcB := m.getOrCreate(repoB)
	rA := &fakeRunner{}
	rB := &fakeRunner{}
	rcA.SetRunner(rA)
	rcB.SetRunner(rB)

	ctx := context.Background()
	if err := m.Ensure(ctx, repoA); err != nil {
		t.Fatalf("Ensure repoA: %v", err)
	}
	if err := m.Ensure(ctx, repoB); err != nil {
		t.Fatalf("Ensure repoB: %v", err)
	}
	if got := rA.countCalls("clone --bare"); got != 1 {
		t.Fatalf("repoA clone calls=%d want 1", got)
	}
	if got := rB.countCalls("clone --bare"); got != 1 {
		t.Fatalf("repoB clone calls=%d want 1", got)
	}
	// 각 RepoCache 의 repoDir 이 다른 slug 로 분리돼야 한다.
	if rcA.RepoDir() == rcB.RepoDir() {
		t.Fatal("repoA and repoB should have different repo dirs")
	}
}

// TestMultiRepoCacheStartPrefetchNoDuplicate 는 같은 repoURL 로 두 번 호출해도
// prefetched 맵에 항목이 1 개만 들어감을 검증 (ticker 중복 방지).
func TestMultiRepoCacheStartPrefetchNoDuplicate(t *testing.T) {
	m := NewMultiRepoCache(t.TempDir(), nil)
	repoURL := "https://github.com/owner/repo"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// interval=0 이면 RepoCache.StartPrefetch 는 즉시 반환.
	m.StartPrefetch(ctx, repoURL, 0)
	m.StartPrefetch(ctx, repoURL, 0)

	m.prefetchMu.Lock()
	n := len(m.prefetched)
	m.prefetchMu.Unlock()
	if n != 1 {
		t.Fatalf("prefetched map len=%d want 1 (no duplicate)", n)
	}
}

// TestMultiRepoCacheStartPrefetchTwoDifferentURLs 는 두 repoURL 각각 prefetch 가 등록됨을 검증.
func TestMultiRepoCacheStartPrefetchTwoDifferentURLs(t *testing.T) {
	m := NewMultiRepoCache(t.TempDir(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.StartPrefetch(ctx, "https://github.com/a/b", 0)
	m.StartPrefetch(ctx, "https://github.com/a/c", 0)

	// 짧은 대기 — goroutine 내 no-op StartPrefetch(interval=0) 완료를 기다림.
	time.Sleep(10 * time.Millisecond)

	m.prefetchMu.Lock()
	n := len(m.prefetched)
	m.prefetchMu.Unlock()
	if n != 2 {
		t.Fatalf("prefetched map len=%d want 2", n)
	}
}

// TestMultiRepoCachePruneStaleWorktrees 는 activeIDs 에 없는 worktree 를 삭제하고
// activeIDs 의 worktree 는 보존함을 검증한다.
func TestMultiRepoCachePruneStaleWorktrees(t *testing.T) {
	workDir := t.TempDir()
	m := NewMultiRepoCache(workDir, nil)

	slug := "github.com_owner_repo"
	worktreesDir := filepath.Join(workDir, "repos", slug, "worktrees")

	activeID := "active-1234"
	staleID := "stale-5678"
	for _, id := range []string{activeID, staleID} {
		dir := filepath.Join(worktreesDir, "preview-"+id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	n, err := m.PruneStaleWorktrees(context.Background(), []string{activeID})
	if err != nil {
		t.Fatalf("PruneStaleWorktrees: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned=%d want 1", n)
	}

	// stale 는 삭제돼야 한다.
	if _, err := os.Stat(filepath.Join(worktreesDir, "preview-"+staleID)); !os.IsNotExist(err) {
		t.Fatal("stale worktree should be removed")
	}
	// active 는 남아있어야 한다.
	if _, err := os.Stat(filepath.Join(worktreesDir, "preview-"+activeID)); err != nil {
		t.Fatalf("active worktree should remain: %v", err)
	}
}

// TestMultiRepoCachePruneNoReposDir 는 repos 디렉토리가 없을 때 오류 없이 0 을 반환함을 검증.
func TestMultiRepoCachePruneNoReposDir(t *testing.T) {
	workDir := t.TempDir()
	m := NewMultiRepoCache(workDir, nil)

	n, err := m.PruneStaleWorktrees(context.Background(), nil)
	if err != nil {
		t.Fatalf("PruneStaleWorktrees on empty workDir: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned=%d want 0", n)
	}
}

// TestMultiRepoCachePruneEmptyActiveIDs 는 activeIDs=nil 이면 모든 worktree 를 삭제함을 검증.
func TestMultiRepoCachePruneEmptyActiveIDs(t *testing.T) {
	workDir := t.TempDir()
	m := NewMultiRepoCache(workDir, nil)

	slug := "example.com_owner_repo"
	worktreesDir := filepath.Join(workDir, "repos", slug, "worktrees")
	for _, id := range []string{"id-a", "id-b"} {
		dir := filepath.Join(worktreesDir, "preview-"+id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	n, err := m.PruneStaleWorktrees(context.Background(), nil)
	if err != nil {
		t.Fatalf("PruneStaleWorktrees: %v", err)
	}
	if n != 2 {
		t.Fatalf("pruned=%d want 2", n)
	}
}
