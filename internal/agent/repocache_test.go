package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepoSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/owner/repo.git", "github.com_owner_repo"},
		{"https://github.com/owner/repo", "github.com_owner_repo"},
		{"git@github.com:owner/repo.git", "github.com_owner_repo"},
		{"file:///tmp/preview-fixture", "tmp_preview-fixture"},
		{"ssh://git@example.com/team/svc.git", "example.com_team_svc"},
		{"", ""},
		{"http://example.com/x.git", "example.com_x"},
		// F-S2-11: Windows drive-letter 가 포함된 file:// URL.
		// `:`, `\` 같은 NTFS 비호환 문자는 sanitize 단계에서 `_`로 치환된다.
		{"file:///C:/tmp/preview-fixture", "C__tmp_preview-fixture"},
		{`file:///C:\tmp\preview-fixture`, "C__tmp_preview-fixture"},
	}
	for _, tc := range cases {
		got := RepoSlug(tc.in)
		if got != tc.want {
			t.Errorf("RepoSlug(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

// fakeRunner 는 호출을 카운트한다. 첫 인자는 명령 이름 (git), 그 뒤는 args.
type fakeRunner struct {
	mu       sync.Mutex
	calls    [][]string
	cloneErr error
	fetchErr error

	// 동시성 측정.
	concurrent int32
	maxConc    int32
	// rev-parse 결과 컨트롤 (default: error → fetch 트리거).
	revParseOK bool
}

func (f *fakeRunner) record(name string, args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) error {
	cur := atomic.AddInt32(&f.concurrent, 1)
	defer atomic.AddInt32(&f.concurrent, -1)
	for {
		old := atomic.LoadInt32(&f.maxConc)
		if cur <= old || atomic.CompareAndSwapInt32(&f.maxConc, old, cur) {
			break
		}
	}
	f.record(name, args)
	if name == "git" && len(args) > 0 {
		// git --git-dir=... rev-parse ...
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "clone --bare"):
			if f.cloneErr != nil {
				return f.cloneErr
			}
			// 디렉토리 생성을 흉내내 isInitialized 가 true 가 되게 함.
			// args 마지막이 destination.
			dest := args[len(args)-1]
			_ = os.MkdirAll(dest, 0o755)
			_ = os.WriteFile(filepath.Join(dest, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
		case strings.Contains(joined, "fetch"):
			// 동시성 측정 위해 약간 지연.
			time.Sleep(20 * time.Millisecond)
			return f.fetchErr
		case strings.Contains(joined, "rev-parse --verify"):
			if !f.revParseOK {
				return errors.New("not found")
			}
			return nil
		case strings.Contains(joined, "worktree add"):
			// args: [--git-dir=... worktree add --detach <path> <sha>]
			// path 는 add --detach 다음.
			for i, a := range args {
				if a == "add" && i+2 < len(args) {
					path := args[i+2]
					_ = os.MkdirAll(path, 0o755)
					return nil
				}
			}
			return nil
		case strings.Contains(joined, "worktree remove"):
			return nil
		}
	}
	return nil
}

func (f *fakeRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	f.record(name, args)
	return "", nil
}

func (f *fakeRunner) countCalls(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), substr) {
			n++
		}
	}
	return n
}

func newFakeCache(t *testing.T) (*RepoCache, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	c := NewRepoCache(dir, "file:///tmp/preview-fixture", nil)
	r := &fakeRunner{}
	c.SetRunner(r)
	return c, r
}

func TestRepoCacheEnsureIdempotent(t *testing.T) {
	c, r := newFakeCache(t)
	ctx := context.Background()
	if err := c.Ensure(ctx); err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	if err := c.Ensure(ctx); err != nil {
		t.Fatalf("Ensure 2: %v", err)
	}
	if got := r.countCalls("clone --bare"); got != 1 {
		t.Fatalf("clone calls=%d want 1", got)
	}
	// HEAD 가 존재해야 함 (fakeRunner 가 만듦).
	if _, err := os.Stat(filepath.Join(c.RepoDir(), "HEAD")); err != nil {
		t.Fatalf("HEAD missing: %v", err)
	}
}

func TestRepoCacheCheckoutFetchSkipWhenShaPresent(t *testing.T) {
	c, r := newFakeCache(t)
	r.revParseOK = true // sha already local.
	ctx := context.Background()
	if err := c.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wt, err := c.Checkout(ctx, "p1", "abc123", "")
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if wt == "" {
		t.Fatalf("worktree path empty")
	}
	if got := r.countCalls("fetch"); got != 0 {
		t.Fatalf("fetch calls=%d want 0 (sha already local)", got)
	}
	if got := r.countCalls("worktree add"); got != 1 {
		t.Fatalf("worktree add calls=%d want 1", got)
	}
}

