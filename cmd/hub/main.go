// Command hub is the Preview control plane entry point.
//
// 서브커맨드:
//
//	(none)              : Hub HTTP/WS 데몬 기동 (Phase 2: GITHUB_WEBHOOK_SECRET 필수)
//	migrate up|down     : 임베드된 마이그레이션 적용/되돌리기
//	migrate version     : 현재 migrate 버전 조회
//	agents list         : DB 의 모든 agent 를 JSON 배열로 stdout 에 출력
//	previews list       : DB 의 모든 preview 를 JSON 배열로 stdout 에 출력 (Phase 2)
//	previews show <id>  : 단건 preview 조회 (Phase 2)
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-9,
//
//	docs/specs/phase-2-webhook-dispatch-proxy.md §5-9.
package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	// 인자 없음 또는 첫 인자가 플래그(- 시작)면 데몬으로 라우팅.
	if len(args) == 0 || (len(args) > 0 && len(args[0]) > 0 && args[0][0] == '-') {
		if err := runDaemon(args); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	switch args[0] {
	case "migrate":
		if err := runMigrate(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	case "agents":
		if err := runAgents(args[1:]); err != nil {
			if err == errAgentsUsage {
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(2)
			}
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	case "previews":
		if err := runPreviews(args[1:]); err != nil {
			if err == errPreviewsUsage {
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(2)
			}
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		os.Exit(2)
	}
}
