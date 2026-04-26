package agent

import (
	"errors"
	"os"
	"testing"
)

func TestParseConfigFlags(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_ADVERTISE_HOST", "AGENT_TRAEFIK_PORT", "AGENT_TRAEFIK_IMAGE"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://localhost:3000/agent/ws",
		"--token", "agt_xxx",
		"--advertise-host", "1.2.3.4",
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
	if cfg.AdvertiseHost != "1.2.3.4" {
		t.Fatalf("AdvertiseHost=%q", cfg.AdvertiseHost)
	}
}

func TestParseConfigEnvFallback(t *testing.T) {
	t.Setenv("HUB_URL", "ws://env-host/agent/ws")
	t.Setenv("HUB_TOKEN", "agt_env")
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
}

func TestParseConfigMissingRequired(t *testing.T) {
	os.Unsetenv("HUB_URL")
	os.Unsetenv("HUB_TOKEN")
	_, err := ParseConfig([]string{})
	if err == nil {
		t.Fatal("expected error when HUB_URL/HUB_TOKEN missing")
	}
	// F-S2-5: 필수 플래그 부재는 sentinel 로 식별되어야 한다 (main.go → exit 2).
	if !errors.Is(err, ErrMissingRequiredFlag) {
		t.Fatalf("err=%v, want errors.Is ErrMissingRequiredFlag", err)
	}
}

// F-21 (Phase 5): --max-jobs 5 가 cfg.MaxJobs 까지 그대로 전달된다.
func TestParseConfigMaxJobsFlag(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_MAX_JOBS"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--max-jobs", "5",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.MaxJobs != 5 {
		t.Fatalf("MaxJobs=%d want 5", cfg.MaxJobs)
	}
}

// F-23 (Phase 5 결정 11): --max-jobs 10000 → cfg.MaxJobs == 64 로 클램프.
func TestParseConfigMaxJobsHardCap(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_MAX_JOBS"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--max-jobs", "10000",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.MaxJobs != maxJobsHardCap {
		t.Fatalf("MaxJobs=%d want %d (hard cap)", cfg.MaxJobs, maxJobsHardCap)
	}
}

// F-23 보강: 정확히 64 입력은 그대로 통과 (경계값).
func TestParseConfigMaxJobsAtCap(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_MAX_JOBS"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--max-jobs", "64",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.MaxJobs != 64 {
		t.Fatalf("MaxJobs=%d want 64", cfg.MaxJobs)
	}
}

// F-23 보강: < 1 입력은 1 로 보정.
func TestParseConfigMaxJobsBelowMin(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_MAX_JOBS"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--max-jobs", "0",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.MaxJobs != 1 {
		t.Fatalf("MaxJobs=%d want 1", cfg.MaxJobs)
	}
}

// Phase 6 결정 14: --traefik-port 가 cfg.TraefikPort 로 전달된다.
func TestParseConfigTraefikPortFlag(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_TRAEFIK_PORT", "AGENT_TRAEFIK_IMAGE"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--traefik-port", "9090",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.TraefikPort != 9090 {
		t.Fatalf("TraefikPort=%d want 9090", cfg.TraefikPort)
	}
}

// Phase 6: AGENT_TRAEFIK_PORT env fallback.
func TestParseConfigTraefikPortEnv(t *testing.T) {
	t.Setenv("HUB_URL", "ws://x")
	t.Setenv("HUB_TOKEN", "agt_y")
	t.Setenv("AGENT_TRAEFIK_PORT", "7777")
	t.Setenv("AGENT_TRAEFIK_IMAGE", "")
	cfg, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.TraefikPort != 7777 {
		t.Fatalf("TraefikPort=%d want 7777", cfg.TraefikPort)
	}
}

// Phase 6: --traefik-port default is 8080.
func TestParseConfigTraefikPortDefault(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_TRAEFIK_PORT", "AGENT_TRAEFIK_IMAGE"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{"--hub-url", "ws://x", "--token", "agt_y"})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.TraefikPort != 8080 {
		t.Fatalf("TraefikPort=%d want 8080 (default)", cfg.TraefikPort)
	}
}

// Phase 6: --traefik-image default is "traefik:v3.1".
func TestParseConfigTraefikImageDefault(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_TRAEFIK_PORT", "AGENT_TRAEFIK_IMAGE"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{"--hub-url", "ws://x", "--token", "agt_y"})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.TraefikImage != "traefik:v3.1" {
		t.Fatalf("TraefikImage=%q want traefik:v3.1", cfg.TraefikImage)
	}
}

// Phase 6: --traefik-image flag passthrough.
func TestParseConfigTraefikImageFlag(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_TRAEFIK_PORT", "AGENT_TRAEFIK_IMAGE"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--traefik-image", "traefik:v2.10",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.TraefikImage != "traefik:v2.10" {
		t.Fatalf("TraefikImage=%q want traefik:v2.10", cfg.TraefikImage)
	}
}
