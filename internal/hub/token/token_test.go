package token

import (
	"strings"
	"testing"
)

func TestGenerateHashVerifyRoundTrip(t *testing.T) {
	g := NewGenerator(4) // fast cost for tests
	raw, hash, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(raw, Prefix) {
		t.Fatalf("raw missing prefix: %q", raw)
	}
	// After stripping prefix, remaining should be base64url.
	suffix := strings.TrimPrefix(raw, Prefix)
	if len(suffix) < 40 {
		t.Fatalf("suffix too short: %d", len(suffix))
	}
	// bcrypt hash prefix check.
	if !strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") && !strings.HasPrefix(hash, "$2y$") {
		t.Fatalf("hash not bcrypt shape: %q", hash)
	}
	if !Verify(raw, hash) {
		t.Fatal("Verify returned false for matching pair")
	}
	if Verify("agt_other", hash) {
		t.Fatal("Verify returned true for mismatched pair")
	}
	if Verify("bad", hash) {
		t.Fatal("Verify returned true for wrong-prefix token")
	}
}

func TestGenerateEntropy(t *testing.T) {
	g := NewGenerator(4)
	seen := make(map[string]struct{})
	for i := 0; i < 20; i++ {
		raw, _, err := g.Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if _, dup := seen[raw]; dup {
			t.Fatalf("duplicate token after %d iterations", i)
		}
		seen[raw] = struct{}{}
	}
}

func TestTokenHashIsBcrypt(t *testing.T) {
	g := NewGenerator(4)
	for i := 0; i < 5; i++ {
		hash, err := g.Hash("agt_dummy")
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}
		if !(strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$")) {
			t.Fatalf("hash does not look like bcrypt: %q", hash)
		}
	}
}
