// 이 파일의 책임:
//   - GET /agent/ws 핸들러: Bearer 토큰 검증 -> WS 업그레이드 -> HELLO/WELCOME 핸드셰이크 -> heartbeat.
//   - 연결 1개당 goroutine 2개 (readLoop + pingTicker). 공유 context cancel 로 연쇄 종료.
//   - 종료 경로: TCP close / Pong 타임아웃 / Hub shutdown.
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §4-3-1, §5-3, 결정 4/5/9/12.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/lnyarl/preview/internal/hub/token"
	"github.com/lnyarl/preview/internal/protocol"
	"github.com/lnyarl/preview/internal/store"
)

// PingInterval 과 PongTimeout 은 결정 5.
// NF-Timing-1 이 이 상수명·값을 grep 으로 검증하므로 수정 금지.
const (
	PingInterval = 10 * time.Second
	PongTimeout  = 5 * time.Second
)

// ReadyHandler 는 Agent 가 READY 메시지를 보냈을 때 호출되는 콜백.
// 보통 *Dispatcher.OnReady 로 wire 된다. nil 이면 READY 는 로그만 남기고 무시.
type ReadyHandler interface {
	OnReady(ctx context.Context, agentID string) error
}

// StatusUpdateHandler 는 Agent 가 STATUS_UPDATE 를 보냈을 때 호출되는 콜백.
// PreviewStore.UpdateStatus 호출을 wrapper 한 핸들러가 주입된다.
type StatusUpdateHandler interface {
	OnStatusUpdate(ctx context.Context, agentID string, data protocol.StatusUpdateData) error
}

// WSHandler 는 /agent/ws 엔드포인트를 처리한다.
type WSHandler struct {
	Store          store.AgentStore
	Registry       *ConnRegistry
	Logger         *slog.Logger
	Ready          ReadyHandler        // nil 허용 (Phase 1 호환).
	StatusUpdate   StatusUpdateHandler // nil 허용.
	PreviewStore   store.PreviewStore  // Phase 3: HELLO sync 용. nil 시 sync 스킵.
	TeardownSender TeardownSender      // Phase 3: orphan container 정리용. nil 시 send 스킵.
}

// NewWSHandler 는 WSHandler 를 조립한다.
func NewWSHandler(s store.AgentStore, reg *ConnRegistry, logger *slog.Logger) *WSHandler {
	return &WSHandler{Store: s, Registry: reg, Logger: logger}
}

// SetReady 는 READY 콜백을 설정한다 (Phase 2: dispatcher).
func (h *WSHandler) SetReady(r ReadyHandler) { h.Ready = r }

// SetStatusUpdate 는 STATUS_UPDATE 콜백을 설정한다 (Phase 2: preview status updater).
func (h *WSHandler) SetStatusUpdate(s StatusUpdateHandler) { h.StatusUpdate = s }

// SetPreviewStore 는 HELLO 동기화 시 사용할 PreviewStore 를 주입한다 (Phase 3 §5-1).
func (h *WSHandler) SetPreviewStore(s store.PreviewStore) { h.PreviewStore = s }

// SetTeardownSender 는 HELLO 동기화 시 orphan container 에 JOB_TEARDOWN 을 송신할
// TeardownSender 를 주입한다 (Phase 3 §5-1).
func (h *WSHandler) SetTeardownSender(s TeardownSender) { h.TeardownSender = s }

// Register 는 mux 에 GET /agent/ws 라우트를 붙인다.
func (h *WSHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /agent/ws", h.ServeHTTP)
}

// ServeHTTP 는 handshake + upgrade + session 을 수행한다.
func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authHdr := r.Header.Get("Authorization")
	if authHdr == "" {
		writeError(w, http.StatusUnauthorized, "missing_auth", "Authorization header required")
		return
	}
	raw, ok := parseBearer(authHdr)
	if !ok || !strings.HasPrefix(raw, token.Prefix) {
		writeError(w, http.StatusUnauthorized, "invalid_token", "bearer token required")
		return
	}
	agent, err := h.findAgentByToken(r.Context(), raw)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid_token", "token does not match any agent")
			return
		}
		h.Logger.Error("ws_auth_lookup_failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "auth lookup failed")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin 검증은 Phase 후속
	})
	if err != nil {
		h.Logger.Warn("ws_accept_failed", "err", err.Error(), "agent_id", agent.ID)
		return
	}

	h.session(r.Context(), conn, agent)
}

