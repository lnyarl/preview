// 이 파일의 책임:
//   - Traefik REST API 의 라우터 status 를 폴링해 모든 라우터가 enabled 가 될 때까지
//     대기한다 (Phase 7, R1 완화).
//   - 외부 의존: 표준 라이브러리 net/http + encoding/json 만 사용 (NF-1).
//   - 준비 판정 규칙: 응답의 status 필드가 정확히 "enabled" 일 때만 ready.
//     "disabled", "warning", 빈 문자열, 누락 등 그 외 모든 값은 미준비.
//
// 참고: docs/specs/phase-7-traefik-readiness.md §4-3, 결정 1/6/9/10.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// traefikPollInitial / traefikPollMax 은 폴링 간격 (결정 6).
// 패키지 변수로 노출 — 단위 테스트가 직접 swap 해 짧게 만들 수 있다 (NF-10).
var (
	traefikPollInitial = 100 * time.Millisecond
	traefikPollMax     = 250 * time.Millisecond
)

// ErrTraefikRoutersTimeout 는 timeout 내 모든 라우터가 enabled 가 되지 않은 경우.
var ErrTraefikRoutersTimeout = errors.New("traefik routers not ready within timeout")

// routerStatus 는 Traefik /api/http/routers/{name} 응답의 좁은 dto.
//   - Status: 결정 1 의 준비 판정 — "enabled" 정확 일치만 ready, 그 외 모두 미준비.
//   - Name: 디버깅 단서로 보관(요청 name 과 응답 name mismatch 검출 가능,
//     본 함수는 검사하지 않지만 향후 로깅 확장 시 활용).
type routerStatus struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

// WaitTraefikRouters 는 Traefik API 의 /api/http/routers/{name}@docker 를 폴링해
// names 의 모든 라우터가 status:"enabled" 가 될 때까지 대기한다.
//
//   - baseURL 예: "http://127.0.0.1:9080"
//   - names 가 비어있으면 즉시 nil (no-op)
//   - timeout <= 0 또는 baseURL == "" 이면 즉시 nil (probe disabled)
//   - ctx 취소 시 ctx.Err() 반환 (sleep 단계는 select 로 즉시, in-flight HTTP 요청은
//     http.NewRequestWithContext 로 cancel — 최대 1 s 지연, NF-15).
//   - 타임아웃 시 ErrTraefikRoutersTimeout (lastErr 가 있으면 wrap).
//   - 에러는 진단용 — 호출자가 best-effort 로 무시해도 무방 (결정 4).
func WaitTraefikRouters(ctx context.Context, baseURL string, names []string, timeout time.Duration) error {
	if baseURL == "" || timeout <= 0 || len(names) == 0 {
		return nil // probe disabled 또는 검사 대상 없음.
	}

	deadline := time.Now().Add(timeout)
	interval := traefikPollInitial
	client := &http.Client{Timeout: 1 * time.Second} // NF-14: 개별 요청 hang 방지.

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		ok, lastErr := allEnabled(ctx, client, baseURL, names)
		if ok {
			return nil
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("%w: last err: %v", ErrTraefikRoutersTimeout, lastErr)
			}
			return ErrTraefikRoutersTimeout
		}

		// backoff (cap = traefikPollMax). 첫 시도는 즉시, 이후 1.5 배씩 증가.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		interval = min(time.Duration(float64(interval)*1.5), traefikPollMax)
	}
}

// allEnabled 는 names 의 모든 라우터가 enabled 일 때 (true, nil),
// 일부 미존재/disabled/warning/빈값 일 때 (false, nil), HTTP 호출 자체가 실패하면 (false, err).
// 응답 status 분기는 §4-3 응답 표 참조.
func allEnabled(ctx context.Context, client *http.Client, baseURL string, names []string) (bool, error) {
	var lastErr error
	for _, name := range names {
		url := baseURL + "/api/http/routers/" + name + "@docker"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			return false, lastErr // 네트워크 에러 — 다음 polling 사이클로.
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return false, nil // 라우터 아직 미생성 — 정상 폴링 계속.
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("traefik api: %s -> HTTP %d", url, resp.StatusCode)
			return false, lastErr
		}
		var rs routerStatus
		if err := json.NewDecoder(resp.Body).Decode(&rs); err != nil {
			_ = resp.Body.Close()
			return false, err
		}
		_ = resp.Body.Close()
		if rs.Status != "enabled" {
			return false, nil
		}
	}
	return true, nil
}
