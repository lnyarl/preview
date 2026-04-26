// 이 파일의 책임:
//   - WaitTraefikRouters 단위 테스트 — httptest.NewServer 로 Traefik API 응답을
//     모킹해 1 회 성공, 2 회 후 성공, 타임아웃, ctx 취소, 부분 성공 미허용,
//     warning/빈값 status 처리, backoff 횟수, URL 포맷, 개별 요청 timeout 등을
//     검증. 외부 docker daemon 또는 실 Traefik 컨테이너 미요구 (NF-9).
//
// 참고: docs/specs/phase-7-traefik-readiness.md §6-2 (F-9~F-20d), 결정 9.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newFakeAPI(handler func(name string) (int, string)) (*httptest.Server, *atomic.Int64) {
	count := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		// path: /api/http/routers/{name}@docker
		const prefix = "/api/http/routers/"
		path := r.URL.Path
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		nameWithSuffix := strings.TrimPrefix(path, prefix)
		name := strings.TrimSuffix(nameWithSuffix, "@docker")
		status, body := handler(name)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return srv, count
}

// F-9: baseURL=="" → 즉시 nil.
func TestWaitTraefikRouters_DisabledByEmptyBaseURL(t *testing.T) {
	err := WaitTraefikRouters(context.Background(), "", []string{"r1"}, time.Second)
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
}

// F-10: timeout==0 → 즉시 nil.
func TestWaitTraefikRouters_DisabledByZeroTimeout(t *testing.T) {
	err := WaitTraefikRouters(context.Background(), "http://x", []string{"r1"}, 0)
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
}

// F-11: names==nil → 즉시 nil.
func TestWaitTraefikRouters_DisabledByNilNames(t *testing.T) {
	err := WaitTraefikRouters(context.Background(), "http://x", nil, time.Second)
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
}

// F-12: 첫 polling 에서 모든 라우터가 enabled → nil 반환, 호출 횟수 == len(names).
func TestWaitTraefikRouters_FirstShotAllEnabled(t *testing.T) {
	srv, count := newFakeAPI(func(name string) (int, string) {
		return http.StatusOK, fmt.Sprintf(`{"name":%q,"status":"enabled"}`, name)
	})
	defer srv.Close()

	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1", "r2"}, time.Second)
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if got := count.Load(); got != 2 {
		t.Fatalf("requests=%d want 2", got)
	}
}

// F-13: 1 번째 polling 에서 404, 2 번째 polling 에서 enabled → nil 반환.
func TestWaitTraefikRouters_RetryUntilEnabled(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	var attempts atomic.Int64
	srv, count := newFakeAPI(func(name string) (int, string) {
		n := attempts.Add(1)
		if n < 2 {
			return http.StatusNotFound, ""
		}
		return http.StatusOK, fmt.Sprintf(`{"name":%q,"status":"enabled"}`, name)
	})
	defer srv.Close()

	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1"}, time.Second)
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if got := count.Load(); got < 2 {
		t.Fatalf("requests=%d want >=2", got)
	}
}

// F-14: timeout 초과 시 ErrTraefikRoutersTimeout 반환.
func TestWaitTraefikRouters_Timeout(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv, _ := newFakeAPI(func(name string) (int, string) {
		return http.StatusNotFound, ""
	})
	defer srv.Close()

	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1"}, 30*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout err, got nil")
	}
	if !errors.Is(err, ErrTraefikRoutersTimeout) {
		t.Fatalf("err=%v want ErrTraefikRoutersTimeout", err)
	}
}

