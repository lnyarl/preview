// 이 파일의 책임:
//   - Hub 데몬 기동 와이어링: Config.Validate -> DB open -> Reset 캠페인 ->
//     Dispatcher/StatusUpdater + ProxyMiddleware + Reconciler 조립 ->
//     HTTP 서버 Run -> signal shutdown.
//
// 이 패키지는 wiring 예외로서 internal/db/sqlite 를 직접 import 할 수 있다(결정 13).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	sqlitestore "github.com/lnyarl/preview/internal/db/sqlite"
	"github.com/lnyarl/preview/internal/hub"
	"github.com/lnyarl/preview/internal/hub/token"
)

// runDaemon 은 Hub HTTP+WS 데몬을 기동한다.
// args 에서 플래그(--reconcile-interval / --stale-after) 를 파싱한다.
// 에러가 발생하면 non-zero exit 를 위해 반환.
func runDaemon(args []string) error {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	var reconcileInterval time.Duration
	var staleAssignedAfter time.Duration
	fs.DurationVar(&reconcileInterval, "reconcile-interval", 0, "reconciler tick interval (0=use env/default)")
	fs.DurationVar(&staleAssignedAfter, "stale-assigned-after", 0, "stale assigned threshold (0=use env/default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = godotenv.Load() // .env 없어도 계속 진행 (env 변수로 직접 설정 가능)
	cfg := hub.DefaultConfig()
	if reconcileInterval > 0 {
		cfg.ReconcileInterval = reconcileInterval
	}
	if staleAssignedAfter > 0 {
		cfg.StaleAssignedAfter = staleAssignedAfter
	}
	logger := hub.NewLogger(cfg.LogLevel)

	// Phase 2: 필수 설정(GITHUB_WEBHOOK_SECRET) 부재 시 fail-fast (NF-Security-3).
	if err := cfg.Validate(); err != nil {
		if errors.Is(err, hub.ErrWebhookSecretMissing) {
			logger.Error("config_invalid", "err", err.Error())
		}
		return err
	}

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
	previewStore := sqlitestore.NewPreviewStore(db)

	// 결정 11(Phase 1): Hub 기동 bulk offline 리셋 (리스크 4 완화).
	resetCount, err := agentStore.ResetAllOnline(ctx)
	if err != nil {
		return fmt.Errorf("reset online: %w", err)
	}
	logger.Info("startup_bulk_offline_reset", "reset_count", resetCount)

	// 마이그레이션 미적용인 상태에서 기동하면 agents 테이블이 없어 위 리셋이 실패했을 것.
	// 따라서 ResetAllOnline 성공이 곧 "스키마 존재" 확인 역할.

	// Phase 2: Hub 기동 시점 잔존 'assigned' preview 를 'queued' 로 복귀
	// (dispatcher 가 다시 매칭/할당하도록).
	resetAssigned, err := previewStore.ResetAllAssigned(ctx)
	if err != nil {
		return fmt.Errorf("reset assigned: %w", err)
	}
	logger.Info("startup_bulk_assigned_reset", "reset_count", resetAssigned)

	tg := token.NewGenerator(cfg.BcryptCost)
	reg := hub.NewConnRegistry()
	admin := hub.NewAdminHandler(agentStore, tg, logger)
	ws := hub.NewWSHandler(agentStore, reg, logger)
	webhook := hub.NewWebhookHandler(previewStore, cfg.WebhookSecret, logger)
	adminUI := hub.NewAdminUIHandler(agentStore, previewStore, tg, logger)
	adminUI.SetRegistry(reg)
	admin.SetUI(adminUI)
	webhook.SetUI(adminUI)
	ws.SetPreviewStore(previewStore)

	// Phase 2 wiring: Dispatcher (READY → JOB_ASSIGN) + StatusUpdater.
	// resolveRepo 는 PREVIEW_REPO_URL env 가 있으면 그 값으로 echo, 없으면
	// repo_full_name 자체를 fallback (file:///... 같은 fixture 환경 호환).
	repoURL := cfg.PreviewRepoURL
	resolveRepo := func(repoFullName string) string {
		if repoURL != "" {
			return repoURL
		}
		return repoFullName
	}
	jobSender := hub.NewWSJobSender(reg, resolveRepo)
	dispatcher := hub.NewDispatcher(agentStore, previewStore, jobSender, resolveRepo, logger)
	statusUpdater := hub.NewStatusUpdater(previewStore, logger)
	ws.SetReady(dispatcher)
	ws.SetStatusUpdate(statusUpdater)
	ws.SetTeardownSender(jobSender)

	// Phase 3: SIGTERM 수신 즉시 dispatcher.Pause — 신규 OnReady 차단.
	// rootCtx 가 취소되면 Run() 의 shutdown() 으로 진입한다.
	go func() {
		<-ctx.Done()
		dispatcher.Pause()
		logger.Info("dispatcher_paused")
	}()

	// Phase 2 Step 3: ProxyMiddleware + Reconciler.
	pm := hub.NewProxyMiddleware(previewStore, cfg.PreviewBaseDomain, logger)
	webhook.SetTeardownSender(jobSender)
	webhook.SetCacheNotifier(pm)
	statusUpdater.SetCacheNotifier(pm)

	reconciler := hub.NewReconciler(previewStore, reg, logger)
	reconciler.Start(ctx, cfg.ReconcileInterval, cfg.StaleAssignedAfter)
	logger.Info("reconciler_started",
		"interval", cfg.ReconcileInterval.String(),
		"stale_assigned_after", cfg.StaleAssignedAfter.String(),
	)

	srv := hub.NewServer(cfg, admin, ws, webhook, reg, logger, pm, adminUI)
	return srv.Run(ctx)
}
