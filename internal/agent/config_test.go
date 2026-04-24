package agent

import (
	"os"
	"testing"
)

func TestParseConfigFlags(t *testing.T) {
	// clear relevant envs.
	for _, k := range []string{"HUB_URL", "HUB_TOKEN", "AGENT_LABELS", "AGENT_ADVERTISE_HOST"} {
		t.Setenv(k, "")
	}
	cfg, err := ParseConfig([]string{
		"--hub-url", "ws://localhost:3000/agent/ws",
		"--token", "agt_xxx",
		"--label", "env=local",
		"--label", "zone=a",
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
	if cfg.Labels["env"] != "local" || cfg.Labels["zone"] != "a" {
		t.Fatalf("Labels=%+v", cfg.Labels)
	}
	if cfg.AdvertiseHost != "1.2.3.4" {
		t.Fatalf("AdvertiseHost=%q", cfg.AdvertiseHost)
	}
}

func TestParseConfigEnvFallback(t *testing.T) {
	t.Setenv("HUB_URL", "ws://env-host/agent/ws")
	t.Setenv("HUB_TOKEN", "agt_env")
	t.Setenv("AGENT_LABELS", "a=1,b=2")
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
	if cfg.Labels["a"] != "1" || cfg.Labels["b"] != "2" {
		t.Fatalf("env labels not applied: %+v", cfg.Labels)
	}
}

func TestParseConfigMissingRequired(t *testing.T) {
	os.Unsetenv("HUB_URL")
	os.Unsetenv("HUB_TOKEN")
	if _, err := ParseConfig([]string{}); err == nil {
		t.Fatal("expected error when HUB_URL/HUB_TOKEN missing")
	}
}

func TestLabelsFlag(t *testing.T) {
	l := labelsFlag{}
	if err := l.Set("a=b"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := l.Set("c=d"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := l.Set("invalid"); err == nil {
		t.Fatal("expected error on invalid label")
	}
	if l.m["a"] != "b" || l.m["c"] != "d" {
		t.Fatalf("map=%+v", l.m)
	}
	if l.String() == "" {
		t.Fatal("String() empty")
	}
}
