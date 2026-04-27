// 이 파일의 책임:
//   - RepoCache: bare clone 1개 + worktree per preview 레이아웃 관리 (§5-13).
//   - Ensure: 최초 호출에서 bare clone, 이후는 idempotent.
//   - Checkout: rev-parse 사전 확인 → fetch(mutex) → worktree add.
//   - Remove: worktree remove --force + RemoveAll.
//   - StartPrefetch: 별도 goroutine, ticker 기반 background fetch.
//
// 결정 13: fetch 만 mutex 직렬화. worktree add/remove 는 mutex 외부.
// 외부 명령은 os/exec 의 git 바이너리를 사용 — runner 인터페이스로 단위 테스트 가능.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CmdRunner 는 git 명령 실행을 추상화. 단위 테스트는 fake 주입.
type CmdRunner interface {
	Run(ctx context.Context, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) (string, error)
}

type execRunner struct{}

// Run 은 명령을 실행하고 stderr 까지 captured 한 에러를 반환한다.
func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w (output: %s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Output 은 stdout 만 trim 후 반환한다.
func (execRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RepoCache 는 단일 git 레포의 bare clone + worktree 들을 관리한다.
type RepoCache struct {
	workDir string // ~/.hub-agent (env AGENT_WORK_DIR)
	repoURL string // 원본 URL (로깅/캐시 키용, 토큰 미포함)
	authURL string // git 실행용 URL (토큰 포함 가능). 빈 값이면 repoURL 사용.
	repoDir string // <workDir>/repos/<slug>
	logger  *slog.Logger
	runner  CmdRunner
	mu      sync.Mutex // fetch 만 직렬화 (결정 13)
}

// NewRepoCache 는 RepoCache 를 만든다. logger 가 nil 이면 silent.
func NewRepoCache(workDir, repoURL string, logger *slog.Logger) *RepoCache {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(silentWriter{}, nil))
	}
	slug := RepoSlug(repoURL)
	return &RepoCache{
		workDir: workDir,
		repoURL: repoURL,
		repoDir: filepath.Join(workDir, "repos", slug),
		logger:  logger,
		runner:  execRunner{},
	}
}

// SetToken 은 HTTPS clone/fetch 에 사용할 PAT 를 주입한다.
// token 이 비어있거나 repoURL 이 https:// 가 아니면 no-op.
// 토큰은 authURL 에만 저장되며 global git config 를 건드리지 않는다.
func (c *RepoCache) SetToken(token string) {
	c.authURL = injectToken(c.repoURL, token)
}

// gitURL 은 실제 git 명령에 사용할 URL 을 반환한다 (authURL 우선).
func (c *RepoCache) gitURL() string {
	if c.authURL != "" {
		return c.authURL
	}
	return c.repoURL
}

// injectToken 은 HTTPS URL 에 PAT 를 userinfo 로 삽입한다.
// https://github.com/o/r.git → https://<token>@github.com/o/r.git
// HTTPS 가 아니거나 token 이 비면 rawURL 을 그대로 반환한다.
func injectToken(rawURL, token string) string {
	if token == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return rawURL
	}
	u.User = url.User(token)
	return u.String()
}

// SetRunner 는 단위 테스트용으로 CmdRunner 를 교체한다.
func (c *RepoCache) SetRunner(r CmdRunner) { c.runner = r }

// RepoDir 은 bare clone 디렉토리 경로를 반환한다.
func (c *RepoCache) RepoDir() string { return c.repoDir }

// silentWriter 는 logger nil 대체용.
type silentWriter struct{}

func (silentWriter) Write(p []byte) (int, error) { return len(p), nil }

