// Command agent is the Preview data plane entry point.
//
// Phase 1: "start" 서브커맨드로 Hub 에 outbound WebSocket 연결.
// Phase 3: graceful shutdown — SIGTERM 시 runner.Pause + in-flight build drain (30s).
// Phase 5: ReadySender + MaxJobs wiring — 다중 동시 build 슬롯 제어.
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-9,
//
//	docs/specs/phase-3-admin-ui-and-mvp.md §5-13,
//	docs/specs/phase-5-multi-job.md §4-2.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lnyarl/preview/internal/agent"
)

// agentShutdownDrainTimeout 는 SIGTERM 후 in-flight build 대기 timeout (결정 13).
const agentShutdownDrainTimeout = 30 * time.Second

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agent start [flags]")
		os.Exit(2)
	}
	switch args[0] {
	case "start":
		if err := runStart(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			// 필수 플래그 부재(검증 단계 실패)는 usage 오류로 exit 2,
			// 그 외 런타임 오류는 exit 1 (NF-Validation, F-S2-5).
			if errors.Is(err, agent.ErrMissingRequiredFlag) {
				os.Exit(2)
			}
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func runStart(args []string) error {
	cfg, err := agent.ParseConfig(args)
	if err != nil {
		return err
	}
	logger := agent.NewLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("agent_start",
		"hub_url", cfg.HubURL,
		"work_dir", cfg.WorkDir,
		"prefetch_interval", cfg.PrefetchInterval.String(),
		"max_jobs", cfg.MaxJobs,
		"traefik_port", cfg.TraefikPort,
	)

	// Phase 6: MultiRepoCache — per-repoURL bare clone 디렉토리 관리 (결정 8).
	cache := agent.NewMultiRepoCache(cfg.WorkDir, logger)

	docker, err := newSDKDockerClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	if err := docker.Ping(ctx); err != nil {
		logger.Warn("docker_ping_failed", "err", err.Error())
	}

	// Phase 6: preview-net + Traefik 사이드카 idempotent 기동 (결정 4).
	if err := agent.EnsureNetwork(ctx, docker, "preview-net"); err != nil {
		logger.Warn("ensure_network_failed", "err", err.Error())
	}
	if err := agent.EnsureTraefik(ctx, docker, agent.TraefikSpec{
		Image:       cfg.TraefikImage,
		HostPort:    cfg.TraefikPort,
		APIHostPort: cfg.TraefikAPIPort, // Phase 7: 0 = probe 비활성.
		Network:     "preview-net",
		Container:   "preview-traefik",
	}); err != nil {
		logger.Warn("ensure_traefik_failed", "err", err.Error())
	}

	c := agent.NewClient(cfg, logger)
	runner := agent.NewRunner(docker, cache, nil, c, cfg.AdvertiseHost, logger)
	runner.SetTraefikPort(cfg.TraefikPort)
	// Phase 7: Traefik readiness probe wiring (0 = 비활성).
	runner.SetTraefikAPIPort(cfg.TraefikAPIPort)
	runner.SetRouterReadyTimeout(cfg.RouterReadyTimeout)
	c.SetRunner(runner)

	// Phase 5: 다중 Job 슬롯 + READY 재전송 의존 주입.
	runner.SetReadySender(c)
	runner.SetMaxJobs(cfg.MaxJobs)

	// Phase 3: orphan container restore (결정 11 / §4-7-1).
	if _, rerr := agent.RestoreOrphans(ctx, docker, runner, cfg.AdvertiseHost, logger); rerr != nil {
		logger.Warn("agent_orphan_restore_failed", "err", rerr.Error())
	}

	// Phase 3: SIGTERM → runner.Pause + in-flight drain (§5-13).
	go func() {
		<-ctx.Done()
		runner.Pause()
		logger.Info("agent_shutdown_pause")
		deadline := time.Now().Add(agentShutdownDrainTimeout)
		for runner.InFlight() > 0 && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		if runner.InFlight() > 0 {
			logger.Warn("agent_shutdown_drain_timeout", "remaining", runner.InFlight())
		}
		// wsClient close 는 ctx cancel 로 client.go 의 goroutine 이 처리.
	}()

	err = c.Run(ctx)
	logger.Info("graceful shutdown")
	return err
}
