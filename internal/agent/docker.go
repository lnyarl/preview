// 이 파일의 책임:
//   - DockerClient 인터페이스: SDK 의존을 격리해 단위 테스트에서 fake 주입 가능 (결정 6).
//   - Phase 2 범위: ImageBuild / ContainerCreate / ContainerStart / ContainerStop /
//     ContainerRemove / Ping. ContainerInspect 는 build 결과 ExposedPort 추출용.
//   - Phase 6 추가: NetworkInspect / NetworkCreate (traefik.go 사용).
//   - Phase 7 변경: CreateOptions 의 단일 HostPort/ExposedPort 두 필드를 제거하고
//     PortBindings []PortBinding 슬라이스로 일원화 (결정 2). Traefik 의 80+8080
//     2 포트 바인딩 등 multi-port 케이스를 지원.
//   - SDK 어댑터(NewSDKDockerClient) 는 cmd/agent 가 wire 한다 (NF-Depguard-2:
//     internal/agent 패키지는 docker/docker/client 직접 import 금지, cmd/agent 만 허용).
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1, 결정 6;
//       docs/specs/phase-7-traefik-readiness.md §4-1, 결정 2/13.
package agent

import (
	"context"
	"errors"
	"io"
)

// ErrDockerNotFound 는 NetworkInspect / ContainerInspect 에서 리소스가 없을 때 반환된다.
// SDK 어댑터가 Docker "not found" 에러를 이 sentinel 로 감싼다.
var ErrDockerNotFound = errors.New("docker: not found")

// BuildOptions 는 ImageBuild 호출에 필요한 최소 옵션.
type BuildOptions struct {
	Tag        string
	Dockerfile string // 기본 "Dockerfile"
}

// PortBinding 은 컨테이너 포트 → 호스트 포트 매핑 1 건 (Phase 7 결정 2/13).
//   - ContainerPort: 컨테이너 내부 포트 (1..65535).
//   - HostPort: 호스트 바인딩 포트. 0 이면 expose only(외부 미노출),
//     SDK 어댑터가 ExposedPorts 에만 등록하고 PortMap 에는 추가하지 않는다.
//   - Protocol: 빈 문자열이면 SDK 어댑터가 "tcp" 로 기본 처리 (결정 13).
type PortBinding struct {
	ContainerPort int
	HostPort      int
	Protocol      string
}

// CreateOptions 는 ContainerCreate 호출에 필요한 옵션.
//
// Phase 7: 단일 HostPort/ExposedPort 두 필드는 PortBindings 슬라이스로 통합되었다.
// PortBindings 가 비어있으면 SDK 어댑터는 nat.PortMap / ExposedPorts 모두 비운다.
type CreateOptions struct {
	Image        string
	Name         string            // Phase 6: 컨테이너 이름 (예: "preview-traefik")
	Labels       map[string]string
	PortBindings []PortBinding     // Phase 7: 다중 포트 바인딩 (결정 2)
	Env          []string
	Networks     []string          // Phase 6: 연결할 Docker 네트워크 이름 목록
	Volumes      []string          // Phase 6: bind mounts "host:container[:opts]"
	Cmd          []string          // Phase 6: 컨테이너 명령 인자 (entrypoint 오버라이드)
}

// RemoveOptions 는 ContainerRemove 옵션.
type RemoveOptions struct {
	Force bool
}

// ContainerSummary 는 ContainerList 결과의 하나의 row.
type ContainerSummary struct {
	ID     string
	Labels map[string]string
}

// ContainerInspectResult 는 ContainerInspect 결과의 좁은 dto.
type ContainerInspectResult struct {
	HostPort int
	Labels   map[string]string // Phase 6: spec hash 비교용 (traefik)
	Status   string            // Phase 6: "running", "exited", etc.
}

// NetworkInspectResult 는 NetworkInspect 결과의 좁은 dto.
type NetworkInspectResult struct {
	ID     string
	Driver string // "bridge", "overlay", ...
}

// NetworkCreateOptions 는 NetworkCreate 호출 옵션.
type NetworkCreateOptions struct {
	Driver     string // "bridge"
	Attachable bool   // --attachable
}

// DockerClient 는 Agent runner 가 사용하는 Docker SDK 의 좁은 인터페이스.
type DockerClient interface {
	// ImageBuild 는 src 디렉토리(tar 또는 디렉토리 경로)로 이미지를 빌드한다.
	// 반환 logs 는 호출자가 io.Copy 후 Close.
	ImageBuild(ctx context.Context, contextDir string, opts BuildOptions) (logs io.ReadCloser, err error)

	// ContainerCreate 는 컨테이너를 생성하고 ID 반환.
	ContainerCreate(ctx context.Context, opts CreateOptions) (containerID string, err error)

	// ContainerStart 는 컨테이너를 시작.
	ContainerStart(ctx context.Context, id string) error

	// ContainerStop 은 컨테이너를 정지 (timeout 후 SIGKILL).
	ContainerStop(ctx context.Context, id string) error

	// ContainerRemove 는 컨테이너를 제거.
	ContainerRemove(ctx context.Context, id string, opts RemoveOptions) error

	// Ping 은 daemon 연결 확인.
	Ping(ctx context.Context) error

	// ContainerList 는 라벨 필터 (key→value) 매칭 컨테이너 목록을 반환한다.
	ContainerList(ctx context.Context, filters map[string]string) ([]ContainerSummary, error)

	// ContainerInspect 는 컨테이너 메타를 반환한다.
	// 컨테이너가 없으면 errors.Is(err, ErrDockerNotFound).
	ContainerInspect(ctx context.Context, id string) (ContainerInspectResult, error)

	// NetworkInspect 는 네트워크 메타를 반환한다.
	// 네트워크가 없으면 errors.Is(err, ErrDockerNotFound).
	NetworkInspect(ctx context.Context, name string) (NetworkInspectResult, error)

	// NetworkCreate 는 Docker 네트워크를 생성한다.
	NetworkCreate(ctx context.Context, name string, opts NetworkCreateOptions) error
}
