//go:build e2e

// 이 파일의 책임:
//   - Hub + Agent 의 실제 코드를 in-process 로 묶고, 외부(Docker/git remote)만 fake 로
//     교체한 E2E 시나리오 검증.
//   - 시나리오:
//     1) /health smoke
//     2) Agent 연결 → online 상태 전이
//     3) Webhook → Preview 'queued' 생성
//     4) Webhook → 디스패치 → 셸 빌드 → 컨테이너 시작 → Preview 'running'
//     5) Webhook(closed) → 컨테이너 정리 → Preview 'done'
//
// 실행: go test -tags=e2e -v -timeout=60s ./tests/e2e/
package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

const (
	testRepo   = "test-owner/test-repo"
	testSecret = "e2e-test-secret-abc"
)

// TestHealth 는 /health 엔드포인트가 200 OK 를 반환하는지 확인한다 (smoke).
func TestHealth(t *testing.T) {
	h := startHub(t, testSecret, "")
	resp, err := http.Get(h.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health: status=%d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("/health decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("/health: body=%v", body)
	}
}

// TestAgentConnectsAndGoesOnline 은 Agent 가 WS 연결 후 status='online' 으로
// 전이되는지 확인한다.
func TestAgentConnectsAndGoesOnline(t *testing.T) {
	h := startHub(t, testSecret, "")
	tok := h.createAgent(t, "e2e-agent")
	startAgent(t, h.WSURL, tok, "file:///nonexistent")
	h.pollAgentStatus(t, "e2e-agent", "online", 5*time.Second)
}

// TestWebhookCreatesPreview 는 Agent 없이 webhook(opened) 만으로 preview row 가
// 'queued' 상태로 생성되는지 확인한다 (Hub-only 경로).
func TestWebhookCreatesPreview(t *testing.T) {
	h := startHub(t, testSecret, "")
	h.sendWebhook(t, "opened", 1, "aaa111", "feat/test", testRepo)
	h.pollPreviewStatus(t, 1, testRepo, "queued", 5*time.Second)
}

// TestFullJobFlow 는 webhook → dispatcher → agent runner → STATUS_UPDATE running
// 의 전 경로를 검증한다.
//
// 디스패치 모델은 Pull 방식: Agent 의 READY 만이 dispatcher.OnReady 를 트리거한다.
// 따라서 큐에 작업이 있는 상태에서 Agent 가 처음 READY 를 보내야 매칭이 일어난다.
func TestFullJobFlow(t *testing.T) {
	h := startHub(t, testSecret, "")
	tok := h.createAgent(t, "flow-agent")

	h.sendWebhook(t, "opened", 2, "bbb222", "feat/flow", testRepo)
	h.pollPreviewStatus(t, 2, testRepo, "queued", 5*time.Second)

	startAgent(t, h.WSURL, tok, "file:///e2e-flow")
	h.pollAgentStatus(t, "flow-agent", "online", 5*time.Second)
	h.pollPreviewStatus(t, 2, testRepo, "running", 15*time.Second)

	p := h.getPreview(t, 2, testRepo)
	if p == nil {
		t.Fatalf("preview not found after running")
	}
	if p.AgentPort == nil || *p.AgentPort == 0 {
		t.Fatalf("agent_port not set: %+v", p)
	}
	if p.AgentHost == nil || *p.AgentHost == "" {
		t.Fatalf("agent_host not set: %+v", p)
	}
}

// TestJobTeardown 은 PR closed webhook 이 Agent 에 JOB_TEARDOWN 을 전달하고
// preview 가 'done' 으로 전이되는지 확인한다. TestFullJobFlow 와 동일 흐름으로
// running 까지 도달시킨 뒤 closed webhook 으로 정리한다.
func TestJobTeardown(t *testing.T) {
	h := startHub(t, testSecret, "")
	tok := h.createAgent(t, "teardown-agent")

	h.sendWebhook(t, "opened", 3, "ccc333", "feat/td", testRepo)
	h.pollPreviewStatus(t, 3, testRepo, "queued", 5*time.Second)

	startAgent(t, h.WSURL, tok, "file:///e2e-teardown")
	h.pollAgentStatus(t, "teardown-agent", "online", 5*time.Second)
	h.pollPreviewStatus(t, 3, testRepo, "running", 15*time.Second)

	h.sendWebhook(t, "closed", 3, "ccc333", "feat/td", testRepo)
	h.pollPreviewStatus(t, 3, testRepo, "done", 10*time.Second)
}

// 사용되지 않는 io import 방지 (필요 시).
var _ = io.Discard
