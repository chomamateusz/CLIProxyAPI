package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type samplerTransport func(*http.Request) (*http.Response, error)

func (f samplerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func (f samplerTransport) RoundTripperFor(*Auth) http.RoundTripper           { return f }

func samplerResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func samplerPayload(now time.Time, weekly float64, resetIn time.Duration) string {
	return fmt.Sprintf(`{"five_hour":{"utilization":4.5,"resets_at":%q},"seven_day":{"utilization":%v,"resets_at":%q},"limits":[{"kind":"weekly_scoped","percent":%v,"resets_at":%q,"is_active":true,"scope":{"model":{"display_name":"Fable 5"}}}]}`, now.Add(time.Hour).Format(time.RFC3339), weekly, now.Add(resetIn).Format(time.RFC3339), weekly, now.Add(resetIn).Format(time.RFC3339))
}

func samplerRegister(t *testing.T, m *Manager, a *Auth) *Auth {
	t.Helper()
	if a.Metadata == nil {
		a.Metadata = map[string]any{"access_token": "fake-" + a.ID}
	}
	registered, err := m.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

func TestSamplerProductionThreeCredentials(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		t.Run(fmt.Sprint(affinity), func(t *testing.T) {
			now := time.Now()
			selector := &UseOrLoseSelector{}
			var routing Selector = selector
			if affinity {
				sticky := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: selector})
				t.Cleanup(sticky.Stop)
				routing = sticky
			}
			m := NewManager(nil, routing, nil)
			exhausted := samplerRegister(t, m, &Auth{ID: "one", Provider: "claude"})
			sampled := samplerRegister(t, m, useOrLoseTestAuth("amazingdesign", now, 4*24*time.Hour, .7))
			fresh := samplerRegister(t, m, &Auth{ID: "three", Provider: "claude"})
			m.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
			for _, a := range []*Auth{exhausted, sampled, fresh} {
				registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5"}, {ID: "claude-fable-5-1"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
			}
			var calls atomic.Int32
			m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.String() != "https://api.anthropic.com/api/oauth/usage" || r.Header.Get("Anthropic-Beta") != "oauth-2025-04-20" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != constant.ClaudeUserAgent {
					t.Error("unexpected usage request")
				}
				switch r.Header.Get("Authorization") {
				case "Bearer fake-one":
					return samplerResponse(samplerPayload(now, 100, 24*time.Hour)), nil
				case "Bearer fake-three":
					return samplerResponse(samplerPayload(now, 0, 23*time.Hour)), nil
				default:
					return nil, errors.New("unexpected credential")
				}
			}))
			if affinity {
				_, err := routing.Pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {"existing"}}}, []*Auth{sampled})
				if err != nil {
					t.Fatal(err)
				}
			}
			m.quotaSampler.scan(context.Background(), now)
			m.quotaSampler.workers.Wait()
			if calls.Load() != 2 {
				t.Fatalf("usage requests = %d, want 2", calls.Load())
			}
			for _, model := range []string{"claude-opus-5-5", "claude-fable-5-1"} {
				picked, _, _, err := m.pickNextMixed(context.Background(), []string{"claude"}, model, cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {"new-" + model}}}, nil)
				if err != nil || picked == nil || picked.ID != fresh.ID {
					t.Fatalf("first post-sample choice = %v, %v", authID(picked), err)
				}
				if picked.ID == exhausted.ID {
					t.Fatal("exhausted credential received a user request")
				}
			}
			if affinity {
				picked, err := routing.Pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {"existing"}}}, m.List())
				if err != nil || picked.ID != sampled.ID {
					t.Fatal("sampling changed an existing viable binding")
				}
			}
		})
	}
}

func authID(a *Auth) string {
	if a == nil {
		return "<nil>"
	}
	return a.ID
}

func TestSamplerBackoffAndConcurrency(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, nil, nil)
	samplerRegister(t, m, &Auth{ID: "backoff", Provider: "claude"})
	entered, release := make(chan struct{}, 10), make(chan struct{})
	var calls atomic.Int32
	m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("ignored provider error"))}, nil
	}))
	s := m.quotaSampler
	s.scan(context.Background(), now)
	<-entered
	var scans sync.WaitGroup
	for range 32 {
		scans.Go(func() { s.scan(context.Background(), now.Add(time.Hour)) })
	}
	scans.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent requests = %d", calls.Load())
	}
	close(release)
	s.workers.Wait()
	for failure := 1; failure <= 6; failure++ {
		s.mu.Lock()
		job := *s.jobs["backoff"]
		s.mu.Unlock()
		if job.inFlight || job.failures != failure {
			t.Fatalf("job state = %+v", job)
		}
		s.scan(context.Background(), job.next.Add(-time.Nanosecond))
		s.workers.Wait()
		if calls.Load() != int32(failure) {
			t.Fatal("backoff did not suppress retry")
		}
		if failure < 6 {
			s.scan(context.Background(), job.next)
			s.workers.Wait()
		}
	}
	job := s.jobs["backoff"]
	if remaining := time.Until(job.next); remaining > time.Hour || remaining < 59*time.Minute {
		t.Fatalf("maximum backoff = %s", remaining)
	}
}

