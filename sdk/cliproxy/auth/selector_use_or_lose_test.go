package auth

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func useOrLoseTestAuth(id string, now time.Time, resetIn time.Duration, used float64) *Auth {
	return &Auth{ID: id, Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":       now.Add(resetIn).Format(time.RFC3339),
		"Anthropic-Ratelimit-Unified-7d-Utilization": fmt.Sprint(used),
	}}}
}

func useOrLoseTestWindow(auth *Auth, name string, reset time.Time, used float64) {
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-"+name+"-Reset"] = reset.Format(time.RFC3339)
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-"+name+"-Utilization"] = fmt.Sprint(used)
}

func TestUseOrLosePick(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		model   string
		prepare func(a, b *Auth) []*Auth
		want    string
	}{
		{name: "observed Opus failure", model: "claude-opus-5-5", want: "devp.it"},
		{name: "observed Fable failure", model: "claude-fable-5-1", want: "devp.it"},
		{name: "five hour cap", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "5h", now.Add(3*time.Hour), .95)
			return []*Auth{a, b}
		}, want: "amazingdesign"},
		{name: "cap boundary is not rounded", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "5h", now.Add(3*time.Hour), .9499)
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "expired short cap", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "5h", now, 1)
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "all capped still selectable by week", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(a, "5h", now.Add(time.Minute), 1)
			useOrLoseTestWindow(b, "5h", now.Add(3*time.Hour), 1)
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "weekly exhaustion alone does not exclude", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "7d", now.Add(24*time.Hour), 1)
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
			return []*Auth{a, b}
		}, want: "amazingdesign"},
		{name: "only eligible credential at weekly limit", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "7d", now.Add(24*time.Hour), 1)
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
			a.Disabled = true
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "weekly utilisation alone demotes", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "7d", now.Add(24*time.Hour), 1)
			return []*Auth{a, b}
		}, want: "amazingdesign"},
		{name: "weekly rejected status alone demotes", prepare: func(a, b *Auth) []*Auth {
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = " ReJeCtEd "
			return []*Auth{a, b}
		}, want: "amazingdesign"},
		{name: "Fable weekly exhaustion", model: "claude-fable-5-1", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "7d", now.Add(72*time.Hour), .86)
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "allowed_warning"
			useOrLoseTestWindow(b, "7d_oi", now.Add(24*time.Hour), 1)
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Status"] = "rejected"
			return []*Auth{a, b}
		}, want: "amazingdesign"},
		{name: "Fable weekly exhaustion does not demote Opus", model: "claude-opus-5-5", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "7d_oi", now.Add(24*time.Hour), 1)
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Status"] = "rejected"
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "lower utilisation in one hour tie", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(a, "7d", now.Add(24*time.Hour), .9)
			useOrLoseTestWindow(b, "7d", now.Add(25*time.Hour), .1)
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "outside one hour tie", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(a, "7d", now.Add(24*time.Hour), .9)
			useOrLoseTestWindow(b, "7d", now.Add(25*time.Hour+time.Second), .1)
			return []*Auth{a, b}
		}, want: "amazingdesign"},
		{name: "tie chain anchored to earliest", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(a, "7d", now.Add(24*time.Hour), .9)
			useOrLoseTestWindow(b, "7d", now.Add(24*time.Hour+45*time.Minute), .5)
			return []*Auth{useOrLoseTestAuth("outside", now, 25*time.Hour+30*time.Minute, 0), b, a}
		}, want: "devp.it"},
		{name: "Fable own weekly", model: "claude-fable-5-1(high)", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(a, "7d_oi", now.Add(24*time.Hour), .86)
			return []*Auth{a, b}
		}, want: "amazingdesign"},
		{name: "Fable weekly fallback", model: "claude-fable-5-1", prepare: func(a, b *Auth) []*Auth {
			delete(b.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Reset")
			delete(b.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Utilization")
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "Fable expired weekly falls back", model: "claude-fable-5-1", prepare: func(a, b *Auth) []*Auth {
			useOrLoseTestWindow(b, "7d_oi", now, 1)
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Status"] = "rejected"
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "Fable weekly without utilisation falls back", model: "claude-fable-5-1", prepare: func(a, b *Auth) []*Auth {
			delete(b.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Utilization")
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Status"] = "rejected"
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "Fable stale observation falls back", model: "claude-fable-5-1", prepare: func(a, b *Auth) []*Auth {
			fable := useOrLoseTestAuth("stale", now.Add(-7*24*time.Hour), 8*24*time.Hour, .5)
			useOrLoseTestWindow(fable, "7d_oi", now.Add(24*time.Hour), 1)
			b.ModelStates = map[string]*ModelState{"claude-fable-5-1": {Quota: fable.Quota}}
			delete(b.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Reset")
			delete(b.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Utilization")
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "fresh shared sample beats old model sample", prepare: func(a, b *Auth) []*Auth {
			b.ModelStates = map[string]*ModelState{"claude-opus-5-5": {Quota: useOrLoseTestAuth("old", now.Add(-time.Minute), 7*24*time.Hour, .9).Quota}}
			return []*Auth{a, b}
		}, want: "devp.it"},
		{name: "Fable window retained across Opus observations", model: "claude-fable-5-1", prepare: func(a, b *Auth) []*Auth {
			fable := useOrLoseTestAuth("fable", now.Add(-time.Minute), 72*time.Hour, .4)
			useOrLoseTestWindow(fable, "7d_oi", now.Add(24*time.Hour), .3)
			a.ModelStates = map[string]*ModelState{"claude-fable-5-1": {Quota: fable.Quota}}
			delete(a.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Reset")
			delete(a.Quota.Signals, "Anthropic-Ratelimit-Unified-7d_oi-Utilization")
			return []*Auth{a, b}
		}, want: "amazingdesign"},
		{name: "disabled", prepare: func(a, b *Auth) []*Auth { b.Disabled = true; return []*Auth{nil, b, a} }, want: "amazingdesign"},
		{name: "credential cooldown", prepare: func(a, b *Auth) []*Auth {
			b.Quota.Exceeded = true
			b.Quota.NextRecoverAt = now.Add(time.Hour)
			return []*Auth{b, a}
		}, want: "amazingdesign"},
		{name: "Fable cooldown", model: "claude-fable-5-1", prepare: func(a, b *Auth) []*Auth {
			b.ModelStates = map[string]*ModelState{"claude-fable-5-1": {Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}}}
			return []*Auth{b, a}
		}, want: "amazingdesign"},
		{name: "unsampled tail", prepare: func(a, b *Auth) []*Auth { b.Quota = QuotaState{}; return []*Auth{b, a} }, want: "amazingdesign"},
		{name: "expired weekly becomes unsampled", prepare: func(a, b *Auth) []*Auth { useOrLoseTestWindow(b, "7d", now, 1); return []*Auth{b, a} }, want: "amazingdesign"},
		{name: "all unsampled preserve initial order", prepare: func(a, b *Auth) []*Auth { a.Quota = QuotaState{}; b.Quota = QuotaState{}; return []*Auth{b, a} }, want: "devp.it"},
		{name: "priority preserved", prepare: func(a, b *Auth) []*Auth { a.Attributes = map[string]string{"priority": "10"}; return []*Auth{a, b} }, want: "amazingdesign"},
		{name: "cap precedes priority", prepare: func(a, b *Auth) []*Auth {
			b.Attributes = map[string]string{"priority": "10"}
			useOrLoseTestWindow(b, "5h", now.Add(time.Hour), .95)
			return []*Auth{a, b}
		}, want: "amazingdesign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := useOrLoseTestAuth("amazingdesign", now, 105*time.Hour, .85)
			b := useOrLoseTestAuth("devp.it", now, 77*time.Hour+59*time.Minute, 0)
			useOrLoseTestWindow(a, "5h", now.Add(time.Hour), .2)
			useOrLoseTestWindow(a, "7d_oi", now.Add(105*time.Hour), .86)
			useOrLoseTestWindow(b, "7d_oi", now.Add(77*time.Hour+59*time.Minute), 0)
			auths := []*Auth{a, b}
			if tc.prepare != nil {
				auths = tc.prepare(a, b)
			}
			before := make([]*Auth, len(auths))
			for i, auth := range auths {
				if auth != nil {
					before[i] = auth.Clone()
				}
			}
			model := tc.model
			if model == "" {
				model = "claude-opus-5-5"
			}
			got, err := (&UseOrLoseSelector{}).pick(context.Background(), "claude", model, auths, now)
			if err != nil || got == nil || got.ID != tc.want {
				t.Fatalf("Pick = %v, %v; want %s", got, err, tc.want)
			}
			if !reflect.DeepEqual(auths, before) {
				t.Fatal("selection mutated auth snapshots")
			}
		})
	}
}

func TestUseOrLoseUnsampledProbes(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, allUnknown := range []bool{false, true} {
		t.Run(fmt.Sprint(allUnknown), func(t *testing.T) {
			selector := &UseOrLoseSelector{}
			known := useOrLoseTestAuth("known", now, 24*time.Hour, .5)
			if allUnknown {
				known.Quota = QuotaState{}
			}
			auths := []*Auth{known, {ID: "unknown-a", Provider: "claude"}, {ID: "unknown-b", Provider: "claude"}}
			for i, want := range []string{"known", "unknown-a", "unknown-b", "known"} {
				at := now
				if i > 0 {
					at = at.Add(useOrLoseProbeInterval)
				}
				got, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", auths, at)
				if err != nil || got.ID != want {
					t.Fatalf("selection %d = %v, %v; want %s", i, got, err, want)
				}
			}
		})
	}
}

func TestUseOrLoseMixedUnsampledProbes(t *testing.T) {
	t.Parallel()
	now := time.Now()
	selector := &UseOrLoseSelector{}
	auths := []*Auth{useOrLoseTestAuth("known", now, 24*time.Hour, .5), {ID: "unknown-a", Provider: " Claude "}, {ID: "unknown-b", Provider: "CODEX"}, {ID: "unsupported", Provider: "gemini"}}
	for _, tc := range []struct {
		at   time.Duration
		want string
	}{
		{0, "known"},
		{useOrLoseProbeInterval, "unknown-a"},
		{2 * useOrLoseProbeInterval, "unknown-b"},
		{2 * useOrLoseProbeInterval, "unknown-a"},
		{2 * useOrLoseProbeInterval, "known"},
	} {
		got, err := selector.pick(context.Background(), "mixed", "claude-opus-5-5", auths, now.Add(tc.at))
		if err != nil || got == nil || got.ID != tc.want {
			t.Fatalf("at %s: Pick = %v, %v; want %s", tc.at, got, err, tc.want)
		}
	}
	for key := range selector.probes {
		if key.provider != "claude" && key.provider != "codex" {
			t.Fatalf("unexpected probe provider: %q", key.provider)
		}
	}
}

func TestUseOrLoseManagerMixedUnsampledProbes(t *testing.T) {
	now := time.Now()
	selector := &UseOrLoseSelector{}
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
	model := "claude-opus-5-5"
	auths := []*Auth{useOrLoseTestAuth("mixed-known", now, 24*time.Hour, .5), {ID: "mixed-unknown-a", Provider: "claude"}, {ID: "mixed-unknown-b", Provider: "claude"}}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	if manager.useSchedulerFastPath() {
		t.Fatal("quota selector entered round-robin fast path")
	}
	counts := map[string]int{}
	for i := range 3 {
		if i > 0 {
			selector.mu.Lock()
			for key, entry := range selector.probes {
				entry.after = entry.after.Add(-useOrLoseProbeInterval)
				entry.seen = entry.seen.Add(-useOrLoseProbeInterval)
				entry.attempted = entry.attempted.Add(-useOrLoseProbeInterval)
				selector.probes[key] = entry
			}
			selector.mu.Unlock()
		}
		got, _, provider, err := manager.pickNextMixed(context.Background(), []string{"claude"}, model, cliproxyexecutor.Options{}, nil)
		if err != nil || got == nil || provider != "claude" {
			t.Fatalf("pickNextMixed = %v, %q, %v", got, provider, err)
		}
		if i == 0 && got.ID != "mixed-known" {
			t.Fatalf("initial selection = %s; want mixed-known", got.ID)
		}
		counts[got.ID]++
	}
	for _, auth := range auths {
		if counts[auth.ID] != 1 {
			t.Fatalf("mixed probe counts = %v; want each credential once", counts)
		}
	}
}

func TestUseOrLoseProbeSurvivesIdleAndIsModelScoped(t *testing.T) {
	t.Parallel()
	now := time.Now()
	selector := &UseOrLoseSelector{}
	auths := []*Auth{useOrLoseTestAuth("known", now, 24*time.Hour, .5), {ID: "unknown", Provider: "claude"}}
	for _, tc := range []struct {
		model string
		at    time.Time
		want  string
	}{
		{"claude-opus-5-5", now, "known"},
		{"claude-fable-5-1", now.Add(time.Minute), "known"},
		{"claude-opus-5-5", now.Add(20 * time.Minute), "unknown"},
	} {
		got, err := selector.pick(context.Background(), "claude", tc.model, auths, tc.at)
		if err != nil || got.ID != tc.want {
			t.Fatalf("Pick = %v, %v; want %s", got, err, tc.want)
		}
	}
}

func TestUseOrLoseConcurrentProbes(t *testing.T) {
	now := time.Now()
	selector := &UseOrLoseSelector{}
	auths := []*Auth{useOrLoseTestAuth("known", now, 24*time.Hour, .5), {ID: "unknown-a", Provider: "claude"}, {ID: "unknown-b", Provider: "claude"}}
	if _, err := selector.pick(context.Background(), "mixed", "claude-opus-5-5", auths, now); err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			got, err := selector.pick(context.Background(), "mixed", "claude-opus-5-5", auths, now.Add(time.Minute))
			if err != nil {
				t.Error(err)
				return
			}
			results <- got.ID
		})
	}
	wg.Wait()
	close(results)
	counts := map[string]int{}
	for id := range results {
		counts[id]++
	}
	if counts["unknown-a"] != 1 || counts["unknown-b"] != 1 || counts["known"] != 30 {
		t.Fatalf("concurrent probe counts = %v", counts)
	}
}

func TestUseOrLoseManagerEligibility(t *testing.T) {
	for _, tc := range []struct {
		name               string
		excluded, cooldown bool
		want               string
	}{
		{"earliest weekly", false, false, "early"},
		{"Fable excluded from credential catalog", true, false, "late"},
		{"Fable backend cooldown", false, true, "late"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			selector := &UseOrLoseSelector{}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
			model := "claude-fable-5-1"
			for _, id := range []string{"early", "late"} {
				reset := 48 * time.Hour
				if id == "early" {
					reset = 24 * time.Hour
				}
				a := useOrLoseTestAuth("use-or-lose-"+id, now, reset, .5)
				if tc.cooldown && id == "early" {
					a.ModelStates = map[string]*ModelState{model: {Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}}}
				}
				if _, err := manager.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				models := []*registry.ModelInfo{{ID: model}, {ID: "claude-opus-5-5"}}
				if tc.excluded && id == "early" {
					models = models[1:]
				}
				registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", models)
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
			}
			if manager.useSchedulerFastPath() {
				t.Fatal("quota selector entered round-robin fast path")
			}
			got, _, err := manager.pickNext(context.Background(), "claude", model, cliproxyexecutor.Options{}, nil)
			if err != nil || got == nil || got.ID != "use-or-lose-"+tc.want {
				t.Fatalf("pickNext = %v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestUseOrLoseConcurrentObservationAndSelection(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, &UseOrLoseSelector{}, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
	for _, id := range []string{"a", "b"} {
		if _, err := manager.Register(context.Background(), useOrLoseTestAuth(id, now, 24*time.Hour, .5)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for range 50 {
				if worker%2 == 0 {
					ctx := internallogging.WithResponseHeadersHolder(context.Background())
					internallogging.SetResponseHeaders(ctx, http.Header{
						"Anthropic-Ratelimit-Unified-7d-Reset":       {now.Add(24 * time.Hour).Format(time.RFC3339)},
						"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.4"},
					})
					manager.MarkResult(ctx, Result{AuthID: "a", Provider: "claude", Success: true})
				} else {
					got, _, err := manager.pickNext(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
					if err != nil || got == nil {
						t.Errorf("pickNext = %v, %v", got, err)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkUseOrLosePick(b *testing.B) {
	now := time.Now()
	auths := []*Auth{useOrLoseTestAuth("a", now, 48*time.Hour, .85), useOrLoseTestAuth("b", now, 24*time.Hour, 0)}
	selector := &UseOrLoseSelector{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, auths); err != nil {
			b.Fatal(err)
		}
	}
}

func TestUseOrLoseCodexWindows(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, capped := range []bool{false, true} {
		t.Run(fmt.Sprint(capped), func(t *testing.T) {
			auths := make([]*Auth, 0, 2)
			for i, id := range []string{"later", "earlier"} {
				used := "20"
				if capped && i == 1 {
					used = "95"
				}
				auths = append(auths, &Auth{ID: id, Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
					"X-Codex-Primary-Window-Minutes":      "300",
					"X-Codex-Primary-Used-Percent":        used,
					"X-Codex-Primary-Reset-After-Seconds": fmt.Sprint(3600 * (i + 1)),
					"X-Codex-Secondary-Window-Minutes":    "10080",
					"X-Codex-Secondary-Used-Percent":      "50",
					"X-Codex-Secondary-Reset-At":          now.Add(time.Duration(48-i*24) * time.Hour).Format(time.RFC3339),
				}}})
			}
			want := "earlier"
			if capped {
				want = "later"
			}
			got, err := (&UseOrLoseSelector{}).pick(context.Background(), "codex", "model", auths, now)
			if err != nil || got.ID != want {
				t.Fatalf("Pick = %v, %v; want %s", got, err, want)
			}
		})
	}
}

func TestUseOrLoseWeeklyLimitExpiry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, reset := range []time.Time{now.Add(time.Minute), now} {
		auth := useOrLoseTestAuth("limited", now, 24*time.Hour, 1)
		useOrLoseTestWindow(auth, "7d", reset, 1)
		auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
		for _, hasUsage := range []bool{true, false} {
			if !hasUsage {
				delete(auth.Quota.Signals, "Anthropic-Ratelimit-Unified-7d-Utilization")
			}
			if got := useOrLoseSampleFor(auth, "claude-opus-5-5", now).atLimit; got != reset.After(now) {
				t.Fatalf("reset %s, hasUsage %t: atLimit = %t", reset, hasUsage, got)
			}
		}
	}
}

func TestUseOrLoseCodexWeeklyLimit(t *testing.T) {
	t.Parallel()
	now := time.Now()
	limited := &Auth{ID: "limited", Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
		"X-Codex-Secondary-Window-Minutes": "10080",
		"X-Codex-Secondary-Used-Percent":   "100",
		"X-Codex-Secondary-Reset-At":       now.Add(24 * time.Hour).Format(time.RFC3339),
	}}}
	room := limited.Clone()
	room.ID = "room"
	room.Quota.Signals["X-Codex-Secondary-Used-Percent"] = "50"
	room.Quota.Signals["X-Codex-Secondary-Reset-At"] = now.Add(48 * time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		auths []*Auth
		want  string
	}{{[]*Auth{limited, room}, "room"}, {[]*Auth{limited}, "limited"}} {
		got, err := (&UseOrLoseSelector{}).pick(context.Background(), "mixed", "model", tc.auths, now)
		if err != nil || got == nil || got.ID != tc.want {
			t.Fatalf("Pick = %v, %v; want %s", got, err, tc.want)
		}
	}
}

func TestUseOrLoseEqualWeeksFollowUtilisationUpdates(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a := useOrLoseTestAuth("a", now, 24*time.Hour, .1)
	b := useOrLoseTestAuth("b", now, 24*time.Hour, .2)
	selector := &UseOrLoseSelector{}
	for _, tc := range []struct {
		used float64
		want string
	}{{.1, "a"}, {.3, "b"}, {.15, "a"}} {
		useOrLoseTestWindow(a, "7d", now.Add(24*time.Hour), tc.used)
		got, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", []*Auth{a, b}, now)
		if err != nil || got.ID != tc.want {
			t.Fatalf("Pick = %v, %v; want %s", got, err, tc.want)
		}
	}
}

func TestUseOrLoseUnsampledProbeRespectsShortCap(t *testing.T) {
	t.Parallel()
	now := time.Now()
	known := useOrLoseTestAuth("known", now, 24*time.Hour, .5)
	unknown := useOrLoseTestAuth("unknown", now, 48*time.Hour, .2)
	delete(unknown.Quota.Signals, "Anthropic-Ratelimit-Unified-7d-Utilization")
	useOrLoseTestWindow(unknown, "5h", now.Add(2*time.Minute), .95)
	selector := &UseOrLoseSelector{}
	for _, tc := range []struct {
		at   time.Duration
		want string
	}{{0, "known"}, {time.Minute, "known"}, {2 * time.Minute, "known"}, {3 * time.Minute, "unknown"}} {
		got, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", []*Auth{known, unknown}, now.Add(tc.at))
		if err != nil || got.ID != tc.want {
			t.Fatalf("at %s: Pick = %v, %v; want %s", tc.at, got, err, tc.want)
		}
	}
}

func TestUseOrLoseUnsupportedProviderKeepsFillFirst(t *testing.T) {
	t.Parallel()
	selector := &UseOrLoseSelector{}
	now := time.Now()
	auths := []*Auth{{ID: "z", Provider: "gemini"}, {ID: "a", Provider: "gemini"}}
	for _, provider := range []string{"gemini", "mixed"} {
		for _, at := range []time.Time{now, now.Add(time.Hour)} {
			got, err := selector.pick(context.Background(), provider, "model", auths, at)
			if err != nil || got == nil || got.ID != "z" {
				t.Fatalf("Pick = %v, %v; want z", got, err)
			}
		}
	}
	if len(selector.probes) != 0 {
		t.Fatalf("unsupported provider probes = %v", selector.probes)
	}
}

func TestUseOrLoseSessionAffinityWeeklyLimit(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5-1"} {
		t.Run(model, func(t *testing.T) {
			now := time.Now()
			selector := NewSessionAffinitySelector(&UseOrLoseSelector{})
			defer selector.Stop()
			opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"use-or-lose-weekly-bound"}`)}
			bound := useOrLoseTestAuth("bound", now, 24*time.Hour, .5)
			other := useOrLoseTestAuth("other", now, 48*time.Hour, .2)
			window := "7d"
			if model == "claude-fable-5-1" {
				window = "7d_oi"
			}
			for _, tc := range []struct {
				used float64
				want string
			}{{.5, "bound"}, {1, "other"}} {
				useOrLoseTestWindow(bound, window, now.Add(24*time.Hour), tc.used)
				got, err := selector.Pick(context.Background(), "mixed", model, opts, []*Auth{bound, other})
				if err != nil || got == nil || got.ID != tc.want {
					t.Fatalf("Pick = %v, %v; want %s", got, err, tc.want)
				}
			}
		})
	}
}

func TestUseOrLoseSessionAffinityCap(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelector(&UseOrLoseSelector{})
	defer selector.Stop()
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"use-or-lose-bound"}`)}
	bound := useOrLoseTestAuth("bound", now, 24*time.Hour, .5)
	other := useOrLoseTestAuth("other", now, 48*time.Hour, .2)
	for _, tc := range []struct {
		used float64
		want string
	}{{.5, "bound"}, {.95, "other"}} {
		useOrLoseTestWindow(bound, "5h", now.Add(time.Hour), tc.used)
		got, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", opts, []*Auth{bound, other})
		if err != nil || got.ID != tc.want {
			t.Fatalf("Pick = %v, %v; want %s", got, err, tc.want)
		}
	}
}
