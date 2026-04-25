// build_config_test.go: Phase 4 F-24 / F-26 / F-27 / NF-8 — Holder.Replace/Snapshot
// 동작 + race-safe 검증.
package agent

import (
	"sync"
	"testing"

	"github.com/lnyarl/preview/internal/protocol"
)

// TestHolderEmptyDefault — F-24: 초기 상태는 빈 Config (= 기본값으로 동작).
func TestHolderEmptyDefault(t *testing.T) {
	h := NewHolder()
	snap := h.Snapshot()
	if len(snap.BuildCommands) != 0 {
		t.Fatalf("BuildCommands=%v want []", snap.BuildCommands)
	}
	if snap.ContainerPort != 0 {
		t.Fatalf("ContainerPort=%d want 0", snap.ContainerPort)
	}
}

// TestHolderReplaceSnapshotRoundTrip — F-26: Replace 후 Snapshot 이 같은 값을 반환.
func TestHolderReplaceSnapshotRoundTrip(t *testing.T) {
	h := NewHolder()
	want := protocol.AgentConfigData{
		BuildCommands: []string{"npm ci", "npm run build"},
		ContainerPort: 3000,
	}
	h.Replace(want)
	got := h.Snapshot()
	if len(got.BuildCommands) != 2 || got.BuildCommands[0] != "npm ci" || got.BuildCommands[1] != "npm run build" {
		t.Fatalf("BuildCommands=%v", got.BuildCommands)
	}
	if got.ContainerPort != 3000 {
		t.Fatalf("ContainerPort=%d", got.ContainerPort)
	}
}

// TestHolderDeepCopyOnReplace — F-27: Replace 는 슬라이스 deep copy 한다.
// 외부에서 원본 슬라이스를 mutate 해도 Holder 내부에는 영향 없다.
func TestHolderDeepCopyOnReplace(t *testing.T) {
	h := NewHolder()
	external := []string{"a", "b"}
	h.Replace(protocol.AgentConfigData{BuildCommands: external})

	// 외부 슬라이스 mutate.
	external[0] = "MUTATED"

	snap := h.Snapshot()
	if snap.BuildCommands[0] != "a" {
		t.Fatalf("Holder leaked external slice: got %q want %q", snap.BuildCommands[0], "a")
	}
}

// TestHolderDeepCopyOnSnapshot — Snapshot 은 deep copy 한다.
// Snapshot 결과를 mutate 해도 Holder 내부 또는 다음 Snapshot 에 영향 없다.
func TestHolderDeepCopyOnSnapshot(t *testing.T) {
	h := NewHolder()
	h.Replace(protocol.AgentConfigData{BuildCommands: []string{"a", "b"}})
	s1 := h.Snapshot()
	s1.BuildCommands[0] = "MUTATED"
	s2 := h.Snapshot()
	if s2.BuildCommands[0] != "a" {
		t.Fatalf("snapshot mutation leaked: got %q", s2.BuildCommands[0])
	}
}

// TestHolderConcurrentReplaceSnapshot — NF-8 / F-28: Race detector 통과 검증.
// `go test -race` 로 실행 시 데이터 레이스가 없어야 한다.
func TestHolderConcurrentReplaceSnapshot(t *testing.T) {
	h := NewHolder()
	const N = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			h.Replace(protocol.AgentConfigData{
				BuildCommands: []string{"cmd-a", "cmd-b"},
				ContainerPort: i + 1,
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			snap := h.Snapshot()
			// 일관성 확인: 슬라이스가 어느 시점의 본이든 길이는 0 또는 2.
			if len(snap.BuildCommands) != 0 && len(snap.BuildCommands) != 2 {
				t.Errorf("inconsistent snapshot len=%d", len(snap.BuildCommands))
				return
			}
		}
	}()
	wg.Wait()
}
