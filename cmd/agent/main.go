// Command agent is the Preview data plane entry point.
//
// Phase 1: "start" 서브커맨드로 Hub 에 outbound WebSocket 연결.
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-9.
package main

import (
	"context"
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

	logger.Info("agent_start", "hub_url", cfg.HubURL)
	c := agent.NewClient(cfg, logger)
	err = c.Run(ctx)
	logger.Info("graceful shutdown")
	return err
}
