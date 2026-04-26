package agent

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestServiceLabelsStripTrue(t *testing.T) {
	svc := PreviewService{Port: 3000, Path: "/front", Strip: true}
	lbls := ServiceLabels("pid1", "frontend", svc)

	must := map[string]string{
		"traefik.enable":        "true",
		"traefik.docker.network": traefikNetwork,
		"traefik.http.routers.pid1-frontend.rule":                             "PathPrefix(`/pid1/front`)",
		"traefik.http.routers.pid1-frontend.entrypoints":                      "web",
		"traefik.http.services.pid1-frontend.loadbalancer.server.port":        "3000",
		"traefik.http.routers.pid1-frontend.middlewares":                      "pid1-frontend-strip",
		"traefik.http.middlewares.pid1-frontend-strip.stripprefix.prefixes":   "/pid1/front",
	}
	for k, want := range must {
		if got := lbls[k]; got != want {
			t.Errorf("label[%s] = %q, want %q", k, got, want)
		}
	}
}

func TestServiceLabelsStripFalse(t *testing.T) {
	svc := PreviewService{Port: 8080, Path: "/admin", Strip: false}
	lbls := ServiceLabels("pid2", "admin", svc)

	if _, ok := lbls["traefik.http.routers.pid2-admin.middlewares"]; ok {
		t.Error("expected no middlewares label when strip=false")
	}
	if _, ok := lbls["traefik.http.middlewares.pid2-admin-strip.stripprefix.prefixes"]; ok {
		t.Error("expected no stripprefix label when strip=false")
	}
	if got := lbls["traefik.http.routers.pid2-admin.rule"]; got != "PathPrefix(`/pid2/admin`)" {
		t.Errorf("rule=%q", got)
	}
	if got := lbls["traefik.http.services.pid2-admin.loadbalancer.server.port"]; got != "8080" {
		t.Errorf("port label=%q", got)
	}
}

func TestServiceLabelsRootPath(t *testing.T) {
	svc := PreviewService{Port: 80, Path: "/", Strip: true}
	lbls := ServiceLabels("abc", "app", svc)

	if got := lbls["traefik.http.routers.abc-app.rule"]; got != "PathPrefix(`/abc/`)" {
		t.Errorf("rule=%q", got)
	}
	if got := lbls["traefik.http.middlewares.abc-app-strip.stripprefix.prefixes"]; got != "/abc/" {
		t.Errorf("stripprefix=%q", got)
	}
}

