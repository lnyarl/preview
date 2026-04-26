// 이 파일의 책임:
//   - Runner.SetTraefikAPIPort / SetRouterReadyTimeout 두 setter (Phase 7 wiring).
//   - Runner.waitRouters 헬퍼: STATUS_UPDATE running 직전에 Traefik 라우터 활성화를
//     polling 으로 대기. 결정 4 의 best-effort 정책 — 타임아웃이어도 송신 흐름 중단
//     없음. unknown buildMode 를 silent skip 하지 않도록 WARN 로그 1 회 (M2 / F-35d).
//   - elapsed_ms 로깅으로 운영자 디버깅 단서 제공 (Q4).
//
// 분할 정책: NF-3 — runner.go 가 300 줄을 초과하지 않도록 Phase 7 추가물(setter+
// 헬퍼) 만 별 파일로 격리. 필드 자체는 runner.go 의 Runner struct 정의에 남는다.
//
// 참고: docs/specs/phase-7-traefik-readiness.md §4-5, 결정 4/11, M2.
package agent

import (
	"context"
	"fmt"
	"time"
)

// SetTraefikAPIPort 는 Traefik API 호스트 포트를 설정한다 (Phase 7).
// 0 이면 readiness probe 비활성 (waitRouters 즉시 return).
func (r *Runner) SetTraefikAPIPort(port int) { r.traefikAPIPort = port }

// SetRouterReadyTimeout 는 readiness probe timeout 을 설정한다 (Phase 7).
// 0 이면 probe 비활성.
func (r *Runner) SetRouterReadyTimeout(d time.Duration) { r.routerReadyTimeout = d }

// waitRouters 는 STATUS_UPDATE running 송신 직전에 Traefik 라우터 활성화를 폴링한다.
// 반환값 없음 — 결정 4 의 best-effort 정책: 타임아웃이어도 호출자는 그대로 진행.
//
// 분기:
//   - traefikAPIPort == 0 또는 routerReadyTimeout <= 0 → 즉시 return (probe disabled).
//   - buildMode 가 compose/dockerfile 외 → WARN 로그 1 회 + return (M2 / F-35d).
//   - RouterNames 가 빈 슬라이스 → 검사 대상 없음, silent return.
//   - WaitTraefikRouters 성공 → Info 로그 (agent_traefik_ready) + elapsed_ms.
//   - 실패(타임아웃/네트워크/ctx 취소) → Warn 로그 (agent_traefik_ready_timeout) +
//     elapsed_ms + err 메시지. 호출 흐름은 그대로 진행.
func (r *Runner) waitRouters(ctx context.Context, pid string, cfg PreviewConfig, buildMode string) {
	if r.traefikAPIPort == 0 || r.routerReadyTimeout <= 0 {
		return // probe disabled.
	}
	// (M2 / F-35d) unknown buildMode 방어 — RouterNames 가 빈 슬라이스 반환하므로
	// silent skip 막기 위해 WARN 1 회.
	if buildMode != buildModeCompose && buildMode != buildModeDockerfile {
		r.logger.Warn("agent_traefik_ready_unknown_buildmode",
			"preview_id", pid, "build_mode", buildMode)
		return
	}
	names := RouterNames(pid, cfg, buildMode)
	if len(names) == 0 {
		return // 라우터 0 개 — 검사 대상 없음 (방어적).
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", r.traefikAPIPort)
	start := time.Now()
	err := WaitTraefikRouters(ctx, base, names, r.routerReadyTimeout)
	elapsed := time.Since(start)
	if err == nil {
		r.logger.Info("agent_traefik_ready",
			"preview_id", pid, "routers", names,
			"elapsed_ms", elapsed.Milliseconds())
		return
	}
	r.logger.Warn("agent_traefik_ready_timeout",
		"preview_id", pid, "err", err.Error(), "routers", names,
		"elapsed_ms", elapsed.Milliseconds())
}
