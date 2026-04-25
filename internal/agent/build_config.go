// 이 파일의 책임:
//   - Agent 의 in-memory 빌드 설정 (BuildCommands + ContainerPort) 보관.
//   - Hub 가 송신하는 AGENT_CONFIG / CONFIG_UPDATE 가 Replace 로 atomic 갱신.
//   - Runner.Handle 이 진입 시점 1회 Snapshot 으로 받고 끝까지 같은 본을 사용 (결정 11).
//   - Replace / Snapshot 둘 다 슬라이스 deep copy 하여 외부 mutate 영향 차단.
//
// 참고: docs/specs/phase-4-agent-build-config.md §4-7, 결정 9.
package agent

import (
	"sync"

	"github.com/lnyarl/preview/internal/protocol"
)

// Holder 는 Agent 의 현재 빌드 설정을 thread-safe 하게 보관한다.
// 빈도 패턴: 쓰기 매우 낮음 (연결당 1회 + 사용자 저장 시), 읽기 빈번 (매 Handle).
// → sync.RWMutex + 값 복사 패턴 (결정 9).
type Holder struct {
	mu  sync.RWMutex
	cfg protocol.AgentConfigData
}

// NewHolder 는 빈 Holder 를 만든다. 초기 상태는 BuildCommands=[], ContainerPort=0
// 즉 "기본값으로 동작" 의도 (결정 2).
func NewHolder() *Holder { return &Holder{} }

// Replace 는 cfg 를 atomic 으로 교체한다. 슬라이스 deep copy 로 외부 mutate 영향 차단.
func (h *Holder) Replace(cfg protocol.AgentConfigData) {
	cmds := make([]string, len(cfg.BuildCommands))
	copy(cmds, cfg.BuildCommands)
	h.mu.Lock()
	h.cfg = protocol.AgentConfigData{
		BuildCommands: cmds,
		ContainerPort: cfg.ContainerPort,
	}
	h.mu.Unlock()
}

// Snapshot 은 현재 설정의 deep copy 를 반환한다. 외부에서 결과 슬라이스를
// mutate 해도 Holder 내부 상태에 영향이 없다.
func (h *Holder) Snapshot() protocol.AgentConfigData {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cmds := make([]string, len(h.cfg.BuildCommands))
	copy(cmds, h.cfg.BuildCommands)
	return protocol.AgentConfigData{
		BuildCommands: cmds,
		ContainerPort: h.cfg.ContainerPort,
	}
}
