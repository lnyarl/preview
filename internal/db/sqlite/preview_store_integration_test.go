// preview_store_integration_test.go: Phase 3 §6 F-S3-10.
// 시나리오: Upsert(신규) → Claim → UpdateStatus 연쇄 → ListPreviewEvents 가
// 정확한 이벤트 시퀀스를 created_at ASC, id ASC 순으로 반환하는지 검증.
package sqlitestore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lnyarl/preview/internal/store"
)

func TestPreviewStoreIntegration(t *testing.T) {
	ps := newTestPreviewStore(t)
	ctx := context.Background()

	agentID := "agent-X"
	seedAgent(t, ps, agentID)

	pid := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := store.Preview{
		ID:           pid,
		RepoFullName: "acme/web",
		PrNumber:     1,
		CommitSha:    "sha-1",
		Branch:       "feat/x",
		Labels:       map[string]string{"env": "test"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// 1. Upsert (신규) — event: NULL → queued.
	created, prev, err := ps.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !created || prev != nil {
		t.Fatalf("Upsert created=%v prev=%v want true,nil", created, prev)
	}

	// 2. Claim — event: queued → assigned.
	claimed, err := ps.Claim(ctx, []string{pid}, agentID, now.Add(1*time.Second))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.ID != pid || claimed.Status != "assigned" {
		t.Fatalf("Claim returned %+v", claimed)
	}

	// 3. UpdateStatus assigned → building.
	if err := ps.UpdateStatus(ctx, pid, "assigned", "building",
		"start build", now.Add(2*time.Second), store.PreviewFields{}); err != nil {
		t.Fatalf("UpdateStatus building: %v", err)
	}

	// 4. UpdateStatus building → running.
	host := "127.0.0.1"
	port := 30001
	cid := "container-abc"
	if err := ps.UpdateStatus(ctx, pid, "building", "running",
		"running", now.Add(3*time.Second), store.PreviewFields{
			ContainerID: &cid, AgentHost: &host, AgentPort: &port,
		}); err != nil {
		t.Fatalf("UpdateStatus running: %v", err)
	}

	// 5. UpdateStatus running → teardown.
	if err := ps.UpdateStatus(ctx, pid, "running", "teardown",
		"closed", now.Add(4*time.Second), store.PreviewFields{}); err != nil {
		t.Fatalf("UpdateStatus teardown: %v", err)
	}

	// 6. UpdateStatus teardown → done.
	if err := ps.UpdateStatus(ctx, pid, "teardown", "done",
		"cleaned", now.Add(5*time.Second), store.PreviewFields{}); err != nil {
		t.Fatalf("UpdateStatus done: %v", err)
	}

	// 검증: ListPreviewEvents (interface 메서드).
	events, err := ps.ListPreviewEvents(ctx, pid, 50, 0)
	if err != nil {
		t.Fatalf("ListPreviewEvents: %v", err)
	}
	wantTransitions := []struct {
		from string
		to   string
	}{
		{"", "queued"},     // FromStatus = nil
		{"queued", "assigned"},
		{"assigned", "building"},
		{"building", "running"},
		{"running", "teardown"},
		{"teardown", "done"},
	}
	if len(events) != len(wantTransitions) {
		t.Fatalf("events=%d want %d:\n%v", len(events), len(wantTransitions), events)
	}
	for i, want := range wantTransitions {
		got := events[i]
		gotFrom := ""
		if got.FromStatus != nil {
			gotFrom = *got.FromStatus
		}
		if gotFrom != want.from || got.ToStatus != want.to {
			t.Errorf("event[%d] = (%s→%s) want (%s→%s)",
				i, gotFrom, got.ToStatus, want.from, want.to)
		}
		if got.PreviewID != pid {
			t.Errorf("event[%d].PreviewID=%s want %s", i, got.PreviewID, pid)
		}
	}

	// limit/offset 동작 확인.
	first2, err := ps.ListPreviewEvents(ctx, pid, 2, 0)
	if err != nil {
		t.Fatalf("ListPreviewEvents limit=2: %v", err)
	}
	if len(first2) != 2 {
		t.Fatalf("limit=2 returned %d", len(first2))
	}
	page2, err := ps.ListPreviewEvents(ctx, pid, 2, 2)
	if err != nil {
		t.Fatalf("ListPreviewEvents offset=2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("offset=2 returned %d", len(page2))
	}
	if page2[0].ToStatus == first2[0].ToStatus {
		t.Errorf("page2 first event matches page1; pagination broken")
	}
}
