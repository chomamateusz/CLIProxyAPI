package auth

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type fleetManagerFixture struct {
	manager  *Manager
	selector *UseOrLoseSelector
	affinity *SessionAffinitySelector
	auths    []*Auth
	model    string
	opts     cliproxyexecutor.Options
}

func newFleetManager(t *testing.T, providers []string, model string, affinity bool, rebind string) *fleetManagerFixture {
	t.Helper()
	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(location)
	f := &fleetManagerFixture{selector: &UseOrLoseSelector{ShortWindowCapPct: 95}, model: model, opts: cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"fleet-session"}`), Metadata: map[string]any{}}}
	var selector Selector = f.selector
	if affinity {
		f.affinity = NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: selector, RebindOn: rebind})
		t.Cleanup(f.affinity.Stop)
		selector = f.affinity
	}
	f.manager = NewManager(nil, selector, nil)
	f.manager.SetConfig(&config.Config{Routing: config.RoutingConfig{Strategy: "use-or-lose", SessionAffinity: affinity, UseOrLose: config.UseOrLoseConfig{ShortWindowCapPct: 95}}, SaveCooldownStatus: false})
	for _, provider := range providers {
		f.manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	}
	for _, account := range fleetAccounts {
		provider := "claude"
		if account.id == "codex" {
			provider = "codex"
		}
		include := false
		for _, candidate := range providers {
			if candidate == provider {
				include = true
			}
		}
		if !include {
			continue
		}
		if account.id == "devp-two" {
			account.short = true
		}
		auth := fleetAuth(account, now, .2, .2, .2)
		auth.ID = t.Name() + "/" + auth.ID
		if _, err := f.manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}, {ID: fleetFable}, {ID: fleetOther}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		f.auths = append(f.auths, auth)
	}
	sort.SliceStable(f.auths, func(i, j int) bool {
		return f.selector.sampleFor(f.auths[i], model, now).window.reset.Before(f.selector.sampleFor(f.auths[j], model, now).window.reset)
	})
	return f
}

func (f *fleetManagerFixture) pick(path string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, error) {
	if path == "mixed" {
		a, _, _, err := f.manager.pickNextMixed(context.Background(), []string{"claude", "codex"}, f.model, opts, tried)
		return a, err
	}
	a, _, err := f.manager.pickNext(context.Background(), path, f.model, opts, tried)
	return a, err
}

func (f *fleetManagerFixture) update(t *testing.T, auth *Auth) {
	t.Helper()
	if _, err := f.manager.Update(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
}

func (f *fleetManagerFixture) expireCooldown(id string) {
	f.manager.mu.Lock()
	auth := f.manager.auths[id]
	auth.NextRetryAfter = time.Now().Add(-time.Second)
	auth.Quota.NextRecoverAt = auth.NextRetryAfter
	for _, state := range auth.ModelStates {
		state.NextRetryAfter = auth.NextRetryAfter
		state.Quota.NextRecoverAt = auth.NextRetryAfter
	}
	f.manager.mu.Unlock()
	f.manager.syncScheduler()
}

func fleetRequirePick(t *testing.T, f *fleetManagerFixture, path string, opts cliproxyexecutor.Options, tried map[string]struct{}, want, reason string) {
	t.Helper()
	got, err := f.pick(path, opts, tried)
	if err != nil || got == nil || got.ID != want {
		t.Fatalf("got %s, %v; want %s: %s", fleetPickedID(got), err, want, reason)
	}
}

func fleetHeaders(auth *Auth) context.Context {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	for name, value := range auth.Quota.Signals {
		headers.Set(name, value)
	}
	internallogging.SetResponseHeaders(ctx, headers)
	return ctx
}

func TestFleetManagerFableAliasAndExclusion(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, path := range []string{"claude", "mixed"} {
			for _, tc := range []struct {
				name, model          string
				excluded, sharedFull bool
			}{
				{"canonical-own-week", fleetFable, false, false},
				{"alias-without-fable-own-week", "premium-writer", false, false},
				{"canonical-shared-week", fleetFable, false, true},
				{"alias-without-fable-shared-week", "premium-writer", false, true},
				{"oauth-excluded-on-one-account", fleetFable, true, false},
			} {
				t.Run(fmt.Sprintf("affinity=%t/%s/%s", affinity, path, tc.name), func(t *testing.T) {
					f := newFleetManager(t, []string{"claude"}, tc.model, affinity, SessionAffinityRebindOnQuota)
					if tc.model != fleetFable {
						f.manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{"claude": {{Name: fleetFable, Alias: tc.model, Fork: true}}})
					}
					limited := f.auths[0].Clone()
					window := "7d_oi"
					if tc.sharedFull {
						window = "7d"
					}
					reset := f.selector.sampleFor(limited, fleetFable, time.Now()).window.reset
					useOrLoseTestWindow(limited, window, reset, 1)
					if tc.excluded {
						registry.GetGlobalRegistry().RegisterClient(limited.ID, "claude", []*registry.ModelInfo{{ID: fleetOther}})
					} else {
						f.update(t, limited)
					}
					fleetRequirePick(t, f, path, f.opts, nil, f.auths[1].ID, "canonical and aliased Fable must honor shared week, Fable week and the filtered OAuth catalog")
				})
			}
		}
	}
}

func TestFleetManagerUnsampledBoundedProbe(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, path := range []string{"claude", "codex", "mixed"} {
			t.Run(fmt.Sprintf("affinity=%t/%s", affinity, path), func(t *testing.T) {
				providers := []string{path}
				model := fleetOther
				if path == "codex" {
					model = "fleet-codex-model"
				}
				if path == "mixed" {
					providers = []string{"claude", "codex"}
				}
				f := newFleetManager(t, providers, model, affinity, SessionAffinityRebindOnQuota)
				unknown := f.auths[len(f.auths)-1].Clone()
				if path == "mixed" {
					for _, auth := range f.auths {
						if auth.Provider == "codex" {
							unknown = auth.Clone()
						}
					}
				}
				unknown.Quota = QuotaState{}
				f.update(t, unknown)
				seen := false
				for request := range 3 {
					if request > 0 {
						f.selector.mu.Lock()
						for key, entry := range f.selector.probes {
							entry.after = time.Now().Add(-time.Nanosecond)
							f.selector.probes[key] = entry
						}
						f.selector.mu.Unlock()
					}
					opts := cliproxyexecutor.Options{OriginalRequest: []byte(fmt.Sprintf(`{"session_id":"probe-%d"}`, request)), Metadata: map[string]any{}}
					got, err := f.pick(path, opts, nil)
					if err != nil || got == nil {
						t.Fatalf("probe request %d: %v", request, err)
					}
					if got.ID == unknown.ID {
						seen = true
						break
					}
				}
				if !seen {
					t.Fatal("new credential starved beyond two one-minute probe intervals")
				}
				sampled := unknown.Clone()
				sampled.Quota = QuotaState{ObservedAt: time.Now(), Signals: map[string]string{}}
				if unknown.Provider == "codex" {
					sampled.Quota.Signals = map[string]string{"X-Codex-Secondary-Window-Minutes": "10080", "X-Codex-Secondary-Used-Percent": "10", "X-Codex-Secondary-Reset-At": time.Now().Add(time.Hour).Format(time.RFC3339)}
				} else {
					useOrLoseTestWindow(sampled, "7d", time.Now().Add(time.Hour), .1)
				}
				f.manager.MarkResult(fleetHeaders(sampled), Result{AuthID: unknown.ID, Provider: unknown.Provider, Model: model, Success: true})
				fresh := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"after-probe-headers"}`), Metadata: map[string]any{}}
				fleetRequirePick(t, f, path, fresh, nil, unknown.ID, "after the bounded probe, fresh upstream headers make the credential the earliest reset")
			})
		}
	}
}

