package hub

import "testing"

// TestLabelsMatch covers the 6 cases from spec §3 결정 4 + nil edge(F-S2-3).
func TestLabelsMatch(t *testing.T) {
	cases := []struct {
		name    string
		preview map[string]string
		agent   map[string]string
		want    bool
	}{
		{"both empty", map[string]string{}, map[string]string{}, true},
		{"preview empty agent has", map[string]string{}, map[string]string{"env": "home"}, true},
		{"preview has agent empty", map[string]string{"env": "home"}, map[string]string{}, false},
		{"agent superset", map[string]string{"env": "home"}, map[string]string{"env": "home", "owner": "alice"}, true},
		{"agent missing key", map[string]string{"env": "home", "owner": "alice"}, map[string]string{"env": "home"}, false},
		{"value mismatch", map[string]string{"env": "home"}, map[string]string{"env": "office"}, false},
		{"nil maps", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LabelsMatch(tc.preview, tc.agent); got != tc.want {
				t.Fatalf("LabelsMatch(%v,%v)=%v want %v", tc.preview, tc.agent, got, tc.want)
			}
		})
	}
}
