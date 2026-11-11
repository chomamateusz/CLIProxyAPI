package auth

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const recoveryFable = "claude-fable-5-2"
const recoveryOther = "claude-opus-5-5"

func recoveryHeaders(now time.Time, weekly, fable float64) http.Header {
	headers := make(http.Header)
	for name, usage := range map[string]float64{"7d": weekly, "7d_oi": fable} {
		prefix := "Anthropic-Ratelimit-Unified-" + name + "-"
		headers.Set(prefix+"Utilization", fmt.Sprint(usage))
		headers.Set(prefix+"Reset", fmt.Sprint(now.Add(7*24*time.Hour).Unix()))
		status := "allowed"
		if usage >= 1 {
			status = "rejected"
		}
		headers.Set(prefix+"Status", status)
	}
	return headers
}

func recoveryRefuse(m *Manager, a *Auth, model string, credential bool, headers http.Header) {
	observed := a.Clone()
	observed.Quota.Signals = make(map[string]string)
	for key := range headers {
		observed.Quota.Signals[key] = headers.Get(key)
	}
	retry := 7 * 24 * time.Hour
	m.MarkResult(fleetHeaders(observed), Result{AuthID: a.ID, Provider: a.Provider, Model: model, CredentialScope: credential, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: "quota exhausted"}})
}

func recoveryManager(t *testing.T, affinity bool) (*Manager, *Auth) {
	t.Helper()
	var selector Selector = &UseOrLoseSelector{}
	if affinity {
		sticky := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: selector})
		t.Cleanup(sticky.Stop)
		selector = sticky
	}
	m := NewManager(nil, selector, nil)
	a := samplerRegister(t, m, &Auth{ID: "quota-recovery-a", Provider: "claude", ModelStates: map[string]*ModelState{recoveryOther: {Status: StatusActive}}})
	m.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
	registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: recoveryFable}, {ID: recoveryOther}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	recoveryRefuse(m, a, recoveryFable, true, recoveryHeaders(time.Now(), 1, 1))
	return m, a
}

func recoveryCurrent(t *testing.T, m *Manager, id string) *Auth {
	t.Helper()
	a, ok := m.GetByID(id)
	if !ok || a == nil {
		t.Fatal("credential missing")
	}
	return a
}

func TestQuotaRecoveryIncidentReplay(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, shadow := range []bool{false, true} {
			t.Run(fmt.Sprintf("affinity=%t/shadow=%t", affinity, shadow), func(t *testing.T) {
				m, a := recoveryManager(t, affinity)
				if shadow {
					recoveryRefuse(m, a, recoveryFable, false, recoveryHeaders(time.Now(), 1, 1))
				}
				b := samplerRegister(t, m, &Auth{ID: "quota-recovery-b", Provider: "claude"})
				registry.GetGlobalRegistry().RegisterClient(b.ID, b.Provider, []*registry.ModelInfo{{ID: recoveryFable}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(b.ID) })
				recoveryRefuse(m, b, recoveryFable, true, recoveryHeaders(time.Now(), 1, 1))
				store := &recordingCooldownStateStore{}
				m.SetCooldownStateStore(store)
				m.persistCooldownStates(context.Background())
				if len(store.savedRecords()) == 0 {
					t.Fatal("refusal cooldown was not persisted")
				}
				hook := logtest.NewLocal(log.StandardLogger())
				t.Cleanup(hook.Reset)
				before := recoveryCurrent(t, m, a.ID)
				if before.Quota.Reason != "credential_quota" || !before.ModelStates[recoveryFable].Quota.Exceeded || m.refusals.latest(a.ID, recoveryFable, "") == nil {
					t.Fatal("incident setup did not install both cooldowns and refusal")
				}
				started := time.Now().Add(time.Second)
				if !m.applyQuotaSample(context.Background(), a, recoveryHeaders(started, 0, 0), started) {
					t.Fatal("sample was not applied")
				}
				got := recoveryCurrent(t, m, a.ID)
				if got.Status != StatusActive || got.Unavailable || got.LastError != nil || !got.NextRetryAfter.IsZero() || got.Quota.Exceeded || len(CooldownSnapshotForAuth(got, time.Now())) != 0 {
					t.Fatalf("credential not fully recovered: %+v", got)
				}
				for model, state := range got.ModelStates {
					if !modelStateIsClean(state) || state.credentialQuotaPropagated || state.credentialQuotaShadow != nil {
						t.Fatalf("model %s retained a cooldown: %+v", model, state)
					}
				}
				if m.refusals.latest(a.ID, recoveryFable, "") != nil {
					t.Fatal("stored provider refusal survived recovery")
				}
				for _, record := range store.savedRecords() {
					if record.AuthID == a.ID {
						t.Fatalf("recovered cooldown persisted: %+v", record)
					}
				}
				if unhealthy, _, _ := fleetCredentialHealth(got, time.Now()); unhealthy {
					t.Fatal("fleet monitor still considers credential unhealthy")
				}
				lifts := 0
				for _, entry := range hook.AllEntries() {
					if entry.Level == log.InfoLevel && strings.Contains(entry.Message, "reason=sample_allowed") {
						lifts++
					}
				}
				if lifts != 2 {
					t.Fatalf("lift log lines = %d, want 2", lifts)
				}
				picked, _, _, err := m.pickNextMixed(context.Background(), []string{"claude"}, recoveryFable, cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {"recovered-session"}}}, nil)
				if err != nil || picked == nil || picked.ID != a.ID {
					t.Fatalf("recovered credential not selected: %s, %v", authID(picked), err)
				}
			})
		}
	}
}

