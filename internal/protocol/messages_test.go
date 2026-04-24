package protocol

import (
	"encoding/json"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	hello := HelloData{Version: ProtoVersion, Labels: map[string]string{"env": "local"}}
	env, err := NewEnvelope(TypeHello, hello)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if env.Type != TypeHello {
		t.Fatalf("type=%q", env.Type)
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Envelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var decoded HelloData
	if err := got.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Version != hello.Version || decoded.Labels["env"] != "local" {
		t.Fatalf("unexpected decoded: %+v", decoded)
	}
}

func TestEnvelopeUnknownType(t *testing.T) {
	env := Envelope{Type: "SOMETHING_NEW", Data: json.RawMessage(`{"foo":"bar"}`)}
	// 알 수 없는 Type 이어도 Envelope 수준에서는 오류가 아니며 수신자가 로그·무시 결정을 한다.
	var m map[string]string
	if err := env.Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["foo"] != "bar" {
		t.Fatalf("unexpected: %+v", m)
	}
}

func TestEnvelopeEmptyData(t *testing.T) {
	env := Envelope{Type: "X"}
	var m map[string]string
	if err := env.Decode(&m); err == nil {
		t.Fatal("expected error on empty data")
	}
}

func TestNilDataYieldsEmptyObject(t *testing.T) {
	env, err := NewEnvelope(TypePing, nil)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if string(env.Data) != "{}" {
		t.Fatalf("unexpected data: %q", env.Data)
	}
}

func TestAllTypeConstants(t *testing.T) {
	// F-19: 9 types + ProtoVersion must exist. This file references them all.
	types := map[string]string{
		"HELLO":         TypeHello,
		"WELCOME":       TypeWelcome,
		"PING":          TypePing,
		"PONG":          TypePong,
		"READY":         TypeReady,
		"JOB_ASSIGN":    TypeJobAssign,
		"STATUS_UPDATE": TypeStatusUpdate,
		"LOG":           TypeLog,
		"JOB_TEARDOWN":  TypeJobTeardown,
	}
	for want, got := range types {
		if got != want {
			t.Fatalf("type constant mismatch: %q != %q", got, want)
		}
	}
	if ProtoVersion != "v1" {
		t.Fatalf("ProtoVersion=%q want v1", ProtoVersion)
	}
}
