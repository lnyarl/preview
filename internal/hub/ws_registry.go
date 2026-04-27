// 이 파일의 책임:
//   - agent_id -> 활성 WS 연결 매핑.
//   - 동일 agent_id 의 중복 연결을 탐지 (결정 12).
//   - Hub shutdown 시 모든 연결에 close frame 을 송신.
//   - Dispatcher.JobSender 인터페이스 구현 (SendJobAssign).
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/coder/websocket"

	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// wsConn 은 단일 WS 세션에 대한 레지스트리 엔트리.
// cancel 을 호출하면 connCtx 가 취소되고 readLoop + pingTicker 가 종료된다.
type wsConn struct {
	agentID string
	conn    *websocket.Conn
	cancel  context.CancelFunc
}

// ConnRegistry 는 현재 online 상태인 Agent 들의 맵.
// Hub 프로세스 내 wiring 용 의존성이므로 cmd/hub 에서 참조한다.
type ConnRegistry struct {
	mu    sync.Mutex
	conns map[string]*wsConn
}

// NewConnRegistry 는 빈 레지스트리를 만든다.
func NewConnRegistry() *ConnRegistry {
	return &ConnRegistry{conns: make(map[string]*wsConn)}
}

// add 는 agent_id 가 이미 online 이면 false 를 반환하고 기존 연결을 건드리지 않는다.
// 이 경우 호출측이 close code 4003 으로 신규 연결을 거절한다.
func (r *ConnRegistry) add(c *wsConn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.conns[c.agentID]; exists {
		return false
	}
	r.conns[c.agentID] = c
	return true
}

// remove 는 등록된 연결을 제거한다. 현재 엔트리가 other 이면 무시 (경쟁 상황 방어).
func (r *ConnRegistry) remove(c *wsConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.conns[c.agentID]; ok && cur == c {
		delete(r.conns, c.agentID)
	}
}

// closeAll 은 등록된 모든 연결에 close frame 을 송신하고 cancel 을 트리거한다.
// Hub graceful shutdown 경로에서 호출.
func (r *ConnRegistry) closeAll(code websocket.StatusCode, reason string) {
	r.mu.Lock()
	items := make([]*wsConn, 0, len(r.conns))
	for _, c := range r.conns {
		items = append(items, c)
	}
	r.mu.Unlock()
	for _, c := range items {
		_ = c.conn.Close(code, reason)
		c.cancel()
	}
}

// connFor 는 agent_id 로 wsConn 을 조회한다. 미등록이면 nil.
func (r *ConnRegistry) connFor(agentID string) *wsConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns[agentID]
}

// OnlineAgentIDs 는 현재 online 인 agent ID 의 집합을 복사본으로 반환한다.
// Reconciler 등이 online 여부를 판단할 때 mu 에 직접 접근하지 않도록 제공.
func (r *ConnRegistry) OnlineAgentIDs() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.conns))
	for id := range r.conns {
		out[id] = true
	}
	return out
}

// WSJobSender 는 Dispatcher.JobSender 를 ConnRegistry 위에 구현한다.
// Phase 6 (결정 6): RepoURLResolver 제거 — preview.RepoCloneURL 을 직접 사용.
type WSJobSender struct {
	Registry *ConnRegistry
}

// NewWSJobSender 는 WSJobSender 를 조립한다.
func NewWSJobSender(reg *ConnRegistry) *WSJobSender {
	return &WSJobSender{Registry: reg}
}

// SendJobAssign 은 agentID 의 WS 에 JOB_ASSIGN envelope 를 송신한다.
// 미연결 agent 는 에러. 호출자는 로그 후 무시 (Claim 은 이미 성공이므로 reconciler 가 회수).
//
// Phase 10: env 인자 추가 — dispatcher 가 RepoSecrets.List 로 hydrate 한 build secrets.
// nil/빈 map 은 JobAssignData.BuildEnv 미설정 (omitempty).
func (s *WSJobSender) SendJobAssign(ctx context.Context, agentID string, p store.Preview, env map[string]string) error {
	c := s.Registry.connFor(agentID)
	if c == nil {
		return fmt.Errorf("ws_job_sender: agent %s not connected", agentID)
	}
	data := JobAssignFromPreview(p, env)
	envelope, err := protocol.NewEnvelope(protocol.TypeJobAssign, data)
	if err != nil {
		return fmt.Errorf("ws_job_sender: envelope: %w", err)
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("ws_job_sender: marshal: %w", err)
	}
	return c.conn.Write(ctx, websocket.MessageText, b)
}

// SendTeardown 은 agentID 의 WS 에 JOB_TEARDOWN 을 송신한다.
// Step 3 webhook close 분기 + Step 2 동작 호환을 위해 미리 노출.
func (s *WSJobSender) SendTeardown(ctx context.Context, agentID string, previewID string) error {
	c := s.Registry.connFor(agentID)
	if c == nil {
		return fmt.Errorf("ws_job_sender: agent %s not connected", agentID)
	}
	env, err := protocol.NewEnvelope(protocol.TypeJobTeardown, protocol.JobTeardownData{PreviewID: previewID})
	if err != nil {
		return fmt.Errorf("ws_job_sender: teardown envelope: %w", err)
	}
	b, _ := json.Marshal(env)
	return c.conn.Write(ctx, websocket.MessageText, b)
}

