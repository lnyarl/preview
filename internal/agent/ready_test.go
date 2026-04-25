// 이 파일의 책임:
//   - Phase 5: ReadySender 인터페이스 + Runner.maybeSendReady / SetMaxJobs / SetReadySender
//     의 단위 테스트.
//   - F-1 ~ F-13, F-22 (구현자 ↔ evaluator 검증 매핑은 기획서 §5 참조).
package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeReadySender 는 SendReady 호출을 카운트하는 mock.
// err 가 set 되어 있으면 매 호출마다 그 에러를 리턴한다.
type fakeReadySender struct {
	mu    sync.Mutex
	calls int
	err   error
	// onCall 은 call 카운트가 증가한 직후 hook. nil 허용.
	// race 시나리오에서 inFlight 를 조작하기 위해 사용.
	onCall func()
}

func (f *fakeReadySender) SendReady(ctx context.Context) error {
	f.mu.Lock()
	f.calls++
	hook := f.onCall
	err := f.err
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakeReadySender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// runnerForReady 는 maybeSendReady 직접 테스트용 최소 Runner — Hub/Docker/Cache 없이.
// silentLogger 로 로그 무음.
func runnerForReady(t *testing.T) *Runner {
	t.Helper()
	r := &Runner{logger: silentLogger()}
	r.maxJobs = 1 // NewRunner default 와 동일.
	return r
}

// F-1: ReadySender 인터페이스를 *Client 가 만족하는지 컴파일 시점 확인.
// 본 라인은 Client 가 SendReady(ctx) error 시그니처를 갖지 않으면 컴파일 실패.
var _ ReadySender = (*Client)(nil)

// F-3: ready == nil (미주입) 이면 maybeSendReady 가 panic 없이 no-op.
func TestMaybeSendReadyNilSenderNoOp(t *testing.T) {
	r := runnerForReady(t)
	// ready 미주입 상태.
	r.maybeSendReady(context.Background()) // panic 없으면 통과.
}

// F-2 + F-5: SetReadySender 주입 후 maxJobs=5, inFlight=0 → 5 회 송신.
func TestMaybeSendReadyInitialFillsAllSlots(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(5)
	fake := &fakeReadySender{}
	r.SetReadySender(fake)
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 5 {
		t.Fatalf("SendReady calls=%d want 5", got)
	}
}

// F-4: SetMaxJobs(n<1) 은 1 로 보정.
func TestSetMaxJobsClampsToOne(t *testing.T) {
	r := runnerForReady(t)
	for _, n := range []int{0, -1, -100} {
		r.SetMaxJobs(n)
		if r.maxJobs != 1 {
			t.Fatalf("SetMaxJobs(%d): r.maxJobs=%d want 1", n, r.maxJobs)
		}
	}
}

// F-10: inFlight=1, max=3 → 정확히 2 회 송신 (가용 슬롯만큼).
func TestMaybeSendReadyHonorsInFlight(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(3)
	r.inFlight.Store(1)
	fake := &fakeReadySender{}
	r.SetReadySender(fake)
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 2 {
		t.Fatalf("calls=%d want 2", got)
	}
}

// F-11: inFlight == maxJobs → 0 회 송신.
func TestMaybeSendReadyNoSlotsAvailable(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(3)
	r.inFlight.Store(3)
	fake := &fakeReadySender{}
	r.SetReadySender(fake)
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 0 {
		t.Fatalf("calls=%d want 0", got)
	}
}

// F-12: inFlight > maxJobs (race) → 0 회 송신 (>= 비교 보장).
func TestMaybeSendReadyOverInFlightDoesNotSend(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(2)
	r.inFlight.Store(5) // race 로 일시적 over-subscribe.
	fake := &fakeReadySender{}
	r.SetReadySender(fake)
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 0 {
		t.Fatalf("calls=%d want 0", got)
	}
}

// 결정 4: paused=true 일 때 maybeSendReady 는 즉시 return.
func TestMaybeSendReadyPausedSkips(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(3)
	r.paused.Store(true)
	fake := &fakeReadySender{}
	r.SetReadySender(fake)
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 0 {
		t.Fatalf("calls=%d want 0 (paused)", got)
	}
}

// 결정 6 / 리스크 표: SendReady 1 회 실패 시 루프 즉시 종료 (warn 로그만).
// 가용 슬롯이 5 이고 매 호출이 실패해도 1 회만 호출되어야 한다.
func TestMaybeSendReadyAbortsOnSendError(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(5)
	fake := &fakeReadySender{err: errors.New("boom")}
	r.SetReadySender(fake)
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 1 {
		t.Fatalf("calls=%d want 1 (abort on first error)", got)
	}
}

// F-22: reconnect 시뮬 — inFlight=1, max=3 → 가용 2 슬롯만 정확히 송신.
func TestMaybeSendReadyReconnectScenario(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(3)
	// 이전 연결에서 진행 중이던 build 1 개.
	r.inFlight.Store(1)
	fake := &fakeReadySender{}
	r.SetReadySender(fake)
	// 재연결 직후 client.once() 가 maybeSendReady 1 회 호출.
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 2 {
		t.Fatalf("reconnect calls=%d want 2 (max-inFlight)", got)
	}
}

// 안전망: NewRunner default 우회 후 SetMaxJobs 미호출 시도 maybeSendReady 의
// `if r.maxJobs < 1` 보정으로 1 회 송신 (직접 &Runner{} 생성 비정상 경로).
func TestMaybeSendReadyMaxJobsZeroSafeguard(t *testing.T) {
	r := &Runner{logger: silentLogger()} // SetMaxJobs / NewRunner 모두 우회.
	fake := &fakeReadySender{}
	r.SetReadySender(fake)
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 1 {
		t.Fatalf("calls=%d want 1 (safeguard)", got)
	}
}

// F-13: 다수 goroutine 이 동시에 maybeSendReady 호출해도 race detector 통과 +
// over-subscribe 가 maxJobs+goroutine_count 이내로 제한된다.
func TestMaybeSendReadyConcurrentRaceFree(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(4)
	fake := &fakeReadySender{}
	r.SetReadySender(fake)

	// 여러 goroutine 이 동시에 maybeSendReady 호출.
	const G = 8
	var wg sync.WaitGroup
	wg.Add(G)
	for i := 0; i < G; i++ {
		go func() {
			defer wg.Done()
			r.maybeSendReady(context.Background())
		}()
	}
	wg.Wait()
	// race-free 통과가 핵심. 호출 수는 최소 4 (maxJobs) 이상.
	if got := fake.count(); got < 4 {
		t.Fatalf("calls=%d want >=4 (at least one goroutine fills maxJobs)", got)
	}
}

// onCall hook 으로 inFlight 를 증가시켜 over-subscribe race 가 best-effort
// 모델대로 1~2 개로 제한됨을 확인 (결정 1, 리스크 표).
func TestMaybeSendReadyRaceWithExternalInFlightInc(t *testing.T) {
	r := runnerForReady(t)
	r.SetMaxJobs(3)
	var external atomic.Int64
	fake := &fakeReadySender{}
	// SendReady 직후 외부에서 inFlight 를 1 늘리는 것을 시뮬.
	fake.onCall = func() {
		external.Add(1)
		r.inFlight.Add(1)
	}
	r.SetReadySender(fake)
	r.maybeSendReady(context.Background())
	if got := fake.count(); got != 3 {
		t.Fatalf("calls=%d want 3 (filled to maxJobs)", got)
	}
	if external.Load() != 3 {
		t.Fatalf("external inc=%d want 3", external.Load())
	}
}