// F-15: ctx 취소 시 ctx.Err() 반환 (ErrTraefikRoutersTimeout 아님).
func TestWaitTraefikRouters_CtxCanceled(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv, _ := newFakeAPI(func(name string) (int, string) {
		return http.StatusNotFound, ""
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	err := WaitTraefikRouters(ctx, srv.URL, []string{"r1"}, time.Second)
	if err == nil {
		t.Fatal("expected ctx err, got nil")
	}
	if errors.Is(err, ErrTraefikRoutersTimeout) {
		t.Fatalf("err=%v should NOT be ErrTraefikRoutersTimeout", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

// F-16: 500 응답 → polling 계속, 마지막 에러를 ErrTraefikRoutersTimeout 에 wrap.
func TestWaitTraefikRouters_500ThenTimeout(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv, _ := newFakeAPI(func(name string) (int, string) {
		return http.StatusInternalServerError, "boom"
	})
	defer srv.Close()

	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1"}, 30*time.Millisecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrTraefikRoutersTimeout) {
		t.Fatalf("err=%v want wrap of ErrTraefikRoutersTimeout", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err=%v want to contain HTTP 500", err)
	}
}

// F-17: 네트워크 에러 (서버 close 후 호출) → polling 계속, timeout.
func TestWaitTraefikRouters_NetworkErrorThenTimeout(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv, _ := newFakeAPI(func(name string) (int, string) {
		return http.StatusOK, `{"status":"enabled"}`
	})
	srv.Close() // 즉시 닫는다 → 후속 요청 connection refused.

	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1"}, 30*time.Millisecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrTraefikRoutersTimeout) {
		t.Fatalf("err=%v want wrap of ErrTraefikRoutersTimeout", err)
	}
}

// F-18: N 개 라우터 중 마지막 1 개만 disabled → nil 반환 X.
func TestWaitTraefikRouters_PartialEnabledNotReady(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv, _ := newFakeAPI(func(name string) (int, string) {
		if name == "r2" {
			return http.StatusOK, `{"name":"r2","status":"disabled"}`
		}
		return http.StatusOK, fmt.Sprintf(`{"name":%q,"status":"enabled"}`, name)
	})
	defer srv.Close()

	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1", "r2"}, 30*time.Millisecond)
	if !errors.Is(err, ErrTraefikRoutersTimeout) {
		t.Fatalf("err=%v want timeout (r2 disabled never resolves)", err)
	}
}

// F-19: backoff 횟수 검증 — 패키지 변수 swap + 항상 404 + 짧은 timeout.
// 누적 sleep 시퀀스: 1ms, 1.5ms, 2.25ms, 3.375ms, 4ms, 4ms, ...
// 20ms timeout 안에서 시도 횟수는 환경(스케줄러 jitter, OS 시계 해상도) 에 민감.
// 기획서의 expected 6 ±1 = [5,7] 범위는 이상적, R8 의 ±50% 허용으로 [4,9] 까지
// 안정화 (Windows 의 timer 해상도가 15ms 까지 가는 경우 대비). 절대 wallclock 미검증.
func TestWaitTraefikRouters_BackoffPollingCount(t *testing.T) {
	savedI, savedM := traefikPollInitial, traefikPollMax
	traefikPollInitial = 1 * time.Millisecond
	traefikPollMax = 4 * time.Millisecond
	defer func() {
		traefikPollInitial = savedI
		traefikPollMax = savedM
	}()

	srv, count := newFakeAPI(func(name string) (int, string) {
		return http.StatusNotFound, ""
	})
	defer srv.Close()

	_ = WaitTraefikRouters(context.Background(), srv.URL, []string{"r1"}, 20*time.Millisecond)
	got := count.Load()
	// 핵심 검증: backoff 가 실제로 동작 — 횟수가 (timeout / initial) ≈ 20 보다 훨씬
	// 적어야 함. [4, 9] 는 backoff 가 정상 작동하는 합리적 범위.
	if got < 4 || got > 9 {
		t.Fatalf("polling count=%d want in [4,9] (backoff active)", got)
	}
}

// F-20: URL 형식이 {baseURL}/api/http/routers/{name}@docker 정확.
func TestWaitTraefikRouters_URLFormat(t *testing.T) {
	var lastPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath.Store(r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"abc-app","status":"enabled"}`))
	}))
	defer srv.Close()

	if err := WaitTraefikRouters(context.Background(), srv.URL, []string{"abc-app"}, time.Second); err != nil {
		t.Fatalf("err=%v", err)
	}
	got := lastPath.Load()
	if got != "/api/http/routers/abc-app@docker" {
		t.Fatalf("path=%v want /api/http/routers/abc-app@docker", got)
	}
}

// F-20b: warning status → 미준비 (polling 계속 → timeout).
func TestWaitTraefikRouters_WarningNotReady(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv, _ := newFakeAPI(func(name string) (int, string) {
		return http.StatusOK, `{"name":"r1","status":"warning"}`
	})
	defer srv.Close()

	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1"}, 30*time.Millisecond)
	if !errors.Is(err, ErrTraefikRoutersTimeout) {
		t.Fatalf("err=%v want timeout (warning never ready)", err)
	}
}

// F-20c: 빈 status → 미준비 (polling 계속 → timeout).
func TestWaitTraefikRouters_EmptyStatusNotReady(t *testing.T) {
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv, _ := newFakeAPI(func(name string) (int, string) {
		return http.StatusOK, `{"name":"r1","status":""}`
	})
	defer srv.Close()

	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1"}, 30*time.Millisecond)
	if !errors.Is(err, ErrTraefikRoutersTimeout) {
		t.Fatalf("err=%v want timeout (empty status never ready)", err)
	}
}

// NF-14: slow handler — 개별 요청 timeout (1s) 안에 본 함수가 hang 없이 timeout 반환.
// 상위 timeout 을 1.5s 로 두고 핸들러가 무한 sleep 하면 client.Timeout=1s 가 발동
// → 한 번의 요청 사이클이 ~1s, 다음 시도 전에 deadline 초과 → ErrTraefikRoutersTimeout.
func TestWaitTraefikRouters_SlowHandlerTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("slow test")
	}
	saved := traefikPollInitial
	traefikPollInitial = 1 * time.Millisecond
	defer func() { traefikPollInitial = saved }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 무한 hang — client.Timeout (1s) 으로 끊겨야 함.
		<-r.Context().Done()
	}))
	defer srv.Close()

	start := time.Now()
	err := WaitTraefikRouters(context.Background(), srv.URL, []string{"r1"}, 1500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// elapsed 이 client.Timeout (1s) 이상, 외부 timeout (1.5s) 근처에서 종료.
	if elapsed > 3*time.Second {
		t.Fatalf("elapsed=%s — slow handler caused excessive hang", elapsed)
	}
}