func TestQuotaRecoveryPartial(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		t.Run(fmt.Sprint(shadow), func(t *testing.T) {
			m, a := recoveryManager(t, false)
			if shadow {
				recoveryRefuse(m, a, recoveryFable, false, recoveryHeaders(time.Now(), 1, 1))
			}
			started := time.Now().Add(time.Second)
			m.applyQuotaSample(context.Background(), a, recoveryHeaders(started, 0, 1), started)
			got := recoveryCurrent(t, m, a.ID)
			if got.Quota.Reason == "credential_quota" {
				t.Fatal("shared credential cooldown survived shared reset")
			}
			if blocked, _, _ := isAuthBlockedForModel(got, recoveryOther, time.Now()); blocked {
				t.Fatal("shared reset did not release other models")
			}
			if blocked, _, _ := isAuthBlockedForModel(got, recoveryFable, time.Now()); !blocked {
				t.Fatal("Fable cooldown cleared while Fable remains rejected")
			}
			if !got.ModelStates[recoveryFable].Quota.Exceeded || got.ModelStates[recoveryFable].Quota.Reason != "quota" {
				t.Fatal("independent Fable quota state lost")
			}
		})
	}
}

func TestQuotaRecoveryRefusesUnsafeObservations(t *testing.T) {
	for _, scenario := range []string{"stale", "equal", "full", "rejected", "response-only-allowed", "missing-usage", "missing-weekly", "short-rejected", "short-stale", "terminal-unauthorized", "disabled", "status-disabled"} {
		t.Run(scenario, func(t *testing.T) {
			m, a := recoveryManager(t, false)
			started := time.Now().Add(time.Second)
			headers := recoveryHeaders(started, 0, 0)
			m.mu.Lock()
			live := m.auths[a.ID]
			switch scenario {
			case "stale":
				started = live.UpdatedAt.Add(-time.Second)
			case "equal":
				started = live.UpdatedAt
			case "full":
				headers = recoveryHeaders(started, 1, 1)
				headers.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
				headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "allowed")
			case "rejected":
				headers.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
				headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
			case "response-only-allowed":
				live.Quota.ObserveResponseHeadersForProvider("claude", headers, started.Add(time.Second))
				headers = recoveryHeaders(started, 1, 1)
			case "missing-usage":
				headers.Del("Anthropic-Ratelimit-Unified-7d-Utilization")
				headers.Del("Anthropic-Ratelimit-Unified-7d_oi-Utilization")
			case "missing-weekly":
				headers = make(http.Header)
				headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0")
			case "short-rejected", "short-stale":
				live.sampledQuota.ObservedAt = live.UpdatedAt.Add(-time.Second)
				live.sampledQuota.Signals = map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1", "Anthropic-Ratelimit-Unified-5h-Status": "rejected"}
				if scenario == "short-rejected" {
					headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "1")
					headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
				}
				headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
			case "terminal-unauthorized":
				live.Status = StatusError
				live.Unavailable = true
				live.NextRetryAfter = time.Time{}
				live.NextRefreshAfter = time.Time{}
				live.LastError = &Error{HTTPStatus: 401, Code: "unauthorized", Message: "auth refused"}
			case "disabled":
				live.Disabled = true
			case "status-disabled":
				live.Status = StatusDisabled
			}
			before := live.Clone()
			m.mu.Unlock()
			m.applyQuotaSample(context.Background(), a, headers, started)
			got := recoveryCurrent(t, m, a.ID)
			if !reflect.DeepEqual(cooldownFieldsOf(got.Quota), cooldownFieldsOf(before.Quota)) || got.Status != before.Status || got.Unavailable != before.Unavailable || !got.NextRetryAfter.Equal(before.NextRetryAfter) {
				t.Fatalf("unsafe sample changed credential cooldown: before=%+v after=%+v", before.Quota, got.Quota)
			}
			for model, state := range before.ModelStates {
				if !reflect.DeepEqual(cooldownFieldsOf(state.Quota), cooldownFieldsOf(got.ModelStates[model].Quota)) || !state.NextRetryAfter.Equal(got.ModelStates[model].NextRetryAfter) {
					t.Fatalf("unsafe sample changed model %s cooldown", model)
				}
			}
			if m.refusals.latest(a.ID, recoveryFable, "") == nil {
				t.Fatal("unsafe sample forgot provider refusal")
			}
		})
	}
}