// RepoSlug 는 §5-13 변환 규칙으로 repo URL 을 파일시스템 친화 슬러그로 변환한다.
//  1. https/http/ssh 접두사 제거.
//  2. git@host:path → host/path (콜론을 슬래시로).
//  3. 끝의 .git 제거.
//  4. 모든 / 를 _ 로 치환.
//
// 마지막 단계에서 NTFS/POSIX 비호환 문자(`:`, `*`, `?`, `"`, `<`, `>`, `|`, `\`)를
// `_`로 sanitize 한다. §5-13 5 케이스(github/file/git@/ssh)는 `:`/`\` 등을 이미
// 앞 단계에서 제거하므로 결과가 변하지 않으며, Windows drive-letter(`C:`) 같은
// 잔존 콜론만 영향을 받는다 (F-S2-11).
//
// 빈 문자열 입력은 빈 문자열 반환 (호출자가 검증).
func RepoSlug(url string) string {
	s := url
	// (1) 스킴 제거.
	for _, prefix := range []string{"https://", "http://", "ssh://"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			s = rest
			break
		}
	}
	// file:// 도 처리 (테스트 fixture).
	if rest, ok := strings.CutPrefix(s, "file://"); ok {
		// file:///tmp/x 와 같은 경로의 leading "/" 제거.
		s = strings.TrimPrefix(rest, "/")
	}
	// (2) ssh 형식: git@host:path → host/path.
	if rest, ok := strings.CutPrefix(s, "git@"); ok {
		s = strings.Replace(rest, ":", "/", 1)
	}
	// (3) .git 제거.
	s = strings.TrimSuffix(s, ".git")
	// (4) / → _.
	s = strings.ReplaceAll(s, "/", "_")
	// (5) NTFS/POSIX 비호환 문자 sanitize. Windows drive-letter 등의 잔존 `:`이 대상.
	s = sanitizePathChars(s)
	// 안전: 빈 segment 제거 (선행 _ 등).
	s = strings.Trim(s, "_")
	return s
}

