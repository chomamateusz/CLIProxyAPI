package management

import "testing"

func TestNormalizeQuotaRoutingStrategy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input string
		want  string
		ok    bool
	}{
		{"nearest-reset", "nearest-reset", true},
		{"nearestreset", "nearest-reset", true},
		{"nr", "nearest-reset", true},
		{" NR ", "nearest-reset", true},
		{"most-used", "most-used", true},
		{"mostused", "most-used", true},
		{"mu", "most-used", true},
		{" MU ", "most-used", true},
		{"unknown", "", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, ok := normalizeRoutingStrategy(tc.input)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("normalizeRoutingStrategy() = %q, %v, want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
