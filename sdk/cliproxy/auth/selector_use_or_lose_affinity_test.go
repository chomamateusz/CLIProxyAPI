package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestUseOrLoseAffinityHealthyAndRecovery(t *testing.T) {
	t.Parallel()
	for _, rebind := range []string{SessionAffinityRebindOnAny, SessionAffinityRebindOnQuota} {
		t.Run(rebind, func(t *testing.T) {
			now := time.Now()
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &UseOrLoseSelector{}, RebindOn: rebind})
			defer selector.Stop()
			a := useOrLoseTestAuth("a", now, 24*time.Hour, .5)
			b := useOrLoseTestAuth("b", now, 48*time.Hour, .2)
			pick := func(session, want string) {
				t.Helper()
				opts := cliproxyexecutor.Options{OriginalRequest: []byte(fmt.Sprintf(`{"session_id":%q}`, session))}
				got, err := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", opts, []*Auth{a, b})
				if err != nil || got == nil || got.ID != want {
					t.Fatalf("session %s: Pick = %v, %v; want %s", session, got, err, want)
				}
			}
			pick("bound", "a")
			useOrLoseTestWindow(b, "7d", now.Add(12*time.Hour), .1)
			pick("bound", "a")
			pick("new", "b")
			useOrLoseTestWindow(a, "5h", now.Add(time.Hour), .96)
			pick("bound", "b")
			useOrLoseTestWindow(a, "5h", now.Add(-time.Second), .96)
			useOrLoseTestWindow(a, "7d", now.Add(6*time.Hour), .1)
			for range 3 {
				pick("bound", "b")
			}
			pick("fresh-after-reset", "a")
			useOrLoseTestWindow(b, "5h", now.Add(time.Hour), .95)
			pick("bound", "a")
		})
	}
}

func TestUseOrLoseAffinityConfiguredThreshold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cap, used float64
		want      string
	}{
		{0, .9499, "bound"},
		{0, .95, "other"},
		{80, .8, "other"},
		{80, .7999, "bound"},
		{99, .96, "bound"},
		{100, 1, "other"},
		{-1, .96, "other"},
	} {
		t.Run(fmt.Sprintf("%g-%g", tc.cap, tc.used), func(t *testing.T) {
			now := time.Now()
			selector := NewSessionAffinitySelector(&UseOrLoseSelector{ShortWindowCapPct: tc.cap})
			defer selector.Stop()
			bound := useOrLoseTestAuth("bound", now, 24*time.Hour, .5)
			other := useOrLoseTestAuth("other", now, 48*time.Hour, .2)
			opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"configured-cap"}`)}
			if got, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, []*Auth{bound, other}); err != nil || got.ID != "bound" {
				t.Fatalf("initial Pick = %v, %v", got, err)
			}
			useOrLoseTestWindow(bound, "5h", now.Add(time.Hour), tc.used)
			got, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, []*Auth{bound, other})
			if err != nil || got == nil || got.ID != tc.want {
				t.Fatalf("Pick = %v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestUseOrLoseAffinityAllCappedReranks(t *testing.T) {
	t.Parallel()
	now := time.Now()
	selector := NewSessionAffinitySelector(&UseOrLoseSelector{})
	defer selector.Stop()
	a := useOrLoseTestAuth("a", now, 24*time.Hour, .5)
	b := useOrLoseTestAuth("b", now, 48*time.Hour, .2)
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"all-capped"}`)}
	if got, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, []*Auth{a, b}); err != nil || got.ID != "a" {
		t.Fatalf("initial Pick = %v, %v", got, err)
	}
	useOrLoseTestWindow(a, "5h", now.Add(time.Hour), .96)
	useOrLoseTestWindow(b, "5h", now.Add(time.Hour), .96)
	useOrLoseTestWindow(b, "7d", now.Add(12*time.Hour), .2)
	if got, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, []*Auth{a, b}); err != nil || got.ID != "b" {
		t.Fatalf("capped Pick = %v, %v; want b", got, err)
	}
}

