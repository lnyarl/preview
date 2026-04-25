package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lnyarl/preview/internal/protocol"
)

// fakeDocker 는 DockerClient 의 in-memory mock.
type fakeDocker struct {
	mu             sync.Mutex
	buildCalls     int
	createCalls    int
	startCalls     int
	stopCalls      int
	removeCalls    int
	lastCreateOpts CreateOptions
	buildErr       error
	createErr      error
	startErr       error
	containerID    string
}

func (f *fakeDocker) ImageBuild(ctx context.Context, dir string, opts BuildOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buildCalls++
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	return io.NopCloser(strings.NewReader("ok")), nil
}
func (f *fakeDocker) ContainerCreate(ctx context.Context, opts CreateOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.lastCreateOpts = opts
	if f.createErr != nil {
		return "", f.createErr
	}
	if f.containerID == "" {
		f.containerID = "container-fake"
	}
	return f.containerID, nil
}
func (f *fakeDocker) ContainerStart(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	return f.startErr
}
func (f *fakeDocker) ContainerStop(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	return nil
}
func (f *fakeDocker) ContainerRemove(ctx context.Context, id string, opts RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	return nil
}
func (f *fakeDocker) Ping(ctx context.Context) error { return nil }
func (f *fakeDocker) ContainerList(ctx context.Context, filters map[string]string) ([]ContainerSummary, error) {
	return nil, nil
}
func (f *fakeDocker) ContainerInspect(ctx context.Context, id string) (ContainerInspectResult, error) {
	return ContainerInspectResult{}, nil
}

// runnerFakeHub 는 HubSender 의 capture mock.
type runnerFakeHub struct {
	mu      sync.Mutex
	updates []protocol.StatusUpdateData
}

func (h *runnerFakeHub) SendStatusUpdate(ctx context.Context, d protocol.StatusUpdateData) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updates = append(h.updates, d)
	return nil
}
func (h *runnerFakeHub) statuses() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.updates))
	for i, u := range h.updates {
		out[i] = u.Status
	}
	return out
}

func newRunnerSetup(t *testing.T, withDockerfile bool) (*Runner, *fakeDocker, *runnerFakeHub, string) {
	t.Helper()
	root := t.TempDir()
	cache := NewRepoCache(root, "file:///tmp/preview-fixture", nil)
	r := &fakeRunner{revParseOK: true}
	cache.SetRunner(r)
	docker := &fakeDocker{}
	hub := &runnerFakeHub{}

	// trackerRunner: worktree add 시 Dockerfile 까지 만든다.
	if withDockerfile {
		cache.SetRunner(&dockerfileRunner{base: r})
	}
	runner := NewRunner(docker, cache, hub, "127.0.0.1", nil)
	return runner, docker, hub, root
}

// dockerfileRunner 는 worktree add 시 Dockerfile 도 함께 만든다.
type dockerfileRunner struct{ base *fakeRunner }

func (d *dockerfileRunner) Run(ctx context.Context, name string, args ...string) error {
	if err := d.base.Run(ctx, name, args...); err != nil {
		return err
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "worktree add") {
		for i, a := range args {
			if a == "add" && i+2 < len(args) {
				path := args[i+2]
				_ = os.WriteFile(filepath.Join(path, "Dockerfile"), []byte("FROM nginx:alpine\n"), 0o644)
				// preview.yml: 테스트에서는 docker 대신 echo 로 빌드 성공 시뮬레이션.
				_ = os.WriteFile(filepath.Join(path, "preview.yml"),
					[]byte("build:\n  - echo \"test build $PREVIEW_IMAGE\"\nport: 80\n"), 0o644)
				return nil
			}
		}
	}
	return nil
}
func (d *dockerfileRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	return d.base.Output(ctx, name, args...)
}

func TestRunnerHappyPath(t *testing.T) {
	runner, docker, hub, _ := newRunnerSetup(t, true)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	err := runner.Handle(ctx, protocol.JobAssignData{
		PreviewID: "p1",
		RepoURL:   "file:///tmp/x",
		CommitSHA: "abc",
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// 빌드는 이제 shell 명령(preview.yml) 으로 수행 — fake docker ImageBuild 미호출.
	if docker.createCalls != 1 || docker.startCalls != 1 {
		t.Fatalf("docker calls: create=%d start=%d", docker.createCalls, docker.startCalls)
	}
	statuses := hub.statuses()
	if len(statuses) < 2 || statuses[0] != "building" || statuses[len(statuses)-1] != "running" {
		t.Fatalf("statuses=%v", statuses)
	}
	// label 검증 (NF-Container-Label-1).
	if docker.lastCreateOpts.Labels["hub-preview-id"] != "p1" {
		t.Fatalf("missing preview-id label: %v", docker.lastCreateOpts.Labels)
	}
}

func TestRunnerNoPreviewConfig(t *testing.T) {
	// preview.yml 없음 → 기본 "docker build" 명령 실행 → 테스트 환경에서 실패 → status=failed.
	runner, _, hub, _ := newRunnerSetup(t, false)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	err := runner.Handle(ctx, protocol.JobAssignData{
		PreviewID: "p1",
		CommitSHA: "abc",
	})
	if err == nil {
		t.Fatalf("expected failure when build command fails")
	}
	statuses := hub.statuses()
	if len(statuses) < 1 || statuses[len(statuses)-1] != "failed" {
		t.Fatalf("statuses=%v want last=failed", statuses)
	}
}

func TestRunnerTeardown(t *testing.T) {
	runner, docker, hub, _ := newRunnerSetup(t, true)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	hub.mu.Lock()
	hub.updates = nil
	hub.mu.Unlock()

	if err := runner.Teardown(ctx, "p1"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if docker.stopCalls != 1 || docker.removeCalls != 1 {
		t.Fatalf("teardown calls: stop=%d remove=%d", docker.stopCalls, docker.removeCalls)
	}
	statuses := hub.statuses()
	if len(statuses) != 1 || statuses[0] != "done" {
		t.Fatalf("statuses=%v want [done]", statuses)
	}
	// jobs 맵에서 제거.
	if _, ok := runner.jobs.Load("p1"); ok {
		t.Fatalf("jobs entry not deleted")
	}
}

func TestRunnerBuildError(t *testing.T) {
	// preview.yml 에 실패하는 명령을 넣어 build error 시뮬레이션.
	runner, _, hub, _ := newRunnerSetup(t, false)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// worktree 경로에 실패 명령이 있는 preview.yml 주입.
	wt, _ := runner.cache.Checkout(ctx, "p1", "abc")
	_ = os.WriteFile(filepath.Join(wt, "preview.yml"),
		[]byte("build:\n  - exit 1\nport: 80\n"), 0o644)
	// 새 Handle 호출을 위해 jobs 맵 초기화.
	runner.jobs.Delete("p1")

	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"}); err == nil {
		t.Fatalf("expected error")
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "failed" {
		t.Fatalf("last=%s", statuses[len(statuses)-1])
	}
}

func TestAllocatePort(t *testing.T) {
	port, err := allocatePort(1)
	if err != nil {
		t.Fatalf("allocatePort: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("port=%d out of range", port)
	}
	// 동일 호출 두 번이 모두 OK 인지 확인 (재시도 로직 동작 확인은 fake listener 가 필요).
	port2, err := allocatePort(2)
	if err != nil {
		t.Fatalf("allocatePort 2: %v", err)
	}
	if port2 <= 0 {
		t.Fatalf("port2=%d", port2)
	}
}
