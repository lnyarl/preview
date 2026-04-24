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

// WSHandler 는 /agent/ws 엔드포인트를 처리한다.
type WSHandler struct {
	Store    store.AgentStore
	Registry *ConnRegistry
	Logger   *slog.Logger
}

// NewWSHandler 는 WSHandler 를 조립한다.
func NewWSHandler(s store.AgentStore, reg *ConnRegistry, logger *slog.Logger) *WSHandler {
	return &WSHandler{Store: s, Registry: reg, Logger: logger}
}

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
	helloData, err := readHello(helloCtx, conn)
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
		// 현재 Phase 1 에서는 HELLO 는 session 시작 시점에만 처리되고
		// 그 외 수신은 Phase 2 READY/STATUS_UPDATE/LOG 대비로 로그만 남긴다.
		if env.Type != "" {
			h.Logger.Debug("ws_message_received", "agent_id", agentID, "type", env.Type)
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

// readHello 는 첫 메시지가 HELLO 인지 확인하고 HelloData 를 반환한다.
func readHello(ctx context.Context, conn *websocket.Conn) (protocol.HelloData, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return protocol.HelloData{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return protocol.HelloData{}, err
	}
	if env.Type != protocol.TypeHello {
		return protocol.HelloData{}, errors.New("first message must be HELLO")
	}
	var hello protocol.HelloData
	if err := env.Decode(&hello); err != nil {
		return protocol.HelloData{}, err
	}
	return hello, nil
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
