// 이 파일의 책임:
//   - Agent 가 보낸 STATUS_UPDATE 메시지를 PreviewStore.UpdateStatus 호출로 매핑.
//   - Phase 6: PreviewURLs (service→URL JSON) 도 PreviewFields 에 전달.
//   - Phase 6 (결정 12): CacheNotifier/ProxyMiddleware 제거.
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §4-3, §5-1, 결정 11.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// StatusUpdater 는 Agent 의 STATUS_UPDATE 를 PreviewStore.UpdateStatus 로 변환한다.
type StatusUpdater struct {
	Store  store.PreviewStore
	Logger *slog.Logger
	now    func() time.Time
}

// NewStatusUpdater 는 StatusUpdater 를 조립한다.
func NewStatusUpdater(s store.PreviewStore, logger *slog.Logger) *StatusUpdater {
	return &StatusUpdater{
		Store:  s,
		Logger: logger,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

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
		CommitSha:    d.CommitSHA, // Phase 9: Agent 가 resolve 한 sha 를 row 의 commit_sha 에 적용 (NULL 일 때만 채움)
	}
	// running 시 assigned_agent_id 도 보장 (claim 시점에 채워졌지만 재시작 복원 케이스 대비).
	if d.Status == "running" || d.Status == "building" {
		aid := agentID
		fields.AssignedAgentID = &aid
	}
	// Phase 6: preview_urls — Agent 가 산출한 service→URL 맵을 JSON 직렬화해 저장.
	if len(d.PreviewURLs) > 0 {
		b, err := json.Marshal(d.PreviewURLs)
		if err == nil {
			s := string(b)
			fields.PreviewURLs = &s
		}
	}
	err := u.Store.UpdateStatus(ctx, d.PreviewID, "", d.Status, d.Message, u.now(), fields)
	if errors.Is(err, store.ErrShaConflict) {
		// Phase 9 결정 6 / R-3: agent 가 보고한 sha 가 row 에 이미 채워진 sha 와 다르면
		// WARN 로그만 남기고 sha 갱신 외 status/필드 갱신은 sha 없이 재시도한다.
		// nil-check 가드 — fields.CommitSha 가 nil 인데 ErrShaConflict 가 도달할 수는 없지만
		// (store 가 fields.CommitSha != nil 일 때만 검사 진입), 방어적 nil 역참조 차단.
		shaForLog := ""
		if d.CommitSHA != nil {
			shaForLog = *d.CommitSHA
		}
		u.Logger.Warn("preview_sha_conflict",
			"preview_id", d.PreviewID,
			"agent_id", agentID,
			"agent_sha", shaForLog,
		)
		fields.CommitSha = nil
		err = u.Store.UpdateStatus(ctx, d.PreviewID, "", d.Status, d.Message, u.now(), fields)
	}
	if err != nil {
		return fmt.Errorf("status_update: %w", err)
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