// parseBearer 는 Authorization 헤더에서 "Bearer <token>" 형식을 추출한다.
func parseBearer(hdr string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(hdr, prefix) {
		return "", false
	}
	v := strings.TrimSpace(hdr[len(prefix):])
	if v == "" {
		return "", false
	}
	return v, true
}

// findAgentByToken 은 전체 Agent 를 순회하며 bcrypt 비교로 매칭을 찾는다.
// 토큰 hint 인덱스는 Phase 후속. Phase 1 규모(수십~수백 agents)에서는 이 방식으로 충분.
func (h *WSHandler) findAgentByToken(ctx context.Context, raw string) (*store.Agent, error) {
	list, err := h.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if token.Verify(raw, list[i].TokenHash) {
			return &list[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// session 은 업그레이드된 연결에 대해 HELLO 핸드셰이크와 heartbeat 를 실행한다.
func (h *WSHandler) session(parentCtx context.Context, conn *websocket.Conn, agent *store.Agent) {
	connCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	entry := &wsConn{agentID: agent.ID, conn: conn, cancel: cancel}

	// HELLO 수신 (TCP read timeout 으로 안전망).
	helloCtx, helloCancel := context.WithTimeout(connCtx, 5*time.Second)
	helloData, helloRaw, err := readHello(helloCtx, conn)
	helloCancel()
	if err != nil {
		h.Logger.Warn("ws_hello_failed", "agent_id", agent.ID, "err", err.Error())
		_ = conn.Close(websocket.StatusPolicyViolation, "hello required")
		return
	}
	if helloData.Version != protocol.ProtoVersion {
		reason := "protocol version mismatch: expected " + protocol.ProtoVersion + ", got " + helloData.Version
		_ = conn.Close(websocket.StatusCode(4001), reason)
		h.Logger.Warn("ws_version_mismatch", "agent_id", agent.ID, "got", helloData.Version)
		return
	}

	// 중복 연결 거절 (결정 12).
	if ok := h.Registry.add(entry); !ok {
		_ = conn.Close(websocket.StatusCode(4003), "agent already online")
		h.Logger.Warn("ws_duplicate_rejected", "agent_id", agent.ID)
		return
	}
	defer h.Registry.remove(entry)

	// online 전환.
	if err := h.Store.UpdateStatus(connCtx, agent.ID, "online", time.Now().UTC()); err != nil {
		h.Logger.Error("ws_online_update_failed", "agent_id", agent.ID, "err", err.Error())
		_ = conn.Close(websocket.StatusInternalError, "status update failed")
		return
	}
	h.Logger.Info("ws_connected", "agent_id", agent.ID, "name", agent.Name)

	// WELCOME 송신.
	welcome, _ := protocol.NewEnvelope(protocol.TypeWelcome, protocol.WelcomeData{
		Version: protocol.ProtoVersion,
		AgentID: agent.ID,
	})
	if err := writeEnvelope(connCtx, conn, welcome); err != nil {
		h.Logger.Warn("ws_welcome_write_failed", "agent_id", agent.ID, "err", err.Error())
		_ = conn.Close(websocket.StatusInternalError, "welcome write")
		h.markOffline(agent.ID)
		return
	}

	// Phase 3: HELLO 동기화 (running_previews 비교) — 별도 goroutine.
	// 실패는 로그만, session 진행에 영향 없음.
	go h.syncOnHello(connCtx, agent.ID, helloData, helloRaw)

	// 2 goroutine: readLoop + pingTicker.
	go h.readLoop(connCtx, cancel, conn, agent.ID)
	h.pingTicker(connCtx, cancel, conn, agent.ID)

	<-connCtx.Done()
	// 정리: close frame 송신 (정상 종료 시나리오) + offline 전환.
	_ = conn.Close(websocket.StatusNormalClosure, "session end")
	h.markOffline(agent.ID)
	h.Logger.Info("ws_disconnected", "agent_id", agent.ID, "reason", disconnectReason(parentCtx, connCtx))
}

// markOffline 은 새 context 로 DB 업데이트를 수행한다 (connCtx 이 이미 취소되었을 수 있음).
func (h *WSHandler) markOffline(agentID string) {
	bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Store.UpdateStatus(bg, agentID, "offline", time.Now().UTC()); err != nil {
		h.Logger.Error("ws_offline_update_failed", "agent_id", agentID, "err", err.Error())
	}
}

// readLoop 은 수신 메시지를 dispatch 한다.
// PONG 은 coder/websocket 라이브러리가 conn.Ping 의 응답으로 내부 처리한다.
// 앱 레벨 PING/PONG (protocol.PingData) 는 Phase 2 이후 확장 여지를 위해 수신만 지원.
//
// Phase 2:
//   - READY: dispatcher.OnReady 를 goroutine 으로 호출 (Read loop blocking 회피).
//   - STATUS_UPDATE: PreviewStore.UpdateStatus 를 wrap 한 핸들러 호출.
//   - 그 외(HELLO/LOG): debug 로그.
func (h *WSHandler) readLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, agentID string) {
	defer cancel()
	for {
		// 리스크 3: cancel 을 같은 iteration 내에서 호출.
		readCtx, rcancel := context.WithTimeout(ctx, PingInterval+PongTimeout)
		_, data, err := conn.Read(readCtx)
		rcancel()
		if err != nil {
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			h.Logger.Warn("ws_decode_failed", "agent_id", agentID, "err", err.Error())
			continue
		}
		switch env.Type {
		case protocol.TypeReady:
			if h.Ready == nil {
				h.Logger.Debug("ws_ready_no_handler", "agent_id", agentID)
				continue
			}
			// goroutine: dispatcher 의 DB 호출이 Read loop 를 막지 않도록.
			go func(id string) {
				bg, c := context.WithTimeout(context.Background(), 10*time.Second)
				defer c()
				if err := h.Ready.OnReady(bg, id); err != nil {
					h.Logger.Warn("ws_ready_dispatch_failed", "agent_id", id, "err", err.Error())
				}
			}(agentID)
		case protocol.TypeStatusUpdate:
			if h.StatusUpdate == nil {
				h.Logger.Debug("ws_status_update_no_handler", "agent_id", agentID)
				continue
			}
			var su protocol.StatusUpdateData
			if err := env.Decode(&su); err != nil {
				h.Logger.Warn("ws_status_update_decode_failed", "agent_id", agentID, "err", err.Error())
				continue
			}
			go func(d protocol.StatusUpdateData) {
				bg, c := context.WithTimeout(context.Background(), 10*time.Second)
				defer c()
				if err := h.StatusUpdate.OnStatusUpdate(bg, agentID, d); err != nil {
					h.Logger.Warn("ws_status_update_failed", "agent_id", agentID, "preview_id", d.PreviewID, "err", err.Error())
				}
			}(su)
		default:
			if env.Type != "" {
				h.Logger.Debug("ws_message_received", "agent_id", agentID, "type", env.Type)
			}
		}
	}
}

// pingTicker 는 PingInterval 마다 Ping 을 보낸다. PongTimeout 내 응답이 없으면 cancel 한다.
func (h *WSHandler) pingTicker(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, agentID string) {
	defer cancel()
	t := time.NewTicker(PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pingCtx, pcancel := context.WithTimeout(ctx, PongTimeout)
			err := conn.Ping(pingCtx)
			pcancel()
			if err != nil {
				h.Logger.Debug("ws_ping_failed", "agent_id", agentID, "err", err.Error())
				return
			}
		}
	}
}

// readHello 는 첫 메시지가 HELLO 인지 확인하고 HelloData + raw envelope.Data 를 반환한다.
// raw 는 hasRunningPreviewsKey 헬퍼가 키 부재 검출에 사용 (결정 8).
func readHello(ctx context.Context, conn *websocket.Conn) (protocol.HelloData, json.RawMessage, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return protocol.HelloData{}, nil, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return protocol.HelloData{}, nil, err
	}
	if env.Type != protocol.TypeHello {
		return protocol.HelloData{}, nil, errors.New("first message must be HELLO")
	}
	var hello protocol.HelloData
	if err := env.Decode(&hello); err != nil {
		return protocol.HelloData{}, nil, err
	}
	return hello, env.Data, nil
}

