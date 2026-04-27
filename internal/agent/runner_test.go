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

const testRepoURL = "file:///tmp/preview-fixture"

// previewYml 은 단일 서비스 .preview.yml 내용.
const previewYml = `services:
  app:
    port: 3000
    path: /app
`

// ---------- fakeDocker (unchanged interface, reused) ----------

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

// ---------- fakeCmd — docker compose 명령 캡처 ----------

type fakeCmd struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (f *fakeCmd) Run(ctx context.Context, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.err
}
func (f *fakeCmd) Output(ctx context.Context, name string, args ...string) (string, error) {
	return "", nil
}
func (f *fakeCmd) lastArgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

// ---------- runnerFakeHub — HubSender mock ----------

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
func (h *runnerFakeHub) lastUpdate() protocol.StatusUpdateData {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.updates) == 0 {
		return protocol.StatusUpdateData{}
	}
	return h.updates[len(h.updates)-1]
}

// ---------- worktreeFileRunner — worktree add 시 파일 생성 ----------

// worktreeFileRunner 는 worktree add 시 .preview.yml + build 파일을 생성한다.
// mode: "dockerfile" | "compose" | "no_build" | "no_config".
type worktreeFileRunner struct {
	base *fakeRunner
	mode string
}

func (w *worktreeFileRunner) Run(ctx context.Context, name string, args ...string) error {
	if err := w.base.Run(ctx, name, args...); err != nil {
		return err
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "worktree add") {
		var path string
		for i, a := range args {
			if a == "add" && i+2 < len(args) {
				path = args[i+2]
				break
			}
		}
		if path == "" {
			return nil
		}
		if w.mode != "no_config" {
			_ = os.WriteFile(filepath.Join(path, ".preview.yml"), []byte(previewYml), 0o644)
		}
		switch w.mode {
		case "dockerfile":
			_ = os.WriteFile(filepath.Join(path, "Dockerfile"), []byte("FROM nginx:alpine\n"), 0o644)
		case "compose":
			_ = os.WriteFile(filepath.Join(path, "docker-compose.yml"),
				[]byte("version: '3'\nservices:\n  app:\n    image: nginx:alpine\n"), 0o644)
		// "no_build": .preview.yml only, no build artifact
		}
	}
	return nil
}
func (w *worktreeFileRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	return w.base.Output(ctx, name, args...)
}

// ---------- 테스트 setup ----------

// newRunnerSetup 은 mode 에 따라 Runner 를 세팅한다.
// mode: "dockerfile" | "compose" | "no_build" | "no_config".
func newRunnerSetup(t *testing.T, mode string) (*Runner, *fakeDocker, *fakeCmd, *runnerFakeHub) {
	t.Helper()
	root := t.TempDir()
	multiCache := NewMultiRepoCache(root, nil)

	// MultiRepoCache 에 fake runner 를 주입한 RepoCache 미리 등록.
	rc := NewRepoCache(root, testRepoURL, nil)
	base := &fakeRunner{revParseOK: true}
	rc.SetRunner(&worktreeFileRunner{base: base, mode: mode})
	multiCache.mu.Lock()
	multiCache.repos[testRepoURL] = rc
	multiCache.mu.Unlock()

	docker := &fakeDocker{}
	hub := &runnerFakeHub{}
	cmd := &fakeCmd{}
	runner := NewRunner(docker, multiCache, cmd, hub, "127.0.0.1", nil)
	return runner, docker, cmd, hub
}

func testMsg(pid string) protocol.JobAssignData {
	return protocol.JobAssignData{PreviewID: pid, RepoURL: testRepoURL, CommitSHA: "abc"}
}

// ---------- Tests ----------