func TestQuotaRecoveryCodex(t *testing.T) {
	for _, secondary := range []float64{0, 100} {
		t.Run(fmt.Sprint(secondary), func(t *testing.T) {
			m := NewManager(nil, &UseOrLoseSelector{}, nil)
			a := samplerRegister(t, m, &Auth{ID: "quota-recovery-codex", Provider: "codex"})
			recoveryRefuse(m, a, "gpt-6.1-sol", true, http.Header{"X-Codex-Primary-Used-Percent": {"100"}, "X-Codex-Secondary-Used-Percent": {"100"}})
			started := time.Now().Add(time.Second)
			headers := http.Header{"X-Codex-Primary-Used-Percent": {"0"}, "X-Codex-Secondary-Used-Percent": {fmt.Sprint(secondary)}}
			m.applyQuotaSample(context.Background(), a, headers, started)
			got := recoveryCurrent(t, m, a.ID)
			if secondary == 0 {
				if got.Status != StatusActive || len(CooldownSnapshotForAuth(got, time.Now())) != 0 || m.refusals.latest(a.ID, "gpt-6.1-sol", "") != nil {
					t.Fatalf("Codex did not recover: %+v", got)
				}
			} else if got.Quota.Reason != "credential_quota" || !got.Quota.Exceeded {
				t.Fatal("Codex recovered with an exhausted secondary window")
			}
		})
	}
}

func TestQuotaRecoveryCadence(t *testing.T) {
	for _, modelOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(modelOnly), func(t *testing.T) {
			m, a := recoveryManager(t, false)
			now := time.Now()
			m.mu.Lock()
			live := m.auths[a.ID]
			live.Quota.ObserveResponseHeadersForProvider("claude", recoveryHeaders(now, 0, 0), now)
			live.sampledFable = true
			live.lastRequestAt = now
			if modelOnly {
				applyCooldownFields(&live.Quota, QuotaState{})
				state := live.ModelStates[recoveryFable]
				state.credentialQuotaPropagated = false
				state.Quota.Reason = "quota"
			}
			m.mu.Unlock()
			var calls atomic.Int32
			m.SetRoundTripperProvider(samplerTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return samplerResponse(samplerPayload(now, 100, 7*24*time.Hour)), nil
			}))
			s := m.quotaSampler
			s.scan(context.Background(), now)
			s.workers.Wait()
			if calls.Load() != 1 {
				t.Fatal("fresh observation suppressed quota-cooldown sample")
			}
			next := s.nextCooldownSampleAt(now)
			if next.IsZero() || !next.Equal(s.jobs[a.ID].next) || next.Before(now.Add(quotaSampleInterval)) || next.After(time.Now().Add(quotaSampleInterval)) {
				t.Fatalf("cooldown sample deadline = %s", next)
			}
			s.scan(context.Background(), now.Add(quotaSampleInterval))
			s.workers.Wait()
			if calls.Load() != 1 {
				t.Fatal("sample interval was ignored")
			}
			s.scan(context.Background(), next)
			s.workers.Wait()
			if calls.Load() != 2 {
				t.Fatal("quota cooldown was not sampled at its timer deadline")
			}
		})
	}
}

