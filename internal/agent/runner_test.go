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
				return nil
			}
		}
	}
	return nil
}
func (d *dockerfileRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	return d.base.Output(ctx, name, args...)
}

// withNoopBuildHolder 는 Holder 를 만들고 ":" (no-op POSIX shell builtin) 1줄을
// build_commands 로 적용한다. 셸 의존만 있고 외부 도구(docker) 없이도 항상 성공.
func withNoopBuildHolder(t *testing.T, runner *Runner) *Holder {
	t.Helper()
	h := NewHolder()
	h.Replace(protocol.AgentConfigData{BuildCommands: []string{":"}})
	runner.SetHolder(h)
	return h
}

func TestRunnerHappyPath(t *testing.T) {
	runner, docker, hub, _ := newRunnerSetup(t, true)
	withNoopBuildHolder(t, runner)
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
	// Phase 4: build 는 셸로 실행되므로 docker.buildCalls = 0. create/start 는 SDK.
	if docker.buildCalls != 0 || docker.createCalls != 1 || docker.startCalls != 1 {
		t.Fatalf("docker calls: build=%d create=%d start=%d", docker.buildCalls, docker.createCalls, docker.startCalls)
	}
	statuses := hub.statuses()
	if len(statuses) < 2 || statuses[0] != "building" || statuses[len(statuses)-1] != "running" {
		t.Fatalf("statuses=%v", statuses)
	}
	// label 검증 (NF-Container-Label-1).
	if docker.lastCreateOpts.Labels["hub-preview-id"] != "p1" {
		t.Fatalf("missing preview-id label: %v", docker.lastCreateOpts.Labels)
	}
	// Phase 4: ContainerPort sentinel 0 → 80 기본값.
	if docker.lastCreateOpts.ExposedPort != 80 {
		t.Fatalf("ExposedPort=%d want 80", docker.lastCreateOpts.ExposedPort)
	}
}

// TestRunnerNoDockerfileNotFatal — Phase 4 결정 4: Dockerfile 강제 검사가 제거되었으므로
// Dockerfile 부재가 더 이상 즉시 실패를 일으키지 않는다. 기본 빌드 명령(`docker build ...`)
// 이 실패하면 build_step_1 으로 fail. 명시적 비-docker 빌드 명령(":") 으로 성공도 가능.
func TestRunnerNoDockerfileNotFatal(t *testing.T) {
	runner, docker, hub, _ := newRunnerSetup(t, false)
	withNoopBuildHolder(t, runner) // ":" 명령은 Dockerfile 없어도 성공.
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	err := runner.Handle(ctx, protocol.JobAssignData{
		PreviewID: "p1",
		CommitSHA: "abc",
	})
	if err != nil {
		t.Fatalf("Handle should succeed without Dockerfile when build cmd does not need it: %v", err)
	}
	if docker.createCalls != 1 {
		t.Fatalf("ContainerCreate calls=%d want 1", docker.createCalls)
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "running" {
		t.Fatalf("last status=%s want running", statuses[len(statuses)-1])
	}
}

func TestRunnerTeardown(t *testing.T) {
	runner, docker, hub, _ := newRunnerSetup(t, true)
	withNoopBuildHolder(t, runner)
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

// TestRunnerBuildError — Phase 4: 빌드 명령이 셸에서 non-zero exit 일 때 STATUS_UPDATE failed.
func TestRunnerBuildError(t *testing.T) {
	runner, _, hub, _ := newRunnerSetup(t, true)
	// "false" 는 항상 exit 1 인 POSIX 명령.
	h := NewHolder()
	h.Replace(protocol.AgentConfigData{BuildCommands: []string{"false"}})
	runner.SetHolder(h)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"}); err == nil {
		t.Fatalf("expected error from failing build command")
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "failed" {
		t.Fatalf("last=%s", statuses[len(statuses)-1])
	}
}

// TestRunnerCustomContainerPort — Phase 4: ContainerPort != 0 이면 ExposedPort 에 반영.
func TestRunnerCustomContainerPort(t *testing.T) {
	runner, docker, _, _ := newRunnerSetup(t, true)
	h := NewHolder()
	h.Replace(protocol.AgentConfigData{BuildCommands: []string{":"}, ContainerPort: 3000})
	runner.SetHolder(h)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if docker.lastCreateOpts.ExposedPort != 3000 {
		t.Fatalf("ExposedPort=%d want 3000", docker.lastCreateOpts.ExposedPort)
	}
}

// TestRunnerMultiLineBuild — Phase 4: 여러 라인이 순서대로 직렬 실행되는지 검증.
// touch + test (-f) 로 1번째 명령의 부수효과가 2번째 명령에서 보이는지 확인 (cwd=worktree).
func TestRunnerMultiLineBuild(t *testing.T) {
	runner, _, hub, _ := newRunnerSetup(t, true)
	h := NewHolder()
	h.Replace(protocol.AgentConfigData{
		BuildCommands: []string{
			"touch step1.marker",
			"test -f step1.marker",
		},
	})
	runner.SetHolder(h)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "running" {
		t.Fatalf("last=%s want running", statuses[len(statuses)-1])
	}
}

// TestRunnerEnvironmentVariables — Phase 4: 4개 + PORT env 가 빌드 명령에서 보인다.
func TestRunnerEnvironmentVariables(t *testing.T) {
	runner, _, hub, root := newRunnerSetup(t, true)
	h := NewHolder()
	h.Replace(protocol.AgentConfigData{
		BuildCommands: []string{
			// env 를 파일에 저장 → 검증.
			"env | grep -E '^(PREVIEW_|PORT=)' > " + filepath.Join(root, "envcap.txt") + " || true",
		},
	})
	runner.SetHolder(h)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := runner.Handle(ctx, protocol.JobAssignData{
		PreviewID: "p1",
		CommitSHA: "deadbeef",
		Branch:    "feat/x",
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if statuses := hub.statuses(); statuses[len(statuses)-1] != "running" {
		t.Fatalf("last=%s want running", statuses[len(statuses)-1])
	}
	data, err := os.ReadFile(filepath.Join(root, "envcap.txt"))
	if err != nil {
		t.Skipf("env capture file missing (env grep may behave differently): %v", err)
	}
	captured := string(data)
	for _, want := range []string{"PREVIEW_ID=p1", "PREVIEW_IMAGE=preview-p1:latest",
		"PREVIEW_SHA=deadbeef", "PREVIEW_BRANCH=feat/x", "PORT=80"} {
		if !strings.Contains(captured, want) {
			t.Errorf("env capture missing %q. got: %s", want, captured)
		}
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
