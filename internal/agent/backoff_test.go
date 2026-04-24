package agent

import (
	"testing"
	"time"
)

func TestBackoffSequence(t *testing.T) {
	b := NewBackoff(42)
	// 기대 중앙값(상한 포함): 1s -> 2s -> 4s -> 8s -> 16s -> 30s -> 30s
	wantMid := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	tol := 0.21 // spec says ±20% jitter; allow 1 pp slack to avoid flakiness.
	for i, mid := range wantMid {
		got := b.Next()
		lo := time.Duration(float64(mid) * (1 - tol))
		hi := time.Duration(float64(mid) * (1 + tol))
		if got < lo || got > hi {
			t.Fatalf("Next#%d = %v, want in [%v, %v] (mid=%v)", i, got, lo, hi, mid)
		}
	}
}

func TestBackoffReset(t *testing.T) {
	b := NewBackoff(11)
	// advance
	_ = b.Next()
	_ = b.Next()
	_ = b.Next()
	b.Reset()
	first := b.Next()
	lo := time.Duration(float64(time.Second) * 0.79)
	hi := time.Duration(float64(time.Second) * 1.21)
	if first < lo || first > hi {
		t.Fatalf("after Reset: Next=%v, want near 1s (%v..%v)", first, lo, hi)
	}
}
