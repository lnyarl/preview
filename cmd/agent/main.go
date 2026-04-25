// Command agent is the Preview data plane entry point.
//
// Phase 1: "start" 서브커맨드로 Hub 에 outbound WebSocket 연결.
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-9.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lnyarl/preview/internal/agent"
)

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
		"repo_url", cfg.RepoURL,
		"work_dir", cfg.WorkDir,
		"prefetch_interval", cfg.PrefetchInterval.String(),
		"max_jobs", cfg.MaxJobs,
	)

	// Phase 2: RepoCache + Docker SDK + Runner + Client wiring.
	cache := agent.NewRepoCache(cfg.WorkDir, cfg.RepoURL, logger)
	if err := cache.Ensure(ctx); err != nil {
		return fmt.Errorf("repocache ensure: %w", err)
	}
	if cfg.PrefetchInterval > 0 {
		go cache.StartPrefetch(ctx, cfg.PrefetchInterval)
	}

	docker, err := newSDKDockerClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	if err := docker.Ping(ctx); err != nil {
		logger.Warn("docker_ping_failed", "err", err.Error())
	}

	c := agent.NewClient(cfg, logger)
	runner := agent.NewRunner(docker, cache, c, cfg.AdvertiseHost, logger)
	c.SetRunner(runner)

	err = c.Run(ctx)
	logger.Info("graceful shutdown")
	return err
}