func TestRunnerHappyPathDockerfile(t *testing.T) {
	runner, docker, _, hub := newRunnerSetup(t, "dockerfile")
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// Dockerfile 모드: ImageBuild + ContainerCreate + ContainerStart.
	if docker.buildCalls != 1 || docker.createCalls != 1 || docker.startCalls != 1 {
		t.Fatalf("docker calls: build=%d create=%d start=%d", docker.buildCalls, docker.createCalls, docker.startCalls)
	}
	statuses := hub.statuses()
	if len(statuses) < 2 || statuses[0] != "building" || statuses[len(statuses)-1] != "running" {
		t.Fatalf("statuses=%v", statuses)
	}
	// preview_urls 확인.
	last := hub.lastUpdate()
	if len(last.PreviewURLs) == 0 {
		t.Errorf("preview_urls empty; want at least one entry")
	}
	if _, ok := last.PreviewURLs["app"]; !ok {
		t.Errorf("preview_urls missing 'app' key: %v", last.PreviewURLs)
	}
	// hub-preview-id 라벨 확인.
	if docker.lastCreateOpts.Labels["hub-preview-id"] != "p1" {
		t.Errorf("missing hub-preview-id label: %v", docker.lastCreateOpts.Labels)
	}
	// Networks: [preview-net].
	if len(docker.lastCreateOpts.Networks) == 0 || docker.lastCreateOpts.Networks[0] != traefikNetwork {
		t.Errorf("Networks=%v want [%s]", docker.lastCreateOpts.Networks, traefikNetwork)
	}
}