func TestSamplerIdleScheduling(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		age, idle time.Duration
		want      bool
	}{
		{"fresh idle", time.Minute, time.Hour, false},
		{"stale idle", 16 * time.Minute, time.Hour, true},
		{"stale recently served", time.Hour, time.Minute, false},
		{"old service no longer prevents sampling", time.Hour, 20 * time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := useOrLoseTestAuth("idle", now.Add(-tc.age), 48*time.Hour, .5)
			a.lastRequestAt = now.Add(-tc.idle)
			if got := quotaSampleNeeded(a, now); got != tc.want {
				t.Fatalf("needed = %t", got)
			}
		})
	}
	if !quotaSampleNeeded(&Auth{Provider: "claude"}, now) {
		t.Fatal("unsampled credential not scheduled")
	}
}

func TestSamplerStartupAddEnableAndCancellation(t *testing.T) {
	m := NewManager(nil, nil, nil)
	samplerRegister(t, m, &Auth{ID: "startup", Provider: "claude"})
	disabled := samplerRegister(t, m, &Auth{ID: "toggle", Provider: "claude", Disabled: true, Status: StatusDisabled})
	calls := make(chan string, 8)
	m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
		calls <- strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fake-")
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); m.StopAutoRefresh() })
	m.StartQuotaSampler(ctx)
	await := func(want string) {
		t.Helper()
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("sampled %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("sampler did not wake")
		}
	}
	await("startup")
	samplerRegister(t, m, &Auth{ID: "added", Provider: "claude"})
	await("added")
	disabled.Disabled, disabled.Status = false, StatusActive
	if _, err := m.Update(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	await("toggle")
	cancel()
	m.StopAutoRefresh()
	for id, job := range m.quotaSampler.jobs {
		if job.inFlight {
			t.Fatalf("%s still in flight after cancellation", id)
		}
	}
}

func TestSamplerReenableResetsBackoff(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := samplerRegister(t, m, &Auth{ID: "reenable", Provider: "claude"})
	var count atomic.Int32
	m.SetRoundTripperProvider(samplerTransport(func(*http.Request) (*http.Response, error) { count.Add(1); return nil, errors.New("fake failure") }))
	s := m.quotaSampler
	s.scan(context.Background(), time.Now())
	s.workers.Wait()
	a.Disabled, a.Status = true, StatusDisabled
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	a.Disabled, a.Status = false, StatusActive
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	s.scan(context.Background(), time.Now())
	s.workers.Wait()
	if count.Load() != 2 {
		t.Fatal("reenabled credential retained failure backoff")
	}
}

func TestSamplerStaleResultsAndCooldownPreservation(t *testing.T) {
	now := time.Now()
	for _, mutation := range []string{"disable", "replace", "response", "unchanged"} {
		t.Run(mutation, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			a := samplerRegister(t, m, &Auth{ID: "stale", Provider: "claude"})
			m.mu.Lock()
			m.auths[a.ID].Quota.Exceeded = true
			m.auths[a.ID].Quota.Reason = "credential_quota"
			m.auths[a.ID].Quota.NextRecoverAt = now.Add(time.Hour)
			switch mutation {
			case "disable":
				m.auths[a.ID].Disabled = true
			case "replace":
				m.auths[a.ID].CredentialVersion++
			case "response":
				m.auths[a.ID].Quota.ObservedAt = now.Add(time.Second)
			}
			m.mu.Unlock()
			headers, err := parseQuotaSample("claude", []byte(samplerPayload(now, 0, 24*time.Hour)), now)
			if err != nil {
				t.Fatal(err)
			}
			changed := m.applyQuotaSample(context.Background(), a, headers, now)
			if changed != (mutation == "unchanged" || mutation == "response") {
				t.Fatalf("changed = %t", changed)
			}
			current := m.List()[0]
			if mutation == "response" && !current.Quota.ObservedAt.Equal(now.Add(time.Second)) {
				t.Fatal("sample overwrote newer response observation")
			}
			if !current.Quota.Exceeded || current.Quota.Reason != "credential_quota" || !current.Quota.NextRecoverAt.Equal(now.Add(time.Hour)) {
				t.Fatal("sampler changed cooldown state")
			}
		})
	}
}

