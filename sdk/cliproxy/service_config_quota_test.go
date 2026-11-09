package cliproxy

import (
	"reflect"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestQuotaRoutingSelectors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input    string
		strategy string
		selector coreauth.Selector
	}{
		{"nearest-reset", "nearest-reset", &coreauth.NearestResetSelector{}},
		{"nearestreset", "nearest-reset", &coreauth.NearestResetSelector{}},
		{"nr", "nearest-reset", &coreauth.NearestResetSelector{}},
		{" NR ", "nearest-reset", &coreauth.NearestResetSelector{}},
		{"most-used", "most-used", &coreauth.MostUsedSelector{}},
		{"mostused", "most-used", &coreauth.MostUsedSelector{}},
		{"mu", "most-used", &coreauth.MostUsedSelector{}},
		{" MU ", "most-used", &coreauth.MostUsedSelector{}},
		{"use-or-lose", "use-or-lose", &coreauth.UseOrLoseSelector{}},
		{" USE-OR-LOSE ", "use-or-lose", &coreauth.UseOrLoseSelector{}},
		{"unknown", "round-robin", &coreauth.RoundRobinSelector{}},
		{"", "round-robin", &coreauth.RoundRobinSelector{}},
	} {
		t.Run(tc.input, func(t *testing.T) {
			state := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: tc.input}})
			if state.strategy != tc.strategy {
				t.Fatalf("strategy = %q, want %q", state.strategy, tc.strategy)
			}
			selector := newRoutingSelector(state)
			if reflect.TypeOf(selector) != reflect.TypeOf(tc.selector) {
				t.Fatalf("selector = %T, want %T", selector, tc.selector)
			}
			state.sessionAffinity = true
			affinity, ok := newRoutingSelector(state).(*coreauth.SessionAffinitySelector)
			if !ok {
				t.Fatal("session affinity wrapper missing")
			}
			affinity.Stop()
		})
	}
}

func TestUseOrLoseRoutingThreshold(t *testing.T) {
	t.Parallel()
	cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "use-or-lose"}}
	initial := normalizedRoutingRuntimeState(cfg)
	if got := newRoutingSelector(initial).(*coreauth.UseOrLoseSelector).ShortWindowCapPct; got != 95 {
		t.Fatalf("default threshold = %g; want 95", got)
	}
	cfg.Routing.UseOrLose.ShortWindowCapPct = 80.5
	updated := normalizedRoutingRuntimeState(cfg)
	if initial == updated {
		t.Fatal("threshold update did not change runtime state")
	}
	if got := newRoutingSelector(updated).(*coreauth.UseOrLoseSelector).ShortWindowCapPct; got != 80.5 {
		t.Fatalf("updated threshold = %g; want 80.5", got)
	}
}