func TestQuotaRecoveryResponseHeadersDoNotLift(t *testing.T) {
	m, a := recoveryManager(t, false)
	before := recoveryCurrent(t, m, a.ID)
	later := a.Clone()
	later.Quota.ObserveResponseHeadersForProvider("claude", recoveryHeaders(time.Now(), 0, 0), time.Now())
	m.MarkResult(fleetHeaders(later), Result{AuthID: a.ID, Provider: "claude", Model: recoveryOther, Success: true})
	got := recoveryCurrent(t, m, a.ID)
	if !reflect.DeepEqual(cooldownFieldsOf(got.Quota), cooldownFieldsOf(before.Quota)) || got.Status != before.Status || got.Unavailable != before.Unavailable || !got.NextRetryAfter.Equal(before.NextRetryAfter) {
		t.Fatalf("response headers changed credential cooldown: before=%+v after=%+v", before.Quota, got.Quota)
	}
	for model, state := range before.ModelStates {
		if !reflect.DeepEqual(cooldownFieldsOf(state.Quota), cooldownFieldsOf(got.ModelStates[model].Quota)) || !state.NextRetryAfter.Equal(got.ModelStates[model].NextRetryAfter) {
			t.Fatalf("response headers changed model %s cooldown", model)
		}
	}
}

func TestQuotaRecoveryModelOnlyPreservesSharedCooldown(t *testing.T) {
	m, a := recoveryManager(t, false)
	recoveryRefuse(m, a, recoveryOther, true, recoveryHeaders(time.Now(), 1, 1))
	recoveryRefuse(m, a, recoveryFable, false, recoveryHeaders(time.Now(), 1, 1))
	started := time.Now().Add(time.Second)
	m.applyQuotaSample(context.Background(), a, recoveryHeaders(started, 1, 0), started)
	got := recoveryCurrent(t, m, a.ID)
	if got.Quota.Reason != "credential_quota" || !got.Quota.Exceeded || !got.Unavailable {
		t.Fatal("model recovery cleared the shared cooldown")
	}
	if got.ModelStates[recoveryFable].credentialQuotaShadow != nil {
		t.Fatal("Fable independent cooldown survived its reset")
	}
	if m.refusals.latest(a.ID, "", "") == nil {
		t.Fatal("model recovery forgot the shared provider refusal")
	}
	m.refusals.mu.Lock()
	_, retained := m.refusals.byAuth[a.ID].byModel[recoveryFable]
	m.refusals.mu.Unlock()
	if retained {
		t.Fatal("model recovery retained the model refusal")
	}
}

func TestQuotaRecoveryPreservesIndependentTransientCooldown(t *testing.T) {
	m, a := recoveryManager(t, false)
	m.mu.Lock()
	state := m.auths[a.ID].ModelStates[recoveryOther]
	state.credentialQuotaShadow = &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour), LastError: &Error{HTTPStatus: 503, Message: "transient"}, UpdatedAt: time.Now()}
	shadow := state.credentialQuotaShadow.cloneShallow()
	m.mu.Unlock()
	started := time.Now().Add(time.Second)
	m.applyQuotaSample(context.Background(), a, recoveryHeaders(started, 0, 0), started)
	got := recoveryCurrent(t, m, a.ID)
	if !reflect.DeepEqual(got.ModelStates[recoveryOther], shadow) {
		t.Fatalf("independent transient state was changed: %+v", got.ModelStates[recoveryOther])
	}
	if got.Quota.Reason == "credential_quota" || got.ModelStates[recoveryFable].Unavailable {
		t.Fatal("independent transient cooldown prevented quota recovery")
	}
}