func TestSamplerPayloadUnitsAndFable(t *testing.T) {
	now := time.Now()
	body := samplerPayload(now, 16, 24*time.Hour)
	headers, err := parseQuotaSample("claude", []byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	a := &Auth{Provider: "claude"}
	a.Quota.ObserveResponseHeadersForProvider("claude", headers, now)
	limits := QuotaLimitsForAuth(a)
	if len(limits.Bars) != 3 {
		t.Fatalf("bars = %d", len(limits.Bars))
	}
	for _, bar := range limits.Bars {
		want := 16.0
		if bar.ID == "session_5h" {
			want = 4.5
		}
		if bar.Percent == nil || *bar.Percent != want {
			t.Fatalf("%s percentage = %v", bar.ID, bar.Percent)
		}
	}
	for _, tc := range []struct {
		name, extra string
		wantFable   bool
	}{
		{"legacy Fable", `"iguana_necktie":{"utilization":20,"resets_at":%q}`, true},
		{"dollar pool not Fable", `"iguana_necktie":{"utilization":20,"limit_dollars":100,"resets_at":%q}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"seven_day":{"utilization":10,"resets_at":%q},%s}`, now.Add(time.Hour).Format(time.RFC3339), fmt.Sprintf(tc.extra, now.Add(time.Hour).Format(time.RFC3339)))
			h, err := parseQuotaSample("claude", []byte(payload), now)
			if err != nil {
				t.Fatal(err)
			}
			if got := h.Get("Anthropic-Ratelimit-Unified-7d_oi-Utilization") != ""; got != tc.wantFable {
				t.Fatalf("Fable = %t", got)
			}
		})
	}
	for _, body := range []string{`{}`, `null`, `{"seven_day":{"utilization":-1,"resets_at":"invalid"}}`, `{"seven_day":{"utilization":100,"resets_at":"2020-01-01T00:00:00Z"}}`} {
		if _, err := parseQuotaSample("claude", []byte(body), now); err == nil {
			t.Fatal("invalid payload accepted")
		}
	}
}

