// 이 파일의 책임:
//   - Hub 데몬 기동 와이어링: DB open -> ResetAllOnline -> HTTP 서버 Run -> signal shutdown.
//
// 이 패키지는 wiring 예외로서 internal/db/sqlite 를 직접 import 할 수 있다(결정 13).
package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/lnyarl/preview/internal/db/sqlite"
	"github.com/lnyarl/preview/internal/hub"
	"github.com/lnyarl/preview/internal/hub/token"
)

// runDaemon 은 Hub HTTP+WS 데몬을 기동한다.
// 에러가 발생하면 non-zero exit 를 위해 반환.
func runDaemon() error {
	cfg := hub.DefaultConfig()
	logger := hub.NewLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := sqlitestore.OpenURL(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		_ = db.Close()
	}()

	agentStore := sqlitestore.NewAgentStore(db)

	// 결정 11: Hub 기동 bulk offline 리셋 (리스크 4 완화).
	resetCount, err := agentStore.ResetAllOnline(ctx)
	if err != nil {
		return fmt.Errorf("reset online: %w", err)
	}
	logger.Info("startup_bulk_offline_reset", "reset_count", resetCount)

	// 마이그레이션 미적용인 상태에서 기동하면 agents 테이블이 없어 위 리셋이 실패했을 것.
	// 따라서 ResetAllOnline 성공이 곧 "스키마 존재" 확인 역할.

	tg := token.NewGenerator(cfg.BcryptCost)
	reg := hub.NewConnRegistry()
	admin := hub.NewAdminHandler(agentStore, tg, logger)
	ws := hub.NewWSHandler(agentStore, reg, logger)

	srv := hub.NewServer(cfg, admin, ws, reg, logger)
	return srv.Run(ctx)
}
