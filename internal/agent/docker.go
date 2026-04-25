// 이 파일의 책임:
//   - DockerClient 인터페이스: SDK 의존을 격리해 단위 테스트에서 fake 주입 가능 (결정 6).
//   - Phase 2 범위: ImageBuild / ContainerCreate / ContainerStart / ContainerStop /
//     ContainerRemove / Ping. ContainerInspect 는 build 결과 ExposedPort 추출용.
//   - SDK 어댑터(NewSDKDockerClient) 는 cmd/agent 가 wire 한다 (NF-Depguard-2:
//     internal/agent 패키지는 docker/docker/client 직접 import 금지, cmd/agent 만 허용).
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1, 결정 6.
package agent

import (
	"context"
	"io"
)

// BuildOptions 는 ImageBuild 호출에 필요한 최소 옵션.
type BuildOptions struct {
	Tag        string
	Dockerfile string // 기본 "Dockerfile"
}

// CreateOptions 는 ContainerCreate 호출에 필요한 옵션.
// PortBindings 는 host_port → container_port 매핑.
type CreateOptions struct {
	Image       string
	Labels      map[string]string
	HostPort    int // 외부 노출 포트
	ExposedPort int // 컨테이너 내부 포트
	Env         []string
}

// RemoveOptions 는 ContainerRemove 옵션.
type RemoveOptions struct {
	Force bool
}

// ContainerSummary 는 ContainerList 결과의 하나의 row.
// 라벨만 사용하므로 좁은 표면.
type ContainerSummary struct {
	ID     string
	Labels map[string]string
}

// ContainerInspectResult 는 ContainerInspect 결과의 좁은 dto.
// HostPort 는 컨테이너의 ExposedPort=80 (Phase 2 단일 포트 가정) 에 바인딩된 host port.
type ContainerInspectResult struct {
	HostPort int
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
	// Phase 3 Agent 재기동 시 orphan 복원 (결정 11 / §4-7-1).
	ContainerList(ctx context.Context, filters map[string]string) ([]ContainerSummary, error)

	// ContainerInspect 는 컨테이너 메타를 반환한다 — 본 Phase 는 HostPort 만 노출.
	ContainerInspect(ctx context.Context, id string) (ContainerInspectResult, error)
}
