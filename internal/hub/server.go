// 이 파일의 책임:
//   - ServeMux 조립, HTTP 서버 실행, graceful shutdown.
//   - 모든 활성 WS 연결에 1001(going away) close frame 송신.
package hub

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

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
func NewServer(cfg Config, admin *AdminHandler, wsh *WSHandler, webhook *WebhookHandler, reg *ConnRegistry, logger *slog.Logger, proxy *ProxyMiddleware) *Server {
	mux := http.NewServeMux()
	admin.Register(mux)
	wsh.Register(mux)
	if webhook != nil {
		webhook.Register(mux)
	}
	var handler http.Handler = mux
	if proxy != nil {
		handler = proxy.Wrap(mux)
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

func (s *Server) shutdown() error {
	s.logger.Info("hub_shutdown_start")
	// 활성 WS 에 close frame (1001) 송신.
	s.registry.closeAll(websocket.StatusGoingAway, "going away")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.http.Shutdown(shutCtx); err != nil {
		s.logger.Warn("hub_shutdown_http_err", "err", err.Error())
		return err
	}
	s.logger.Info("hub_shutdown_done")
	return nil
}
