package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRoutingRuntimeStateDrainEndingCredentials(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name  string
		value *bool
		want  bool
	}{
		{"default", nil, true},
		{"explicit on", &on, true},
		{"explicit off", &off, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Routing: config.RoutingConfig{Strategy: "use-or-lose", UseOrLose: config.UseOrLoseConfig{DrainEndingCredentials: tc.value}}}
			state := normalizedRoutingRuntimeState(cfg)
			selector, ok := newRoutingSelector(state).(*coreauth.UseOrLoseSelector)
			if !ok || state.useOrLoseDrainEndingCredentials != tc.want || selector.DrainEndingCredentials != tc.want {
				t.Fatalf("state drain = %t, selector = %#v; want drain %t", state.useOrLoseDrainEndingCredentials, selector, tc.want)
			}
		})
	}
	if normalizedRoutingRuntimeState(&config.Config{Routing: config.RoutingConfig{Strategy: "use-or-lose", UseOrLose: config.UseOrLoseConfig{DrainEndingCredentials: &off}}}) == normalizedRoutingRuntimeState(&config.Config{Routing: config.RoutingConfig{Strategy: "use-or-lose"}}) {
		t.Fatal("toggling drain-ending-credentials does not change the applied routing state")
	}
}

func TestRoutingRuntimeStateDrainEndingResets(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name  string
		value *bool
		want  bool
	}{
		{"default", nil, true},
		{"explicit on", &on, true},
		{"explicit off", &off, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Routing: config.RoutingConfig{Strategy: "use-or-lose", UseOrLose: config.UseOrLoseConfig{DrainEndingResets: tc.value}}}
			state := normalizedRoutingRuntimeState(cfg)
			selector, ok := newRoutingSelector(state).(*coreauth.UseOrLoseSelector)
			if !ok || state.useOrLoseDrainEndingResets != tc.want || selector.DrainEndingResets != tc.want {
				t.Fatalf("state drain resets = %t, selector = %#v; want %t", state.useOrLoseDrainEndingResets, selector, tc.want)
			}
		})
	}
	if normalizedRoutingRuntimeState(&config.Config{Routing: config.RoutingConfig{Strategy: "use-or-lose", UseOrLose: config.UseOrLoseConfig{DrainEndingResets: &off}}}) == normalizedRoutingRuntimeState(&config.Config{Routing: config.RoutingConfig{Strategy: "use-or-lose"}}) {
		t.Fatal("toggling drain-ending-resets does not change the applied routing state")
	}
}