func TestQuotaRecoveryEmitsExistingRecoveredEvent(t *testing.T) {
	m, a := recoveryManager(t, false)
	monitor, recorder, now := fleetAlertFixture(t)
	*now = time.Now()
	monitor.list = m.List
	monitor.step(context.Background(), true)
	*now = now.Add(time.Hour)
	monitor.step(context.Background(), true)
	events := recorder.events()
	if len(events) != 1 || events[0].Event != "credential_unhealthy" {
		t.Fatalf("unhealthy event missing: %+v", events)
	}
	started := time.Now().Add(time.Second)
	m.applyQuotaSample(context.Background(), a, recoveryHeaders(started, 0, 0), started)
	monitor.step(context.Background(), true)
	monitor.step(context.Background(), true)
	events = recorder.events()
	if len(events) != 2 || events[1].Event != "credential_recovered" {
		t.Fatalf("existing recovered event missing or duplicated: %+v", events)
	}
}

func TestQuotaRecoveryIgnoresStaleSiblingError(t *testing.T) {
	for _, credential := range []bool{false, true} {
		t.Run(fmt.Sprint(credential), func(t *testing.T) {
			m, a := recoveryManager(t, false)
			if !credential {
				if _, _, err := m.ResetQuota(context.Background(), a.ID); err != nil {
					t.Fatal(err)
				}
				recoveryRefuse(m, a, recoveryFable, false, recoveryHeaders(time.Now(), 0, 1))
			}
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: recoveryOther, Error: &Error{HTTPStatus: 529, Message: "overloaded"}})
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: recoveryOther, Success: true})
			before := recoveryCurrent(t, m, a.ID)
			if before.LastError == nil || before.LastError.HTTPStatus != 529 {
				t.Fatalf("stale sibling error missing: %+v", before.LastError)
			}
			started := time.Now().Add(time.Second)
			m.applyQuotaSample(context.Background(), a, recoveryHeaders(started, 0, 0), started)
			got := recoveryCurrent(t, m, a.ID)
			if blocked, _, _ := isAuthBlockedForModel(got, recoveryFable, time.Now()); blocked {
				t.Fatalf("stale sibling error prevented quota recovery: %+v", got)
			}
			if got.Quota.Reason == "credential_quota" || m.refusals.latest(a.ID, recoveryFable, "") != nil {
				t.Fatal("quota recovery retained cooldown or provider refusal")
			}
		})
	}
}

func TestQuotaRecoveryPreservesNonQuotaFailures(t *testing.T) {
	for _, status := range []int{503, 401, 402, 403} {
		for _, shadow := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/shadow=%t", status, shadow), func(t *testing.T) {
				m, a := recoveryManager(t, false)
				if _, _, err := m.ResetQuota(context.Background(), a.ID); err != nil {
					t.Fatal(err)
				}
				if shadow {
					recoveryRefuse(m, a, recoveryFable, true, recoveryHeaders(time.Now(), 1, 1))
				} else {
					recoveryRefuse(m, a, recoveryFable, false, recoveryHeaders(time.Now(), 0, 1))
				}
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: recoveryOther, Error: &Error{HTTPStatus: status, Message: "unrelated failure"}})
				before := recoveryCurrent(t, m, a.ID).ModelStates[recoveryOther]
				if shadow {
					before = before.credentialQuotaShadow
				}
				if before == nil || !isModelStateActiveCooldown(before, time.Now()) || before.LastError == nil || before.LastError.HTTPStatus != status {
					t.Fatalf("non-quota failure missing: %+v", before)
				}
				started := time.Now().Add(time.Second)
				m.applyQuotaSample(context.Background(), a, recoveryHeaders(started, 0, 0), started)
				got := recoveryCurrent(t, m, a.ID)
				if !reflect.DeepEqual(got.ModelStates[recoveryOther], before) {
					t.Fatalf("non-quota cooldown changed: before=%+v after=%+v", before, got.ModelStates[recoveryOther])
				}
				if got.Quota.Reason == "credential_quota" || got.ModelStates[recoveryFable].Unavailable {
					t.Fatal("non-quota sibling failure prevented quota recovery")
				}
			})
		}
	}
}