func TestSamplerCodexKnownEndpoint(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, &UseOrLoseSelector{}, nil)
	samplerRegister(t, m, &Auth{ID: "codex", Provider: "codex", Metadata: map[string]any{"access_token": "fake-codex", "account_id": "fake-account"}})
	m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || r.Header.Get("Chatgpt-Account-Id") != "fake-account" || r.Header.Get("User-Agent") != constant.CodexUserAgent {
			t.Error("unexpected Codex request")
		}
		return samplerResponse(fmt.Sprintf(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":97,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":30,"limit_window_seconds":604800,"reset_at":%d}},"credits":{"has_credits":true,"unlimited":false,"balance":"10"}}`, now.Add(time.Hour).Unix(), now.Add(24*time.Hour).Unix())), nil
	}))
	m.quotaSampler.scan(context.Background(), now)
	m.quotaSampler.workers.Wait()
	a := m.List()[0]
	sample := (&UseOrLoseSelector{}).sampleFor(a, "codex", now)
	if !sample.sampled || sample.window.utilisation != .3 || sample.capped || sample.atLimit {
		t.Fatalf("Codex sample = %+v", sample)
	}
}

func TestUseOrLoseProductionWithoutSampler(t *testing.T) {
	now := time.Now()
	for _, state := range []string{"rejected", "cooldown-watermark"} {
		t.Run(state, func(t *testing.T) {
			exhausted := &Auth{ID: "one", Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{}}}
			if state == "rejected" {
				exhausted.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
				exhausted.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"] = now.Add(time.Hour).Format(time.RFC3339)
			} else {
				exhausted.Quota.Exceeded = true
				exhausted.Quota.NextRecoverAt = now.Add(time.Hour)
			}
			known := useOrLoseTestAuth("amazingdesign", now, 4*24*time.Hour, .7)
			fresh := &Auth{ID: "three", Provider: "claude"}
			selector := &UseOrLoseSelector{}
			picked, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", []*Auth{exhausted, known, fresh}, now)
			if err != nil || picked == nil || picked.ID != fresh.ID {
				t.Fatalf("first fallback = %s, %v", authID(picked), err)
			}
		})
	}
}

func TestUseOrLoseFirstProbeRespectsKnownBindingReset(t *testing.T) {
	now := time.Now()
	known := useOrLoseTestAuth("known", now, time.Hour, .4)
	unknown := &Auth{ID: "unknown", Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": now.Add(48 * time.Hour).Format(time.RFC3339)}}}
	picked, err := (&UseOrLoseSelector{}).pick(context.Background(), "claude", "claude-opus-5-5", []*Auth{unknown, known}, now)
	if err != nil || picked.ID != known.ID {
		t.Fatal("later known reset displaced better sampled window")
	}
}

func TestSamplerPeriodicWakeAndRecovery(t *testing.T) {
	m := NewManager(nil, nil, nil)
	samplerRegister(t, m, &Auth{ID: "periodic", Provider: "claude"})
	calls := make(chan struct{}, 4)
	var count atomic.Int32
	m.SetRoundTripperProvider(samplerTransport(func(*http.Request) (*http.Response, error) {
		call := count.Add(1)
		calls <- struct{}{}
		if call == 1 {
			return nil, errors.New("fake transient failure")
		}
		return samplerResponse(samplerPayload(time.Now(), 10, 24*time.Hour)), nil
	}))
	s := m.quotaSampler
	select {
	case <-s.wake:
	default:
	}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx, ticks) }()
	t.Cleanup(func() { cancel(); <-done; s.workers.Wait() })
	await := func() {
		t.Helper()
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("periodic sampler did not run")
		}
		s.workers.Wait()
	}
	await()
	s.mu.Lock()
	next := s.jobs["periodic"].next
	s.mu.Unlock()
	ticks <- next
	await()
	s.mu.Lock()
	failures := s.jobs["periodic"].failures
	s.mu.Unlock()
	if failures != 0 {
		t.Fatal("successful sample did not reset failure backoff")
	}
	if !(&UseOrLoseSelector{}).sampleFor(m.List()[0], "", time.Now()).sampled {
		t.Fatal("retry did not publish the usage observation")
	}
}

func TestSamplerPreservesFableAcrossResponsesAndAuthUpdates(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, nil, nil)
	a := samplerRegister(t, m, &Auth{ID: "fable-preserved", Provider: "claude"})
	headers, err := parseQuotaSample("claude", []byte(samplerPayload(now, 20, 24*time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	if !m.applyQuotaSample(context.Background(), a, headers, now) {
		t.Fatal("sample not applied")
	}
	response := useOrLoseTestAuth(a.ID, now.Add(time.Second), 24*time.Hour, .3)
	m.MarkResult(fleetHeaders(response), Result{AuthID: a.ID, Provider: "claude", Model: "claude-opus-5-5", Success: true})
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	current := m.List()[0]
	selector := &UseOrLoseSelector{}
	if got := selector.sampleFor(current, "claude-fable-5-1", now).window.utilisation; got != .2 {
		t.Fatalf("Fable sample lost after other-model response: %v", got)
	}
	clone := current.Clone()
	clone.sampledQuota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Utilization"] = "0.9"
	if current.sampledQuota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Utilization"] != "0.2" {
		t.Fatal("cloning aliases sampled headers")
	}
}

func TestSamplerCredentialReplacementWhileInFlight(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := samplerRegister(t, m, &Auth{ID: "replacement", Provider: "claude"})
	entered, release := make(chan struct{}, 4), make(chan struct{})
	var active atomic.Int32
	var calls atomic.Int32
	m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
		if active.Add(1) != 1 {
			t.Error("overlapping samples for one credential")
		}
		defer active.Add(-1)
		call := calls.Add(1)
		entered <- struct{}{}
		if call == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return samplerResponse(samplerPayload(time.Now(), 100, 24*time.Hour)), nil
		}
		if r.Header.Get("Authorization") != "Bearer fake-replacement-new" {
			t.Error("replacement credential was not used")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); m.StopAutoRefresh() })
	m.StartQuotaSampler(ctx)
	await := func() {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("replacement sampler did not wake")
		}
	}
	await()
	a.Metadata["access_token"] = "fake-replacement-new"
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	close(release)
	await()
	if len(m.List()[0].sampledQuota.Signals) != 0 {
		t.Fatal("stale credential sample was applied")
	}
}

func TestUseOrLoseUnknownBeatsStatusOnlyExhaustion(t *testing.T) {
	now := time.Now()
	for _, window := range []string{"7d", "7d_oi", "5h"} {
		t.Run(window, func(t *testing.T) {
			exhausted := &Auth{ID: "exhausted", Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-" + window + "-Reset":  now.Add(time.Hour).Format(time.RFC3339),
				"Anthropic-Ratelimit-Unified-" + window + "-Status": "rejected",
			}}}
			unknown := &Auth{ID: "unknown", Provider: "claude"}
			got, err := (&UseOrLoseSelector{}).pick(context.Background(), "claude", "claude-fable-5-1", []*Auth{exhausted, unknown}, now)
			if err != nil || got.ID != unknown.ID {
				t.Fatal("known exhausted credential displaced unknown")
			}
		})
	}
}
