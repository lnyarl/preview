// 이 파일의 책임:
//   - Agent CLI 플래그·env 파싱.
//   - Phase 2: --work-dir / --prefetch-interval / --max-jobs.
//   - Phase 5: --max-jobs hard cap 64 클램프 (결정 11).
//   - Phase 6: --repo-url 제거, --traefik-port / --traefik-image 추가 (결정 13/14).
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-7,
//
//	docs/specs/phase-6-docker-native.md §4-8.
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
const maxJobsHardCap = 64

// Config 는 Agent start 서브커맨드 실행에 필요한 설정.
type Config struct {
	HubURL           string
	Token            string
	AdvertiseHost    string
	LogLevel         string
	WorkDir          string        // RepoCache 루트.
	PrefetchInterval time.Duration // 0 = 비활성.
	MaxJobs          int           // 동시 슬롯 한도. 기본 1.
	TraefikPort      int           // Phase 6: Traefik 호스트 바인딩 포트 (default 8080).
	TraefikImage     string        // Phase 6: Traefik Docker 이미지 (default "traefik:v3.1").
}

// ParseConfig 는 주어진 args 로부터 Config 를 만든다. args 는 os.Args[2:] 상당.
// env: HUB_URL, HUB_TOKEN, AGENT_ADVERTISE_HOST, LOG_LEVEL,
// AGENT_WORK_DIR, AGENT_PREFETCH_INTERVAL, AGENT_MAX_JOBS,
// AGENT_TRAEFIK_PORT, AGENT_TRAEFIK_IMAGE.
func ParseConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("agent start", flag.ContinueOnError)
	var (
		hubURL      = fs.String("hub-url", os.Getenv("HUB_URL"), "Hub WebSocket URL")
		tokenFlg    = fs.String("token", os.Getenv("HUB_TOKEN"), "Agent token (agt_...)")
		advHost     = fs.String("advertise-host", os.Getenv("AGENT_ADVERTISE_HOST"), "Host address to advertise in HELLO")
		logLvl      = fs.String("log-level", envOr("LOG_LEVEL", "info"), "log level: debug/info/warn/error")
		workDir     = fs.String("work-dir", envOr("AGENT_WORK_DIR", defaultWorkDir()), "RepoCache root directory")
		prefetch    = fs.String("prefetch-interval", envOr("AGENT_PREFETCH_INTERVAL", "5m"), "background fetch interval; 0 disables")
		maxJobs     = fs.Int("max-jobs", envInt("AGENT_MAX_JOBS", 1), "max concurrent preview jobs")
		traefikPort = fs.Int("traefik-port", envInt("AGENT_TRAEFIK_PORT", 8080), "Traefik host binding port")
		traefikImg  = fs.String("traefik-image", envOr("AGENT_TRAEFIK_IMAGE", "traefik:v3.1"), "Traefik Docker image")
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
	pf, err := time.ParseDuration(*prefetch)
	if err != nil {
		return Config{}, fmt.Errorf("invalid --prefetch-interval %q: %w", *prefetch, err)
	}
	if *maxJobs < 1 {
		*maxJobs = 1
	}
	// Phase 5 결정 11: hard cap 64.
	if *maxJobs > maxJobsHardCap {
		fmt.Fprintf(os.Stderr,
			"warn: --max-jobs=%d exceeds hard cap %d; clamping to %d\n",
			*maxJobs, maxJobsHardCap, maxJobsHardCap)
		*maxJobs = maxJobsHardCap
	}
	if *traefikPort < 1 || *traefikPort > 65535 {
		return Config{}, fmt.Errorf("invalid --traefik-port %d: must be 1..65535", *traefikPort)
	}
	return Config{
		HubURL:           *hubURL,
		Token:            *tokenFlg,
		AdvertiseHost:    *advHost,
		LogLevel:         *logLvl,
		WorkDir:          *workDir,
		PrefetchInterval: pf,
		MaxJobs:          *maxJobs,
		TraefikPort:      *traefikPort,
		TraefikImage:     *traefikImg,
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