func TestQuotaRecoverySharedRefusalWithoutSampledFableWindow(t *testing.T) {
	for _, fable := range []string{"absent", "allowed", "rejected", "request-allowed-after-rejected"} {
		t.Run(fable, func(t *testing.T) {
			m, a := recoveryManager(t, false)
			if _, _, err := m.ResetQuota(context.Background(), a.ID); err != nil {
				t.Fatal(err)
			}
			headers := recoveryHeaders(time.Now(), 1, 0.2)
			switch fable {
			case "absent":
				for key := range headers {
					if strings.Contains(key, "7d_oi") {
						headers.Del(key)
					}
				}
			case "rejected", "request-allowed-after-rejected":
				headers = recoveryHeaders(time.Now(), 1, 1)
			}
			recoveryRefuse(m, a, recoveryFable, true, headers)
			if fable == "request-allowed-after-rejected" {
				observed := a.Clone()
				observed.Quota.ObserveResponseHeadersForProvider("claude", recoveryHeaders(time.Now(), 0, 0), time.Now())
				m.MarkResult(fleetHeaders(observed), Result{AuthID: a.ID, Provider: a.Provider, Model: recoveryOther, Success: true})
			}
			started := time.Now().Add(time.Second)
			headers = recoveryHeaders(started, 0, 0)
			for key := range headers {
				if strings.Contains(key, "7d_oi") {
					headers.Del(key)
				}
			}
			m.applyQuotaSample(context.Background(), a, headers, started)
			got := recoveryCurrent(t, m, a.ID)
			if got.Quota.Reason == "credential_quota" {
				t.Fatal("shared cooldown survived shared reset")
			}
			blocked, _, _ := isAuthBlockedForModel(got, recoveryFable, time.Now())
			wantBlocked := fable == "rejected" || fable == "request-allowed-after-rejected"
			if blocked != wantBlocked {
				t.Fatalf("Fable blocked = %t, want %t: %+v", blocked, wantBlocked, got.ModelStates[recoveryFable])
			}
			if !wantBlocked && (!modelStateIsClean(got.ModelStates[recoveryFable]) || m.refusals.latest(a.ID, recoveryFable, "") != nil) {
				t.Fatal("credential-derived Fable state or refusal survived shared reset")
			}
		})
	}
}

func TestQuotaRecoveryExpiredCredentialWrap(t *testing.T) {
	m, a := recoveryManager(t, false)
	if _, _, err := m.ResetQuota(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	recoveryRefuse(m, a, recoveryFable, false, recoveryHeaders(time.Now(), 0, 1))
	retry := time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: recoveryOther, CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: "shared quota exhausted"}})
	m.mu.Lock()
	live := m.auths[a.ID]
	live.Quota.NextRecoverAt = time.Now().Add(-time.Second)
	live.NextRetryAfter = live.Quota.NextRecoverAt
	m.mu.Unlock()
	started := time.Now().Add(time.Second)
	m.applyQuotaSample(context.Background(), a, recoveryHeaders(started, 1, 0), started)
	got := recoveryCurrent(t, m, a.ID)
	if !modelStateIsClean(got.ModelStates[recoveryFable]) || got.ModelStates[recoveryFable].credentialQuotaPropagated || got.ModelStates[recoveryFable].credentialQuotaShadow != nil {
		t.Fatalf("Fable recovery retained expired credential wrap: %+v", got.ModelStates[recoveryFable])
	}
	if blocked, _, _ := isAuthBlockedForModel(got, recoveryFable, time.Now()); blocked {
		t.Fatal("expired credential wrap still blocks Fable")
	}
}

func TestQuotaRecoveryCadenceTimer(t *testing.T) {
	m, a := recoveryManager(t, false)
	s := m.quotaSampler
	calls := make(chan struct{}, 1)
	m.SetRoundTripperProvider(samplerTransport(func(*http.Request) (*http.Response, error) {
		calls <- struct{}{}
		return samplerResponse(samplerPayload(time.Now(), 100, 7*24*time.Hour)), nil
	}))
	s.jobs[a.ID] = &quotaSampleJob{epoch: a.RegistrationEpoch, version: a.CredentialVersion, next: time.Now().Add(50 * time.Millisecond)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done; s.workers.Wait() })
	go func() {
		defer close(done)
		s.run(ctx, nil)
	}()
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("cooldown sampler waited for another periodic tick instead of its deadline")
	}
}