func TestUseOrLoseAffinityManagerRebinding(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		change      func(*Auth, time.Time)
	}{
		{"short cap", "claude-opus-5-5", func(a *Auth, now time.Time) { useOrLoseTestWindow(a, "5h", now.Add(time.Hour), .96) }},
		{"weekly utilisation", "claude-opus-5-5", func(a *Auth, now time.Time) { useOrLoseTestWindow(a, "7d", now.Add(24*time.Hour), 1) }},
		{"weekly rejected", "claude-opus-5-5", func(a *Auth, now time.Time) { a.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = " ReJeCtEd " }},
		{"Fable weekly", "claude-fable-5-1", func(a *Auth, now time.Time) { useOrLoseTestWindow(a, "7d_oi", now.Add(24*time.Hour), 1) }},
		{"model exclusion", "claude-fable-5-1", func(a *Auth, now time.Time) {
			registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5"}})
		}},
		{"model cooldown", "claude-fable-5-1", func(a *Auth, now time.Time) {
			a.ModelStates = map[string]*ModelState{"claude-fable-5-1": {Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}}}
		}},
		{"transient cooldown", "claude-opus-5-5", func(a *Auth, now time.Time) {
			a.ModelStates = map[string]*ModelState{"claude-opus-5-5": {Unavailable: true, NextRetryAfter: now.Add(time.Hour), LastError: &Error{HTTPStatus: 503}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &UseOrLoseSelector{}, RebindOn: SessionAffinityRebindOnQuota})
			defer selector.Stop()
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
			a := useOrLoseTestAuth("affinity-a", now, 24*time.Hour, .5)
			b := useOrLoseTestAuth("affinity-b", now, 48*time.Hour, .2)
			for _, auth := range []*Auth{a, b} {
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: tc.model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			pick := func(want string) {
				t.Helper()
				opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"manager-rebind"}`)}
				got, _, _, err := manager.pickNextMixed(context.Background(), []string{"claude"}, tc.model, opts, nil)
				if err != nil || got == nil || got.ID != want {
					t.Fatalf("pickNextMixed = %v, %v; want %s", got, err, want)
				}
			}
			pick(a.ID)
			changed := a.Clone()
			tc.change(changed, now)
			if _, err := manager.Update(context.Background(), changed); err != nil {
				t.Fatal(err)
			}
			pick(b.ID)
			registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: tc.model}})
			if _, err := manager.Update(context.Background(), a.Clone()); err != nil {
				t.Fatal(err)
			}
			pick(b.ID)
		})
	}
}

func TestUseOrLoseAffinityConcurrentSessions(t *testing.T) {
	t.Parallel()
	now := time.Now()
	selector := NewSessionAffinitySelector(&UseOrLoseSelector{})
	defer selector.Stop()
	a := useOrLoseTestAuth("a", now, 24*time.Hour, .5)
	b := useOrLoseTestAuth("b", now, 48*time.Hour, .2)
	auths := []*Auth{a, b}
	phase := func(want func(int) string) {
		t.Helper()
		var wg sync.WaitGroup
		for session := range 32 {
			wg.Go(func() {
				for range 10 {
					opts := cliproxyexecutor.Options{OriginalRequest: []byte(fmt.Sprintf(`{"session_id":"concurrent-%d"}`, session))}
					got, err := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", opts, auths)
					if err != nil || got == nil || got.ID != want(session) {
						t.Errorf("session %d: Pick = %v, %v; want %s", session, got, err, want(session))
						return
					}
				}
			})
		}
		wg.Wait()
	}
	for session := range 32 {
		opts := cliproxyexecutor.Options{OriginalRequest: []byte(fmt.Sprintf(`{"session_id":"concurrent-%d"}`, session))}
		if _, err := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", opts, []*Auth{auths[session%2]}); err != nil {
			t.Fatal(err)
		}
	}
	phase(func(session int) string { return auths[session%2].ID })
	useOrLoseTestWindow(a, "5h", now.Add(time.Hour), .96)
	phase(func(int) string { return "b" })
	useOrLoseTestWindow(a, "5h", now.Add(-time.Second), .96)
	phase(func(int) string { return "b" })
	useOrLoseTestWindow(b, "5h", now.Add(time.Hour), .96)
	phase(func(int) string { return "a" })
}

func TestUseOrLoseAffinityLCPRecovery(t *testing.T) {
	t.Parallel()
	now := time.Now()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &UseOrLoseSelector{}, RebindOn: SessionAffinityRebindOnQuota})
	defer selector.Stop()
	a := useOrLoseTestAuth("a", now, 24*time.Hour, .5)
	b := useOrLoseTestAuth("b", now, 48*time.Hour, .2)
	pick := func(payload, want string) cliproxyexecutor.Options {
		t.Helper()
		opts := cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAI,
			OriginalRequest: []byte(payload),
			Metadata:        map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "use-or-lose-lcp"},
		}
		got, err := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", opts, []*Auth{a, b})
		if err != nil || got == nil || got.ID != want {
			t.Fatalf("Pick = %v, %v; want %s", got, err, want)
		}
		if opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] == nil {
			t.Fatal("selection did not use LCP affinity")
		}
		return opts
	}
	first := `{"messages":[{"role":"user","content":"hello"}]}`
	second := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}]}`
	third := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"},{"role":"assistant","content":"answer"},{"role":"user","content":"continue"}]}`
	pick(first, "a")
	useOrLoseTestWindow(b, "7d", now.Add(12*time.Hour), .2)
	oldAttempt := pick(second, "a")
	useOrLoseTestWindow(a, "5h", now.Add(time.Hour), .96)
	pick(second, "b")
	selector.OnResult(Result{AuthID: a.ID, Provider: "mixed", Model: "claude-opus-5-5", Success: true, Options: oldAttempt})
	useOrLoseTestWindow(a, "5h", now.Add(-time.Second), .96)
	useOrLoseTestWindow(a, "7d", now.Add(6*time.Hour), .1)
	pick(third, "b")
}
