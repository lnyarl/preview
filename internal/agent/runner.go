// 이 파일의 책임:
//   - JOB_ASSIGN: MultiRepoCache.Ensure+Checkout → .preview.yml 로드 →
//     compose / Dockerfile 분기 빌드 → STATUS_UPDATE running (preview_urls 포함).
//   - JOB_TEARDOWN: buildMode 에 따라 compose down / docker stop+rm →
//     worktree remove → STATUS_UPDATE done.
//   - Phase 6: Holder/run_commands 제거 (결정 13). MultiRepoCache (결정 8).
//     Traefik path-prefix 라우팅 (결정 10/11). preview_urls 계산 (결정 11/14).
//   - Phase 5: Handle 의 defer 가 슬롯 회복 후 maybeSendReady (ready.go) 를 호출.
//
// 참고: phase-6 §4-4, 결정 3/5/8/9/10/11/13/14.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lnyarl/preview/internal/protocol"
)

const (
	buildModeCompose    = "compose"
	buildModeDockerfile = "dockerfile"
)

// HubSender 는 Agent → Hub 메시지 송신 인터페이스.
// 구현은 client.go 의 Client 가 conn.Write 를 호출.
type HubSender interface {
	SendStatusUpdate(ctx context.Context, d protocol.StatusUpdateData) error
}

// runningJob 은 jobs 맵의 엔트리.
type runningJob struct {
	previewID    string
	containerID  string // Dockerfile 모드 전용. compose 모드에서는 비어있음.
	host         string
	port         int // Traefik 호스트 포트 (preview 접근 진입점).
	worktreePath string
	repoURL      string
	buildMode    string // buildModeCompose 또는 buildModeDockerfile.
	composeFile  string // compose 모드에서 사용한 compose 파일 절대경로.
}

// Runner 는 JOB_ASSIGN/TEARDOWN 처리 로직.
type Runner struct {
	docker             DockerClient
	cache              *MultiRepoCache
	cmd                CmdRunner // docker compose 명령 실행용 (Phase 6).
	hub                HubSender
	advHost            string
	traefikPort        int           // Traefik 웹 호스트 포트 (Phase 6, default 8080).
	traefikAPIPort     int           // Phase 7: Traefik API 호스트 포트 (default 9080, 0=비활성).
	routerReadyTimeout time.Duration // Phase 7: WaitTraefikRouters timeout (default 30s, 0=비활성).
	logger             *slog.Logger
	ready              ReadySender // Phase 5: READY 송신 의존 (nil 허용 = no-op).
	maxJobs            int         // Phase 5: 동시 슬롯 한도 (NewRunner default = 1).

	jobs     sync.Map    // previewID -> *runningJob
	paused   atomic.Bool // Phase 3: Pause() 후 신규 JOB_ASSIGN 거절.
	inFlight atomic.Int64
}

// Pause 는 graceful shutdown 시 신규 JOB_ASSIGN 을 거절하도록 표시한다.
func (r *Runner) Pause() { r.paused.Store(true) }

// Paused 는 현재 paused 여부를 반환한다.
func (r *Runner) Paused() bool { return r.paused.Load() }

// InFlight 는 현재 진행 중인 build/run 갯수를 반환한다.
func (r *Runner) InFlight() int { return int(r.inFlight.Load()) }

// SetTraefikPort 는 Traefik 호스트 포트를 설정한다 (Phase 6, default 8080).
func (r *Runner) SetTraefikPort(port int) {
	if port > 0 {
		r.traefikPort = port
	}
}

// Cmd 는 Runner 가 내부에서 docker/git 명령 실행에 사용하는 CmdRunner 를
// 외부 호출자(예: main.go 의 PruneComposeOrphans wiring) 에 노출한다.
// Phase 8: orphan cleanup 의 cmd 주입 경로 (결정 2 옵션 (b)).
func (r *Runner) Cmd() CmdRunner { return r.cmd }

// RunningPreviewIDs 는 jobs 맵에 있는 previewID 슬라이스 (HELLO.RunningPreviews 채움용).
func (r *Runner) RunningPreviewIDs() []string {
	out := []string{}
	r.jobs.Range(func(k, _ any) bool {
		if id, ok := k.(string); ok {
			out = append(out, id)
		}
		return true
	})
	return out
}

