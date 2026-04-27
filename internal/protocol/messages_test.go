package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	hello := HelloData{Version: ProtoVersion, AdvertiseHost: "1.2.3.4"}
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
	if decoded.Version != hello.Version || decoded.AdvertiseHost != "1.2.3.4" {
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

func TestJobAssignDataRoundTrip(t *testing.T) {
	src := JobAssignData{
		PreviewID:    "p1",
		RepoFullName: "acme/web",
		RepoURL:      "https://github.com/acme/web.git",
		CommitSHA:    "abc123",
		Branch:       "feature/x",
		Labels:       []string{"test"},
	}
	env, err := NewEnvelope(TypeJobAssign, src)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	b, _ := json.Marshal(env)
	var got Envelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var dec JobAssignData
	if err := got.Decode(&dec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.PreviewID != src.PreviewID || dec.RepoURL != src.RepoURL ||
		dec.CommitSHA != src.CommitSHA || len(dec.Labels) != 1 || dec.Labels[0] != "test" {
		t.Fatalf("unexpected: %+v", dec)
	}
}

// TestJobAssignDataBuildEnvRoundTrip (Phase 10 F-19, NF-Wire-2):
// BuildEnv 가 있을 때 wire round-trip + 빈 map 일 때 omitempty 로 키 생략.
func TestJobAssignDataBuildEnvRoundTrip(t *testing.T) {
	src := JobAssignData{
		PreviewID:    "p1",
		RepoFullName: "acme/web",
		RepoURL:      "https://github.com/acme/web.git",
		CommitSHA:    "abc",
		BuildEnv: map[string]string{
			"DATABASE_URL": "postgres://x",
			"API_KEY":      "k1",
		},
	}
	env, err := NewEnvelope(TypeJobAssign, src)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	b, _ := json.Marshal(env)
	var got Envelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var dec JobAssignData
	if err := got.Decode(&dec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dec.BuildEnv) != 2 || dec.BuildEnv["DATABASE_URL"] != "postgres://x" || dec.BuildEnv["API_KEY"] != "k1" {
		t.Fatalf("BuildEnv mismatch: %+v", dec.BuildEnv)
	}
}

// TestJobAssignDataBuildEnvOmitempty (NF-Wire-2): nil/빈 map 일 때 wire JSON 에 키 자체 생략.
func TestJobAssignDataBuildEnvOmitempty(t *testing.T) {
	for name, src := range map[string]JobAssignData{
		"nil":   {PreviewID: "p1", RepoFullName: "a/b", RepoURL: "u", CommitSHA: "s", BuildEnv: nil},
		"empty": {PreviewID: "p1", RepoFullName: "a/b", RepoURL: "u", CommitSHA: "s", BuildEnv: map[string]string{}},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := json.Marshal(src)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if bytes.Contains(b, []byte("build_env")) {
				t.Fatalf("build_env key should be omitted; got: %s", b)
			}
		})
	}
}

func TestStatusUpdateDataRoundTrip(t *testing.T) {
	host := "127.0.0.1"
	port := 8080
	cid := "abc"
	src := StatusUpdateData{
		PreviewID:   "p1",
		Status:      "running",
		ContainerID: &cid,
		AgentHost:   &host,
		AgentPort:   &port,
	}
	env, err := NewEnvelope(TypeStatusUpdate, src)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	b, _ := json.Marshal(env)
	var got Envelope
	_ = json.Unmarshal(b, &got)
	var dec StatusUpdateData
	if err := got.Decode(&dec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.AgentHost == nil || *dec.AgentHost != host || dec.AgentPort == nil || *dec.AgentPort != port {
		t.Fatalf("unexpected: %+v", dec)
	}
}

func TestReadyDataRoundTrip(t *testing.T) {
	src := ReadyData{SlotID: "s1", Capacity: 1}
	env, err := NewEnvelope(TypeReady, src)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	b, _ := json.Marshal(env)
	var got Envelope
	_ = json.Unmarshal(b, &got)
	var dec ReadyData
	if err := got.Decode(&dec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.SlotID != "s1" || dec.Capacity != 1 {
		t.Fatalf("unexpected: %+v", dec)
	}
}

func TestJobTeardownAndLogDataRoundTrip(t *testing.T) {
	td := JobTeardownData{PreviewID: "p1"}
	env, _ := NewEnvelope(TypeJobTeardown, td)
	b, _ := json.Marshal(env)
	var got Envelope
	_ = json.Unmarshal(b, &got)
	var dec JobTeardownData
	if err := got.Decode(&dec); err != nil {
		t.Fatalf("decode teardown: %v", err)
	}
	if dec.PreviewID != "p1" {
		t.Fatalf("teardown previewID=%s", dec.PreviewID)
	}
	lg := LogData{PreviewID: "p1", Stream: "stdout", Line: "hello", TS: 1234}
	env2, _ := NewEnvelope(TypeLog, lg)
	b2, _ := json.Marshal(env2)
	var got2 Envelope
	_ = json.Unmarshal(b2, &got2)
	var dec2 LogData
	if err := got2.Decode(&dec2); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if dec2.Line != "hello" || dec2.TS != 1234 {
		t.Fatalf("log dec=%+v", dec2)
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
