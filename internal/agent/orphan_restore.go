// 이 파일의 책임:
//   - Agent 시작 시 docker.ContainerList(filter label=hub-preview-id) 로
//     이전 인스턴스의 살아있는 컨테이너를 발견하고, jobs 맵에 복원한다.
//   - 복원된 previewID 슬라이스를 반환 — Client 가 HELLO.RunningPreviews 에 실어 송신.
//   - 결정 11 / §4-7-1: 컨테이너는 Agent 재기동에도 살아있고, Hub 가 HELLO 시점에
//     DB 와 비교해 lost/orphan 결정 (Phase 3 §5-3).
//
// 참고: docs/specs/phase-3-admin-ui-and-mvp.md §4-4, 결정 11.
package agent

import (
	"context"
	"log/slog"
)

// RestoreOrphans 는 docker.ContainerList(label=hub-preview-id) 로 발견된
// 컨테이너를 runner.jobs 맵에 복원하고 previewID 슬라이스를 반환한다.
// docker 가 nil 또는 ContainerList 실패 시 빈 슬라이스 + nil error.
func RestoreOrphans(ctx context.Context, docker DockerClient, runner *Runner, advertiseHost string, logger *slog.Logger) ([]string, error) {
	if docker == nil || runner == nil {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	containers, err := docker.ContainerList(ctx, map[string]string{
		"hub-preview-id": "", // empty value = "label key 존재" 매칭. SDK adapter 가 해석.
	})
	if err != nil {
		logger.Warn("orphan_restore_list_failed", "err", err.Error())
		return nil, err
	}
	out := make([]string, 0, len(containers))
	for _, c := range containers {
		pid, ok := c.Labels["hub-preview-id"]
		if !ok || pid == "" {
			continue
		}
		port := 0
		if insp, err := docker.ContainerInspect(ctx, c.ID); err == nil {
			port = insp.HostPort
		} else {
			logger.Warn("orphan_restore_inspect_failed",
				"container_id", c.ID, "preview_id", pid, "err", err.Error())
		}
		host := advertiseHost
		if host == "" {
			host = "127.0.0.1"
		}
		runner.RegisterRestoredJob(pid, c.ID, host, port)
		logger.Info("orphan_restore_recovered",
			"preview_id", pid, "container_id", c.ID, "host", host, "port", port)
		out = append(out, pid)
	}
	return out, nil
}