// RegisterRestoredJob 는 orphan_restore.go 가 발견한 컨테이너를 jobs 맵에 등록한다.
// Phase 6: Dockerfile 모드 컨테이너(hub-preview-id 라벨 보유)만 복원 가능.
func (r *Runner) RegisterRestoredJob(previewID, containerID, host string, port int) {
	r.jobs.Store(previewID, &runningJob{
		previewID:   previewID,
		containerID: containerID,
		host:        host,
		port:        port,
		buildMode:   buildModeDockerfile,
	})
}

// NewRunner 는 Runner 를 조립한다. cmd 가 nil 이면 execRunner{} 를 사용한다.
func NewRunner(docker DockerClient, cache *MultiRepoCache, cmd CmdRunner, hub HubSender, advHost string, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(silentWriter{}, nil))
	}
	if advHost == "" {
		advHost = "127.0.0.1"
	}
	if cmd == nil {
		cmd = execRunner{}
	}
	return &Runner{
		docker:      docker,
		cache:       cache,
		cmd:         cmd,
		hub:         hub,
		advHost:     advHost,
		traefikPort: 8080,
		logger:      logger,
		maxJobs:     1,
	}
}

// Handle 은 JOB_ASSIGN 1건을 처리한다. 실패 시 STATUS_UPDATE failed 송신.
func (r *Runner) Handle(ctx context.Context, msg protocol.JobAssignData) error {
	pid := msg.PreviewID
	if r.paused.Load() {
		r.logger.Info("agent_shutdown_reject_job", "preview_id", pid)
		errMsg := "agent shutting down"
		_ = r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
			PreviewID:    pid,
			Status:       "failed",
			Message:      "agent shutting down",
			ErrorMessage: &errMsg,
		})
		return nil
	}
	r.inFlight.Add(1)
	defer func() { r.inFlight.Add(-1); r.maybeSendReady(ctx) }()
	r.logger.Info("agent_job_assign", "preview_id", pid, "repo_url", msg.RepoURL, "sha", msg.CommitSHA)

	// (1) 첫 번째 building 송신 — assigned → building 즉시 전이.
	// 이 메시지가 Checkout(수십 초 가능) 전에 가야 reconciler 의 staleAssigned 회수 race 를
	// 회피한다(Phase 9 결정 5). sha 는 아직 모름 → nil.
	if err := r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
		PreviewID: pid,
		Status:    "building",
		Message:   "checkout_started",
		CommitSHA: nil,
	}); err != nil {
		r.logger.Warn("status_update_building_failed", "preview_id", pid, "err", err.Error())
	}

	if msg.RepoURL == "" {
		return r.fail(ctx, pid, "repo_url_missing", errors.New("JobAssignData.RepoURL is empty"))
	}

	// (2) repo Ensure + Checkout.
	if err := r.cache.Ensure(ctx, msg.RepoURL); err != nil {
		return r.fail(ctx, pid, "repocache_ensure", err)
	}
	worktree, err := r.cache.Checkout(ctx, msg.RepoURL, pid, msg.CommitSHA, msg.Branch)
	if err != nil {
		return r.fail(ctx, pid, "repocache_checkout", err)
	}

	// (2b) 두 번째 building 송신 — sha 동봉 (Phase 9 결정 5).
	// worktree HEAD 의 git rev-parse 로 실제 sha 추출. 빈 message 로 보내 첫 번째 message
	// 를 덮어쓰지 않게 한다(store 가 빈 message 를 무변경 처리). resolve 실패해도 빌드 중단하지
	// 않음(NF-8) — 단순히 sha=nil 로 보내 hub row 의 commit_sha 는 NULL 유지.
	resolvedSha := ""
	if out, gerr := r.cmd.Output(ctx, "git", "-C", worktree, "rev-parse", "HEAD"); gerr == nil {
		resolvedSha = strings.TrimSpace(out)
	} else {
		r.logger.Warn("rev_parse_head_failed", "preview_id", pid, "err", gerr.Error())
	}
	var shaPtr *string
	if resolvedSha != "" {
		shaPtr = &resolvedSha
	}
	if err := r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
		PreviewID: pid,
		Status:    "building", // 자기-루프 전이 (building → building)
		Message:   "",         // 빈 message → store 단계에서 무변경 처리, 첫 message 보존
		CommitSHA: shaPtr,
	}); err != nil {
		r.logger.Warn("status_update_building_sha_failed", "preview_id", pid, "err", err.Error())
	}

	// (3) .preview.yml 로드.
	cfg, err := LoadPreviewConfig(worktree)
	if err != nil {
		if errors.Is(err, ErrPreviewConfigMissing) {
			return r.fail(ctx, pid, "preview_config_missing", err)
		}
		return r.fail(ctx, pid, "preview_config_invalid", err)
	}

	// (4) 빌드 파일 감지 + 분기.
	composefile, _, mode, detectErr := r.detectBuildFiles(worktree, cfg)
	if detectErr != nil {
		return r.fail(ctx, pid, "no_build_artifact", detectErr)
	}

	var containerID string
	switch mode {
	case buildModeCompose:
		containerID, err = r.handleCompose(ctx, pid, worktree, composefile, cfg)
	default:
		containerID, err = r.handleDockerfile(ctx, pid, worktree, cfg)
	}
	if err != nil {
		return err
	}

	// (5) Phase 7: Traefik 라우터 활성화 대기 (best-effort, 결정 4/11).
	r.waitRouters(ctx, pid, cfg, mode)

	// (6) preview URLs.
	urls := PreviewURLs(pid, cfg, r.advHost, r.traefikPort)

	// (7) jobs 맵 등록.
	r.jobs.Store(pid, &runningJob{
		previewID:    pid,
		containerID:  containerID,
		host:         r.advHost,
		port:         r.traefikPort,
		worktreePath: worktree,
		repoURL:      msg.RepoURL,
		buildMode:    mode,
		composeFile:  composefile,
	})

	// (8) running STATUS_UPDATE.
	host := r.advHost
	port := r.traefikPort
	var cidPtr *string
	if containerID != "" {
		cidPtr = &containerID
	}
	if err := r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
		PreviewID:   pid,
		Status:      "running",
		ContainerID: cidPtr,
		AgentHost:   &host,
		AgentPort:   &port,
		PreviewURLs: urls,
	}); err != nil {
		r.logger.Warn("status_update_running_failed", "preview_id", pid, "err", err.Error())
	}
	return nil
}

