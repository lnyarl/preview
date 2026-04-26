package agent

import (
	"context"
	"errors"
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
func (f *fakeDocker) NetworkInspect(ctx context.Context, name string) (NetworkInspectResult, error) {
	return NetworkInspectResult{}, ErrDockerNotFound
}
func (f *fakeDocker) NetworkCreate(ctx context.Context, name string, opts NetworkCreateOptions) error {
	return nil
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
	// Phase 6 rewrite pending: build 단계 없음. create/start 만 호출.
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
	// Phase 6: 기본 컨테이너 포트 80.
	if docker.lastCreateOpts.ExposedPort != 80 {
		t.Fatalf("ExposedPort=%d want 80", docker.lastCreateOpts.ExposedPort)
	}
}

// TestRunnerNoDockerfileNotFatal — Phase 6: Dockerfile 부재는 runner 입장에서 관여하지 않는다.
// ContainerCreate 까지 성공하는 경로를 검증.
func TestRunnerNoDockerfileNotFatal(t *testing.T) {
	runner, docker, hub, _ := newRunnerSetup(t, false)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	err := runner.Handle(ctx, protocol.JobAssignData{
		PreviewID: "p1",
		CommitSHA: "abc",
	})
	if err != nil {
		t.Fatalf("Handle should succeed: %v", err)
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

// TestRunnerBuildError — ContainerCreate 실패 시 STATUS_UPDATE failed 송신.
func TestRunnerBuildError(t *testing.T) {
	runner, docker, hub, _ := newRunnerSetup(t, true)
	docker.createErr = errors.New("simulated create error")
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"}); err == nil {
		t.Fatalf("expected error from failing container create")
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

// F-6 (Phase 5): 단일 Handle 성공 path 후 ReadySender 가 1 회 호출된다.
// MaxJobs=1 이고 inFlight 가 defer 안에서 1→0 으로 감소한 직후 maybeSendReady
// 가 1 회 송신.
func TestRunnerHandleDeferTriggersReady(t *testing.T) {
	runner, _, _, _ := newRunnerSetup(t, true)
	fake := &fakeReadySender{}
	runner.SetReadySender(fake)
	runner.SetMaxJobs(1)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("ReadySender calls=%d want 1", got)
	}
}

// F-7: build 실패 path 에서도 defer 가 maybeSendReady 를 1 회 호출.
// docker.createErr 로 fail() 분기를 강제.
func TestRunnerHandleDeferOnFailureTriggersReady(t *testing.T) {
	runner, docker, _, _ := newRunnerSetup(t, true)
	docker.createErr = errors.New("docker create fail")
	fake := &fakeReadySender{}
	runner.SetReadySender(fake)
	runner.SetMaxJobs(2)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Handle 가 fail 을 리턴해도 defer 는 실행되어야 한다.
	_ = runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"})
	// inFlight 0 + maxJobs 2 → 2 회 송신.
	if got := fake.count(); got != 2 {
		t.Fatalf("ReadySender calls=%d want 2", got)
	}
}

// F-8 (결정 4): paused=true 일 때 Handle 즉시 거절 분기는 inFlight 증가 없이
// return 하고 defer 미등록 → maybeSendReady 호출 안 됨.
func TestRunnerHandlePausedRejectsWithoutReady(t *testing.T) {
	runner, _, hub, _ := newRunnerSetup(t, true)
	fake := &fakeReadySender{}
	runner.SetReadySender(fake)
	runner.SetMaxJobs(3)
	runner.Pause() // paused=true.
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", CommitSHA: "abc"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// paused 거절 분기는 inFlight 미증가 + defer 미등록 → READY 송신 0.
	if got := fake.count(); got != 0 {
		t.Fatalf("ReadySender calls=%d want 0 (paused rejection)", got)
	}
	// 거절 시 failed STATUS_UPDATE 만 남아 있어야 한다 (Phase 3 결정 11 흐름 유지).
	statuses := hub.statuses()
	if len(statuses) != 1 || statuses[0] != "failed" {
		t.Fatalf("statuses=%v want [failed]", statuses)
	}
}

// F-9 (결정적 케이스): Handle 진행 중 Pause() 가 호출되면, defer 의
// maybeSendReady 가 paused 검사로 송신을 차단한다.
// — Pause 호출이 maybeSendReady 진입보다 먼저인 결정적 시점만 검사.
func TestRunnerHandleInFlightPauseBlocksReady(t *testing.T) {
	runner, _, _, _ := newRunnerSetup(t, true)
	fake := &fakeReadySender{}
	runner.SetReadySender(fake)
	runner.SetMaxJobs(2)
	ctx := context.Background()
	if err := runner.cache.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	runner.inFlight.Add(1)
	defer func() { runner.inFlight.Add(-1) }()
	runner.Pause()
	runner.maybeSendReady(ctx)
	if got := fake.count(); got != 0 {
		t.Fatalf("ReadySender calls=%d want 0 (paused mid-handle)", got)
	}
}
