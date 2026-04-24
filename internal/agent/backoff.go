// 이 파일의 책임:
//   - 지수 백오프 계산 (1s -> 2s -> 4s -> 8s -> 16s -> 30s 상한, ±20% jitter).
//   - 성공 후 Reset 으로 시퀀스 재시작.
//
// 참고: 결정 시퀀스 is 기획서 §5-1 `internal/agent.Backoff`.
package agent

import (
	"math/rand"
	"sync"
	"time"
)

// 기본 상수. 단위 테스트가 이 값들에 의존.
const (
	BackoffInitial = 1 * time.Second
	BackoffMax     = 30 * time.Second
	BackoffJitter  = 0.20 // ±20%
)

// Backoff 는 goroutine-safe 한 지수 백오프 발생기.
type Backoff struct {
	mu      sync.Mutex
	current time.Duration
	rng     *rand.Rand
}

// NewBackoff 는 새 Backoff 를 만든다. 시드를 직접 지정하면 결정적 테스트에 쓴다.
func NewBackoff(seed int64) *Backoff {
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	// nolint:gosec  // G404: non-crypto 용도(백오프 jitter).
	return &Backoff{
		rng: rand.New(rand.NewSource(seed)),
	}
}

// Next 는 다음 대기 시간을 반환한다. jitter 적용.
// 호출할 때마다 current 를 2배로 증가시키되 BackoffMax 를 상한으로 한다.
func (b *Backoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current == 0 {
		b.current = BackoffInitial
	} else {
		b.current *= 2
		if b.current > BackoffMax {
			b.current = BackoffMax
		}
	}
	return jitter(b.current, BackoffJitter, b.rng)
}

// Reset 은 시퀀스를 초기 상태로 되돌린다. 다음 Next 는 BackoffInitial ±20%.
func (b *Backoff) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.current = 0
}

// jitter 는 d 에 ±factor 범위의 무작위 jitter 를 적용한다.
func jitter(d time.Duration, factor float64, rng *rand.Rand) time.Duration {
	if factor <= 0 {
		return d
	}
	// delta in [-factor, +factor]
	delta := (rng.Float64()*2 - 1) * factor
	out := float64(d) * (1 + delta)
	return time.Duration(out)
}
