// Package protocol defines the wire format between Hub and Agent.
//
// 이 파일의 책임:
//   - Envelope 와 메시지 타입 상수 선언
//   - Phase 1 에서 실제 교환되는 HELLO/WELCOME/PING/PONG DTO
//   - Phase 2 에서 사용할 메시지 타입 문자열 동결 (구조체는 Phase 2)
//
// 참고: docs/specs/phase-1-agent-registration-and-ws.md §5-6 (프로토콜 타입 상수)
package protocol

import (
	"encoding/json"
	"errors"
)

// 메시지 타입 상수. 모든 Envelope.Type 값은 이 상수 중 하나여야 한다.
const (
	TypeHello   = "HELLO"
	TypeWelcome = "WELCOME"
	TypePing    = "PING"
	TypePong    = "PONG"
	// Phase 2 에서 구조체와 함께 구현. 상수만 선언하여 프로토콜 버전을 동결.
	TypeReady        = "READY"
	TypeJobAssign    = "JOB_ASSIGN"
	TypeStatusUpdate = "STATUS_UPDATE"
	TypeLog          = "LOG"
	TypeJobTeardown  = "JOB_TEARDOWN"
	// Phase 4: Hub-managed Agent 빌드 설정 (HELLO 직후 1회 + 변경 시 푸시).
	// 두 메시지의 페이로드 스키마는 동일하다 (결정 8). 이름만 다르다 — 의도가 다르기 때문.
	TypeAgentConfig  = "AGENT_CONFIG"
	TypeConfigUpdate = "CONFIG_UPDATE"
)

// ProtoVersion 은 HELLO/WELCOME 에 실리는 프로토콜 버전 문자열.
// 결정 9: 단순 문자열 비교로 족함. semver 파싱 없음.
const ProtoVersion = "v1"

// ErrInvalidEnvelope 는 Envelope JSON 이 스키마(type, data)를 만족하지 않을 때 반환된다.
var ErrInvalidEnvelope = errors.New("protocol: invalid envelope")

// Envelope 는 모든 Hub<->Agent 메시지의 공통 래퍼.
// Data 는 Type 을 보고 구체 타입으로 Unmarshal 한다.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// HelloData 는 Agent 가 연결 직후 송신하는 HELLO 본문.
//
// Phase 3 추가: RunningPreviews 는 omitempty 미적용 (결정 8 / §5-2).
// 레거시 Agent (Phase 2) 는 이 키 자체를 보내지 않으므로 Hub 측이
// hasRunningPreviewsKey 헬퍼로 부재를 구분해 동기화 SKIP.
//
// Labels 는 단순 값 슬라이스 (예: ["home", "us-east"]). 키-값 형태가 아니므로
// 단순한 set-membership 매칭으로 라우팅한다.
type HelloData struct {
	Version         string   `json:"version"`
	Labels          []string `json:"labels,omitempty"`
	AdvertiseHost   string   `json:"advertise_host,omitempty"`
	RunningPreviews []string `json:"running_previews"`
}

// WelcomeData 는 Hub 가 HELLO 에 대한 응답으로 보내는 본문.
type WelcomeData struct {
	Version string `json:"version"`
	AgentID string `json:"agent_id"`
}

// PingData 는 Hub 가 주기적으로 송신하는 PING 본문. ts 는 진단용 Unix ms.
type PingData struct {
	TS int64 `json:"ts"`
}

// PongData 는 Agent 가 PING 수신 직후 송신하는 응답. Hub 에서 받은 ts 를 에코한다.
type PongData struct {
	TS int64 `json:"ts"`
}

// ReadyData 는 Agent 가 빈 슬롯이 생겼을 때 Hub 로 송신하는 본문 (결정 10).
// MVP: capacity 페이로드 없이 빈도로 표현. SlotID 는 디버깅용 nonce (옵션).
type ReadyData struct {
	SlotID   string `json:"slot_id,omitempty"`
	Capacity int    `json:"capacity,omitempty"`
}

// JobAssignData 는 Hub 가 Claim 후 Agent 에게 전달하는 작업 본문.
// Labels 는 preview 의 라벨 echo (Agent 측 진단용).
type JobAssignData struct {
	PreviewID    string   `json:"preview_id"`
	RepoFullName string   `json:"repo_full_name"`
	RepoURL      string   `json:"repo_url"`
	CommitSHA    string   `json:"commit_sha"`
	Branch       string   `json:"branch,omitempty"`
	Labels       []string `json:"labels,omitempty"`
}

// StatusUpdateData 는 Agent 가 building → running → done|failed 전이 시
// Hub 로 보내는 본문. nullable 필드는 포인터로 명시(빈 값 set 과 미설정 구분).
type StatusUpdateData struct {
	PreviewID    string  `json:"preview_id"`
	Status       string  `json:"status"`
	Message      string  `json:"message,omitempty"`
	ContainerID  *string `json:"container_id,omitempty"`
	AgentHost    *string `json:"agent_host,omitempty"`
	AgentPort    *int    `json:"agent_port,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
}

// JobTeardownData 는 Hub 가 PR closed 등으로 컨테이너 정리를 요청할 때 송신.
type JobTeardownData struct {
	PreviewID string `json:"preview_id"`
}

// LogData 는 docker logs 스트림 라인 (Phase 2 구조체만 동결, 와이어 미구현 — 결정 14).
type LogData struct {
	PreviewID string `json:"preview_id"`
	Stream    string `json:"stream"`
	Line      string `json:"line"`
	TS        int64  `json:"ts"`
}

// AgentConfigData 는 AGENT_CONFIG / CONFIG_UPDATE 의 본문 (Phase 4).
//
// RunCommands 가 빈 슬라이스([]) 면 실행을 건너뛴다 (기본 명령 없음).
// ContainerPort == 0 이면 기본값(80) 적용.
//
// 두 sentinel 모두 wire 에 명시 송신된다 (omitempty 미적용) — Agent 측에서
// "키 부재" 와 "기본값 적용" 의도를 구분할 필요 없이 단일 경로로 처리하기 위함.
type AgentConfigData struct {
	RunCommands   []string `json:"run_commands"`
	ContainerPort int      `json:"container_port"`
}

// NewEnvelope 는 Type 과 임의의 Data 로부터 Envelope 를 만든다.
// data 가 nil 이면 빈 JSON 객체({})가 Data 필드에 실린다.
func NewEnvelope(typ string, data any) (Envelope, error) {
	if data == nil {
		return Envelope{Type: typ, Data: json.RawMessage(`{}`)}, nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Type: typ, Data: raw}, nil
}

// Decode 는 Envelope 의 Data 를 v 로 Unmarshal 한다. v 는 포인터여야 한다.
func (e Envelope) Decode(v any) error {
	if len(e.Data) == 0 {
		return ErrInvalidEnvelope
	}
	return json.Unmarshal(e.Data, v)
}
