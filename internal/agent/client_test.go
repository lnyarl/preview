package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lnyarl/preview/internal/protocol"
)

// fakeHub 은 /agent/ws 에 접속한 클라이언트와 HELLO/WELCOME 를 주고받는 최소 서버.
type fakeHub struct {
	t        *testing.T
	helloCh  chan protocol.HelloData
	sendCode websocket.StatusCode // > 0 이면 WELCOME 대신 close frame 으로 응답
}

func (h *fakeHub) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		http.Error(w, "missing auth", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "read")
		return
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "decode")
		return
	}
	if env.Type != protocol.TypeHello {
		_ = conn.Close(websocket.StatusPolicyViolation, "expected HELLO")
		return
	}
	var hello protocol.HelloData
	_ = env.Decode(&hello)
	if h.helloCh != nil {
		h.helloCh <- hello
	}
	if h.sendCode != 0 {
		_ = conn.Close(h.sendCode, "policy")
		return
	}
	welcome, _ := protocol.NewEnvelope(protocol.TypeWelcome, protocol.WelcomeData{
		Version: protocol.ProtoVersion,
		AgentID: "stub",
	})
	b, _ := json.Marshal(welcome)
	_ = conn.Write(ctx, websocket.MessageText, b)
	// block until context ends or client closes.
	_, _, _ = conn.Read(ctx)
	_ = conn.Close(websocket.StatusNormalClosure, "done")
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestClientHandshakeSuccessAndShutdown(t *testing.T) {
	helloCh := make(chan protocol.HelloData, 1)
	fh := &fakeHub{t: t, helloCh: helloCh}
	srv := httptest.NewServer(http.HandlerFunc(fh.handle))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/ws"
	cfg := Config{
		HubURL: wsURL,
		Token:  "agt_fake",
	}
	c := NewClient(cfg, silentLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = c.Run(ctx)
		close(done)
	}()

	select {
	case hello := <-helloCh:
		if hello.Version != protocol.ProtoVersion {
			t.Fatalf("hello.version=%q want %q", hello.Version, protocol.ProtoVersion)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hello not received in time")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not shut down")
	}
}

func TestClientDialFailureReturnsError(t *testing.T) {
	// Dial a port that is almost certainly not listening.
	cfg := Config{HubURL: "ws://127.0.0.1:1/agent/ws", Token: "agt_x"}
	c := NewClient(cfg, silentLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.once(ctx); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestClientRunRespectsContextCancel(t *testing.T) {
	// same broken URL but exercise Run so backoff branch is hit.
	cfg := Config{HubURL: "ws://127.0.0.1:1/agent/ws", Token: "agt_x"}
	c := NewClient(cfg, silentLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx)
}

func TestClientReceivesCloseFrame(t *testing.T) {
	fh := &fakeHub{t: t, helloCh: make(chan protocol.HelloData, 1), sendCode: websocket.StatusGoingAway}
	srv := httptest.NewServer(http.HandlerFunc(fh.handle))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/ws"
	cfg := Config{HubURL: wsURL, Token: "agt_fake"}
	c := NewClient(cfg, silentLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Run once loop should quickly reconnect on close; we only care that `once` returns.
	err := c.once(ctx)
	if err == nil {
		t.Fatal("expected error from once() after close frame")
	}
}

// F-14 (Phase 5): conn=nil (미연결) 상태에서 SendReady 가 panic 없이 error 리턴.
func TestClientSendReadyNotConnectedReturnsError(t *testing.T) {
	cfg := Config{HubURL: "ws://invalid/agent/ws", Token: "agt_fake"}
	c := NewClient(cfg, silentLogger())
	if err := c.SendReady(context.Background()); err == nil {
		t.Fatal("expected error when conn is nil")
	}
}

// readyHub 는 HELLO/WELCOME 후 들어오는 메시지(들)를 readyMsgs 채널로 전달.
// F-15 / F-17 검증용.
type readyHub struct {
	helloCh   chan protocol.HelloData
	readyMsgs chan protocol.Envelope
}

func (h *readyHub) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		http.Error(w, "missing auth", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "read")
		return
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "decode")
		return
	}
	var hello protocol.HelloData
	_ = env.Decode(&hello)
	if h.helloCh != nil {
		h.helloCh <- hello
	}
	welcome, _ := protocol.NewEnvelope(protocol.TypeWelcome, protocol.WelcomeData{
		Version: protocol.ProtoVersion,
		AgentID: "stub",
	})
	wb, _ := json.Marshal(welcome)
	if err := conn.Write(ctx, websocket.MessageText, wb); err != nil {
		return
	}
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var rEnv protocol.Envelope
		if err := json.Unmarshal(raw, &rEnv); err == nil {
			h.readyMsgs <- rEnv
		}
	}
}

// F-15 (Phase 5): SendReady 가 정상 conn 에 READY envelope 1 개를 write.
// runner 미주입 시 once() 의 fallback 경로로 SendReady 1 회 호출됨을 검증.
func TestClientSendReadyWritesReadyEnvelope(t *testing.T) {
	rh := &readyHub{
		helloCh:   make(chan protocol.HelloData, 1),
		readyMsgs: make(chan protocol.Envelope, 4),
	}
	srv := httptest.NewServer(http.HandlerFunc(rh.handle))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/ws"
	cfg := Config{HubURL: wsURL, Token: "agt_fake"}
	c := NewClient(cfg, silentLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	select {
	case <-rh.helloCh:
	case <-time.After(3 * time.Second):
		t.Fatal("hello not received")
	}
	select {
	case env := <-rh.readyMsgs:
		if env.Type != protocol.TypeReady {
			t.Fatalf("first msg type=%s want %s", env.Type, protocol.TypeReady)
		}
		var data protocol.ReadyData
		if err := env.Decode(&data); err != nil {
			t.Fatalf("decode ready: %v", err)
		}
		if data.Capacity != 1 {
			t.Fatalf("capacity=%d want 1", data.Capacity)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("READY not received")
	}
}

// F-17 (Phase 5): runner 가 주입되어 있고 maxJobs=3 이면 once() 가
// maybeSendReady 호출로 정확히 3 개의 READY 송신 (초기 inFlight=0).
func TestClientOnceCallsMaybeSendReadyWithMaxJobs(t *testing.T) {
	rh := &readyHub{
		helloCh:   make(chan protocol.HelloData, 1),
		readyMsgs: make(chan protocol.Envelope, 8),
	}
	srv := httptest.NewServer(http.HandlerFunc(rh.handle))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/ws"
	cfg := Config{HubURL: wsURL, Token: "agt_fake"}
	c := NewClient(cfg, silentLogger())

	// 최소 Runner 만 만들어 maybeSendReady 경로만 검증.
	runner := &Runner{logger: silentLogger()}
	runner.SetMaxJobs(3)
	runner.SetReadySender(c)
	c.SetRunner(runner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	select {
	case <-rh.helloCh:
	case <-time.After(3 * time.Second):
		t.Fatal("hello not received")
	}

	deadline := time.After(3 * time.Second)
	got := 0
	for got < 3 {
		select {
		case env := <-rh.readyMsgs:
			if env.Type != protocol.TypeReady {
				t.Fatalf("unexpected msg type=%s", env.Type)
			}
			got++
		case <-deadline:
			t.Fatalf("only %d/3 READY messages received", got)
		}
	}
}
