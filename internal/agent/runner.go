// 이 파일의 책임:
//   - JOB_ASSIGN 수신 시 building → docker build → docker run → STATUS_UPDATE running 송신.
//   - JOB_TEARDOWN 수신 시 컨테이너 stop+rm → worktree remove → STATUS_UPDATE done 송신.
//   - 동적 포트 할당 (net.Listen ":0") + 1회 재시도 (결정 8, F-S2-14).
//   - jobs map: previewID → 메모리 상태 (containerID, port, worktree). 재시작 복원은 Step 3 이월.
//
// fake DockerClient 로 단위 테스트 주입 (NF-Test-Docker-1).
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1, 결정 6/8.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/lnyarl/preview/internal/protocol"
)

// HubSender 는 Agent → Hub 메시지 송신 인터페이스.
// 구현은 client.go 의 Client 가 conn.Write 를 호출.
type HubSender interface {
	SendStatusUpdate(ctx context.Context, d protocol.StatusUpdateData) error
}

// runningJob 은 jobs 맵의 엔트리.
type runningJob struct {
	previewID    string
	containerID  string
	host         string
	port         int
	worktreePath string
}

// Runner 는 JOB_ASSIGN/TEARDOWN 처리 로직.
type Runner struct {
	docker  DockerClient
	cache   *RepoCache
	hub     HubSender
	advHost string
	logger  *slog.Logger

	jobs     sync.Map    // previewID -> *runningJob
	paused   atomic.Bool // Phase 3: Pause() 후 신규 JOB_ASSIGN 거절.
	inFlight atomic.Int64
}

// Pause 는 graceful shutdown 시 신규 JOB_ASSIGN 을 거절하도록 표시한다.
// 진행 중 build 는 끝까지 진행 (결정 11).
func (r *Runner) Pause() { r.paused.Store(true) }

// Paused 는 현재 paused 여부를 반환한다.
func (r *Runner) Paused() bool { return r.paused.Load() }

// InFlight 는 현재 진행 중인 build/run 갯수를 반환한다 (drain polling 용).
func (r *Runner) InFlight() int { return int(r.inFlight.Load()) }

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

// RegisterRestoredJob 는 orphan_restore.go 가 docker.ContainerList 결과로 발견한
// 컨테이너를 jobs 맵에 등록한다 (HELLO 직전 호출).
func (r *Runner) RegisterRestoredJob(previewID, containerID, host string, port int) {
	r.jobs.Store(previewID, &runningJob{
		previewID:   previewID,
		containerID: containerID,
		host:        host,
		port:        port,
	})
}

// NewRunner 는 Runner 를 조립한다.
func NewRunner(docker DockerClient, cache *RepoCache, hub HubSender, advHost string, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(silentWriter{}, nil))
	}
	if advHost == "" {
		advHost = "127.0.0.1"
	}
	return &Runner{
		docker:  docker,
		cache:   cache,
		hub:     hub,
		advHost: advHost,
		logger:  logger,
	}
}

// Handle 은 JOB_ASSIGN 1건을 처리한다. 실패 시 STATUS_UPDATE failed 송신.
// 호출자는 보통 별도 goroutine 으로 호출.
//
// Phase 3: Pause() 호출 후 진입한 호출은 즉시 STATUS_UPDATE(failed,
// "agent shutting down") 송신 후 무시 (결정 11 / §5-13).
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
	defer r.inFlight.Add(-1)
	r.logger.Info("agent_job_assign", "preview_id", pid, "repo_url", msg.RepoURL, "sha", msg.CommitSHA)

	// (1) building 송신.
	if err := r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
		PreviewID: pid,
		Status:    "building",
	}); err != nil {
		r.logger.Warn("status_update_building_failed", "preview_id", pid, "err", err.Error())
	}

	// (2) repo Ensure + Checkout.
	if err := r.cache.Ensure(ctx); err != nil {
		return r.fail(ctx, pid, "repocache_ensure", err)
	}
	worktree, err := r.cache.Checkout(ctx, pid, msg.CommitSHA)
	if err != nil {
		return r.fail(ctx, pid, "repocache_checkout", err)
	}

	// (3) Dockerfile 존재 확인.
	if _, err := os.Stat(filepath.Join(worktree, "Dockerfile")); err != nil {
		return r.fail(ctx, pid, "dockerfile_missing", err)
	}

	// (4) docker build.
	tag := "preview-" + pid + ":latest"
	logs, err := r.docker.ImageBuild(ctx, worktree, BuildOptions{Tag: tag})
	if err != nil {
		return r.fail(ctx, pid, "image_build", err)
	}
	if logs != nil {
		_, _ = io.Copy(io.Discard, logs)
		_ = logs.Close()
	}

	// (5) 동적 포트 할당.
	hostPort, err := allocatePort(2)
	if err != nil {
		return r.fail(ctx, pid, "allocate_port", err)
	}

	// (6) 컨테이너 생성/시작.
	cid, err := r.docker.ContainerCreate(ctx, CreateOptions{
		Image:       tag,
		Labels:      map[string]string{"hub-preview-id": pid},
		HostPort:    hostPort,
		ExposedPort: 80,
	})
	if err != nil {
		return r.fail(ctx, pid, "container_create", err)
	}
	if err := r.docker.ContainerStart(ctx, cid); err != nil {
		_ = r.docker.ContainerRemove(ctx, cid, RemoveOptions{Force: true})
		return r.fail(ctx, pid, "container_start", err)
	}

	// (7) jobs 맵 등록.
	r.jobs.Store(pid, &runningJob{
		previewID:    pid,
		containerID:  cid,
		host:         r.advHost,
		port:         hostPort,
		worktreePath: worktree,
	})

	// (8) running STATUS_UPDATE.
	host := r.advHost
	port := hostPort
	if err := r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{
		PreviewID:   pid,
		Status:      "running",
		ContainerID: &cid,
		AgentHost:   &host,
		AgentPort:   &port,
	}); err != nil {
		r.logger.Warn("status_update_running_failed", "preview_id", pid, "err", err.Error())
	}
	return nil
}

// Teardown 은 JOB_TEARDOWN 1건을 처리한다.
func (r *Runner) Teardown(ctx context.Context, previewID string) error {
	v, ok := r.jobs.Load(previewID)
	if !ok {
		// 메모리 미등록 — 그래도 worktree 정리 시도 + done 송신.
		_ = r.cache.Remove(ctx, previewID)
		_ = r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{PreviewID: previewID, Status: "done"})
		return nil
	}
	job := v.(*runningJob)
	_ = r.docker.ContainerStop(ctx, job.containerID)
	_ = r.docker.ContainerRemove(ctx, job.containerID, RemoveOptions{Force: true})
	if err := r.cache.Remove(ctx, previewID); err != nil {
		r.logger.Warn("teardown_worktree_remove_failed", "preview_id", previewID, "err", err.Error())
	}
	r.jobs.Delete(previewID)
	if err := r.hub.SendStatusUpdate(ctx, protocol.StatusUpdateData{PreviewID: previewID, Status: "done"}); err != nil {
		return fmt.Errorf("runner.Teardown: send done: %w", err)
	}
	return nil
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
// retries 가 1 이상이면 1회 재시도. 0 이면 단일 시도.
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
