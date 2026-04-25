// 이 파일의 책임:
//   - Runner 의 슬롯 capacity / READY 송신 트리거 (Phase 5).
//   - ReadySender 의존 인터페이스 정의 (Runner 가 *Client 를 직접 import 하지 않도록 의존 역전).
//   - SetReadySender / SetMaxJobs 주입 메서드 + maybeSendReady 트리거 메서드.
//
// runner.go 가 300 줄 한도 근처여서 본 파일로 분할했다 (NF-3).
//
// 참고: docs/specs/phase-5-multi-job.md §3 결정 1/2/5/8, §4-1.
package agent

import "context"

// ReadySender 는 Runner 가 Hub 로 READY 를 송신할 때 사용하는 의존.
// 구현은 client.go 의 Client 가 conn.Write 를 수행한다.
// nil 주입 허용 — nil 이면 maybeSendReady 가 no-op.
type ReadySender interface {
	SendReady(ctx context.Context) error
}

// SetReadySender 는 READY 송신 의존을 주입한다 (Phase 5).
// nil 도 허용 — maybeSendReady 가 no-op 으로 동작한다.
func (r *Runner) SetReadySender(rs ReadySender) { r.ready = rs }

// SetMaxJobs 는 동시 슬롯 한도를 설정한다. n < 1 이면 1 로 보정.
// CLI/env 단의 클램프(1..64)는 ParseConfig 가 책임지며, 본 메서드는
// 직접 호출 경로의 안전망이다.
func (r *Runner) SetMaxJobs(n int) {
	if n < 1 {
		n = 1
	}
	r.maxJobs = n
}

// maybeSendReady 는 가용 슬롯 수만큼 READY 를 Hub 에 송신한다.
//
// 동작:
//   - 진입 시 (maxJobs - inFlight) 를 가용 슬롯 수로 계산해 그 횟수만큼 1 개씩 보낸다.
//   - 매 iter 진입 시 paused 검사 — 송신 도중 Pause 가 호출되면 즉시 종료 (결정 4).
//   - 송신 실패는 warn 로그만 남기고 루프를 즉시 종료한다 (1 회 실패 시 다음 슬롯
//     으로 넘어가지 않음 — backpressure 보호, 결정 6).
//   - ready == nil 이면 no-op.
//
// 자기 치유 경로: 일시 실패로 비어 있는 슬롯이 남아도 (1) 다음 Handle 종료 시
// defer 가 maybeSendReady 를 다시 호출하거나, (2) WS 재연결 후 client.once() 가
// maybeSendReady 를 1 회 호출해 가용 슬롯 전체를 다시 채운다.
//
// race 주의: 진입 시 가용 슬롯 수를 한 번 snapshot 하므로, 그 사이 다른 goroutine
// 이 새 job 을 받아 inFlight 를 늘리면 일시적으로 1~2 개 over-subscribe 가능.
// Hub dispatcher 가 큐 비면 READY 를 무시하므로 운영상 영향은 미미 (best-effort,
// 결정 1).
func (r *Runner) maybeSendReady(ctx context.Context) {
	if r.ready == nil {
		return
	}
	if r.maxJobs < 1 {
		r.maxJobs = 1 // 안전 보정 (NewRunner default 우회 경로 대비).
	}
	cur := r.inFlight.Load()
	slots := int64(r.maxJobs) - cur
	if slots <= 0 {
		return
	}
	for i := int64(0); i < slots; i++ {
		if r.paused.Load() {
			return
		}
		if err := r.ready.SendReady(ctx); err != nil {
			r.logger.Warn("agent_ready_send_failed", "err", err.Error())
			return
		}
	}
}
