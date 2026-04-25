//go:build e2e

// 이 파일의 책임:
//   - testing.T.Logf 로 로그 라인을 흘려보내는 io.Writer 어댑터.
//   - go test -v 일 때만 활성화 (testing.Verbose()).
package e2e

import (
	"strings"
	"testing"
)

// testWriter 는 io.Writer 를 testing.T.Logf 로 변환한다. -v 모드에서만 사용.
type testWriter struct {
	t      *testing.T
	prefix string
}

// Write 는 입력 바이트를 문자열로 변환해 t.Logf 한다.
// trailing newline 을 제거해 t.Logf 가 자체 개행을 추가하도록 둔다.
func (w testWriter) Write(p []byte) (int, error) {
	s := strings.TrimRight(string(p), "\n")
	if s == "" {
		return len(p), nil
	}
	w.t.Logf("%s%s", w.prefix, s)
	return len(p), nil
}