// handleCompose 는 compose 모드: override 파일 생성 + docker compose up -d.
func (r *Runner) handleCompose(ctx context.Context, pid, worktree, composefile string, cfg PreviewConfig) (string, error) {
	overrideData, err := ComposeOverrideYAML(pid, cfg)
	if err != nil {
		return "", r.fail(ctx, pid, "compose_override_yaml", err)
	}
	overridePath := filepath.Join(worktree, ".preview-override-"+pid+".yml")
	if err := os.WriteFile(overridePath, overrideData, 0o644); err != nil {
		return "", r.fail(ctx, pid, "compose_override_write", err)
	}
	projectName := "preview-" + pid
	if err := r.cmd.Run(ctx, "docker", "compose",
		"-f", composefile,
		"-f", overridePath,
		"--project-name", projectName,
		"up", "-d",
	); err != nil {
		return "", r.fail(ctx, pid, "compose_up", err)
	}
	return "", nil
}

// handleDockerfile 는 Dockerfile 모드: docker build + ContainerCreate/Start.
func (r *Runner) handleDockerfile(ctx context.Context, pid, worktree string, cfg PreviewConfig) (string, error) {
	tag := "preview-" + pid + ":latest"
	if _, err := r.docker.ImageBuild(ctx, worktree, BuildOptions{Tag: tag}); err != nil {
		return "", r.fail(ctx, pid, "image_build", err)
	}
	svcName, svc, ok := cfg.FirstService()
	if !ok {
		return "", r.fail(ctx, pid, "no_service", errors.New("no service defined in .preview.yml"))
	}
	labels := ServiceLabels(pid, svcName, svc)
	labels["hub-preview-id"] = pid

	cid, err := r.docker.ContainerCreate(ctx, CreateOptions{
		Image:    tag,
		Name:     "preview-" + pid,
		Labels:   labels,
		Networks: []string{traefikNetwork},
		// Phase 7: 외부 노출 X (path-prefix 라우팅) — HostPort=0 expose only.
		PortBindings: []PortBinding{
			{ContainerPort: svc.Port, HostPort: 0},
		},
	})
	if err != nil {
		return "", r.fail(ctx, pid, "container_create", err)
	}
	if err := r.docker.ContainerStart(ctx, cid); err != nil {
		_ = r.docker.ContainerRemove(ctx, cid, RemoveOptions{Force: true})
		return "", r.fail(ctx, pid, "container_start", err)
	}
	return cid, nil
}

