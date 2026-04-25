// 이 파일의 책임:
//   - ServeMux 조립, HTTP 서버 실행, graceful shutdown.
//   - 모든 활성 WS 연결에 1001(going away) close frame 송신.
//   - Phase 3: dual-mux 분리 (mainMux + adminMux) + BasicAuthMiddleware on /admin/*.
//   - Phase 3: shutdown 시퀀스 — http.Shutdown(30s) drain → registry.closeAll →
//             hub_shutdown_done 로그.
package hub

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// shutdownTimeout 는 graceful shutdown 의 in-flight HTTP drain timeout (결정 13).
// 코드 상수로 hardcoded — env 미노출.
const shutdownTimeout = 30 * time.Second

// Server 는 Hub 런타임 HTTP 서버와 WS 레지스트리를 묶는다.
type Server struct {
	cfg      Config
	http     *http.Server
	registry *ConnRegistry
	logger   *slog.Logger
}

// NewServer 는 구성 요소를 조립해 Server 를 반환한다.
// admin/ws/webhook 핸들러가 공유 레지스트리와 mux 를 사용한다.
// proxy 가 nil 이 아니면 mux 를 ProxyMiddleware 로 감싸 호스트 헤더 기반 라우팅을 활성화.
//
// Phase 3: dual-mux 패턴 (§5-4 / 결정 4).
//   mainMux:  /health, /webhooks/*, /agent/ws, /admin/ → BasicAuthMiddleware(adminMux)
//   adminMux: /admin/agents*, /admin/previews*, /admin/agents/token, etc.
//
// adminUI 가 nil 이면 SSR 라우트 등록을 건너뛴다 (테스트 호환).
func NewServer(cfg Config, admin *AdminHandler, wsh *WSHandler, webhook *WebhookHandler, reg *ConnRegistry, logger *slog.Logger, proxy *ProxyMiddleware, adminUI *AdminUIHandler) *Server {
	adminMux := http.NewServeMux()
	admin.RegisterAdminOnly(adminMux)
	if webhook != nil {
		// /admin/previews* 라우트들은 webhook handler 에 있음. 이를 adminMux 로 옮겨야
		// auth gate 안으로 들어간다. webhook 의 handlers 를 직접 다시 등록.
		adminMux.HandleFunc("GET /admin/previews", webhook.listPreviews)
		adminMux.HandleFunc("GET /admin/previews/{id}", webhook.getPreview)
		adminMux.HandleFunc("DELETE /admin/previews/{id}", webhook.deletePreview)
	}
	if adminUI != nil {
		adminUI.Register(adminMux)
	}

	mainMux := http.NewServeMux()
	mainMux.HandleFunc("GET /health", admin.Health)
	wsh.Register(mainMux)
	if webhook != nil {
		// webhook only — /admin/* 는 adminMux 로 이전.
		mainMux.HandleFunc("POST /webhooks/github", webhook.handleWebhook)
	}
	mainMux.Handle("/admin/", BasicAuthMiddleware(adminMux, cfg.AdminPassword, logger))
	// /admin (no slash) 도 같은 게이트로 처리하기 위해 별도 등록.
	mainMux.Handle("/admin", BasicAuthMiddleware(adminMux, cfg.AdminPassword, logger))

	var handler http.Handler = mainMux
	if proxy != nil {
		handler = proxy.Wrap(mainMux)
	}
	return &Server{
		cfg:      cfg,
		registry: reg,
		logger:   logger,
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
}

// Handler 는 조립된 http.Handler 를 노출한다. e2e 테스트(httptest.NewServer) 에서
// Hub 를 in-process 기동할 때 사용한다. Run 경로는 영향받지 않는다.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run 은 HTTP 서버를 기동한다. ctx 가 취소되면 Shutdown 을 호출한다.
// 반환 에러는 서버 비정상 종료 이유.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("hub_listening", "addr", s.cfg.Addr)
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-errCh:
		return err
	}
}

// shutdown 은 Phase 3 시퀀스 (§5-13).
//   1. http.Shutdown(30s ctx) — in-flight HTTP drain.
//   2. registry.closeAll(StatusGoingAway, "going away") — WS 1001 close.
//   3. hub_shutdown_done 로그.
// 30s 내 drain 미완료 시 hub_shutdown_drain_timeout WARN + http.Shutdown error 반환.
func (s *Server) shutdown() error {
	s.logger.Info("hub_shutdown_start")
	start := time.Now()

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	httpErr := s.http.Shutdown(shutCtx)
	if httpErr != nil {
		if errors.Is(httpErr, context.DeadlineExceeded) {
			s.logger.Warn("hub_shutdown_drain_timeout", "timeout", shutdownTimeout.String())
		} else {
			s.logger.Warn("hub_shutdown_http_err", "err", httpErr.Error())
		}
	}

	// WS close frame 은 timeout 분기에서도 시도.
	s.registry.closeAll(websocket.StatusGoingAway, "going away")

	s.logger.Info("hub_shutdown_done", "duration_ms", time.Since(start).Milliseconds())
	return httpErr
}
