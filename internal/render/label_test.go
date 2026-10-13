package render

import "testing"

func TestLabelUsesReadableIDs(t *testing.T) {
	for _, tc := range []struct {
		parts []string
		want  string
	}{
		{[]string{"ORD-001", "scenario"}, "tbl-scn-ORD-001"},
		{[]string{"ORD-001", "request", "create"}, "tbl-req-ORD-001:create"},
		{[]string{"ORD-001", "expect", "inc.call"}, "tbl-exp-ORD-001:inc_2e_call"},
		{[]string{"A:B", "http"}, "tbl-http-A_3a_B"},
		{[]string{"A_3a_B", "http"}, "tbl-http-A_5f_3a_5f_B"},
	} {
		if got := label(tc.parts...); got != tc.want {
			t.Errorf("label(%q) = %q, want %q", tc.parts, got, tc.want)
		}
	}
}
