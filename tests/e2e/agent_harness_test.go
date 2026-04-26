//go:build e2e

// 이 파일의 책임:
//   - 실제 internal/agent 코드(Client + Runner + RepoCache + Holder) 를 띄우되,
//     외부 의존(Docker daemon, 실제 git remote)만 fake 로 교체한다.
//   - fakeDockerClient: DockerClient 인터페이스 (internal/agent/docker.go) 구현.
//     ImageBuild 는 Phase 4 부터 호출되지 않으나 인터페이스 충족을 위해 제공.
//   - fakeCmdRunner: RepoCache 의 CmdRunner 인터페이스 구현.
//     git clone --bare → 디렉토리 생성, fetch → no-op, worktree add → mkdir,
//     worktree remove → rm -rf, rev-parse --verify → 항상 success (sha 보유 가정).
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1 (Docker 인터페이스),
//       internal/agent/repocache.go (Ensure/Checkout/Remove 명령 패턴).
package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lnyarl/preview/internal/agent"
)

// ---------- fake DockerClient ----------

// fakeDockerClient 는 SDK 의존을 제거한 인메모리 Docker 시뮬레이터.
// Create→Start→Stop→Remove 라이프사이클만 검증한다 (포트 매핑은 실제 바인딩하지 않음).
type fakeDockerClient struct {
	mu         sync.Mutex
	nextID     int
	containers map[string]*fakeContainer
}

type fakeContainer struct {
	id      string
	image   string
	labels  map[string]string
	started bool
	opts    agent.CreateOptions
}

func newFakeDockerClient() *fakeDockerClient {
	return &fakeDockerClient{containers: make(map[string]*fakeContainer)}
}

// ImageBuild 는 contextDir 의 파일 시스템을 그대로 무시하고 빈 reader 를 돌려준다.
// Phase 4 이후 Runner 가 호출하지 않지만 인터페이스 충족용.
func (f *fakeDockerClient) ImageBuild(_ context.Context, _ string, _ agent.BuildOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeDockerClient) ContainerCreate(_ context.Context, opts agent.CreateOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("fake-container-%04d", f.nextID)
	f.containers[id] = &fakeContainer{
		id:     id,
		image:  opts.Image,
		labels: opts.Labels,
		opts:   opts,
	}
	return id, nil
}

func (f *fakeDockerClient) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("fakeDocker: container %s not found", id)
	}
	c.started = true
	return nil
}

func (f *fakeDockerClient) ContainerStop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		c.started = false
	}
	return nil
}

func (f *fakeDockerClient) ContainerRemove(_ context.Context, id string, _ agent.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.containers, id)
	return nil
}

func (f *fakeDockerClient) Ping(_ context.Context) error { return nil }

func (f *fakeDockerClient) ContainerList(_ context.Context, filters map[string]string) ([]agent.ContainerSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []agent.ContainerSummary{}
	for _, c := range f.containers {
		match := true
		for k, v := range filters {
			if got := c.labels[k]; got != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, agent.ContainerSummary{ID: c.id, Labels: c.labels})
		}
	}
	return out, nil
}

func (f *fakeDockerClient) ContainerInspect(_ context.Context, id string) (agent.ContainerInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return agent.ContainerInspectResult{}, fmt.Errorf("%w: container %s", agent.ErrDockerNotFound, id)
	}
	return agent.ContainerInspectResult{HostPort: c.opts.HostPort}, nil
}

func (f *fakeDockerClient) NetworkInspect(_ context.Context, _ string) (agent.NetworkInspectResult, error) {
	return agent.NetworkInspectResult{}, agent.ErrDockerNotFound
}
func (f *fakeDockerClient) NetworkCreate(_ context.Context, _ string, _ agent.NetworkCreateOptions) error {
	return nil
}

// ---------- fake CmdRunner ----------

// fakeCmdRunner 는 RepoCache 의 git 호출을 가로채 파일시스템 조작으로 대체한다.
// 패턴 매칭: 명령 인자의 첫 토큰을 보고 분기.
//
// 지원 명령:
//   - git clone --bare <repoURL> <repoDir>
//     → repoDir 생성 + bare repo 마커(HEAD 파일, objects 디렉토리) 작성.
//   - git --git-dir=... fetch --prune origin
//     → no-op (성공).
//   - git --git-dir=... rev-parse --verify <sha>^{commit}
//     → no-op (sha 가 항상 존재한다고 가정 — Checkout 의 첫 시도가 success).
//   - git --git-dir=... worktree add --detach <path> <sha>
//     → path 디렉토리 생성.
//   - git --git-dir=... worktree remove --force <path>
//     → path 디렉토리 제거.
//   - 그 외 → no-op (성공) 으로 fallback.
type fakeCmdRunner struct{}