// hasRunningPreviewsKey 는 envelope.Data 의 raw JSON 에서 "running_previews" 키
// 존재 여부를 확인한다 (결정 8 / §5-3 / 리스크 6 완화). map decode 로 substring
// false-positive 방지.
func hasRunningPreviewsKey(data json.RawMessage) bool {
	if len(data) == 0 {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	_, ok := m["running_previews"]
	return ok
}

// syncOnHello 는 HELLO 수신 직후 Agent 의 RunningPreviews 와 DB 의 active preview
// 집합을 비교한다 (Phase 3 §5-3, 결정 8/9). 비교 결과:
//   - Agent − DB (orphan): JOB_TEARDOWN 송신 + reconcile_hello_orphan_container 로그.
//   - DB − Agent (lost):  UpdateStatus(*→failed, "agent restart lost container")
//                         + reconcile_hello_lost_container 로그. ErrStaleState 는 무시.
//
// 레거시 Agent (running_previews 키 부재) 는 비교 SKIP + WARN reconcile_hello_legacy_agent.
// PreviewStore 가 nil 이면 (테스트) 즉시 반환.
func (h *WSHandler) syncOnHello(ctx context.Context, agentID string, hello protocol.HelloData, raw json.RawMessage) {
	if h.PreviewStore == nil {
		return
	}
	if !hasRunningPreviewsKey(raw) {
		h.Logger.Warn("reconcile_hello_legacy_agent", "agent_id", agentID)
		return
	}

	agentSet := make(map[string]struct{}, len(hello.RunningPreviews))
	for _, id := range hello.RunningPreviews {
		agentSet[id] = struct{}{}
	}

	dbList, err := h.PreviewStore.ListByAgent(ctx, agentID,
		[]string{"assigned", "building", "running", "teardown"})
	if err != nil {
		h.Logger.Warn("reconcile_hello_listbyagent_failed",
			"agent_id", agentID, "err", err.Error())
		return
	}
	dbSet := make(map[string]store.Preview, len(dbList))
	for _, p := range dbList {
		dbSet[p.ID] = p
	}

	// Agent − DB: orphan container — JOB_TEARDOWN 송신.
	for id := range agentSet {
		if _, ok := dbSet[id]; !ok {
			if h.TeardownSender == nil {
				h.Logger.Info("reconcile_hello_orphan_container",
					"preview_id", id, "agent_id", agentID, "note", "no_teardown_sender")
				continue
			}
			if err := h.TeardownSender.SendTeardown(ctx, agentID, id); err != nil {
				h.Logger.Warn("reconcile_hello_teardown_send_failed",
					"preview_id", id, "agent_id", agentID, "err", err.Error())
			} else {
				h.Logger.Info("reconcile_hello_orphan_container",
					"preview_id", id, "agent_id", agentID)
			}
		}
	}

	// DB − Agent: lost container — UpdateStatus(*→failed).
	now := time.Now().UTC()
	for id, p := range dbSet {
		if _, ok := agentSet[id]; ok {
			continue
		}
		if uerr := h.PreviewStore.UpdateStatus(ctx, id, p.Status, "failed",
			"agent restart lost container", now,
			store.PreviewFields{}); uerr != nil {
			if errors.Is(uerr, store.ErrStaleState) {
				continue
			}
			h.Logger.Warn("reconcile_hello_lost_failed",
				"preview_id", id, "err", uerr.Error())
			continue
		}
		h.Logger.Info("reconcile_hello_lost_container",
			"preview_id", id, "agent_id", agentID)
	}
}

// writeEnvelope 는 Envelope 를 JSON binary 로 WS 메시지에 싣는다.
func writeEnvelope(ctx context.Context, conn *websocket.Conn, env protocol.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

// disconnectReason 은 로그용. connCtx 취소 사유 추정.
func disconnectReason(parent, conn context.Context) string {
	if parent.Err() != nil {
		return "shutdown"
	}
	if conn.Err() != nil {
		return "canceled"
	}
	return "unknown"
}
