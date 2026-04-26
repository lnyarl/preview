// 이 파일의 책임:
//   - Runner.SetTraefikAPIPort / SetRouterReadyTimeout setter + waitRouters 헬퍼의
//     단위 테스트 (F-27 ~ F-34, F-29 buildMode 인자, NF-3 사전 분리).
//   - 외부 docker daemon 미요구 (NF-9). httptest.Server 로 Traefik API stub.
//   - logger 캡처는 bytes.Buffer + slog.NewTextHandler 로 텍스트 검사.
//
// 참고: docs/specs/phase-7-traefik-readiness.md §6-4 (F-27~F-35d), 결정 11.
package agent

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// captureLogger 는 bytes.Buffer 위에서 동작하는 slog 로거를 만든다.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// minimalRunner 는 Handle 흐름을 사용하지 않는 setter/waitRouters 단위 테스트용.
func minimalRunner(t *testing.T) *Runner {
	t.Helper()
	logger, _ := captureLogger()
	return NewRunner(nil, nil, nil, nil, "127.0.0.1", logger)
}

// F-27: SetTraefikAPIPort + SetRouterReadyTimeout 가 필드를 업데이트.
func TestRunnerSetReadinessFields(t *testing.T) {
	r := minimalRunner(t)
	r.SetTraefikAPIPort(9080)
	r.SetRouterReadyTimeout(30 * time.Second)
	if r.traefikAPIPort != 9080 {
		t.Errorf("traefikAPIPort=%d want 9080", r.traefikAPIPort)
	}
	if r.routerReadyTimeout != 30*time.Second {
		t.Errorf("routerReadyTimeout=%v want 30s", r.routerReadyTimeout)
	}
}

// F-30: traefikAPIPort==0 일 때 waitRouters 즉시 return → fake server 호출 0 회.
func TestWaitRoutersDisabledByZeroAPIPort(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	logger, _ := captureLogger()
	r := NewRunner(nil, nil, nil, nil, "127.0.0.1", logger)
	r.SetTraefikAPIPort(0) // 명시적 비활성.
	r.SetRouterReadyTimeout(time.Second)

	cfg := PreviewConfig{Services: map[string]PreviewService{"app": {Port: 80, Path: "/app"}}}
	r.waitRouters(context.Background(), "p1", cfg, buildModeCompose)

	if got := hits.Load(); got != 0 {
		t.Fatalf("requests=%d want 0 (probe disabled)", got)
	}
}

// F-31: routerReadyTimeout==0 일 때도 즉시 return.
func TestWaitRoutersDisabledByZeroTimeout(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	logger, _ := captureLogger()
	r := NewRunner(nil, nil, nil, nil, "127.0.0.1", logger)
	r.SetTraefikAPIPort(9080)
	r.SetRouterReadyTimeout(0)

	cfg := PreviewConfig{Services: map[string]PreviewService{"app": {Port: 80, Path: "/app"}}}
	r.waitRouters(context.Background(), "p1", cfg, buildModeCompose)

	if got := hits.Load(); got != 0 {
		t.Fatalf("requests=%d want 0 (timeout=0)", got)
	}
}

// F-33: 정상 폴링 성공 시 logger 가 agent_traefik_ready 1 회.
func TestWaitRoutersSuccessLogsReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"p1-app","status":"enabled"}`))
	}))
	defer srv.Close()

	port := portFromTestURL(t, srv.URL)

	logger, buf := captureLogger()
	r := NewRunner(nil, nil, nil, nil, "127.0.0.1", logger)
	r.SetTraefikAPIPort(port)
	r.SetRouterReadyTimeout(time.Second)

	cfg := PreviewConfig{Services: map[string]PreviewService{"app": {Port: 80, Path: "/app"}}}
	r.waitRouters(context.Background(), "p1", cfg, buildModeCompose)

	out := buf.String()
	if !strings.Contains(out, "agent_traefik_ready") {
		t.Fatalf("log missing agent_traefik_ready:\n%s", out)
	}
	if strings.Contains(out, "agent_traefik_ready_timeout") {
		t.Errorf("log should not contain timeout:\n%s", out)
	}
	if !strings.Contains(out, "elapsed_ms") {
		t.Errorf("log missing elapsed_ms:\n%s", out)
	}
}

// F-34: 타임아웃 시 logger 가 agent_traefik_ready_timeout 1 회 기록.
func TestWaitRoutersTimeoutLogsWarn(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := portFromTestURL(t, srv.URL)

	logger, buf := captureLogger()
	r := NewRunner(nil, nil, nil, nil, "127.0.0.1", logger)
	r.SetTraefikAPIPort(port)
	r.SetRouterReadyTimeout(20 * time.Millisecond)

	cfg := PreviewConfig{Services: map[string]PreviewService{"app": {Port: 80, Path: "/app"}}}
	r.waitRouters(context.Background(), "p1", cfg, buildModeCompose)

	out := buf.String()
	if !strings.Contains(out, "agent_traefik_ready_timeout") {
		t.Fatalf("log missing agent_traefik_ready_timeout:\n%s", out)
	}
}

// F-29: dockerfile 분기 — names 가 cfg.FirstService() 1 개만 polling 됨.
// fake server 가 받은 요청 path 의 라우터 이름이 정확히 1 개.
func TestWaitRoutersDockerfileSingleRouter(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	gotNames := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/api/http/routers/"
		path := r.URL.Path
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		nameWithSuffix := strings.TrimPrefix(path, prefix)
		name := strings.TrimSuffix(nameWithSuffix, "@docker")
		gotNames[name]++
		// 즉시 enabled 응답 → 1 회만 폴링.
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"name":%q,"status":"enabled"}`, name)
	}))
	defer srv.Close()

	port := portFromTestURL(t, srv.URL)

	logger, _ := captureLogger()
	r := NewRunner(nil, nil, nil, nil, "127.0.0.1", logger)
	r.SetTraefikAPIPort(port)
	r.SetRouterReadyTimeout(time.Second)

	cfg := PreviewConfig{
		Services: map[string]PreviewService{
			"a": {Port: 80, Path: "/a"},
			"b": {Port: 81, Path: "/b"},
			"c": {Port: 82, Path: "/c"},
		},
	}
	r.waitRouters(context.Background(), "p1", cfg, buildModeDockerfile)

	if len(gotNames) != 1 {
		t.Fatalf("polled names=%v want exactly 1 (dockerfile mode → FirstService only)", gotNames)
	}
	// FirstService = 알파벳 첫 키 = "a" → "p1-a".
	if _, ok := gotNames["p1-a"]; !ok {
		t.Errorf("expected p1-a polled, got %v", gotNames)
	}
}