// sanitizePathChars 는 파일시스템 디렉토리명에 부적합한 문자를 `_`로 치환한다.
// `/`는 이미 RepoSlug 단계 (4)에서 변환되므로 여기서 다루지 않는다.
func sanitizePathChars(s string) string {
	const bad = `:*?"<>|\`
	if !strings.ContainsAny(s, bad) {
		return s
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(bad, s[i]) >= 0 {
			b = append(b, '_')
			continue
		}
		b = append(b, s[i])
	}
	return string(b)
}

// Ensure 는 bare clone 이 없으면 만들고, 있으면 no-op 후 logger.Info 한다.
// 두 번째 호출은 git clone 을 실행하지 않는다 (F-S2-6 idempotency).
func (c *RepoCache) Ensure(ctx context.Context) error {
	if c.isInitialized() {
		c.logger.Info("repocache_already_initialized", "repo_dir", c.repoDir)
		return nil
	}
	parent := filepath.Dir(c.repoDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("repocache.Ensure: mkdir parent: %w", err)
	}
	// bare clone — fetch 와 동일한 mutex 보호 (clone 도 .git/objects 쓰기).
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.runner.Run(ctx, "git", "clone", "--bare", c.gitURL(), c.repoDir); err != nil {
		return fmt.Errorf("repocache.Ensure: clone: %w", err)
	}
	c.logger.Info("repocache_cloned", "repo_url", c.repoURL, "repo_dir", c.repoDir)
	return nil
}

// isInitialized 는 bare clone 디렉토리가 이미 존재하는지 확인한다.
// HEAD 파일 또는 objects 디렉토리를 검사 — bare repo 의 안정적 표지자.
func (c *RepoCache) isInitialized() bool {
	if _, err := os.Stat(filepath.Join(c.repoDir, "HEAD")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(c.repoDir, "objects")); err == nil {
		return true
	}
	return false
}

// Checkout 는 sha (또는 sha가 빈 경우 branch 최신 커밋)의 worktree 를 만든다.
//  - sha 비어있음: fetch 후 branch tip을 resolve (branch도 비면 "HEAD" 사용).
//  - sha 있음:
//    1. rev-parse --verify 로 로컬 확인 (있으면 fetch skip).
//    2. 없으면 fetch 후 재확인.
//    3. git worktree add --detach <path> <sha>.
func (c *RepoCache) Checkout(ctx context.Context, previewID, sha, branch string) (string, error) {
	worktreePath := filepath.Join(c.repoDir, "worktrees", "preview-"+previewID)

	if sha == "" {
		// sha 미지정: fetch 후 branch tip resolve.
		if ferr := c.fetch(ctx); ferr != nil {
			return "", fmt.Errorf("repocache.Checkout: fetch: %w", ferr)
		}
		// bare clone 은 fetch refspec 이 +refs/heads/*:refs/heads/* 이므로
		// 브랜치가 refs/heads/<branch> 에 직접 저장된다 (refs/remotes/origin/* 아님).
		ref := "HEAD"
		if branch != "" {
			ref = branch
		}
		resolved, err := c.runner.Output(ctx, "git", "--git-dir="+c.repoDir, "rev-parse", ref+"^{commit}")
		if err != nil {
			return "", fmt.Errorf("repocache.Checkout: resolve ref %q: %w", ref, err)
		}
		sha = strings.TrimSpace(resolved)
	} else {
		// (1) sha 사전 확인.
		if err := c.runner.Run(ctx, "git", "--git-dir="+c.repoDir, "rev-parse", "--verify", sha+"^{commit}"); err != nil {
			// (2) 없으면 fetch.
			if ferr := c.fetch(ctx); ferr != nil {
				return "", fmt.Errorf("repocache.Checkout: fetch: %w", ferr)
			}
			// 재시도 — 그래도 없으면 force-push 시나리오.
			if err := c.runner.Run(ctx, "git", "--git-dir="+c.repoDir, "rev-parse", "--verify", sha+"^{commit}"); err != nil {
				return "", fmt.Errorf("repocache.Checkout: sha %s not found after fetch: %w", sha, err)
			}
		}
	}
	// worktree add (mutex 불필요).
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", fmt.Errorf("repocache.Checkout: mkdir worktrees: %w", err)
	}
	// 이전 실패 빌드가 남긴 stale worktree 를 제거한다. Re-run 시 동일 previewID 로
	// worktree add 하면 "already exists" 오류가 나므로 사전에 정리한다.
	if _, statErr := os.Stat(worktreePath); statErr == nil {
		_ = c.runner.Run(ctx, "git", "--git-dir="+c.repoDir, "worktree", "remove", "--force", worktreePath)
		_ = os.RemoveAll(worktreePath)
	}
	if err := c.runner.Run(ctx, "git", "--git-dir="+c.repoDir, "worktree", "add", "--detach", worktreePath, sha); err != nil {
		return "", fmt.Errorf("repocache.Checkout: worktree add: %w", err)
	}
	return worktreePath, nil
}

// Remove 는 worktree 를 정리한다. 이미 없는 경우 no-op.
func (c *RepoCache) Remove(ctx context.Context, previewID string) error {
	worktreePath := filepath.Join(c.repoDir, "worktrees", "preview-"+previewID)
	if _, err := os.Stat(worktreePath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// git worktree remove --force; 실패해도 RemoveAll 으로 디렉토리 정리 시도.
	_ = c.runner.Run(ctx, "git", "--git-dir="+c.repoDir, "worktree", "remove", "--force", worktreePath)
	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("repocache.Remove: remove all: %w", err)
	}
	return nil
}

// StartPrefetch 는 ticker 로 fetch 를 주기 호출한다. interval==0 이면 즉시 종료.
// goroutine 종료는 ctx.Done.
func (c *RepoCache) StartPrefetch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.fetch(ctx); err != nil {
				c.logger.Warn("repocache_prefetch_failed", "err", err.Error())
			}
		}
	}
}

// fetch 는 mutex 보호 하에 git fetch 를 수행한다.
// URL 을 직접 전달해 bare clone 의 origin 설정을 변경하지 않는다.
func (c *RepoCache) fetch(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// refs/heads/* → refs/remotes/origin/* 로 매핑해 Checkout 의 "origin/<branch>" resolve 와 일치.
	const refspec = "+refs/heads/*:refs/remotes/origin/*"
	return c.runner.Run(ctx, "git", "--git-dir="+c.repoDir, "fetch", "--prune", c.gitURL(), refspec)
}
