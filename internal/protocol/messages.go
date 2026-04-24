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
type HelloData struct {
	Version       string            `json:"version"`
	Labels        map[string]string `json:"labels,omitempty"`
	AdvertiseHost string            `json:"advertise_host,omitempty"`
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