func TestRunnerHappyPathCompose(t *testing.T) {
	runner, docker, cmd, hub := newRunnerSetup(t, "compose")
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// compose 모드: ImageBuild / ContainerCreate/Start 없음.
	if docker.buildCalls != 0 || docker.createCalls != 0 {
		t.Fatalf("unexpected docker calls: build=%d create=%d", docker.buildCalls, docker.createCalls)
	}
	// docker compose up -d が呼ばれたか.
	last := cmd.lastArgs()
	if len(last) == 0 {
		t.Fatal("docker compose not called")
	}
	if last[0] != "docker" || last[1] != "compose" {
		t.Errorf("cmd args=%v want docker compose ...", last)
	}
	found := false
	for _, a := range last {
		if a == "up" {
			found = true
		}
	}
	if !found {
		t.Errorf("compose up not in args: %v", last)
	}
	// preview_urls 확인.
	last2 := hub.lastUpdate()
	if len(last2.PreviewURLs) == 0 {
		t.Errorf("preview_urls empty for compose mode")
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "running" {
		t.Fatalf("last status=%s want running", statuses[len(statuses)-1])
	}
}

func TestRunnerPreviewConfigMissing(t *testing.T) {
	runner, _, _, hub := newRunnerSetup(t, "no_config")
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err == nil {
		t.Fatal("expected error from missing .preview.yml")
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "failed" {
		t.Fatalf("last status=%s want failed", statuses[len(statuses)-1])
	}
}

func TestRunnerNoBuildArtifact(t *testing.T) {
	runner, _, _, hub := newRunnerSetup(t, "no_build")
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err == nil {
		t.Fatal("expected error: no build artifact")
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "failed" {
		t.Fatalf("last status=%s want failed", statuses[len(statuses)-1])
	}
}

func TestRunnerTeardownDockerfile(t *testing.T) {
	runner, docker, _, hub := newRunnerSetup(t, "dockerfile")
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err != nil {
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
	if _, ok := runner.jobs.Load("p1"); ok {
		t.Fatal("jobs entry not deleted after teardown")
	}
}

func TestRunnerTeardownCompose(t *testing.T) {
	runner, _, cmd, hub := newRunnerSetup(t, "compose")
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	cmd.mu.Lock()
	cmd.calls = nil
	cmd.mu.Unlock()
	hub.mu.Lock()
	hub.updates = nil
	hub.mu.Unlock()

	if err := runner.Teardown(ctx, "p1"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	last := cmd.lastArgs()
	if len(last) == 0 {
		t.Fatal("docker compose down not called")
	}
	found := false
	for _, a := range last {
		if a == "down" {
			found = true
		}
	}
	if !found {
		t.Errorf("compose down not in args: %v", last)
	}
	statuses := hub.statuses()
	if len(statuses) != 1 || statuses[0] != "done" {
		t.Fatalf("statuses=%v want [done]", statuses)
	}
}

func TestRunnerBuildError(t *testing.T) {
	runner, docker, _, hub := newRunnerSetup(t, "dockerfile")
	docker.buildErr = errors.New("simulated build error")
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err == nil {
		t.Fatal("expected error from failing ImageBuild")
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "failed" {
		t.Fatalf("last=%s want failed", statuses[len(statuses)-1])
	}
}

func TestRunnerContainerCreateError(t *testing.T) {
	runner, docker, _, hub := newRunnerSetup(t, "dockerfile")
	docker.createErr = errors.New("simulated create error")
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err == nil {
		t.Fatal("expected error from failing ContainerCreate")
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "failed" {
		t.Fatalf("last=%s want failed", statuses[len(statuses)-1])
	}
}

func TestRunnerRepoURLMissing(t *testing.T) {
	runner, _, _, hub := newRunnerSetup(t, "dockerfile")
	ctx := context.Background()

	if err := runner.Handle(ctx, protocol.JobAssignData{PreviewID: "p1", RepoURL: "", CommitSHA: "abc"}); err == nil {
		t.Fatal("expected error for empty RepoURL")
	}
	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "failed" {
		t.Fatalf("last=%s want failed", statuses[len(statuses)-1])
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
	port2, err := allocatePort(2)
	if err != nil {
		t.Fatalf("allocatePort 2: %v", err)
	}
	if port2 <= 0 {
		t.Fatalf("port2=%d", port2)
	}
}

// TestRunnerHandleDeferTriggersReady — Handle 성공 후 ReadySender 1회 호출.
func TestRunnerHandleDeferTriggersReady(t *testing.T) {
	runner, _, _, _ := newRunnerSetup(t, "dockerfile")
	fake := &fakeReadySender{}
	runner.SetReadySender(fake)
	runner.SetMaxJobs(1)
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("ReadySender calls=%d want 1", got)
	}
}

// TestRunnerHandleDeferOnFailureTriggersReady — 실패 path 에서도 defer 가 maybeSendReady 호출.
func TestRunnerHandleDeferOnFailureTriggersReady(t *testing.T) {
	runner, docker, _, _ := newRunnerSetup(t, "dockerfile")
	docker.createErr = errors.New("docker create fail")
	fake := &fakeReadySender{}
	runner.SetReadySender(fake)
	runner.SetMaxJobs(2)
	ctx := context.Background()

	_ = runner.Handle(ctx, testMsg("p1"))
	if got := fake.count(); got != 2 {
		t.Fatalf("ReadySender calls=%d want 2", got)
	}
}

// TestRunnerHandlePausedRejectsWithoutReady — paused=true 시 Handle 즉시 거절.
func TestRunnerHandlePausedRejectsWithoutReady(t *testing.T) {
	runner, _, _, hub := newRunnerSetup(t, "dockerfile")
	fake := &fakeReadySender{}
	runner.SetReadySender(fake)
	runner.SetMaxJobs(3)
	runner.Pause()
	ctx := context.Background()

	if err := runner.Handle(ctx, testMsg("p1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := fake.count(); got != 0 {
		t.Fatalf("ReadySender calls=%d want 0 (paused)", got)
	}
	statuses := hub.statuses()
	if len(statuses) != 1 || statuses[0] != "failed" {
		t.Fatalf("statuses=%v want [failed]", statuses)
	}
}

// TestRunnerHandleInFlightPauseBlocksReady — Handle 진행 중 Pause 호출 시 maybeSendReady 차단.
func TestRunnerHandleInFlightPauseBlocksReady(t *testing.T) {
	runner, _, _, _ := newRunnerSetup(t, "dockerfile")
	fake := &fakeReadySender{}
	runner.SetReadySender(fake)
	runner.SetMaxJobs(2)
	ctx := context.Background()

	runner.inFlight.Add(1)
	defer func() { runner.inFlight.Add(-1) }()
	runner.Pause()
	runner.maybeSendReady(ctx)
	if got := fake.count(); got != 0 {
		t.Fatalf("ReadySender calls=%d want 0 (paused mid-handle)", got)
	}
}

// shaResolveCmd 는 worktreeFileRunner 와 같은 Run 동작을 유지하면서, Output("git rev-parse HEAD")
// 호출에 대해서만 미리 설정된 sha 를 반환하는 cmd. F-11 / F-21 / NF-8 검증용.
type shaResolveCmd struct {
	mu        sync.Mutex
	calls     [][]string
	outCalls  [][]string
	revParse  string // "" 면 Output 이 에러를 반환하도록 한다.
	outputErr error
}

func (c *shaResolveCmd) Run(ctx context.Context, name string, args ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, append([]string{name}, args...))
	return nil
}

func (c *shaResolveCmd) Output(ctx context.Context, name string, args ...string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outCalls = append(c.outCalls, append([]string{name}, args...))
	if c.outputErr != nil {
		return "", c.outputErr
	}
	// git rev-parse HEAD 시나리오만 처리. 그 외에는 빈 문자열.
	joined := strings.Join(args, " ")
	if name == "git" && strings.Contains(joined, "rev-parse") {
		if c.revParse == "" {
			return "", errors.New("rev-parse failed (test fake)")
		}
		return c.revParse, nil
	}
	return "", nil
}

// TestRunnerSendsBuildingTwiceWithSha (F-21, F-11): Handle 가 building STATUS_UPDATE 를
// 두 번 송신하며, 첫 번째는 CommitSHA=nil, 두 번째는 worktree HEAD resolve 결과 동봉.
func TestRunnerSendsBuildingTwiceWithSha(t *testing.T) {
	root := t.TempDir()
	multi := NewMultiRepoCache(root, nil)
	rc := NewRepoCache(root, testRepoURL, nil)
	base := &fakeRunner{revParseOK: true}
	rc.SetRunner(&worktreeFileRunner{base: base, mode: "dockerfile"})
	multi.mu.Lock()
	multi.repos[testRepoURL] = rc
	multi.mu.Unlock()

	docker := &fakeDocker{}
	hub := &runnerFakeHub{}
	cmd := &shaResolveCmd{revParse: "ff00aa"}
	runner := NewRunner(docker, multi, cmd, hub, "127.0.0.1", nil)

	if err := runner.Handle(context.Background(), testMsg("p1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()

	// building 횟수 ≥ 2 (첫 building + sha 동봉 building).
	buildingUpdates := []protocol.StatusUpdateData{}
	for _, u := range hub.updates {
		if u.Status == "building" {
			buildingUpdates = append(buildingUpdates, u)
		}
	}
	if len(buildingUpdates) < 2 {
		t.Fatalf("building updates=%d want >=2", len(buildingUpdates))
	}
	// 첫 번째: CommitSHA=nil.
	if buildingUpdates[0].CommitSHA != nil {
		t.Errorf("first building CommitSHA=%v want nil", buildingUpdates[0].CommitSHA)
	}
	// 두 번째: CommitSHA=&"ff00aa".
	if buildingUpdates[1].CommitSHA == nil || *buildingUpdates[1].CommitSHA != "ff00aa" {
		t.Errorf("second building CommitSHA=%v want &\"ff00aa\"", buildingUpdates[1].CommitSHA)
	}
	// rev-parse HEAD 가 호출됐는지.
	revParseCalled := false
	for _, c := range cmd.outCalls {
		if len(c) >= 4 && c[0] == "git" && c[len(c)-2] == "rev-parse" && c[len(c)-1] == "HEAD" {
			revParseCalled = true
		}
	}
	if !revParseCalled {
		t.Errorf("git rev-parse HEAD not called: %+v", cmd.outCalls)
	}
}

// TestRunnerShaResolveFailureDoesNotAbortBuild (NF-8): rev-parse 실패해도 빌드는 진행되며,
// 두 번째 building STATUS_UPDATE 의 CommitSHA 는 nil 로 남는다.
func TestRunnerShaResolveFailureDoesNotAbortBuild(t *testing.T) {
	root := t.TempDir()
	multi := NewMultiRepoCache(root, nil)
	rc := NewRepoCache(root, testRepoURL, nil)
	base := &fakeRunner{revParseOK: true}
	rc.SetRunner(&worktreeFileRunner{base: base, mode: "dockerfile"})
	multi.mu.Lock()
	multi.repos[testRepoURL] = rc
	multi.mu.Unlock()

	docker := &fakeDocker{}
	hub := &runnerFakeHub{}
	cmd := &shaResolveCmd{revParse: ""} // 빈 sha → Output 에러 반환.
	runner := NewRunner(docker, multi, cmd, hub, "127.0.0.1", nil)

	if err := runner.Handle(context.Background(), testMsg("p1")); err != nil {
		t.Fatalf("Handle: %v (NF-8: 빌드 중단 안 함)", err)
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	// running 까지 도달했는지 확인.
	last := hub.updates[len(hub.updates)-1]
	if last.Status != "running" {
		t.Fatalf("last status=%s want running (NF-8)", last.Status)
	}
	// building 메시지 모두 CommitSHA=nil (resolve 실패 → nil).
	for i, u := range hub.updates {
		if u.Status == "building" && u.CommitSHA != nil {
			t.Errorf("building[%d] CommitSHA=%v want nil (resolve failed)", i, u.CommitSHA)
		}
	}
}
