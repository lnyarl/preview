// 이 파일의 책임:
//   - 1분 주기(기본) reconciliation: stale assigned → queued.
//   - offline agent 의 running preview 는 보존 + 로그만.
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §3 결정 12, F-S3-8/9.
package hub

import (
	"context"
	"log/slog"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

// Reconciler 는 Hub 생명주기 동안 주기적으로 stale preview 를 정리한다.
type Reconciler struct {
	previews store.PreviewStore
	registry *ConnRegistry
	logger   *slog.Logger
}

// NewReconciler 는 Reconciler 를 만든다.
func NewReconciler(previews store.PreviewStore, registry *ConnRegistry, logger *slog.Logger) *Reconciler {
	return &Reconciler{previews: previews, registry: registry, logger: logger}
}

// Start 는 백그라운드 goroutine 에서 reconciliation 루프를 실행한다.
// interval: 루프 주기 (기본 60s). staleAfter: assigned 임계 (기본 5m).
func (rc *Reconciler) Start(ctx context.Context, interval, staleAfter time.Duration) {
	go rc.run(ctx, interval, staleAfter)
}

func (rc *Reconciler) run(ctx context.Context, interval, staleAfter time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rc.tick(ctx, staleAfter)
		}
	}
}

// tick 은 한 번의 reconciliation cycle 을 수행한다 (테스트에서 직접 호출 가능).
func (rc *Reconciler) tick(ctx context.Context, staleAfter time.Duration) {
	cutoff := time.Now().UTC().Add(-staleAfter)

	// (1) stale assigned → queued.
	stales, err := rc.previews.ListStaleAssigned(ctx, cutoff)
	if err != nil {
		rc.logger.Warn("reconciler_stale_list_failed", "err", err.Error())
	} else {
		for _, p := range stales {
			if uerr := rc.previews.UpdateStatus(ctx, p.ID, "assigned", "queued",
				"reconciliation: stale assigned", time.Now().UTC(), store.PreviewFields{}); uerr != nil {
				rc.logger.Warn("reconciler_requeue_failed", "preview_id", p.ID, "err", uerr.Error())
			} else {
				rc.logger.Info("reconciler_requeued", "preview_id", p.ID)
			}
		}
		if len(stales) > 0 {
			rc.logger.Info("reconciler_stale_assigned", "count", len(stales))
		}
	}

	// (2) offline agent 의 running preview 는 보존 + 로그.
	onlineIDs := rc.registry.OnlineAgentIDs()

	// 모든 running preview 조회.
	// NOTE: ListAll + 메모리 필터로 구현 (ListByAgent 는 agent 별 조회이므로
	// 전체 running 을 한번에 가져오는 편이 단순하다. preview 수가 적은 환경 가정).
	all, err := rc.previews.ListAll(ctx)
	if err != nil {
		rc.logger.Warn("reconciler_list_all_failed", "err", err.Error())
		return
	}
	orphanCount := 0
	for _, p := range all {
		if p.Status != "running" {
			continue
		}
		if p.AssignedAgentID == nil {
			continue
		}
		if !onlineIDs[*p.AssignedAgentID] {
			orphanCount++
		}
	}
	if orphanCount > 0 {
		rc.logger.Warn("reconciler_orphan_running", "count", orphanCount)
	}
}
