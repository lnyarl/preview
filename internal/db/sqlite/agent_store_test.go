package sqlitestore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lnyarl/preview/internal/store"
)

func newTestStore(t *testing.T) *AgentStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	dsn := "sqlite://" + path
	if _, err := MigrateUp(dsn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	ctx := context.Background()
	db, err := OpenURL(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenURL: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return NewAgentStore(db)
}

func TestAgentStoreCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	agent := store.Agent{
		ID:        "00000000-0000-0000-0000-000000000001",
		Name:      "agent-1",
		TokenHash: "$2a$04$dummyhash",
		Labels:    map[string]string{"env": "local"},
		Status:    "offline",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, agent); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// duplicate name must fail.
	err := s.Create(ctx, agent)
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("Create duplicate: err=%v want ErrDuplicate", err)
	}

	got, err := s.GetByName(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.ID != agent.ID {
		t.Fatalf("GetByName ID=%q want %q", got.ID, agent.ID)
	}
	if got.Labels["env"] != "local" {
		t.Fatalf("labels not preserved: %+v", got.Labels)
	}

	byID, err := s.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.Name != "agent-1" {
		t.Fatalf("GetByID: Name=%q", byID.Name)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len=%d want 1", len(list))
	}

	// UpdateStatus online.
	now := time.Now().UTC()
	if err := s.UpdateStatus(ctx, agent.ID, "online", now); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	after, err := s.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if after.Status != "online" {
		t.Fatalf("status=%q want online", after.Status)
	}
	if after.LastSeenAt == nil {
		t.Fatalf("LastSeenAt nil after update")
	}

	// UpdateStatus on nonexistent ID.
	err = s.UpdateStatus(ctx, "no-such-id", "offline", now)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateStatus missing: err=%v want ErrNotFound", err)
	}

	// GetByID / GetByName missing.
	if _, err := s.GetByID(ctx, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetByID missing: %v", err)
	}
	if _, err := s.GetByName(ctx, "no-such-name"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetByName missing: %v", err)
	}

	// Delete.
	if err := s.Delete(ctx, agent.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, agent.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestResetAllOnline(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// insert three agents, two online, one offline.
	for i, st := range []string{"online", "online", "offline"} {
		a := store.Agent{
			ID:        "id-" + string(rune('a'+i)),
			Name:      "n-" + string(rune('a'+i)),
			TokenHash: "$2a$04$h",
			Status:    st,
			CreatedAt: now,
		}
		if err := s.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	n, err := s.ResetAllOnline(ctx)
	if err != nil {
		t.Fatalf("ResetAllOnline: %v", err)
	}
	if n != 2 {
		t.Fatalf("reset count=%d want 2", n)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, a := range list {
		if a.Status != "offline" {
			t.Fatalf("agent %s status=%s want offline", a.Name, a.Status)
		}
	}
}

// TestAgentBuildConfig — Phase 4 F-4 / F-5 / F-6 / F-7.
func TestAgentBuildConfig(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 신규 Agent 등록 (run_commands NULL, container_port NULL).
	a := store.Agent{
		ID:        "agent-bc-1",
		Name:      "agent-bc",
		TokenHash: "$2a$04$h",
		Status:    "offline",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// F-6: NULL → ([], 0).
	cmds, port, err := s.GetBuildConfig(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetBuildConfig fresh: %v", err)
	}
	if len(cmds) != 0 {
		t.Fatalf("fresh cmds=%v want []", cmds)
	}
	if port != 0 {
		t.Fatalf("fresh port=%d want 0", port)
	}

	// F-4: 저장 → 검색 round-trip.
	rawSave := "npm ci\nnpm run build\n./deploy.sh"
	if err := s.SaveBuildConfig(ctx, a.ID, rawSave, 3000); err != nil {
		t.Fatalf("SaveBuildConfig: %v", err)
	}
	cmds, port, err = s.GetBuildConfig(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetBuildConfig saved: %v", err)
	}
	if len(cmds) != 3 {
		t.Fatalf("cmds len=%d want 3, got %v", len(cmds), cmds)
	}
	if cmds[0] != "npm ci" || cmds[1] != "npm run build" || cmds[2] != "./deploy.sh" {
		t.Fatalf("cmds=%v", cmds)
	}
	if port != 3000 {
		t.Fatalf("port=%d want 3000", port)
	}

	// F-7: multi-line 정규화 — 빈 줄 제거.
	if err := s.SaveBuildConfig(ctx, a.ID, "a\n\nb\n", 0); err != nil {
		t.Fatalf("SaveBuildConfig multi: %v", err)
	}
	cmds, port, err = s.GetBuildConfig(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetBuildConfig multi: %v", err)
	}
	if len(cmds) != 2 || cmds[0] != "a" || cmds[1] != "b" {
		t.Fatalf("normalized cmds=%v want [a b]", cmds)
	}
	if port != 0 {
		t.Fatalf("port=%d want 0 (NULL)", port)
	}

	// F-5: 빈 문자열 + 0 → NULL/NULL → ([], 0).
	if err := s.SaveBuildConfig(ctx, a.ID, "", 0); err != nil {
		t.Fatalf("SaveBuildConfig reset: %v", err)
	}
	cmds, port, err = s.GetBuildConfig(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetBuildConfig after reset: %v", err)
	}
	if len(cmds) != 0 || port != 0 {
		t.Fatalf("after reset cmds=%v port=%d want [] 0", cmds, port)
	}
}

func TestOpenURLPostgresUnsupported(t *testing.T) {
	_, err := OpenURL(context.Background(), "postgres://user:pass@host/db")
	if err == nil {
		t.Fatal("expected error for postgres URL")
	}
}

func TestOpenURLUnknownScheme(t *testing.T) {
	_, err := OpenURL(context.Background(), "mysql://x")
	if err == nil {
		t.Fatal("expected error for unknown scheme")
	}
}
