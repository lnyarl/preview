// 이 파일의 책임:
//   - MultiRepoCache: repoURL → *RepoCache 의 thin wrapper.
//   - getOrCreate: lazy 인스턴스 생성 (sync.Mutex 보호).
//   - StartPrefetch: 같은 repoURL 은 ticker 1 개만 (중복 no-op).
//   - PruneStaleWorktrees: {workDir}/repos/*/worktrees/preview-* 스캔 + 미사용 삭제.
//
// §4-2, 결정 8.
package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MultiRepoCache 는 repoURL → *RepoCache 의 thin wrapper 다.
// RepoCache 는 무변경 — 모든 로직은 대응하는 RepoCache 메서드에 위임한다.
type MultiRepoCache struct {
	workDir   string
	logger    *slog.Logger
	cmdRunner CmdRunner // nil = execRunner{}. SetCmdRunner 로 테스트에서 주입 가능.

	mu      sync.Mutex
	repos   map[string]*RepoCache // key: raw repoURL

	prefetchMu sync.Mutex
	prefetched map[string]struct{} // repoURLs with ticker running
}

// SetCmdRunner 는 이후 생성되는 모든 RepoCache 에 주입할 CmdRunner 를 설정한다.
// 이미 생성된 RepoCache 에는 영향을 주지 않는다. 주로 테스트용.
func (m *MultiRepoCache) SetCmdRunner(r CmdRunner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cmdRunner = r
}

// NewMultiRepoCache 는 빈 MultiRepoCache 를 반환한다. logger 가 nil 이면 silent.
func NewMultiRepoCache(workDir string, logger *slog.Logger) *MultiRepoCache {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(silentWriter{}, nil))
	}
	return &MultiRepoCache{
		workDir:    workDir,
		logger:     logger,
		repos:      make(map[string]*RepoCache),
		prefetched: make(map[string]struct{}),
	}
}

// getOrCreate 는 repoURL 에 대응하는 *RepoCache 를 반환한다. 없으면 생성.
// cmdRunner 가 설정되어 있으면 새로 생성한 RepoCache 에 주입한다.
func (m *MultiRepoCache) getOrCreate(repoURL string) *RepoCache {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rc, ok := m.repos[repoURL]; ok {
		return rc
	}
	rc := NewRepoCache(m.workDir, repoURL, m.logger)
	if m.cmdRunner != nil {
		rc.SetRunner(m.cmdRunner)
	}
	m.repos[repoURL] = rc
	return rc
}

// Ensure 는 repoURL 의 bare clone 을 만든다 (idempotent).
func (m *MultiRepoCache) Ensure(ctx context.Context, repoURL string) error {
	return m.getOrCreate(repoURL).Ensure(ctx)
}

// Checkout 은 repoURL 의 sha 에 대한 worktree 를 만들어 경로를 반환한다.
func (m *MultiRepoCache) Checkout(ctx context.Context, repoURL, previewID, sha string) (string, error) {
	return m.getOrCreate(repoURL).Checkout(ctx, previewID, sha)
}

// Remove 는 repoURL 의 previewID worktree 를 정리한다. 없으면 no-op.
func (m *MultiRepoCache) Remove(ctx context.Context, repoURL, previewID string) error {
	return m.getOrCreate(repoURL).Remove(ctx, previewID)
}

// StartPrefetch 는 repoURL 에 대해 prefetch ticker 를 기동한다.
// 같은 repoURL 로 두 번 호출하면 두 번째는 no-op (ticker 중복 방지).
func (m *MultiRepoCache) StartPrefetch(ctx context.Context, repoURL string, interval time.Duration) {
	m.prefetchMu.Lock()
	if _, running := m.prefetched[repoURL]; running {
		m.prefetchMu.Unlock()
		return
	}
	m.prefetched[repoURL] = struct{}{}
	m.prefetchMu.Unlock()

	rc := m.getOrCreate(repoURL)
	go rc.StartPrefetch(ctx, interval)
}

// PruneStaleWorktrees 는 {workDir}/repos/*/worktrees/preview-* 를 스캔해
// activeIDs 에 없는 worktree 디렉토리를 삭제한다 (orphan cleanup).
//
// activeIDs 가 빈 슬라이스면 모든 worktree 를 제거 — 호출자 책임.
// 반환: 삭제된 worktree 수 + 첫 번째 에러 (이후 항목은 계속 처리).
func (m *MultiRepoCache) PruneStaleWorktrees(ctx context.Context, activeIDs []string) (int, error) {
	active := make(map[string]struct{}, len(activeIDs))
	for _, id := range activeIDs {
		active[id] = struct{}{}
	}

	reposDir := filepath.Join(m.workDir, "repos")
	slugDirs, err := os.ReadDir(reposDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var (
		pruned   int
		firstErr error
	)
	for _, slugEntry := range slugDirs {
		if !slugEntry.IsDir() {
			continue
		}
		worktreesDir := filepath.Join(reposDir, slugEntry.Name(), "worktrees")
		entries, err := os.ReadDir(worktreesDir)
		if err != nil {
			continue // worktrees dir 없으면 건너뜀
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "preview-") {
				continue
			}
			previewID := strings.TrimPrefix(name, "preview-")
			if _, ok := active[previewID]; ok {
				continue
			}
			path := filepath.Join(worktreesDir, name)
			if err := os.RemoveAll(path); err != nil {
				m.logger.Warn("prune_stale_worktree_failed", "path", path, "err", err.Error())
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			m.logger.Info("prune_stale_worktree", "path", path)
			pruned++
		}
	}
	return pruned, firstErr
}
