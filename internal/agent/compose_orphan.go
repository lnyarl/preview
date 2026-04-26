// 이 파일의 책임:
//   - `docker compose ls --format json` 출력으로 실행 중 compose 프로젝트를 발견하고,
//     이름이 "preview-" 접두사이면서 runner.jobs 맵에 없는 (orphan) 프로젝트를
//     `docker compose --project-name {name} down` 으로 정리한다.
//   - 호출자(cmd/agent/main.go)는 best-effort 로 사용 — 실패 시 WARN + 계속.
//
// 참고: docs/specs/phase-8-orphan-cleanup.md §4-1, 결정 1/3/6/7/8.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

const composeProjectPrefix = "preview-"

// composeProject 는 `docker compose ls --format json` 응답의 1 항목.
// 본 Phase 로직은 Name 만 사용. Status/ConfigFiles 는 정의만 하고 미참조 —
// 향후 정책 확장 시(예: exited 프로젝트만 정리) 즉시 사용 가능 (결정 10).
type composeProject struct {
	Name        string `json:"Name"`
	Status      string `json:"Status"`
	ConfigFiles string `json:"ConfigFiles"`
}

// PruneComposeOrphans 는 실행 중 compose 프로젝트 중 runner.jobs 맵에 없는
// preview-* 프로젝트를 down 한다.
//
//   - 반환: 정리된 프로젝트 수, 첫 번째 에러(`compose ls` 실패 또는 down 1 건 실패).
//   - `compose ls` 실패 시 (0, err) 반환 — down 호출 0.
//   - down 1 건 실패해도 나머지 프로젝트는 계속 처리, 첫 에러를 보관.
//   - cmd == nil 또는 runner == nil 이면 (0, nil) — defensive (production 미진입).
func PruneComposeOrphans(ctx context.Context, cmd CmdRunner, runner *Runner, logger *slog.Logger) (int, error) {
	if runner == nil || cmd == nil {
		return 0, nil
	}

	out, err := cmd.Output(ctx, "docker", "compose", "ls", "--format", "json")
	if err != nil {
		return 0, fmt.Errorf("compose ls: %w", err)
	}

	projects, err := parseComposeLs(out)
	if err != nil {
		return 0, fmt.Errorf("parse compose ls: %w", err)
	}

	active := make(map[string]struct{})
	for _, id := range runner.RunningPreviewIDs() {
		active[composeProjectPrefix+id] = struct{}{}
	}

	var (
		pruned   int
		firstErr error
	)
	for _, p := range projects {
		if !strings.HasPrefix(p.Name, composeProjectPrefix) {
			continue
		}
		if _, ok := active[p.Name]; ok {
			continue
		}
		if derr := cmd.Run(ctx, "docker", "compose", "--project-name", p.Name, "down"); derr != nil {
			logger.Warn("agent_compose_orphan_down_failed",
				"project", p.Name, "err", derr.Error())
			if firstErr == nil {
				firstErr = derr
			}
			continue
		}
		logger.Info("agent_compose_orphan_pruned", "project", p.Name)
		pruned++
	}
	return pruned, firstErr
}

// parseComposeLs 는 `docker compose ls --format json` 출력을 파싱한다.
//   - 빈 출력 / 빈 배열 → (nil, nil).
//   - 잘못된 JSON → (nil, err).
func parseComposeLs(out string) ([]composeProject, error) {
	out = strings.TrimSpace(out)
	if out == "" || out == "[]" {
		return nil, nil
	}
	var ps []composeProject
	if err := json.Unmarshal([]byte(out), &ps); err != nil {
		return nil, err
	}
	return ps, nil
}
