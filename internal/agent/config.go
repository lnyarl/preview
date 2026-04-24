// 이 파일의 책임:
//   - Agent CLI 플래그·env 파싱.
//   - --label key=value 반복 옵션 처리.
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-7.
package agent

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// Config 는 Agent start 서브커맨드 실행에 필요한 설정.
type Config struct {
	HubURL        string
	Token         string
	Labels        map[string]string
	AdvertiseHost string
	LogLevel      string
}

// labelsFlag 는 --label key=value 형식을 반복 수집.
type labelsFlag struct {
	m map[string]string
}

func (l *labelsFlag) String() string {
	if l == nil || l.m == nil {
		return ""
	}
	parts := make([]string, 0, len(l.m))
	for k, v := range l.m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (l *labelsFlag) Set(s string) error {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 {
		return fmt.Errorf("label must be key=value, got %q", s)
	}
	if l.m == nil {
		l.m = map[string]string{}
	}
	l.m[s[:idx]] = s[idx+1:]
	return nil
}

// ParseConfig 는 주어진 args 로부터 Config 를 만든다. args 는 os.Args[2:] 상당.
// env: HUB_URL, HUB_TOKEN, AGENT_ADVERTISE_HOST, LOG_LEVEL, AGENT_LABELS("k=v,k2=v2").
func ParseConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("agent start", flag.ContinueOnError)
	var (
		hubURL   = fs.String("hub-url", os.Getenv("HUB_URL"), "Hub WebSocket URL, e.g. ws://localhost:3000/agent/ws")
		tokenFlg = fs.String("token", os.Getenv("HUB_TOKEN"), "Agent token (agt_...)")
		advHost  = fs.String("advertise-host", os.Getenv("AGENT_ADVERTISE_HOST"), "Host address to advertise in HELLO")
		logLvl   = fs.String("log-level", envOr("LOG_LEVEL", "info"), "log level: debug/info/warn/error")
	)
	var labels labelsFlag
	fs.Var(&labels, "label", "label in key=value form (repeatable)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	// env AGENT_LABELS=k=v,k2=v2 도 병합 (flag 가 우선).
	if labels.m == nil {
		labels.m = map[string]string{}
	}
	if envLabels := os.Getenv("AGENT_LABELS"); envLabels != "" {
		for _, part := range strings.Split(envLabels, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			idx := strings.IndexByte(part, '=')
			if idx <= 0 {
				continue
			}
			k := part[:idx]
			if _, exists := labels.m[k]; !exists {
				labels.m[k] = part[idx+1:]
			}
		}
	}

	if *hubURL == "" {
		return Config{}, fmt.Errorf("--hub-url or HUB_URL required")
	}
	if *tokenFlg == "" {
		return Config{}, fmt.Errorf("--token or HUB_TOKEN required")
	}
	return Config{
		HubURL:        *hubURL,
		Token:         *tokenFlg,
		Labels:        labels.m,
		AdvertiseHost: *advHost,
		LogLevel:      *logLvl,
	}, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