func TestFleetManagerCodexCreditsAndBackendRefusal(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, path := range []string{"codex", "mixed"} {
		for _, weekly := range []float64{0, .4, .94, .95, 1} {
			t.Run(fmt.Sprintf("%s/weekly=%g", path, weekly), func(t *testing.T) {
				f := newFleetManager(t, []string{"codex"}, "fleet-codex-model", true, SessionAffinityRebindOnQuota)
				auth := f.auths[0].Clone()
				auth.Quota.Signals["X-Codex-Secondary-Used-Percent"] = fmt.Sprint(weekly * 100)
				f.update(t, auth)
				fleetRequirePick(t, f, path, f.opts, nil, auth.ID, "the sole ChatGPT Pro credential remains usable on credits at exhausted plan quota")
				retry := time.Hour
				f.manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: f.model, Options: f.opts, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: "backend refuses plan and credits"}})
				if got, err := f.pick(path, f.opts, nil); err == nil || got != nil {
					t.Fatalf("backend refusal must exclude the only credential; got %s, %v", fleetPickedID(got), err)
				}
			})
		}
	}
}

func TestFleetManagerAffinityTransitions(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, path := range []string{"claude", "mixed"} {
		for _, event := range []string{"short-cap", "shared-week", "fable-week", "429-reset", "403-model-excluded", "catalog-excluded"} {
			t.Run(path+"/"+event, func(t *testing.T) {
				f := newFleetManager(t, []string{"claude"}, fleetFable, true, SessionAffinityRebindOnQuota)
				bound, other := f.auths[0], f.auths[1]
				var seedTried map[string]struct{}
				if event == "short-cap" {
					for _, auth := range f.auths {
						if auth.Metadata["email"] == "kontakt@amazingdesign" {
							bound = auth
						}
					}
					seedTried = map[string]struct{}{}
					for _, auth := range f.auths {
						if auth.ID != bound.ID {
							seedTried[auth.ID] = struct{}{}
						}
					}
					for _, auth := range f.auths {
						if auth.ID != bound.ID {
							other = auth
							break
						}
					}
				}
				fleetRequirePick(t, f, path, f.opts, seedTried, bound.ID, "seed a healthy session; the short-cap case binds amazing, never the no-5h devp-one")
				earlier := other.Clone()
				useOrLoseTestWindow(earlier, "7d", time.Now().Add(time.Hour), .1)
				useOrLoseTestWindow(earlier, "7d_oi", time.Now().Add(time.Hour), .1)
				f.update(t, earlier)
				fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "healthy binding survives a competitor's earlier reset")
				changed := bound.Clone()
				reset := time.Now().Add(24 * time.Hour)
				switch event {
				case "short-cap":
					useOrLoseTestWindow(changed, "5h", reset, .95)
					f.update(t, changed)
				case "shared-week":
					useOrLoseTestWindow(changed, "7d", reset, 1)
					f.update(t, changed)
				case "fable-week":
					useOrLoseTestWindow(changed, "7d_oi", reset, 1)
					f.update(t, changed)
				case "catalog-excluded":
					registry.GetGlobalRegistry().RegisterClient(bound.ID, "claude", []*registry.ModelInfo{{ID: fleetOther}})
				default:
					retry := time.Hour
					status, message := 429, "weekly quota reset"
					if event == "403-model-excluded" {
						status, message = 403, "model_excluded"
					}
					f.manager.MarkResult(context.Background(), Result{AuthID: bound.ID, Provider: "claude", Model: f.model, Options: f.opts, RetryAfter: &retry, Error: &Error{HTTPStatus: status, Message: message}})
					if event == "429-reset" {
						f.manager.mu.RLock()
						state := f.manager.auths[bound.ID].ModelStates[f.model]
						deadline := state.Quota.NextRecoverAt
						f.manager.mu.RUnlock()
						if deadline.Before(time.Now().Add(59 * time.Minute)) {
							t.Fatalf("429 reset deadline not retained: %s", deadline)
						}
					}
				}
				fleetRequirePick(t, f, path, f.opts, nil, other.ID, "only cap, exhaustion, exclusion or backend cooldown moves the session")
				registry.GetGlobalRegistry().RegisterClient(bound.ID, "claude", []*registry.ModelInfo{{ID: f.model}, {ID: fleetOther}})
				f.update(t, bound.Clone())
				f.expireCooldown(bound.ID)
				tried := map[string]struct{}{}
				for _, auth := range f.auths {
					if auth.ID != bound.ID {
						tried[auth.ID] = struct{}{}
					}
				}
				fresh := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"recovered-credential-check"}`), Metadata: map[string]any{}}
				fleetRequirePick(t, f, path, fresh, tried, bound.ID, "the original credential is usable again after the constraint clears")
				for range 3 {
					fleetRequirePick(t, f, path, f.opts, nil, other.ID, "clearing the previous credential must not immediately flip the binding back")
				}
			})
		}
	}
}

func TestFleetManagerAffinityTransientRetryDoesNotRebind(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, rebind := range []string{"", SessionAffinityRebindOnQuota} {
		for _, path := range []string{"claude", "mixed"} {
			t.Run(fmt.Sprintf("rebind=%s/%s", rebind, path), func(t *testing.T) {
				f := newFleetManager(t, []string{"claude"}, fleetOther, true, rebind)
				bound, other := f.auths[0], f.auths[1]
				fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "initial healthy binding")
				f.manager.MarkResult(context.Background(), Result{AuthID: bound.ID, Provider: "claude", Model: f.model, Options: f.opts, Error: &Error{HTTPStatus: 503, Message: "transient upstream failure"}})
				got, err := f.pick(path, f.opts, map[string]struct{}{bound.ID: {}})
				if err != nil || got == nil {
					t.Fatalf("transient request must fail over: %v", err)
				}
				if got.ID != other.ID {
					t.Fatalf("retry got %s; want %s", got.ID, other.ID)
				}
				f.manager.MarkResult(context.Background(), Result{AuthID: got.ID, Provider: "claude", Model: f.model, Options: f.opts, Success: true})
				f.expireCooldown(bound.ID)
				fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "a successful transient 5xx retry is not a permanent session migration")
			})
		}
	}
}

func TestFleetManagerCooldownExpiryAndRestart(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, path := range []string{"claude", "mixed"} {
		t.Run(path, func(t *testing.T) {
			f := newFleetManager(t, []string{"claude"}, fleetOther, false, "")
			bound, other := f.auths[0], f.auths[1]
			retry := time.Hour
			f.manager.MarkResult(context.Background(), Result{AuthID: bound.ID, Provider: "claude", Model: f.model, RetryAfter: &retry, CredentialScope: true, Error: &Error{HTTPStatus: 429, Message: "unified quota reset"}})
			fleetRequirePick(t, f, path, f.opts, nil, other.ID, "backend 429 excludes credential until reset")
			f.expireCooldown(bound.ID)
			fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "expired cooldown restores earliest weekly credential without clearing its usage headers")
			f.manager.MarkResult(context.Background(), Result{AuthID: bound.ID, Provider: "claude", Model: f.model, RetryAfter: &retry, CredentialScope: true, Error: &Error{HTTPStatus: 429, Message: "quota refused before restart"}})
			fleetRequirePick(t, f, path, f.opts, nil, other.ID, "the old process has an active cooldown immediately before restart")
			restarted := NewManager(nil, &UseOrLoseSelector{}, nil)
			restarted.SetConfig(&config.Config{SaveCooldownStatus: false})
			restarted.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
			for _, auth := range f.auths {
				if _, err := restarted.Register(context.Background(), auth.Clone()); err != nil {
					t.Fatal(err)
				}
			}
			got, _, err := restarted.pickNext(context.Background(), "claude", f.model, cliproxyexecutor.Options{}, nil)
			if err != nil || got == nil || got.ID != bound.ID {
				t.Fatalf("restart without cooldown persistence: got %s, %v; want %s", fleetPickedID(got), err, bound.ID)
			}
		})
	}
}

func TestFleetManagerAffinityAllCappedDoesNotFlipFlop(t *testing.T) {
	for _, path := range []string{"claude", "mixed"} {
		t.Run(path, func(t *testing.T) {
			f := newFleetManager(t, []string{"claude"}, fleetOther, true, SessionAffinityRebindOnQuota)
			for _, auth := range f.auths {
				a := auth.Clone()
				window := "5h"
				if a.Metadata["email"] == "one+claude@devp.it" {
					window = "7d"
				}
				reset := f.selector.sampleFor(a, fleetOther, time.Now()).window.reset
				useOrLoseTestWindow(a, window, reset, 1)
				f.update(t, a)
			}
			want := f.auths[0].ID
			for request := range 20 {
				fleetRequirePick(t, f, path, f.opts, nil, want, "all-capped fallback remains deterministic across repeated requests")
				a := f.auths[1].Clone()
				reset := f.selector.sampleFor(a, fleetOther, time.Now()).window.reset
				if request%2 == 0 {
					reset = time.Now().Add(time.Hour)
				}
				if a.Metadata["email"] == "one+claude@devp.it" {
					useOrLoseTestWindow(a, "7d", reset, 1)
				} else {
					useOrLoseTestWindow(a, "7d", reset, .1)
					useOrLoseTestWindow(a, "5h", time.Now().Add(time.Hour), 1)
				}
				f.update(t, a)
			}
		})
	}
}

func TestFleetManagerAffinityCapHotReloadPreservesBindings(t *testing.T) {
	for _, path := range []string{"claude", "mixed"} {
		t.Run(path, func(t *testing.T) {
			f := newFleetManager(t, []string{"claude"}, fleetOther, true, SessionAffinityRebindOnQuota)
			bound, other := f.auths[0], f.auths[1]
			fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "initial binding")
			a := other.Clone()
			useOrLoseTestWindow(a, "7d", time.Now().Add(time.Hour), .1)
			f.update(t, a)
			fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "healthy session remains bound before config reload")
			replacement := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &UseOrLoseSelector{ShortWindowCapPct: 80}, RebindOn: SessionAffinityRebindOnQuota})
			t.Cleanup(replacement.Stop)
			f.manager.SetSelector(replacement)
			fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "service cap-only reload replaces its selector; existing healthy session bindings must survive")
		})
	}
}

func TestFleetManagerAffinityFiftyConcurrentSessions(t *testing.T) {
	for _, path := range []string{"claude", "mixed"} {
		t.Run(path, func(t *testing.T) {
			f := newFleetManager(t, []string{"claude"}, fleetOther, true, SessionAffinityRebindOnQuota)
			opts := make([]cliproxyexecutor.Options, 50)
			expected := make([]string, 50)
			for session := range 50 {
				opts[session] = cliproxyexecutor.Options{OriginalRequest: []byte(fmt.Sprintf(`{"session_id":"fleet-concurrent-%d"}`, session)), Metadata: map[string]any{}}
				target := f.auths[session%len(f.auths)]
				expected[session] = target.ID
				tried := map[string]struct{}{}
				for _, auth := range f.auths {
					if auth.ID != target.ID {
						tried[auth.ID] = struct{}{}
					}
				}
				fleetRequirePick(t, f, path, opts[session], tried, target.ID, "seed distinct session bindings")
			}
			phase := func() {
				t.Helper()
				var wg sync.WaitGroup
				for session := range 50 {
					wg.Go(func() {
						for range 10 {
							got, err := f.pick(path, opts[session], nil)
							if err != nil || got == nil || got.ID != expected[session] {
								t.Errorf("session %d: got %s, %v; want stable %s", session, fleetPickedID(got), err, expected[session])
								return
							}
						}
					})
				}
				wg.Wait()
			}
			phase()
			blocked := f.auths[0].Clone()
			useOrLoseTestWindow(blocked, "7d", time.Now().Add(24*time.Hour), 1)
			f.update(t, blocked)
			for session := range 50 {
				if expected[session] == blocked.ID {
					expected[session] = f.auths[1].ID
				}
			}
			phase()
			f.update(t, f.auths[0].Clone())
			phase()
		})
	}
}

func TestFleetManagerMixedCodexCreditsSurvivePlanBars(t *testing.T) {
	for _, weekly := range []float64{0, .4, .94, .95, 1} {
		for _, short := range []float64{0, .4, .94, .95, 1} {
			t.Run(fmt.Sprintf("weekly=%g/5h=%g", weekly, short), func(t *testing.T) {
				f := newFleetManager(t, []string{"claude", "codex"}, "fleet-shared-route", true, SessionAffinityRebindOnQuota)
				var codexID string
				for _, original := range f.auths {
					auth := original.Clone()
					if auth.Provider == "codex" {
						codexID = auth.ID
						auth.Quota.Signals["X-Codex-Secondary-Used-Percent"] = fmt.Sprint(weekly * 100)
						auth.Quota.Signals["X-Codex-Primary-Window-Minutes"] = "300"
						auth.Quota.Signals["X-Codex-Primary-Used-Percent"] = fmt.Sprint(short * 100)
						auth.Quota.Signals["X-Codex-Primary-Reset-At"] = time.Now().Add(time.Hour).Format(time.RFC3339)
					} else {
						reset := f.selector.sampleFor(auth, f.model, time.Now()).window.reset
						useOrLoseTestWindow(auth, "7d", reset, 1)
						auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
					}
					f.update(t, auth)
				}
				fleetRequirePick(t, f, "mixed", f.opts, nil, codexID, "plan bars do not exhaust available credits; rejected Claude accounts are not better alternatives")
			})
		}
	}
}

func TestFleetManagerAmazingFableExcluded(t *testing.T) {
	for _, model := range []string{fleetFable, "premium-writer"} {
		t.Run(model, func(t *testing.T) {
			f := newFleetManager(t, []string{"claude"}, model, true, SessionAffinityRebindOnQuota)
			if model != fleetFable {
				f.manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{"claude": {{Name: fleetFable, Alias: model, Fork: true}}})
			}
			var allowed []*Auth
			for _, auth := range f.auths {
				if auth.Metadata["email"] == "kontakt@amazingdesign" {
					registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: fleetOther}})
				} else {
					allowed = append(allowed, auth)
				}
			}
			fleetRequirePick(t, f, "mixed", f.opts, nil, allowed[0].ID, "amazing's OAuth catalog excludes Fable, including aliases without the word fable")
		})
	}
}
