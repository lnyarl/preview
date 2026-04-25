package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// traefikFakeDocker 는 EnsureNetwork / EnsureTraefik 단위 테스트용 최소 mock.
type traefikFakeDocker struct {
	mu sync.Mutex

	networks   map[string]string            // name → driver
	containers map[string]ContainerInspectResult // name → inspect result

	networkCreateCalls []string
	createCalls        []CreateOptions
	startCalls         []string
	stopCalls          []string
	removeCalls        []string

	inspectErr    error // ContainerInspect 를 강제 에러 처리 (nil = "not found" 처리 안 함)
	networkErr    error // NetworkInspect 를 강제 에러 처리
	networkCrtErr error // NetworkCreate 를 강제 에러 처리
}

func newTraefikFakeDocker() *traefikFakeDocker {
	return &traefikFakeDocker{
		networks:   make(map[string]string),
		containers: make(map[string]ContainerInspectResult),
	}
}

// DockerClient interface compliance check.
var _ DockerClient = (*traefikFakeDocker)(nil)

func (f *traefikFakeDocker) ImageBuild(_ context.Context, _ string, _ BuildOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *traefikFakeDocker) ContainerList(_ context.Context, _ map[string]string) ([]ContainerSummary, error) {
	return nil, nil
}
func (f *traefikFakeDocker) Ping(_ context.Context) error { return nil }

func (f *traefikFakeDocker) NetworkInspect(_ context.Context, name string) (NetworkInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.networkErr != nil {
		return NetworkInspectResult{}, f.networkErr
	}
	if d, ok := f.networks[name]; ok {
		return NetworkInspectResult{ID: name, Driver: d}, nil
	}
	return NetworkInspectResult{}, fmt.Errorf("%w: network %s", ErrDockerNotFound, name)
}

func (f *traefikFakeDocker) NetworkCreate(_ context.Context, name string, opts NetworkCreateOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.networkCrtErr != nil {
		return f.networkCrtErr
	}
	f.networkCreateCalls = append(f.networkCreateCalls, name)
	f.networks[name] = opts.Driver
	return nil
}

func (f *traefikFakeDocker) ContainerInspect(_ context.Context, id string) (ContainerInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return ContainerInspectResult{}, f.inspectErr
	}
	if r, ok := f.containers[id]; ok {
		return r, nil
	}
	return ContainerInspectResult{}, fmt.Errorf("%w: container %s", ErrDockerNotFound, id)
}

func (f *traefikFakeDocker) ContainerCreate(_ context.Context, opts CreateOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, opts)
	f.containers[opts.Name] = ContainerInspectResult{Labels: opts.Labels}
	return "fake-" + opts.Name, nil
}

func (f *traefikFakeDocker) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, id)
	return nil
}

func (f *traefikFakeDocker) ContainerStop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, id)
	return nil
}

func (f *traefikFakeDocker) ContainerRemove(_ context.Context, id string, _ RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls = append(f.removeCalls, id)
	delete(f.containers, id)
	return nil
}

// --- EnsureNetwork tests ---

func TestEnsureNetworkCreatesWhenMissing(t *testing.T) {
	dc := newTraefikFakeDocker()
	if err := EnsureNetwork(context.Background(), dc, "preview-net"); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if len(dc.networkCreateCalls) != 1 || dc.networkCreateCalls[0] != "preview-net" {
		t.Fatalf("networkCreateCalls=%v", dc.networkCreateCalls)
	}
	if dc.networks["preview-net"] != "bridge" {
		t.Fatalf("driver=%q want bridge", dc.networks["preview-net"])
	}
}

func TestEnsureNetworkNoOpWhenExists(t *testing.T) {
	dc := newTraefikFakeDocker()
	dc.networks["preview-net"] = "bridge"
	if err := EnsureNetwork(context.Background(), dc, "preview-net"); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if len(dc.networkCreateCalls) != 0 {
		t.Fatal("should be no-op when bridge network already exists")
	}
}

func TestEnsureNetworkErrorOnWrongDriver(t *testing.T) {
	dc := newTraefikFakeDocker()
	dc.networks["preview-net"] = "overlay"
	if err := EnsureNetwork(context.Background(), dc, "preview-net"); err == nil {
		t.Fatal("expected error when network driver != bridge")
	}
}

// --- EnsureTraefik tests ---

