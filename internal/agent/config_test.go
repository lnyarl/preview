package agent

import (
	"errors"
	"os"
	"testing"
	"time"
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

// ---------- Phase 7: --traefik-api-port / --router-ready-timeout ----------

func resetPhase7Env(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HUB_URL", "HUB_TOKEN",
		"AGENT_TRAEFIK_PORT", "AGENT_TRAEFIK_IMAGE",
		"AGENT_TRAEFIK_API_PORT", "AGENT_ROUTER_READY_TIMEOUT",
	} {
		t.Setenv(k, "")
	}
}

// F-37: --traefik-api-port 미지정 시 default 9080.
func TestParseConfigTraefikAPIPortDefault(t *testing.T) {
	resetPhase7Env(t)
	cfg, err := ParseConfig([]string{"--hub-url", "ws://x", "--token", "agt_y"})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.TraefikAPIPort != 9080 {
		t.Fatalf("TraefikAPIPort=%d want 9080", cfg.TraefikAPIPort)
	}
}

// F-38: --traefik-api-port=0 명시 → cfg.TraefikAPIPort==0 (정상).
func TestParseConfigTraefikAPIPortZero(t *testing.T) {
	resetPhase7Env(t)
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--traefik-api-port", "0",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.TraefikAPIPort != 0 {
		t.Fatalf("TraefikAPIPort=%d want 0", cfg.TraefikAPIPort)
	}
}

// F-39: --traefik-api-port=70000 → 에러.
func TestParseConfigTraefikAPIPortOutOfRange(t *testing.T) {
	resetPhase7Env(t)
	_, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--traefik-api-port", "70000",
	})
	if err == nil {
		t.Fatal("expected error for --traefik-api-port=70000")
	}
}

// F-40: --traefik-api-port == --traefik-port → 에러 (must differ).
func TestParseConfigTraefikAPIPortSameAsTraefik(t *testing.T) {
	resetPhase7Env(t)
	_, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--traefik-port", "8080",
		"--traefik-api-port", "8080",
	})
	if err == nil {
		t.Fatal("expected error for --traefik-port == --traefik-api-port")
	}
}

// F-41: --router-ready-timeout=30s → 30s.
func TestParseConfigRouterReadyTimeoutFlag(t *testing.T) {
	resetPhase7Env(t)
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--router-ready-timeout", "30s",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.RouterReadyTimeout != 30*time.Second {
		t.Fatalf("RouterReadyTimeout=%v want 30s", cfg.RouterReadyTimeout)
	}
}

// F-42: --router-ready-timeout=0 → 0 (probe disabled).
func TestParseConfigRouterReadyTimeoutZero(t *testing.T) {
	resetPhase7Env(t)
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--router-ready-timeout", "0s",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.RouterReadyTimeout != 0 {
		t.Fatalf("RouterReadyTimeout=%v want 0", cfg.RouterReadyTimeout)
	}
}

// F-43: --router-ready-timeout=-5s → 에러.
func TestParseConfigRouterReadyTimeoutNegative(t *testing.T) {
	resetPhase7Env(t)
	_, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--router-ready-timeout", "-5s",
	})
	if err == nil {
		t.Fatal("expected error for negative router-ready-timeout")
	}
}

// F-44: --router-ready-timeout=foo → 에러 (parse 실패).
func TestParseConfigRouterReadyTimeoutInvalid(t *testing.T) {
	resetPhase7Env(t)
	_, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y",
		"--router-ready-timeout", "foo",
	})
	if err == nil {
		t.Fatal("expected parse error for --router-ready-timeout=foo")
	}
}

// F-45: env AGENT_TRAEFIK_API_PORT=9090 가 default 9080 보다 우선.
func TestParseConfigTraefikAPIPortEnv(t *testing.T) {
	t.Setenv("HUB_URL", "ws://x")
	t.Setenv("HUB_TOKEN", "agt_y")
	t.Setenv("AGENT_TRAEFIK_API_PORT", "9090")
	t.Setenv("AGENT_ROUTER_READY_TIMEOUT", "")
	cfg, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.TraefikAPIPort != 9090 {
		t.Fatalf("TraefikAPIPort=%d want 9090", cfg.TraefikAPIPort)
	}
}

// F-46: env AGENT_ROUTER_READY_TIMEOUT=10s 가 default 30s 보다 우선.
func TestParseConfigRouterReadyTimeoutEnv(t *testing.T) {
	t.Setenv("HUB_URL", "ws://x")
	t.Setenv("HUB_TOKEN", "agt_y")
	t.Setenv("AGENT_TRAEFIK_API_PORT", "")
	t.Setenv("AGENT_ROUTER_READY_TIMEOUT", "10s")
	cfg, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.RouterReadyTimeout != 10*time.Second {
		t.Fatalf("RouterReadyTimeout=%v want 10s", cfg.RouterReadyTimeout)
	}
}

// Default RouterReadyTimeout — 30s.
func TestParseConfigRouterReadyTimeoutDefault(t *testing.T) {
	resetPhase7Env(t)
	cfg, err := ParseConfig([]string{"--hub-url", "ws://x", "--token", "agt_y"})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.RouterReadyTimeout != 30*time.Second {
		t.Fatalf("RouterReadyTimeout=%v want 30s", cfg.RouterReadyTimeout)
	}
}
