// messages_roundtrip_test.go: Phase 3 §6 F-S2-2 / F-S3-9.
// 9개 메시지 타입 (HELLO, WELCOME, PING, PONG, READY, JOB_ASSIGN,
// STATUS_UPDATE, JOB_TEARDOWN, LOG) 의 Marshal → Unmarshal → DeepEqual.
package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	helloLabels := map[string]string{"env": "home"}
	jobLabels := map[string]string{"env": "home"}
	containerID := "container-123"
	host := "127.0.0.1"
	port := 30001
	errMsg := "boom"

	cases := []struct {
		name string
		typ  string
		data any
		want any
	}{
		{
			name: "HELLO",
			typ:  TypeHello,
			data: HelloData{
				Version:         "v1",
				Labels:          helloLabels,
				AdvertiseHost:   "127.0.0.1",
				RunningPreviews: []string{"p1", "p2"},
			},
			want: &HelloData{},
		},
		{
			name: "WELCOME",
			typ:  TypeWelcome,
			data: WelcomeData{Version: "v1", AgentID: "a1"},
			want: &WelcomeData{},
		},
		{
			name: "PING",
			typ:  TypePing,
			data: PingData{TS: 1700000000000},
			want: &PingData{},
		},
		{
			name: "PONG",
			typ:  TypePong,
			data: PongData{TS: 1700000000000},
			want: &PongData{},
		},
		{
			name: "READY",
			typ:  TypeReady,
			data: ReadyData{SlotID: "slot-A", Capacity: 1},
			want: &ReadyData{},
		},
		{
			name: "JOB_ASSIGN",
			typ:  TypeJobAssign,
			data: JobAssignData{
				PreviewID:    "p1",
				RepoFullName: "acme/web",
				RepoURL:      "git://example.com/r.git",
				CommitSHA:    "abc1234",
				Branch:       "main",
				Labels:       jobLabels,
			},
			want: &JobAssignData{},
		},
		{
			name: "STATUS_UPDATE",
			typ:  TypeStatusUpdate,
			data: StatusUpdateData{
				PreviewID:    "p1",
				Status:       "running",
				Message:      "ok",
				ContainerID:  &containerID,
				AgentHost:    &host,
				AgentPort:    &port,
				ErrorMessage: &errMsg,
			},
			want: &StatusUpdateData{},
		},
		{
			name: "JOB_TEARDOWN",
			typ:  TypeJobTeardown,
			data: JobTeardownData{PreviewID: "p1"},
			want: &JobTeardownData{},
		},
		{
			name: "LOG",
			typ:  TypeLog,
			data: LogData{
				PreviewID: "p1",
				Stream:    "stdout",
				Line:      "hello world",
				TS:        1700000000000,
			},
			want: &LogData{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := NewEnvelope(tc.typ, tc.data)
			if err != nil {
				t.Fatalf("NewEnvelope: %v", err)
			}
			b, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got Envelope
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Type != tc.typ {
				t.Fatalf("type=%q want %q", got.Type, tc.typ)
			}
			if err := got.Decode(tc.want); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			// 재마샬해서 원본과 비교 — ptr 비교 회피.
			origJSON, _ := json.Marshal(tc.data)
			gotJSON, _ := json.Marshal(reflect.ValueOf(tc.want).Elem().Interface())
			if string(origJSON) != string(gotJSON) {
				t.Fatalf("round-trip mismatch:\n  orig=%s\n  got =%s", origJSON, gotJSON)
			}
		})
	}
}
