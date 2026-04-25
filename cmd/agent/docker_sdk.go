// 이 파일의 책임:
//   - github.com/docker/docker/client SDK 어댑터를 internal/agent.DockerClient
//     인터페이스에 매핑한다 (NF-Depguard-2: SDK 직접 import 는 cmd/agent 만 허용).
//   - Phase 2 범위의 6 메서드만 구현 — ImageBuild, ContainerCreate, ContainerStart,
//     ContainerStop, ContainerRemove, Ping.
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §5-1, 결정 6.
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

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
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
	exposedKey := nat.Port(strconv.Itoa(opts.ExposedPort) + "/tcp")
	hostConf := &container.HostConfig{
		PortBindings: nat.PortMap{
			exposedKey: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(opts.HostPort)}},
		},
	}
	cfg := &container.Config{
		Image:  opts.Image,
		Labels: opts.Labels,
		Env:    opts.Env,
		ExposedPorts: nat.PortSet{
			exposedKey: struct{}{},
		},
	}
	resp, err := s.cli.ContainerCreate(ctx, cfg, hostConf, &network.NetworkingConfig{}, nil, "")
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
