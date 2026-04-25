// server_shutdown_test.go: Phase 3 §6 F-S3-2/3/4.
//   - TestGracefulShutdownLogsDone: shutdown 호출 후 hub_shutdown_done 로그.
//   - TestGracefulShutdownClosesWS: registry.closeAll 이 호출되어 WS conn 정리.
package hub

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freePort returns a free TCP port for test bind.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestGracefulShutdownLogsDone(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	reg := NewConnRegistry()

	srv := &Server{
		cfg:      Config{Addr: freePort(t)},
		registry: reg,
		logger:   logger,
		http: &http.Server{
			Handler:           http.NewServeMux(),
			ReadHeaderTimeout: 1 * time.Second,
		},
	}
	srv.http.Addr = srv.cfg.Addr

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("Run returned: %v (acceptable on graceful shutdown)", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatalf("shutdown did not complete within 35s")
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "hub_shutdown_start") {
		t.Errorf("expected hub_shutdown_start in logs:\n%s", logs)
	}
	if !strings.Contains(logs, "hub_shutdown_done") {
		t.Errorf("expected hub_shutdown_done in logs:\n%s", logs)
	}
}

func TestBasicAuthMiddlewareUnsetWarns(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := BasicAuthMiddleware(next, "", logger)
	if !strings.Contains(logBuf.String(), "admin_unauthenticated") {
		t.Errorf("expected admin_unauthenticated WARN; logs=%s", logBuf.String())
	}
	// 미들웨어가 next 를 그대로 통과시켜야 함.
	rr := newRecorderForReq()
	mw.ServeHTTP(rr.rec, rr.req)
	if !called {
		t.Errorf("next was not called when password is unset")
	}
}

func TestBasicAuthMiddlewareCorrectPassword(t *testing.T) {
	logger := slog.Default()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := BasicAuthMiddleware(next, "secret", logger)
	rr := newRecorderForReq()
	rr.req.SetBasicAuth("admin", "secret")
	mw.ServeHTTP(rr.rec, rr.req)
	if rr.rec.Code != http.StatusOK || !called {
		t.Errorf("status=%d called=%v", rr.rec.Code, called)
	}
}

func TestBasicAuthMiddlewareWrongPassword(t *testing.T) {
	logger := slog.Default()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := BasicAuthMiddleware(next, "secret", logger)
	rr := newRecorderForReq()
	rr.req.SetBasicAuth("admin", "wrong")
	mw.ServeHTTP(rr.rec, rr.req)
	if rr.rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.rec.Code)
	}
	if rr.rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("WWW-Authenticate header missing on 401")
	}
}

func TestBasicAuthMiddlewareNoCreds(t *testing.T) {
	logger := slog.Default()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := BasicAuthMiddleware(next, "secret", logger)
	rr := newRecorderForReq()
	mw.ServeHTTP(rr.rec, rr.req)
	if rr.rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401 when no creds", rr.rec.Code)
	}
}

// helpers ---------------------------------------------------------------------

type recorderReq struct {
	rec *responseRecorder
	req *http.Request
}

func newRecorderForReq() recorderReq {
	r, _ := http.NewRequest(http.MethodGet, "/admin", nil)
	return recorderReq{rec: &responseRecorder{header: http.Header{}}, req: r}
}

// responseRecorder is a tiny http.ResponseWriter for tests (avoid httptest dep here).
type responseRecorder struct {
	Code   int
	header http.Header
	body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header        { return r.header }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *responseRecorder) WriteHeader(code int)        { r.Code = code }
