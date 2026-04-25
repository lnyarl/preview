package hub

import "testing"

// TestLabelsMatch covers the 6 cases from spec §3 결정 4 + nil edge(F-S2-3).
// Labels 가 []string 으로 단순화되었으므로 set-membership 의미론으로 매칭한다.
func TestLabelsMatch(t *testing.T) {
	cases := []struct {
		name    string
		preview []string
		agent   []string
		want    bool
	}{
		{"both empty", []string{}, []string{}, true},
		{"preview empty agent has", []string{}, []string{"home"}, true},
		{"preview has agent empty", []string{"home"}, []string{}, false},
		{"agent superset", []string{"home"}, []string{"home", "alice"}, true},
		{"agent missing value", []string{"home", "alice"}, []string{"home"}, false},
		{"value mismatch", []string{"home"}, []string{"office"}, false},
		{"nil slices", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LabelsMatch(tc.preview, tc.agent); got != tc.want {
				t.Fatalf("LabelsMatch(%v,%v)=%v want %v", tc.preview, tc.agent, got, tc.want)
			}
		})
	}
}
