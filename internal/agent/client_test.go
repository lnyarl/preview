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