// F-35d (Runner.waitRouters 책임): unknown buildMode WARN 로그 1 회 + 폴링 0 회.
func TestWaitRoutersUnknownBuildModeWarns(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	port := portFromTestURL(t, srv.URL)

	logger, buf := captureLogger()
	r := NewRunner(nil, nil, nil, nil, "127.0.0.1", logger)
	r.SetTraefikAPIPort(port)
	r.SetRouterReadyTimeout(time.Second)

	cfg := PreviewConfig{Services: map[string]PreviewService{"a": {Port: 80, Path: "/a"}}}
	r.waitRouters(context.Background(), "p1", cfg, "weird-mode")

	if got := hits.Load(); got != 0 {
		t.Errorf("requests=%d want 0 (skip on unknown buildMode)", got)
	}
	out := buf.String()
	if !strings.Contains(out, "agent_traefik_ready_unknown_buildmode") {
		t.Fatalf("log missing agent_traefik_ready_unknown_buildmode:\n%s", out)
	}
}

// waitRouters: Empty Services + dockerfile → 폴링 0 회 (방어적 silent skip).
func TestWaitRoutersEmptyServicesSilent(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	port := portFromTestURL(t, srv.URL)
	logger, _ := captureLogger()
	r := NewRunner(nil, nil, nil, nil, "127.0.0.1", logger)
	r.SetTraefikAPIPort(port)
	r.SetRouterReadyTimeout(time.Second)

	cfg := PreviewConfig{}
	r.waitRouters(context.Background(), "p1", cfg, buildModeDockerfile)

	if got := hits.Load(); got != 0 {
		t.Errorf("requests=%d want 0 (no services → silent skip)", got)
	}
}

// F-32 / F-28: Handle 통합 — fake server 가 영원히 404 라도 STATUS_UPDATE running
// 이 송신되어야 한다 (best-effort 결정 4). compose 분기에서 waitRouters 1 회 호출.
func TestRunnerHandleSendsRunningEvenOnReadinessTimeout(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	port := portFromTestURL(t, srv.URL)

	runner, _, _, hub := newRunnerSetup(t, "compose")
	runner.SetTraefikAPIPort(port)
	runner.SetRouterReadyTimeout(20 * time.Millisecond)

	if err := runner.Handle(context.Background(), testMsg("p1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	statuses := hub.statuses()
	if statuses[len(statuses)-1] != "running" {
		t.Fatalf("statuses=%v want last=running", statuses)
	}
	if hits.Load() == 0 {
		t.Errorf("waitRouters expected to poll fake server at least once")
	}
	last := hub.lastUpdate()
	if len(last.PreviewURLs) == 0 {
		t.Errorf("preview_urls empty in best-effort timeout path: %+v", last)
	}
}

// portFromTestURL 은 httptest.Server.URL("http://127.0.0.1:NNNN") 에서 포트만 추출.
func portFromTestURL(t *testing.T, url string) int {
	t.Helper()
	const prefix = "http://127.0.0.1:"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("unexpected test URL: %s", url)
	}
	rest := strings.TrimPrefix(url, prefix)
	var p int
	if _, err := fmt.Sscanf(rest, "%d", &p); err != nil {
		t.Fatalf("parse port from %q: %v", url, err)
	}
	return p
}
