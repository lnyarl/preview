package agent

import (
	"errors"
	"os"
	"testing"
)

func TestParseConfigFlags(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_ADVERTISE_HOST", "AGENT_REPO_URL"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://localhost:3000/agent/ws",
		"--token", "agt_xxx",
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
	if cfg.AdvertiseHost != "1.2.3.4" {
		t.Fatalf("AdvertiseHost=%q", cfg.AdvertiseHost)
	}
}

func TestParseConfigEnvFallback(t *testing.T) {
	t.Setenv("HUB_URL", "ws://env-host/agent/ws")
	t.Setenv("HUB_TOKEN", "agt_env")
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

// F-21 (Phase 5): --max-jobs 5 가 cfg.MaxJobs 까지 그대로 전달된다.
func TestParseConfigMaxJobsFlag(t *testing.T) {
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_REPO_URL", "AGENT_MAX_JOBS"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y", "--repo-url", "file:///tmp/r",
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
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_REPO_URL", "AGENT_MAX_JOBS"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y", "--repo-url", "file:///tmp/r",
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
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_REPO_URL", "AGENT_MAX_JOBS"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y", "--repo-url", "file:///tmp/r",
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
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_REPO_URL", "AGENT_MAX_JOBS"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://x", "--token", "agt_y", "--repo-url", "file:///tmp/r",
		"--max-jobs", "0",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.MaxJobs != 1 {
		t.Fatalf("MaxJobs=%d want 1", cfg.MaxJobs)
	}
}
