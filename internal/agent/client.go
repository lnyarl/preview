// 이 파일의 책임:
//   - Hub 에 outbound WebSocket 으로 연결.
//   - HELLO 송신 -> WELCOME 수신.
//   - Hub 의 PING 수신 시 내부 PONG 은 coder/websocket 라이브러리가 처리하므로,
//     앱 레벨에서는 read loop 가 다른 메시지들을 기다린다.
//   - 연결 유실 시 지수 백오프로 재시도.
//   - Phase 2: WELCOME 후 READY 1회 송신 + JOB_ASSIGN/JOB_TEARDOWN dispatch.
//
// 참고: 기획서 §4-3-1, 결정 4/5/10.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lnyarl/preview/internal/protocol"
)

// Client 는 Agent 의 Hub 연결 루프.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	backoff *Backoff

	runner *Runner

	connMu sync.Mutex
	conn   *websocket.Conn
}

// NewClient 는 주어진 설정으로 Client 를 만든다.
// runner 는 nil 이어도 동작 (Phase 1 호환). nil 이면 JOB_ASSIGN 은 로그만.
func NewClient(cfg Config, logger *slog.Logger) *Client {
	return &Client{cfg: cfg, logger: logger, backoff: NewBackoff(0)}
}

// SetRunner 는 JOB_ASSIGN/TEARDOWN 처리 runner 를 주입한다.
func (c *Client) SetRunner(r *Runner) { c.runner = r }

// SendStatusUpdate 는 현재 conn 으로 STATUS_UPDATE envelope 를 송신한다.
// Runner.HubSender 인터페이스 만족.
func (c *Client) SendStatusUpdate(ctx context.Context, d protocol.StatusUpdateData) error {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn == nil {
		return errors.New("client.SendStatusUpdate: not connected")
	}
	env, err := protocol.NewEnvelope(protocol.TypeStatusUpdate, d)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(env)
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// Run 은 ctx 가 취소될 때까지 연결·재연결 루프를 돈다.
// ctx 취소 시 현재 연결에 close frame 을 송신하고 반환한다.
func (c *Client) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		err := c.once(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.logger.Warn("ws_session_ended", "err", err.Error())
		}
		// 재연결 대기.
		wait := c.backoff.Next()
		c.logger.Info("agent_reconnect_wait", "wait_ms", wait.Milliseconds())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// once 는 단일 연결을 처리한다. 정상 종료·오류 모두에서 conn 은 닫힌 상태로 반환된다.
func (c *Client) once(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	headers := map[string][]string{
		"Authorization": {"Bearer " + c.cfg.Token},
	}
	conn, _, err := websocket.Dial(dialCtx, c.cfg.HubURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	// 연결 성공 시 backoff 리셋.
	c.backoff.Reset()

	// HELLO 송신.
	hello, err := protocol.NewEnvelope(protocol.TypeHello, protocol.HelloData{
		Version:       protocol.ProtoVersion,
		Labels:        c.cfg.Labels,
		AdvertiseHost: c.cfg.AdvertiseHost,
	})
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "hello marshal")
		return fmt.Errorf("build hello: %w", err)
	}
	b, _ := json.Marshal(hello)
	writeCtx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	err = conn.Write(writeCtx, websocket.MessageText, b)
	wcancel()
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "hello write")
		return fmt.Errorf("write hello: %w", err)
	}

	// WELCOME 수신.
	welcomeCtx, wcancel2 := context.WithTimeout(ctx, 10*time.Second)
	_, data, err := conn.Read(welcomeCtx)
	wcancel2()
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "welcome read")
		return fmt.Errorf("read welcome: %w", err)
	}
	var welcomeEnv protocol.Envelope
	if err := json.Unmarshal(data, &welcomeEnv); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "welcome decode")
		return fmt.Errorf("decode welcome: %w", err)
	}
	if welcomeEnv.Type != protocol.TypeWelcome {
		_ = conn.Close(websocket.StatusPolicyViolation, "expected WELCOME")
		return fmt.Errorf("unexpected first message: %s", welcomeEnv.Type)
	}
	c.logger.Info("ws_connected", "hub_url", c.cfg.HubURL)

	// 연결 등록 — SendStatusUpdate 가 사용.
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	defer func() {
		c.connMu.Lock()
		c.conn = nil
		c.connMu.Unlock()
	}()

	// ctx 취소 시 close frame 송신을 보장.
	go func() {
		<-ctx.Done()
		_ = conn.Close(websocket.StatusNormalClosure, "graceful shutdown")
	}()

	// READY 1회 송신 (결정 10: capacity=1 MVP).
	readyEnv, _ := protocol.NewEnvelope(protocol.TypeReady, protocol.ReadyData{Capacity: 1})
	rb, _ := json.Marshal(readyEnv)
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	if werr := conn.Write(rctx, websocket.MessageText, rb); werr != nil {
		rcancel()
		c.logger.Warn("ws_ready_write_failed", "err", werr.Error())
	} else {
		rcancel()
	}

	// Read loop — Hub 의 PING 은 라이브러리가 처리.
	// 수신 에러가 나면 루프를 빠져 나온다. Hub 의 close frame(1001 등) 도 여기서 감지된다.
	for {
		readCtx, rcancel := context.WithCancel(ctx)
		_, msg, err := conn.Read(readCtx)
		rcancel()
		if err != nil {
			_ = conn.CloseNow()
			if ce := (&websocket.CloseError{}); errors.As(err, ce) {
				c.logger.Info("hub_sent_close_frame", "close_code", int(ce.Code), "reason", ce.Reason)
				return fmt.Errorf("close: code=%d %s", ce.Code, ce.Reason)
			}
			return fmt.Errorf("read: %w", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			c.logger.Warn("ws_decode_failed", "err", err.Error())
			continue
		}
		c.dispatchMessage(ctx, env)
	}
}

// dispatchMessage 는 수신 envelope 를 type 별로 dispatch.
func (c *Client) dispatchMessage(ctx context.Context, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeJobAssign:
		var data protocol.JobAssignData
		if err := env.Decode(&data); err != nil {
			c.logger.Warn("job_assign_decode_failed", "err", err.Error())
			return
		}
		if c.runner == nil {
			c.logger.Warn("job_assign_received_without_runner", "preview_id", data.PreviewID)
			return
		}
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			if err := c.runner.Handle(bg, data); err != nil {
				c.logger.Warn("runner_handle_failed", "preview_id", data.PreviewID, "err", err.Error())
			}
		}()
	case protocol.TypeJobTeardown:
		var data protocol.JobTeardownData
		if err := env.Decode(&data); err != nil {
			c.logger.Warn("job_teardown_decode_failed", "err", err.Error())
			return
		}
		if c.runner == nil {
			c.logger.Warn("job_teardown_received_without_runner", "preview_id", data.PreviewID)
			return
		}
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := c.runner.Teardown(bg, data.PreviewID); err != nil {
				c.logger.Warn("runner_teardown_failed", "preview_id", data.PreviewID, "err", err.Error())
			}
		}()
	default:
		c.logger.Debug("ws_message_received", "type", env.Type)
	}
}