// Teardown 은 JOB_TEARDOWN 1건을 처리한다.
func (r *Runner) Teardown(ctx context.Context, previewID string) error {
	v, ok := r.jobs.Load(previewID)
	if !ok {
		_ = r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{PreviewID: previewID, Status: "done"})
		return nil
	}
	job := v.(*runningJob)

	switch job.buildMode {
	case buildModeCompose:
		projectName := "preview-" + previewID
		if err := r.cmd.Run(ctx, "docker", "compose", "--project-name", projectName, "down"); err != nil {
			r.logger.Warn("compose_down_failed", "preview_id", previewID, "err", err.Error())
		}
		overridePath := filepath.Join(job.worktreePath, ".preview-override-"+previewID+".yml")
		_ = os.Remove(overridePath)
	default:
		_ = r.docker.ContainerStop(ctx, job.containerID)
		_ = r.docker.ContainerRemove(ctx, job.containerID, RemoveOptions{Force: true})
	}

	if job.repoURL != "" {
		if err := r.cache.Remove(ctx, job.repoURL, previewID); err != nil {
			r.logger.Warn("teardown_worktree_remove_failed", "preview_id", previewID, "err", err.Error())
		}
	}
	r.jobs.Delete(previewID)

	if err := r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{PreviewID: previewID, Status: "done"}); err != nil {
		return fmt.Errorf("runner.Teardown: send done: %w", err)
	}
	return nil
}

// detectBuildFiles 는 worktree 에서 compose/Dockerfile 파일을 감지한다.
// compose 파일 우선 (결정 9).
func (r *Runner) detectBuildFiles(worktree string, cfg PreviewConfig) (composefile, dockerfilepath, mode string, err error) {
	if cfg.ComposeFile != "" {
		return filepath.Join(worktree, cfg.ComposeFile), "", buildModeCompose, nil
	}
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		p := filepath.Join(worktree, name)
		if _, statErr := os.Stat(p); statErr == nil {
			return p, "", buildModeCompose, nil
		}
	}
	if cfg.Dockerfile != "" {
		return "", filepath.Join(worktree, cfg.Dockerfile), buildModeDockerfile, nil
	}
	p := filepath.Join(worktree, "Dockerfile")
	if _, statErr := os.Stat(p); statErr == nil {
		return "", p, buildModeDockerfile, nil
	}
	return "", "", "", errors.New("no docker-compose.yml or Dockerfile found in worktree")
}

// fail 은 STATUS_UPDATE failed 를 송신하고 에러를 반환한다.
func (r *Runner) fail(ctx context.Context, previewID, reason string, cause error) error {
	msg := reason
	if cause != nil {
		msg = reason + ": " + cause.Error()
	}
	r.logger.Warn("agent_job_failed", "preview_id", previewID, "reason", reason, "err", cause)
	errMsg := msg
	_ = r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
		PreviewID:    previewID,
		Status:       "failed",
		Message:      reason,
		ErrorMessage: &errMsg,
	})
	return errors.New(msg)
}

// allocatePort 는 net.Listen(":0") 으로 free port 를 추출한다.
// Phase 6: 컨테이너 직접 host 포트 노출이 불필요해졌으나 유틸리티로 유지.
func allocatePort(retries int) (int, error) {
	var lastErr error
	if retries < 1 {
		retries = 1
	}
	for i := 0; i < retries; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			lastErr = err
			continue
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		if port > 0 {
			return port, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("allocatePort: no port")
	}
	return 0, lastErr
}
