// 이 파일의 책임:
//   - github.com/docker/docker/client SDK 어댑터를 internal/agent.DockerClient
//     인터페이스에 매핑한다 (NF-Depguard-2: SDK 직접 import 는 cmd/agent 만 허용).
//   - Phase 2 범위: ImageBuild, ContainerCreate, ContainerStart, ContainerStop,
//     ContainerRemove, Ping, ContainerList, ContainerInspect.
//   - Phase 6 추가: NetworkInspect, NetworkCreate + CreateOptions 확장 필드 처리.
//   - Phase 7 변경: ContainerCreate 가 CreateOptions.PortBindings 슬라이스를
//     순회해 nat.PortMap / ExposedPorts 구성. HostPort==0 은 ExposedPorts 만 등록
//     (expose only). HostIP 분기: ContainerPort==80 → 0.0.0.0(웹 트래픽 외부 노출),
//     그 외 → 127.0.0.1(loopback, Traefik API 등 보안 민감 포트).
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1, 결정 6;
//       docs/specs/phase-7-traefik-readiness.md §4-1, 결정 7/13.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/go-connections/nat"

	"github.com/lnyarl/preview/internal/agent"
)

// sdkDockerClient 는 docker/docker/client.Client 위 wrapper.
type sdkDockerClient struct {
	cli *client.Client
}

// newSDKDockerClient 는 환경변수 기반 docker daemon 연결 클라이언트를 만든다.
func newSDKDockerClient() (agent.DockerClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &sdkDockerClient{cli: cli}, nil
}

func (s *sdkDockerClient) Ping(ctx context.Context) error {
	_, err := s.cli.Ping(ctx)
	return err
}

// ImageBuild 는 contextDir 를 tar 로 묶어 build 컨텍스트로 전달한다.
func (s *sdkDockerClient) ImageBuild(ctx context.Context, contextDir string, opts agent.BuildOptions) (io.ReadCloser, error) {
	tarBuf, err := tarDir(contextDir)
	if err != nil {
		return nil, fmt.Errorf("tar context: %w", err)
	}
	dockerfile := opts.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	resp, err := s.cli.ImageBuild(ctx, tarBuf, dockertypesImageBuildOptions(opts.Tag, dockerfile))
	if err != nil {
		return nil, err
	}
	// build stream 검증: jsonmessage 디코딩 후 에러 확인.
	if err := drainBuildStream(resp.Body); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	// 호출자 측에서 close 하도록 일단 새 ReadCloser 로 nil 리턴.
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (s *sdkDockerClient) ContainerCreate(ctx context.Context, opts agent.CreateOptions) (string, error) {
	// Phase 7: PortBindings 슬라이스 순회 (결정 2/7/13).
	hostConf := &container.HostConfig{
		PortBindings: nat.PortMap{},
		Binds:        opts.Volumes, // Phase 6: bind mounts
	}
	exposed := nat.PortSet{}
	for _, pb := range opts.PortBindings {
		proto := pb.Protocol
		if proto == "" {
			proto = "tcp" // 결정 13: zero value 보존, 어댑터에서 기본 처리.
		}
		key := nat.Port(strconv.Itoa(pb.ContainerPort) + "/" + proto)
		exposed[key] = struct{}{}
		if pb.HostPort > 0 {
			// 결정 7: 80 만 외부 노출(0.0.0.0), 그 외(예: Traefik API 8080)는 loopback.
			bindIP := "127.0.0.1"
			if pb.ContainerPort == 80 {
				bindIP = "0.0.0.0"
			}
			hostConf.PortBindings[key] = []nat.PortBinding{{
				HostIP: bindIP, HostPort: strconv.Itoa(pb.HostPort),
			}}
		}
	}
	cfg := &container.Config{
		Image:        opts.Image,
		Labels:       opts.Labels,
		Env:          opts.Env,
		Cmd:          opts.Cmd, // Phase 6: command args
		ExposedPorts: exposed,
	}
	// Phase 6: connect to specified networks at creation time (first one via NetworkingConfig).
	netCfg := &network.NetworkingConfig{}
	if len(opts.Networks) > 0 {
		netCfg.EndpointsConfig = map[string]*network.EndpointSettings{
			opts.Networks[0]: {},
		}
	}
	resp, err := s.cli.ContainerCreate(ctx, cfg, hostConf, netCfg, nil, opts.Name)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (s *sdkDockerClient) ContainerStart(ctx context.Context, id string) error {
	return s.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (s *sdkDockerClient) ContainerStop(ctx context.Context, id string) error {
	timeout := 30
	return s.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (s *sdkDockerClient) ContainerRemove(ctx context.Context, id string, opts agent.RemoveOptions) error {
	return s.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: opts.Force})
}

// ContainerList 는 라벨 필터로 컨테이너를 조회한다.
// filters map 의 key 가 라벨 키, value 가 빈 문자열이면 "라벨 키 존재" 매칭,
// non-empty 면 "key=value" 정확 매칭.
func (s *sdkDockerClient) ContainerList(ctx context.Context, filters map[string]string) ([]agent.ContainerSummary, error) {
	args := dockerfilters.NewArgs()
	for k, v := range filters {
		if v == "" {
			args.Add("label", k)
		} else {
			args.Add("label", k+"="+v)
		}
	}
	list, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, err
	}
	out := make([]agent.ContainerSummary, 0, len(list))
	for _, c := range list {
		out = append(out, agent.ContainerSummary{ID: c.ID, Labels: c.Labels})
	}
	return out, nil
}

// ContainerInspect 는 컨테이너의 메타를 조회한다.
// 컨테이너가 없으면 agent.ErrDockerNotFound 를 wrapping 해 반환.
func (s *sdkDockerClient) ContainerInspect(ctx context.Context, id string) (agent.ContainerInspectResult, error) {
	insp, err := s.cli.ContainerInspect(ctx, id)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return agent.ContainerInspectResult{}, fmt.Errorf("%w: container %s", agent.ErrDockerNotFound, id)
		}
		return agent.ContainerInspectResult{}, err
	}
	res := agent.ContainerInspectResult{}
	if insp.Config != nil {
		res.Labels = insp.Config.Labels
	}
	if insp.State != nil {
		res.Status = insp.State.Status
	}
	if insp.NetworkSettings != nil {
		for portKey, bindings := range insp.NetworkSettings.Ports {
			if portKey == "80/tcp" || string(portKey) == "80/tcp" {
				if len(bindings) > 0 {
					if n, perr := strconv.Atoi(bindings[0].HostPort); perr == nil {
						res.HostPort = n
						return res, nil
					}
				}
			}
		}
		for _, bindings := range insp.NetworkSettings.Ports {
			if len(bindings) > 0 {
				if n, perr := strconv.Atoi(bindings[0].HostPort); perr == nil {
					res.HostPort = n
					return res, nil
				}
			}
		}
	}
	return res, nil
}

