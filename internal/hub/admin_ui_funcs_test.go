// admin_ui_funcs_test.go: Phase 12 §7 NF-2 보안 회귀 방지용 helper 단위테스트.
//
//   - statusBadge 가 알 수 없는 입력 status 를 반드시 escape 후 삽입하는지
//     (raw <script> 누출 0, &lt;script&gt; 포함) 검증.
//   - 7개 known status 가 §5 표의 핵심 substring 을 출력하는지 검증.
//
// 본 Phase 의 §2 비범위 ("새 단위테스트 추가") 는 *기능 동작 테스트* 한정이며,
// 보안 회귀 방지 helper 테스트 1건은 §7 NF-2 가 명시적으로 허용한다.
package hub

import (
	"strings"
	"testing"
)

// TestStatusBadge_EscapesUnknownStatus 는 NF-2 핵심: 알 수 없는 status 의
// 위험한 메타문자(<, >, ", ' 등)가 raw 로 출력되지 않고 HTML escape 되는지 검증.
func TestStatusBadge_EscapesUnknownStatus(t *testing.T) {
	got := string(statusBadge("oops<script>"))
	if strings.Contains(got, "<script>") {
		t.Fatalf("statusBadge leaked raw <script>; got=%q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("statusBadge did not HTML-escape angle brackets; got=%q", got)
	}
}

// TestStatusBadge_KnownStatuses 는 §5 표의 7개 status 가 각각 의미적
// element / 색 토큰을 출력하는지 핵심 substring 만 검증한다 (정확한 padding/색
// 값은 디자인 미세조정 영역이라 검증 대상 아님).
func TestStatusBadge_KnownStatuses(t *testing.T) {
	cases := []struct {
		status string
		// substring: 결과 HTML 에 반드시 포함돼야 하는 토큰들 (논리곱).
		substrings []string
	}{
		{"queued", []string{`status-pill`, `>queued<`, `--pico-secondary`}},
		{"assigned", []string{`status-pill`, `>assigned<`, `--pico-secondary`}},
		{"building", []string{`status-pill`, `>building<`, `--pico-primary`}},
		// running 은 Pico mark 토큰 사용 — span 없음.
		{"running", []string{`<mark>running</mark>`}},
		{"teardown", []string{`status-pill`, `>teardown<`, `#fff3cd`}},
		{"done", []string{`status-pill`, `>done<`, `--pico-muted-color`}},
		// failed 는 red — Pico color-red-500 토큰.
		{"failed", []string{`status-pill`, `>failed<`, `--pico-color-red-500`}},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			got := string(statusBadge(tc.status))
			for _, sub := range tc.substrings {
				if !strings.Contains(got, sub) {
					t.Fatalf("statusBadge(%q) missing substring %q\n got=%s",
						tc.status, sub, got)
				}
			}
		})
	}
}
