package agent

import (
	"errors"
	"os"
	"testing"
)

func TestParseConfigFlags(t *testing.T) {
	// clear relevant envs.
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_LABELS", "AGENT_ADVERTISE_HOST", "AGENT_REPO_URL"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://localhost:3000/agent/ws",
		"--token", "agt_xxx",
		"--label", "local",
		"--label", "a",
		"--advertise-host", "1.2.3.4",
		"--repo-url", "file:///tmp/x",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.HubURL != "ws://localhost:3000/agent/ws" {
		t.Fatalf("HubURL=%q", cfg.HubURL)
	}
	if cfg.Token != "agt_xxx" {
		t.Fatalf("Token=%q", cfg.Token)
	}
	if !labelsContain(cfg.Labels, "local") || !labelsContain(cfg.Labels, "a") {
		t.Fatalf("Labels=%+v", cfg.Labels)
	}
	if cfg.AdvertiseHost != "1.2.3.4" {
		t.Fatalf("AdvertiseHost=%q", cfg.AdvertiseHost)
	}
}

func TestParseConfigEnvFallback(t *testing.T) {
	t.Setenv("HUB_URL", "ws://env-host/agent/ws")
	t.Setenv("HUB_TOKEN", "agt_env")
	t.Setenv("AGENT_LABELS", "a,b")
	t.Setenv("AGENT_REPO_URL", "file:///tmp/y")
	cfg, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.HubURL != "ws://env-host/agent/ws" {
		t.Fatalf("HubURL=%q", cfg.HubURL)
	}
	if cfg.Token != "agt_env" {
		t.Fatalf("Token=%q", cfg.Token)
	}
	if !labelsContain(cfg.Labels, "a") || !labelsContain(cfg.Labels, "b") {
		t.Fatalf("env labels not applied: %+v", cfg.Labels)
	}
}

// labelsContain 은 슬라이스 멤버십 검사 헬퍼.
// 라벨이 []string 으로 단순화되었으므로 map lookup 대신 사용한다.
func labelsContain(labels []string, v string) bool {
	for _, l := range labels {
		if l == v {
			return true
		}
	}
	return false
}

func TestParseConfigMissingRequired(t *testing.T) {
	os.Unsetenv("HUB_URL")
	os.Unsetenv("HUB_TOKEN")
	os.Unsetenv("AGENT_REPO_URL")
	_, err := ParseConfig([]string{})
	if err == nil {
		t.Fatal("expected error when HUB_URL/HUB_TOKEN missing")
	}
	// F-S2-5: 필수 플래그 부재는 sentinel 로 식별되어야 한다 (main.go → exit 2).
	if !errors.Is(err, ErrMissingRequiredFlag) {
		t.Fatalf("err=%v, want errors.Is ErrMissingRequiredFlag", err)
	}
}

// TestParseConfigMissingRepoURL 는 --repo-url 만 누락된 경우도
// ErrMissingRequiredFlag 로 식별되어야 함을 검증한다 (F-S2-5).
func TestParseConfigMissingRepoURL(t *testing.T) {
	os.Unsetenv("AGENT_REPO_URL")
	_, err := ParseConfig([]string{
		"--hub-url", "ws://x",
		"--token", "agt_y",
	})
	if err == nil {
		t.Fatal("expected error when AGENT_REPO_URL missing")
	}
	if !errors.Is(err, ErrMissingRequiredFlag) {
		t.Fatalf("err=%v, want errors.Is ErrMissingRequiredFlag", err)
	}
}

func TestLabelsFlag(t *testing.T) {
	l := labelsFlag{}
	if err := l.Set("a"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := l.Set("b"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := l.Set(""); err == nil {
		t.Fatal("expected error on empty label")
	}
	if len(l.vals) != 2 || l.vals[0] != "a" || l.vals[1] != "b" {
		t.Fatalf("vals=%+v", l.vals)
	}
	if l.String() == "" {
		t.Fatal("String() empty")
	}
}