func TestEnsureTraefikCreatesWhenMissing(t *testing.T) {
	dc := newTraefikFakeDocker()
	spec := TraefikSpec{Image: "traefik:v3.1", HostPort: 8080, Network: "preview-net", Container: "preview-traefik"}

	if err := EnsureTraefik(context.Background(), dc, spec); err != nil {
		t.Fatalf("EnsureTraefik: %v", err)
	}
	if len(dc.createCalls) != 1 {
		t.Fatalf("createCalls=%d want 1", len(dc.createCalls))
	}
	if len(dc.startCalls) != 1 {
		t.Fatalf("startCalls=%d want 1", len(dc.startCalls))
	}
	opts := dc.createCalls[0]
	if opts.Name != "preview-traefik" {
		t.Fatalf("name=%q", opts.Name)
	}
	if opts.Image != "traefik:v3.1" {
		t.Fatalf("image=%q", opts.Image)
	}
	if opts.Labels[traefikSpecLabel] == "" {
		t.Fatal("spec hash label missing")
	}
	if len(opts.Cmd) == 0 {
		t.Fatal("Cmd must not be empty")
	}
}

func TestEnsureTraefikNoOpWhenSpecMatches(t *testing.T) {
	dc := newTraefikFakeDocker()
	spec := TraefikSpec{Image: "traefik:v3.1", HostPort: 8080, Network: "preview-net", Container: "preview-traefik"}
	dc.containers["preview-traefik"] = ContainerInspectResult{
		Labels: map[string]string{traefikSpecLabel: specHash(spec)},
	}

	if err := EnsureTraefik(context.Background(), dc, spec); err != nil {
		t.Fatalf("EnsureTraefik: %v", err)
	}
	if len(dc.createCalls) != 0 {
		t.Fatal("should be no-op when spec hash matches")
	}
	if len(dc.stopCalls) != 0 {
		t.Fatal("should not stop when spec hash matches")
	}
}

func TestEnsureTraefikReplacesWhenSpecMismatch(t *testing.T) {
	dc := newTraefikFakeDocker()
	dc.containers["preview-traefik"] = ContainerInspectResult{
		Labels: map[string]string{traefikSpecLabel: "old-different-hash"},
	}
	spec := TraefikSpec{Image: "traefik:v3.1", HostPort: 8080, Network: "preview-net", Container: "preview-traefik"}

	if err := EnsureTraefik(context.Background(), dc, spec); err != nil {
		t.Fatalf("EnsureTraefik: %v", err)
	}
	if len(dc.stopCalls) != 1 {
		t.Fatalf("stopCalls=%d want 1", len(dc.stopCalls))
	}
	if len(dc.removeCalls) != 1 {
		t.Fatalf("removeCalls=%d want 1", len(dc.removeCalls))
	}
	if len(dc.createCalls) != 1 {
		t.Fatalf("createCalls=%d want 1 (recreate)", len(dc.createCalls))
	}
}

func TestEnsureTraefikLabelMissingTriggersReplace(t *testing.T) {
	dc := newTraefikFakeDocker()
	// Container exists but has no spec label.
	dc.containers["preview-traefik"] = ContainerInspectResult{
		Labels: map[string]string{},
	}
	spec := TraefikSpec{Image: "traefik:v3.1", HostPort: 8080, Network: "preview-net", Container: "preview-traefik"}

	if err := EnsureTraefik(context.Background(), dc, spec); err != nil {
		t.Fatalf("EnsureTraefik: %v", err)
	}
	if len(dc.stopCalls) != 1 {
		t.Fatal("container without spec label should be replaced")
	}
}

func TestSpecHashDeterministic(t *testing.T) {
	s := TraefikSpec{Image: "traefik:v3.1", HostPort: 8080, Network: "preview-net", Container: "c"}
	if h1, h2 := specHash(s), specHash(s); h1 != h2 {
		t.Fatal("specHash not deterministic")
	}
}

func TestSpecHashDifferentForDifferentSpec(t *testing.T) {
	s1 := TraefikSpec{Image: "traefik:v3.1", HostPort: 8080, Network: "preview-net", Container: "c"}
	s2 := TraefikSpec{Image: "traefik:v3.1", HostPort: 9090, Network: "preview-net", Container: "c"}
	if specHash(s1) == specHash(s2) {
		t.Fatal("specHash should differ for different HostPort")
	}
}