func TestComposeOverrideYAMLStructure(t *testing.T) {
	cfg := PreviewConfig{
		Services: map[string]PreviewService{
			"frontend": {Port: 3000, Path: "/front", Strip: true},
			"admin":    {Port: 8080, Path: "/admin", Strip: false},
		},
	}
	b, err := ComposeOverrideYAML("testpid", cfg)
	if err != nil {
		t.Fatalf("ComposeOverrideYAML: %v", err)
	}

	// Must be valid YAML and parse back to expected structure.
	var parsed struct {
		Services map[string]struct {
			Networks []string `yaml:"networks"`
			Labels   []string `yaml:"labels"`
		} `yaml:"services"`
		Networks map[string]struct {
			External bool `yaml:"external"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal override yaml: %v\n%s", err, b)
	}

	// Both services must be present.
	for _, name := range []string{"frontend", "admin"} {
		svc, ok := parsed.Services[name]
		if !ok {
			t.Fatalf("service %q missing from override yaml", name)
		}
		if len(svc.Networks) != 1 || svc.Networks[0] != traefikNetwork {
			t.Errorf("service %q networks=%v", name, svc.Networks)
		}
		foundEnable := false
		for _, l := range svc.Labels {
			if l == "traefik.enable=true" {
				foundEnable = true
			}
		}
		if !foundEnable {
			t.Errorf("service %q missing traefik.enable=true label", name)
		}
	}

	// preview-net must be declared external.
	net, ok := parsed.Networks[traefikNetwork]
	if !ok {
		t.Fatal("networks.preview-net missing from override yaml")
	}
	if !net.External {
		t.Error("networks.preview-net.external should be true")
	}

	// frontend (strip=true) must have strip middleware label.
	frontLabels := parsed.Services["frontend"].Labels
	hasStrip := false
	for _, l := range frontLabels {
		if strings.Contains(l, "stripprefix") {
			hasStrip = true
		}
	}
	if !hasStrip {
		t.Error("frontend (strip=true) missing stripprefix middleware label")
	}

	// admin (strip=false) must NOT have strip middleware label.
	adminLabels := parsed.Services["admin"].Labels
	for _, l := range adminLabels {
		if strings.Contains(l, "stripprefix") {
			t.Errorf("admin (strip=false) should not have stripprefix label: %q", l)
		}
	}
}

func TestPreviewURLs(t *testing.T) {
	cfg := PreviewConfig{
		Services: map[string]PreviewService{
			"frontend": {Port: 3000, Path: "/front", Strip: true},
			"admin":    {Port: 8080, Path: "/admin", Strip: false},
		},
	}
	urls := PreviewURLs("mypid", cfg, "1.2.3.4", 8080)

	want := map[string]string{
		"frontend": "http://1.2.3.4:8080/mypid/front",
		"admin":    "http://1.2.3.4:8080/mypid/admin",
	}
	for name, wantURL := range want {
		if got := urls[name]; got != wantURL {
			t.Errorf("urls[%q] = %q, want %q", name, got, wantURL)
		}
	}
}

func TestPreviewURLsDefaultHost(t *testing.T) {
	cfg := PreviewConfig{
		Services: map[string]PreviewService{
			"app": {Port: 3000, Path: "/", Strip: true},
		},
	}
	urls := PreviewURLs("pid", cfg, "", 8080)
	if got := urls["app"]; !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("expected 127.0.0.1 fallback, got %q", got)
	}
}

// ---------- Phase 7: RouterNames ----------

// F-35: services 3 개에서 [pid-a, pid-b, pid-c] 알파벳 오름차순.
func TestRouterNamesComposeSorted(t *testing.T) {
	cfg := PreviewConfig{
		Services: map[string]PreviewService{
			"c": {Port: 3000, Path: "/c"},
			"a": {Port: 3001, Path: "/a"},
			"b": {Port: 3002, Path: "/b"},
		},
	}
	got := RouterNames("pid", cfg, buildModeCompose)
	want := []string{"pid-a", "pid-b", "pid-c"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}

// F-35b: dockerfile 모드는 services 3 개여도 cfg.FirstService() 1 개만.
func TestRouterNamesDockerfileFirstOnly(t *testing.T) {
	cfg := PreviewConfig{
		Services: map[string]PreviewService{
			"c": {Port: 3000, Path: "/c"},
			"a": {Port: 3001, Path: "/a"},
			"b": {Port: 3002, Path: "/b"},
		},
	}
	got := RouterNames("pid", cfg, buildModeDockerfile)
	if len(got) != 1 || got[0] != "pid-a" {
		t.Fatalf("got=%v want=[pid-a] (alphabetical first)", got)
	}
}

// F-35c: dockerfile + services 비어있음 → 빈 슬라이스.
func TestRouterNamesDockerfileEmptyServices(t *testing.T) {
	cfg := PreviewConfig{Services: map[string]PreviewService{}}
	got := RouterNames("pid", cfg, buildModeDockerfile)
	if len(got) != 0 {
		t.Fatalf("got=%v want empty slice", got)
	}
}

// F-35d (RouterNames part): unknown buildMode → 빈 슬라이스 (logger 없음).
func TestRouterNamesUnknownBuildMode(t *testing.T) {
	cfg := PreviewConfig{
		Services: map[string]PreviewService{"app": {Port: 80, Path: "/app"}},
	}
	got := RouterNames("pid", cfg, "xyz")
	if len(got) != 0 {
		t.Fatalf("got=%v want empty for unknown buildMode", got)
	}
}

// F-36: RouterNames 의 결과가 ServiceLabels 의 router 키 prefix 와 정확히 일치.
func TestRouterNamesMatchesServiceLabelsPrefix(t *testing.T) {
	cfg := PreviewConfig{
		Services: map[string]PreviewService{
			"app":   {Port: 3000, Path: "/app"},
			"admin": {Port: 8080, Path: "/admin"},
		},
	}
	names := RouterNames("pid", cfg, buildModeCompose)
	for _, name := range names {
		// name 형태는 "pid-{svc}".
		svcName := strings.TrimPrefix(name, "pid-")
		svc := cfg.Services[svcName]
		lbls := ServiceLabels("pid", svcName, svc)
		// ServiceLabels 가 "traefik.http.routers.{name}." prefix 라벨을 적어도 1개 이상 보유.
		key := "traefik.http.routers." + name + ".rule"
		if _, ok := lbls[key]; !ok {
			t.Errorf("label %q missing from ServiceLabels output for %q", key, name)
		}
	}
}
