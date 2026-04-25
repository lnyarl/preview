// 이 파일의 책임:
//   - Agent 의 in-memory 실행 설정 (RunCommands + ContainerPort) 보관.
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

// Holder 는 Agent 의 현재 실행 설정을 thread-safe 하게 보관한다.
// 빈도 패턴: 쓰기 매우 낮음 (연결당 1회 + 사용자 저장 시), 읽기 빈번 (매 Handle).
// → sync.RWMutex + 값 복사 패턴 (결정 9).
type Holder struct {
	mu  sync.RWMutex
	cfg protocol.AgentConfigData
}

// NewHolder 는 빈 Holder 를 만든다. 초기 상태는 RunCommands=[], ContainerPort=0
// 즉 "실행 명령 없음 + 포트 기본값(80)" 의도.
func NewHolder() *Holder { return &Holder{} }

// Replace 는 cfg 를 atomic 으로 교체한다. 슬라이스 deep copy 로 외부 mutate 영향 차단.
func (h *Holder) Replace(cfg protocol.AgentConfigData) {
	cmds := make([]string, len(cfg.RunCommands))
	copy(cmds, cfg.RunCommands)
	h.mu.Lock()
	h.cfg = protocol.AgentConfigData{
		RunCommands:   cmds,
		ContainerPort: cfg.ContainerPort,
	}
	h.mu.Unlock()
}

// Snapshot 은 현재 설정의 deep copy 를 반환한다. 외부에서 결과 슬라이스를
// mutate 해도 Holder 내부 상태에 영향이 없다.
func (h *Holder) Snapshot() protocol.AgentConfigData {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cmds := make([]string, len(h.cfg.RunCommands))
	copy(cmds, h.cfg.RunCommands)
	return protocol.AgentConfigData{
		RunCommands:   cmds,
		ContainerPort: h.cfg.ContainerPort,
	}
}
