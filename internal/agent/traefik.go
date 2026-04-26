// 이 파일의 책임:
//   - EnsureNetwork: "preview-net" Docker bridge+attachable 네트워크를 idempotent 보장.
//   - EnsureTraefik: "preview-traefik" 컨테이너를 spec 해시 기반으로 idempotent 기동.
//     이미 일치하는 컨테이너가 있으면 no-op; 다르면 stop+rm 후 재생성.
//   - Phase 7: TraefikSpec.APIHostPort 추가. > 0 일 때 cmd 에 --api.insecure=true
//     + --entrypoints.traefik.address=:8080 를 더하고 8080 포트 바인딩(loopback).
//     specHash 에 APIHostPort 포함(결정 5). HostPort==APIHostPort 충돌 방어(S5).
//
// 참고: phase-6 §4-3, 결정 4/14/16; phase-7 §4-2, 결정 5/7/12, S4/S5.
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

// ErrTraefikPortConflict 는 spec.HostPort == spec.APIHostPort > 0 인 경우 (Phase 7 S5).
// ParseConfig 가 1차 방어선이고, EnsureTraefik 는 wiring 우회 호출자에 대한 2차 방어선.
var ErrTraefikPortConflict = errors.New("traefik: HostPort and APIHostPort must differ")

// TraefikSpec 은 Traefik 컨테이너 기동에 필요한 파라미터.
type TraefikSpec struct {
	Image       string // 예: "traefik:v3.1"
	HostPort    int    // 웹 트래픽 호스트 바인딩 포트 (default 8080).
	APIHostPort int    // Phase 7: Traefik API 호스트 바인딩 포트 (default 9080, 0 = 비활성).
	Network     string // "preview-net"
	Container   string // "preview-traefik"
}

// specHash 는 spec 의 변경 감지에 사용할 sha256 기반 해시를 반환한다.
// Phase 7: APIHostPort 포함 — API 포트 변경 시 컨테이너 재생성 트리거 (결정 5).
func specHash(s TraefikSpec) string {
	h := sha256.Sum256([]byte(
		s.Image + "|" +
			strconv.Itoa(s.HostPort) + "|" +
			s.Network + "|" +
			strconv.Itoa(s.APIHostPort),
	))
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
//
// Phase 7: spec.APIHostPort > 0 일 때 cmd 에 --api.insecure=true +
// --entrypoints.traefik.address=:8080 추가, 8080 컨테이너 포트를 호스트로 바인딩.
// HostPort 와 APIHostPort 가 같으면 ErrTraefikPortConflict (S5 2 차 방어선).
//
// 전제: spec.HostPort > 0. ParseConfig 가 --traefik-port 1..65535 를 강제하므로
// 본 함수는 추가 방어 없이 가정 (테스트나 외부 호출자에서 0 주입 시 docker 가
// nat.PortBinding 단계에서 에러).
func EnsureTraefik(ctx context.Context, dc DockerClient, spec TraefikSpec) error {
	// (S5) 동일 포트 방어: ParseConfig 우회 wiring(테스트, library 사용)에 대한 2차 방어.
	if spec.APIHostPort > 0 && spec.HostPort == spec.APIHostPort {
		return ErrTraefikPortConflict
	}

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

	// Phase 7: 기본 cmd + 조건부 API 활성화 (결정 12, S4).
	cmd := []string{
		"--providers.docker=true",
		"--providers.docker.exposedbydefault=false",
		"--providers.docker.network=" + spec.Network,
		"--entrypoints.web.address=:80",
		"--api.dashboard=false",
	}
	pbs := []PortBinding{
		{ContainerPort: 80, HostPort: spec.HostPort}, // 0.0.0.0 (SDK adapter 분기).
	}
	if spec.APIHostPort > 0 {
		cmd = append(cmd,
			"--api.insecure=true",
			// (S4) Traefik default(:8080) 가 향후 버전에서 변경될 위험 차단 — redundant 라도 명시.
			"--entrypoints.traefik.address=:8080",
		)
		pbs = append(pbs, PortBinding{
			ContainerPort: 8080, HostPort: spec.APIHostPort,
		}) // 127.0.0.1 (SDK adapter 분기, ContainerPort != 80).
	}

	id, err := dc.ContainerCreate(ctx, CreateOptions{
		Image:        spec.Image,
		Name:         spec.Container,
		Labels:       map[string]string{traefikSpecLabel: hash},
		PortBindings: pbs,
		Networks:     []string{spec.Network},
		Volumes:      []string{"/var/run/docker.sock:/var/run/docker.sock:ro"},
		Cmd:          cmd,
	})
	if err != nil {
		return fmt.Errorf("ensure_traefik: create: %w", err)
	}
	if err := dc.ContainerStart(ctx, id); err != nil {
		return fmt.Errorf("ensure_traefik: start: %w", err)
	}
	return nil
}
