// 이 파일의 책임:
//   - Agent 가 보낸 STATUS_UPDATE 메시지를 PreviewStore.UpdateStatus 호출로 매핑.
//   - fromStatus 는 비어있는 단순화 (Step 2): WS handler 가 in-memory cache 를 두지
//     않고 DB transaction 의 CAS-less update 로 진행. fields 는 building/running 시
//     ContainerID/AgentHost/AgentPort 를 채운다.
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §4-3, §5-1, 결정 11.
package hub

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// StatusUpdater 는 Agent 의 STATUS_UPDATE 를 PreviewStore.UpdateStatus 로 변환한다.
// CacheNotifier 가 주입되면 terminal 상태(done/failed/teardown) 전이 시 proxy 캐시를 무효화한다.
type StatusUpdater struct {
	Store         store.PreviewStore
	Logger        *slog.Logger
	CacheNotifier PreviewCacheNotifier // optional, nil-safe
	now           func() time.Time
}

// NewStatusUpdater 는 StatusUpdater 를 조립한다.
func NewStatusUpdater(s store.PreviewStore, logger *slog.Logger) *StatusUpdater {
	return &StatusUpdater{
		Store:  s,
		Logger: logger,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// SetCacheNotifier 는 proxy 캐시 무효화 콜백을 주입한다.
func (u *StatusUpdater) SetCacheNotifier(n PreviewCacheNotifier) { u.CacheNotifier = n }

// OnStatusUpdate 는 STATUS_UPDATE 메시지 1건을 처리한다. fromStatus="" (no CAS).
func (u *StatusUpdater) OnStatusUpdate(ctx context.Context, agentID string, d protocol.StatusUpdateData) error {
	if d.PreviewID == "" || d.Status == "" {
		return fmt.Errorf("status_update: invalid payload preview_id=%q status=%q", d.PreviewID, d.Status)
	}
	fields := store.PreviewFields{
		ContainerID:  d.ContainerID,
		AgentHost:    d.AgentHost,
		AgentPort:    d.AgentPort,
		ErrorMessage: d.ErrorMessage,
	}
	// running 시 assigned_agent_id 도 보장 (claim 시점에 채워졌지만 재시작 복원 케이스 대비).
	if d.Status == "running" || d.Status == "building" {
		aid := agentID
		fields.AssignedAgentID = &aid
	}
	if err := u.Store.UpdateStatus(ctx, d.PreviewID, "", d.Status, d.Message, u.now(), fields); err != nil {
		return fmt.Errorf("status_update: %w", err)
	}
	// 비-running 으로 전이하면 proxy 캐시에서 항목을 제거
	// (running 진입 시점에는 캐시가 비어있는 게 정상 — 다음 요청에서 새로 채워짐).
	if u.CacheNotifier != nil && d.Status != "running" {
		u.CacheNotifier.Invalidate(d.PreviewID)
	}
	switch d.Status {
	case "running":
		host := ""
		port := 0
		if d.AgentHost != nil {
			host = *d.AgentHost
		}
		if d.AgentPort != nil {
			port = *d.AgentPort
		}
		u.Logger.Info("agent_status_update_running",
			"preview_id", d.PreviewID,
			"agent_id", agentID,
			"host", host,
			"port", port,
		)
	default:
		u.Logger.Info("agent_status_update",
			"preview_id", d.PreviewID,
			"agent_id", agentID,
			"status", d.Status,
		)
	}
	return nil
}
