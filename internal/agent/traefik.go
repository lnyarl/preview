// 이 파일의 책임:
//   - EnsureNetwork: "preview-net" Docker bridge+attachable 네트워크를 idempotent 보장.
//   - EnsureTraefik: "preview-traefik" 컨테이너를 spec 해시 기반으로 idempotent 기동.
//     이미 일치하는 컨테이너가 있으면 no-op; 다르면 stop+rm 후 재생성.
//
// §4-3, 결정 4, 14, 16.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// traefikSpecLabel 은 Traefik 컨테이너에 저장되는 spec 해시 라벨 키.
const traefikSpecLabel = "preview.traefik.spec"

// TraefikSpec 은 Traefik 컨테이너 기동에 필요한 파라미터.
type TraefikSpec struct {
	Image     string // 예: "traefik:v3.1"
	HostPort  int    // 호스트 바인딩 포트 (default 8080)
	Network   string // "preview-net"
	Container string // "preview-traefik"
}

// specHash 는 spec 의 변경 감지에 사용할 sha256 기반 해시를 반환한다.
// image|hostPort|network 세 파라미터만 포함 — 나머지 cmd 인자는 여기서 파생되므로.
func specHash(s TraefikSpec) string {
	h := sha256.Sum256([]byte(s.Image + "|" + strconv.Itoa(s.HostPort) + "|" + s.Network))
	return hex.EncodeToString(h[:])
}

// EnsureNetwork 는 name 이름의 bridge+attachable Docker 네트워크를 idempotent 보장한다.
//
// - 이미 존재하고 driver=bridge 면 no-op.
// - 없으면 생성.
// - 존재하지만 driver≠bridge 면 에러 반환.
func EnsureNetwork(ctx context.Context, dc DockerClient, name string) error {
	result, err := dc.NetworkInspect(ctx, name)
	if err == nil {
		if result.Driver != "bridge" {
			return fmt.Errorf("ensure_network: %q exists but driver=%q, expected bridge",
				name, result.Driver)
		}
		return nil // 이미 있고 driver 일치 — no-op.
	}
	if !errors.Is(err, ErrDockerNotFound) {
		return fmt.Errorf("ensure_network: inspect: %w", err)
	}
	// 없으므로 생성.
	if err := dc.NetworkCreate(ctx, name, NetworkCreateOptions{
		Driver:     "bridge",
		Attachable: true,
	}); err != nil {
		return fmt.Errorf("ensure_network: create: %w", err)
	}
	return nil
}

// EnsureTraefik 은 spec 에 맞는 Traefik 컨테이너를 idempotent 보장한다.
//
// - 이미 존재하고 spec 해시가 일치하면 no-op.
// - 존재하지만 해시가 다르면(또는 라벨 미존재) stop+rm 후 재생성.
// - 없으면 생성+기동.
func EnsureTraefik(ctx context.Context, dc DockerClient, spec TraefikSpec) error {
	hash := specHash(spec)

	insp, err := dc.ContainerInspect(ctx, spec.Container)
	if err == nil {
		if insp.Labels[traefikSpecLabel] == hash {
			return nil // spec 일치 — no-op.
		}
		// spec 불일치: stop+rm 후 재생성.
		_ = dc.ContainerStop(ctx, spec.Container)
		if err := dc.ContainerRemove(ctx, spec.Container, RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("ensure_traefik: remove stale container: %w", err)
		}
	} else if !errors.Is(err, ErrDockerNotFound) {
		return fmt.Errorf("ensure_traefik: inspect: %w", err)
	}

	// 컨테이너 생성. Phase 7: PortBindings 슬라이스로 통합 (결정 2).
	id, err := dc.ContainerCreate(ctx, CreateOptions{
		Image:  spec.Image,
		Name:   spec.Container,
		Labels: map[string]string{traefikSpecLabel: hash},
		PortBindings: []PortBinding{
			{ContainerPort: 80, HostPort: spec.HostPort},
		},
		Networks: []string{spec.Network},
		Volumes:  []string{"/var/run/docker.sock:/var/run/docker.sock:ro"},
		Cmd: []string{
			"--providers.docker=true",
			"--providers.docker.exposedbydefault=false",
			"--providers.docker.network=" + spec.Network,
			"--entrypoints.web.address=:80",
			"--api.dashboard=false",
		},
	})
	if err != nil {
		return fmt.Errorf("ensure_traefik: create: %w", err)
	}
	if err := dc.ContainerStart(ctx, id); err != nil {
		return fmt.Errorf("ensure_traefik: start: %w", err)
	}
	return nil
}