func TestRepoCacheCheckoutFetchOnMissingSha(t *testing.T) {
	c, r := newFakeCache(t)
	// rev-parse 첫 호출 실패 → fetch 후 재시도. 두 번째 호출은 OK 가 되도록 트래커.
	var calls int32
	c.SetRunner(&trackerRunner{base: r, onRevParse: func() error {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return errors.New("missing")
		}
		return nil
	}})
	ctx := context.Background()
	if err := c.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wt, err := c.Checkout(ctx, "p1", "abc", "")
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if wt == "" {
		t.Fatalf("worktree path empty")
	}
}

// trackerRunner 는 rev-parse 호출만 컨트롤하고 그 외는 base 에 위임.
type trackerRunner struct {
	base       *fakeRunner
	onRevParse func() error
}

func (t *trackerRunner) Run(ctx context.Context, name string, args ...string) error {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "rev-parse --verify") && t.onRevParse != nil {
		t.base.record(name, args)
		return t.onRevParse()
	}
	return t.base.Run(ctx, name, args...)
}
func (t *trackerRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	return t.base.Output(ctx, name, args...)
}

func TestRepoCacheRemove(t *testing.T) {
	c, r := newFakeCache(t)
	r.revParseOK = true
	ctx := context.Background()
	if err := c.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wt, err := c.Checkout(ctx, "p1", "abc", "")
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
	if err := c.Remove(ctx, "p1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree dir still exists: %v", err)
	}
	// 미존재 두 번째 호출 — no-op.
	if err := c.Remove(ctx, "p1"); err != nil {
		t.Fatalf("Remove(absent): %v", err)
	}
}

func TestRepoCachePrefetchCancel(t *testing.T) {
	c, _ := newFakeCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	done := make(chan struct{})
	go func() {
		c.StartPrefetch(ctx, 30*time.Millisecond)
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("prefetch did not exit after cancel")
	}
}

// TestRepoCacheFetchSerialized 는 fetch 가 mutex 로 직렬화되는지 검증.
// 50 goroutine 이 fetch 를 호출해도 동시 실행은 1.
func TestRepoCacheFetchSerialized(t *testing.T) {
	c, r := newFakeCache(t)
	ctx := context.Background()
	if err := c.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	const N = 20
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.fetch(ctx)
		}()
	}
	wg.Wait()
	if max := atomic.LoadInt32(&r.maxConc); max > 1 {
		t.Fatalf("max concurrent=%d want 1", max)
	}
}

// 통합 테스트: 실제 git 바이너리를 사용하는 fixture 기반 테스트.
// git 미설치 시 skip. Ensure/Checkout/Remove 의 실제 동작 확인.
// Windows 의 경우 임시 디렉토리 경로가 콜론(드라이브 문자)을 포함할 수 있어
// repo-slug 파일시스템 호환을 위해 fakeRunner 가 아닌 실제 git 호출 시 슬러그
// 디렉토리에서 콜론을 만나면 NTFS 가 거부한다 — 이 통합 테스트는 file:// URL 의
// 경로가 짧고 콜론이 없는 환경에서만 의미있다. 따라서 build tag 로 분리하지
// 않고 path 변수에 OS-specific 짧은 경로를 직접 만들어 슬러그가 안전하도록 한다.
func TestRepoCacheRealGitIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	if testing.Short() {
		t.Skip("short")
	}
	// Windows path 콜론 회피: fixed home-relative scratch dir.
	scratch, err := os.MkdirTemp("", "preview-repocache-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	bare := filepath.Join(scratch, "bare")
	src := filepath.Join(scratch, "src")
	for _, d := range []string{bare, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	// 슬러그가 콜론(드라이브 문자)을 포함하면 윈도우즈에서 디렉토리 생성 실패 →
	// 그 환경에서는 이 테스트를 skip.
	slug := RepoSlug("file://" + filepath.ToSlash(bare))
	if strings.ContainsAny(slug, ":") {
		t.Skipf("repo-slug %q contains characters not allowed by filesystem", slug)
	}
	// bare repo 생성.
	if err := exec.Command("git", "init", "--bare", bare).Run(); err != nil {
		t.Fatalf("git init bare: %v", err)
	}
	cmds := [][]string{
		{"init"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"checkout", "-b", "main"},
	}
	for _, c := range cmds {
		cmd := exec.Command("git", c...)
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", c, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "Dockerfile"), []byte("FROM nginx:alpine\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	for _, c := range [][]string{
		{"add", "."},
		{"commit", "-m", "init"},
		{"push", bare, "HEAD:main"},
	} {
		cmd := exec.Command("git", c...)
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", c, err, out)
		}
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = src
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	sha := strings.TrimSpace(string(out))

	wd := filepath.Join(scratch, "wd")
	c := NewRepoCache(wd, "file://"+filepath.ToSlash(bare), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wt, err := c.Checkout(ctx, "p1", sha, "")
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "Dockerfile")); err != nil {
		t.Fatalf("Dockerfile missing in worktree: %v", err)
	}
	if err := c.Remove(ctx, "p1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists")
	}
}
