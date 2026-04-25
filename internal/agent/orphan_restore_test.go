// orphan_restore_test.go: Phase 3 §6 F-S2-7.
// fakeDocker 에 라벨 매칭 컨테이너 사전 등록 → RestoreOrphans 호출 →
// runner.jobs 맵 + RunningPreviewIDs 슬라이스에 복원 검증.
package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
)

// orphanFakeDocker 는 ContainerList/ContainerInspect 만 동작하는 mock.
type orphanFakeDocker struct {
	mu       sync.Mutex
	listResp []ContainerSummary
	listErr  error
	inspect  map[string]ContainerInspectResult
}

func (f *orphanFakeDocker) ImageBuild(_ context.Context, _ string, _ BuildOptions) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}
func (f *orphanFakeDocker) ContainerCreate(_ context.Context, _ CreateOptions) (string, error) {
	return "", errors.New("not used")
}
func (f *orphanFakeDocker) ContainerStart(_ context.Context, _ string) error {
	return errors.New("not used")
}
func (f *orphanFakeDocker) ContainerStop(_ context.Context, _ string) error {
	return errors.New("not used")
}
func (f *orphanFakeDocker) ContainerRemove(_ context.Context, _ string, _ RemoveOptions) error {
	return errors.New("not used")
}
func (f *orphanFakeDocker) Ping(_ context.Context) error { return nil }
func (f *orphanFakeDocker) ContainerList(_ context.Context, _ map[string]string) ([]ContainerSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]ContainerSummary, len(f.listResp))
	copy(out, f.listResp)
	return out, nil
}
func (f *orphanFakeDocker) ContainerInspect(_ context.Context, id string) (ContainerInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.inspect[id]; ok {
		return r, nil
	}
	return ContainerInspectResult{}, nil
}
func (f *orphanFakeDocker) NetworkInspect(_ context.Context, _ string) (NetworkInspectResult, error) {
	return NetworkInspectResult{}, ErrDockerNotFound
}
func (f *orphanFakeDocker) NetworkCreate(_ context.Context, _ string, _ NetworkCreateOptions) error {
	return nil
}

func TestOrphanRestore(t *testing.T) {
	docker := &orphanFakeDocker{
		listResp: []ContainerSummary{
			{ID: "c1", Labels: map[string]string{"hub-preview-id": "p1"}},
			{ID: "c2", Labels: map[string]string{"hub-preview-id": "p2"}},
		},
		inspect: map[string]ContainerInspectResult{
			"c1": {HostPort: 30001},
			"c2": {HostPort: 30002},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRunner(docker, nil, nil, "127.0.0.1", logger)

	ids, err := RestoreOrphans(context.Background(), docker, r, "127.0.0.1", logger)
	if err != nil {
		t.Fatalf("RestoreOrphans: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("returned ids=%v want 2", ids)
	}
	sort.Strings(ids)
	if ids[0] != "p1" || ids[1] != "p2" {
		t.Errorf("ids=%v want [p1 p2]", ids)
	}

	got := r.RunningPreviewIDs()
	sort.Strings(got)
	if len(got) != 2 || got[0] != "p1" || got[1] != "p2" {
		t.Errorf("RunningPreviewIDs=%v want [p1 p2]", got)
	}
}

func TestOrphanRestoreNoContainers(t *testing.T) {
	docker := &orphanFakeDocker{listResp: nil}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRunner(docker, nil, nil, "127.0.0.1", logger)

	ids, err := RestoreOrphans(context.Background(), docker, r, "127.0.0.1", logger)
	if err != nil {
		t.Fatalf("RestoreOrphans: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids=%v want 0", ids)
	}
}

func TestOrphanRestoreNilDocker(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRunner(nil, nil, nil, "127.0.0.1", logger)
	ids, err := RestoreOrphans(context.Background(), nil, r, "127.0.0.1", logger)
	if err != nil {
		t.Errorf("RestoreOrphans(nil docker) err=%v want nil", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids=%v want 0", ids)
	}
}
