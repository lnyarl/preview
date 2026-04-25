// 이 파일의 책임:
//   - Agent CLI 플래그·env 파싱.
//   - Phase 2: --repo-url(필수) / --work-dir / --prefetch-interval / --max-jobs.
//   - Phase 5: --max-jobs hard cap 64 클램프 (결정 11).
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-7,
//
//	docs/specs/phase-2-webhook-dispatch-proxy.md §5-9,
//	docs/specs/phase-5-multi-job.md §3 결정 11.
package agent

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

// ErrMissingRequiredFlag 는 필수 플래그(env 포함) 부재를 표시하는 sentinel.
// main.go 가 이 오류를 식별해 exit code 2(usage error)로 매핑한다 (F-S2-5).
var ErrMissingRequiredFlag = errors.New("missing required flag")

// maxJobsHardCap 은 --max-jobs 의 상한 (Phase 5 결정 11).
// 일반 머신 코어 수의 4~8 배 상한 — 비현실적 입력 방어선.
const maxJobsHardCap = 64

// Config 는 Agent start 서브커맨드 실행에 필요한 설정.
// 라벨은 Hub 에서 관리하므로 Agent 에 없다 — Hub 대시보드에서 에이전트별로 설정한다.
type Config struct {
	HubURL           string
	Token            string
	AdvertiseHost    string
	LogLevel         string
	RepoURL          string        // Phase 2: 결정 9 (1 Agent = 1 repo).
	WorkDir          string        // Phase 2: RepoCache 루트.
	PrefetchInterval time.Duration // Phase 2: 0 = 비활성.
	MaxJobs          int           // Phase 2: 동시 슬롯 한도. 기본 1.
}

// ParseConfig 는 주어진 args 로부터 Config 를 만든다. args 는 os.Args[2:] 상당.
// env: HUB_URL, HUB_TOKEN, AGENT_ADVERTISE_HOST, LOG_LEVEL,
// AGENT_REPO_URL, AGENT_WORK_DIR, AGENT_PREFETCH_INTERVAL, AGENT_MAX_JOBS.
func ParseConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("agent start", flag.ContinueOnError)
	var (
		hubURL   = fs.String("hub-url", os.Getenv("HUB_URL"), "Hub WebSocket URL, e.g. ws://localhost:3000/agent/ws")
		tokenFlg = fs.String("token", os.Getenv("HUB_TOKEN"), "Agent token (agt_...)")
		advHost  = fs.String("advertise-host", os.Getenv("AGENT_ADVERTISE_HOST"), "Host address to advertise in HELLO")
		logLvl   = fs.String("log-level", envOr("LOG_LEVEL", "info"), "log level: debug/info/warn/error")
		repoURL  = fs.String("repo-url", os.Getenv("AGENT_REPO_URL"), "git repo URL (required for Phase 2)")
		workDir  = fs.String("work-dir", envOr("AGENT_WORK_DIR", defaultWorkDir()), "RepoCache root directory")
		prefetch = fs.String("prefetch-interval", envOr("AGENT_PREFETCH_INTERVAL", "5m"), "background fetch interval; 0 disables")
		maxJobs  = fs.Int("max-jobs", envInt("AGENT_MAX_JOBS", 1), "max concurrent preview jobs")
	)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if *hubURL == "" {
		return Config{}, fmt.Errorf("%w: --hub-url or HUB_URL required", ErrMissingRequiredFlag)
	}
	if *tokenFlg == "" {
		return Config{}, fmt.Errorf("%w: --token or HUB_TOKEN required", ErrMissingRequiredFlag)
	}
	if *repoURL == "" {
		return Config{}, fmt.Errorf("%w: --repo-url or AGENT_REPO_URL required", ErrMissingRequiredFlag)
	}
	pf, err := time.ParseDuration(*prefetch)
	if err != nil {
		return Config{}, fmt.Errorf("invalid --prefetch-interval %q: %w", *prefetch, err)
	}
	if *maxJobs < 1 {
		*maxJobs = 1
	}
	// Phase 5 결정 11: hard cap 64 — 운영자 실수(예: --max-jobs 10000)로
	// maybeSendReady 의 conn.Write 폭주를 방지한다. 거절(exit 2)이 아닌 클램프
	// + warn 로그로 처리해 자동 부팅 스크립트의 운영 사고를 막는다.
	if *maxJobs > maxJobsHardCap {
		fmt.Fprintf(os.Stderr,
			"warn: --max-jobs=%d exceeds hard cap %d; clamping to %d\n",
			*maxJobs, maxJobsHardCap, maxJobsHardCap)
		*maxJobs = maxJobsHardCap
	}
	return Config{
		HubURL:           *hubURL,
		Token:            *tokenFlg,
		AdvertiseHost:    *advHost,
		LogLevel:         *logLvl,
		RepoURL:          *repoURL,
		WorkDir:          *workDir,
		PrefetchInterval: pf,
		MaxJobs:          *maxJobs,
	}, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// defaultWorkDir 은 ~/.hub-agent (HOME 미설정 시 ./.hub-agent).
func defaultWorkDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h + string(os.PathSeparator) + ".hub-agent"
	}
	return ".hub-agent"
}