// Run 은 명령을 분기 처리한다.
func (fakeCmdRunner) Run(_ context.Context, name string, args ...string) error {
	if name != "git" {
		return nil
	}
	// 인자 패턴 분류. clone 은 `clone --bare <url> <dir>` 시작.
	// 나머지는 첫 인자가 `--git-dir=...` 이고 두 번째가 동사.
	if len(args) >= 4 && args[0] == "clone" && args[1] == "--bare" {
		repoDir := args[3]
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			return err
		}
		// bare repo marker — repocache.isInitialized 가 검사하는 두 파일.
		if err := os.WriteFile(filepath.Join(repoDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(repoDir, "objects"), 0o755); err != nil {
			return err
		}
		return nil
	}
	// `--git-dir=` prefix 가 args[0] 인 형식.
	if len(args) >= 2 && strings.HasPrefix(args[0], "--git-dir=") {
		verb := args[1]
		switch verb {
		case "fetch", "rev-parse":
			return nil
		case "worktree":
			if len(args) >= 3 {
				switch args[2] {
				case "add":
					// `worktree add --detach <path> <sha>` 또는 `worktree add <path> <sha>`.
					var path string
					for i := 3; i < len(args); i++ {
						if !strings.HasPrefix(args[i], "-") {
							path = args[i]
							break
						}
					}
					if path == "" {
						return nil
					}
					if err := os.MkdirAll(path, 0o755); err != nil {
						return err
					}
					// Phase 6: .preview.yml + Dockerfile を生成して runner が処理できるようにする.
					previewYml := "services:\n  app:\n    port: 80\n    path: /app\n"
					_ = os.WriteFile(filepath.Join(path, ".preview.yml"), []byte(previewYml), 0o644)
					_ = os.WriteFile(filepath.Join(path, "Dockerfile"), []byte("FROM nginx:alpine\n"), 0o644)
					return nil
				case "remove":
					// `worktree remove --force <path>`.
					var path string
					for i := 3; i < len(args); i++ {
						if !strings.HasPrefix(args[i], "-") {
							path = args[i]
							break
						}
					}
					if path == "" {
						return nil
					}
					return os.RemoveAll(path)
				}
			}
			return nil
		}
	}
	return nil
}

// Output 은 rev-parse HEAD 같은 호출에 대비해 고정 SHA 를 돌려준다.
// 본 e2e 에서 RepoCache 는 Output 을 호출하지 않지만 인터페이스 충족용.
func (fakeCmdRunner) Output(_ context.Context, name string, args ...string) (string, error) {
	if name == "git" && len(args) >= 2 && args[len(args)-1] == "HEAD" {
		return "deadbeefdeadbeef", nil
	}
	return "", nil
}

// ---------- agent harness ----------

// agentHarness 는 e2e 테스트가 Agent 와 통신하는 표면.
// docker 는 fake 컨테이너 라이프사이클 검증용.
type agentHarness struct {
	docker *fakeDockerClient
	cancel context.CancelFunc
}

// startAgent 는 실제 agent.Client + agent.Runner + MultiRepoCache 를
// fake docker/cmd runner 와 함께 기동한다. ctx 취소는 t.Cleanup 으로 보장.
// Phase 6: MultiRepoCache 로 교체; repoURL 파라미터 제거 — msg.RepoURL 을 동적으로 수신.
func startAgent(t *testing.T, wsURL, agentToken, _ string) *agentHarness {
	t.Helper()
	var logOut io.Writer = io.Discard
	if testing.Verbose() {
		logOut = testWriter{t: t, prefix: "[agent] "}
	}
	logger := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := agent.Config{
		HubURL:        wsURL,
		Token:         agentToken,
		AdvertiseHost: "127.0.0.1",
		LogLevel:      "debug",
		WorkDir:       t.TempDir(),
		MaxJobs:       1,
	}

	// Phase 6: MultiRepoCache — SetCmdRunner で git 操作を fake に差し替え.
	multiCache := agent.NewMultiRepoCache(cfg.WorkDir, logger)
	multiCache.SetCmdRunner(fakeCmdRunner{})

	docker := newFakeDockerClient()

	client := agent.NewClient(cfg, logger)
	// cmd=nil → execRunner{} (compose は使わないので no-op 相当).
	runner := agent.NewRunner(docker, multiCache, nil, client, cfg.AdvertiseHost, logger)
	client.SetRunner(runner)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = client.Run(ctx)
	}()

	t.Cleanup(cancel)
	return &agentHarness{docker: docker, cancel: cancel}
}