// NetworkInspect 는 네트워크 메타를 조회한다. 없으면 agent.ErrDockerNotFound 를 반환.
func (s *sdkDockerClient) NetworkInspect(ctx context.Context, name string) (agent.NetworkInspectResult, error) {
	nr, err := s.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return agent.NetworkInspectResult{}, fmt.Errorf("%w: network %s", agent.ErrDockerNotFound, name)
		}
		return agent.NetworkInspectResult{}, err
	}
	return agent.NetworkInspectResult{ID: nr.ID, Driver: nr.Driver}, nil
}

// NetworkCreate 는 Docker 네트워크를 생성한다.
func (s *sdkDockerClient) NetworkCreate(ctx context.Context, name string, opts agent.NetworkCreateOptions) error {
	_, err := s.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver:     opts.Driver,
		Attachable: opts.Attachable,
	})
	return err
}

// dockertypesImageBuildOptions 는 SDK 의 build option 객체를 만든다 (별도 함수로 격리).
func dockertypesImageBuildOptions(tag, dockerfile string) imageBuildOpts {
	return imageBuildOpts{
		Tags:        []string{tag},
		Dockerfile:  dockerfile,
		Remove:      true,
		ForceRemove: true,
	}
}

// imageBuildOpts 는 client.ImageBuild 호출용 type alias.
// SDK 가 types.ImageBuildOptions 위치를 build 패키지로 이전했으므로 재선언.
type imageBuildOpts = build.ImageBuildOptions

// tarDir 는 디렉토리를 tar bytes 로 묶는다.
func tarDir(root string) (*bytes.Buffer, error) {
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	defer tw.Close()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, f)
			_ = f.Close()
			if copyErr != nil {
				return copyErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// drainBuildStream 은 build response 의 jsonmessage stream 을 디코드해 에러를 검출한다.
func drainBuildStream(rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()
	// stream 안의 ErrorDetail 을 검사한다 (terminal=0=non-tty, isTerm=false).
	return jsonmessage.DisplayJSONMessagesStream(rc, io.Discard, 0, false, nil)
}

// 컴파일 시간 인터페이스 만족 확인.
var _ agent.DockerClient = (*sdkDockerClient)(nil)

// time 사용 — 빈 import 방지.
var _ = time.Second
