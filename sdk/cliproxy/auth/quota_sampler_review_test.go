package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

type samplerBlockedBody struct {
	ctx    context.Context
	closed atomic.Bool
}

func (b *samplerBlockedBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *samplerBlockedBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestSamplerDeadlineReleasesJobAndRetries(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := NewManager(nil, nil, nil)
				a := samplerRegister(t, m, &Auth{ID: "deadline", Provider: "claude"})
				var calls atomic.Int32
				var blocked *samplerBlockedBody
				m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
					deadline, ok := r.Context().Deadline()
					if !ok || time.Until(deadline) != quotaSampleTimeout {
						t.Errorf("deadline not bounded to %s", quotaSampleTimeout)
					}
					if calls.Add(1) > 1 {
						return samplerResponse(samplerPayload(time.Now(), 5, 24*time.Hour)), nil
					}
					if phase == "headers" {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					blocked = &samplerBlockedBody{ctx: r.Context()}
					return &http.Response{StatusCode: http.StatusOK, Body: blocked}, nil
				}))
				s := m.quotaSampler
				started := time.Now()
				s.scan(context.Background(), started)
				s.workers.Wait()
				job := s.jobs[a.ID]
				if job.inFlight || job.failures != 1 || !job.next.Equal(started.Add(quotaSampleTimeout+quotaSampleInterval)) {
					t.Fatalf("timed out job = %+v", job)
				}
				if blocked != nil && !blocked.closed.Load() {
					t.Fatal("timed out response body was not closed")
				}
				s.scan(context.Background(), job.next.Add(-time.Nanosecond))
				s.workers.Wait()
				if calls.Load() != 1 {
					t.Fatal("timeout retry ignored backoff")
				}
				s.scan(context.Background(), job.next)
				s.workers.Wait()
				if calls.Load() != 2 || job.inFlight || job.failures != 0 || len(m.List()[0].sampledQuota.Signals) == 0 {
					t.Fatal("timeout retry did not recover")
				}
			})
		})
	}
}

func TestSamplerTokenRotationPreservesExhaustedFable(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, &UseOrLoseSelector{}, nil)
	a := samplerRegister(t, m, &Auth{ID: "a", Provider: "claude", Metadata: map[string]any{"access_token": "fake-old", "account_uuid": "same-account"}})
	b := samplerRegister(t, m, useOrLoseTestAuth("b", now, 48*time.Hour, .5))
	m.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
	for _, a := range []*Auth{a, b} {
		registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-fable-5-1"}, {ID: "claude-opus-5-5"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	}
	m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
		body := strings.Replace(samplerPayload(now, 100, 24*time.Hour), `"seven_day":{"utilization":100`, `"seven_day":{"utilization":10`, 1)
		return samplerResponse(body), nil
	}))
	m.quotaSampler.scan(context.Background(), now)
	m.quotaSampler.workers.Wait()
	m.MarkResult(fleetHeaders(useOrLoseTestAuth(a.ID, now, 24*time.Hour, .1)), Result{AuthID: a.ID, Provider: "claude", Model: "claude-opus-5-5", Success: true})
	a.Metadata["access_token"] = "fake-new"
	beforeVersion := a.CredentialVersion
	updated, err := m.Update(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CredentialVersion <= beforeVersion || !updated.sampledFable {
		t.Fatal("rotation failed to preserve account sample metadata")
	}
	picked, _, _, err := m.pickNextMixed(context.Background(), []string{"claude"}, "claude-fable-5-1", cliproxyexecutor.Options{}, nil)
	if err != nil || picked == nil || picked.ID != b.ID {
		t.Fatalf("rotated exhausted Fable account selected: %s, %v", authID(picked), err)
	}
}

func TestSamplerAccountReplacementClearsObservations(t *testing.T) {
	for _, identity := range []string{"account_uuid", "organization_uuid", "account_id", "organization_id", "email", "provider"} {
		t.Run(identity, func(t *testing.T) {
			now := time.Now()
			m := NewManager(nil, nil, nil)
			a := samplerRegister(t, m, &Auth{ID: "account", Provider: "claude", Metadata: map[string]any{"access_token": "fake", identity: "old"}})
			h, err := parseQuotaSample("claude", []byte(samplerPayload(now, 100, 24*time.Hour)), now)
			if err != nil || !m.applyQuotaSample(context.Background(), a, h, now) {
				t.Fatal("sample not applied")
			}
			a = m.List()[0]
			a.ModelStates = map[string]*ModelState{"claude-fable-5-1": {Quota: a.Quota.Clone()}}
			if identity == "provider" {
				a.Provider = "codex"
			} else {
				a.Metadata[identity] = "new"
			}
			beforeVersion := a.CredentialVersion
			updated, err := m.Update(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			if updated.CredentialVersion <= beforeVersion || len(quotaObservations(updated)) != 0 || updated.sampledFable || !quotaSampleNeeded(updated, now) {
				t.Fatal("replacement retained previous account observations")
			}
		})
	}
}

func TestSamplerMissingKnownFableWindowNeedsSample(t *testing.T) {
	now := time.Now()
	a := useOrLoseTestAuth("known-fable", now, 24*time.Hour, .1)
	a.sampledFable = true
	a.lastRequestAt = now
	if !quotaSampleNeeded(a, now) {
		t.Fatal("generic weekly observation suppressed missing Fable sampling")
	}
	a.sampledQuota = useOrLoseTestAuth("sample", now, 24*time.Hour, .1).Quota
	useOrLoseTestWindow(a, "7d_oi", now.Add(time.Hour), .5)
	if quotaSampleNeeded(a, now) {
		t.Fatal("usable Fable observation triggered unnecessary sample")
	}
}

func TestSamplerPendingProbeDefersUntilFailure(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "affinity"}[affinity], func(t *testing.T) {
			now := time.Now()
			var selector Selector = &UseOrLoseSelector{}
			if affinity {
				sticky := NewSessionAffinitySelector(selector)
				t.Cleanup(sticky.Stop)
				selector = sticky
			}
			m := NewManager(nil, selector, nil)
			unknown := samplerRegister(t, m, &Auth{ID: "unknown", Provider: "claude"})
			known := samplerRegister(t, m, useOrLoseTestAuth("known", now, 24*time.Hour, .5))
			m.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
			for _, a := range []*Auth{unknown, known} {
				registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
			}
			pick := func(want, phase string) {
				t.Helper()
				got, _, _, err := m.pickNextMixed(context.Background(), []string{"claude"}, "claude-opus-5-5", cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {phase}}}, nil)
				if err != nil || got == nil || got.ID != want {
					t.Fatalf("%s pick = %s, %v, want %s", phase, authID(got), err, want)
				}
			}
			s := m.quotaSampler
			s.mu.Lock()
			s.running = true
			s.mu.Unlock()
			pick(known.ID, "pending")
			entered, release := make(chan struct{}), make(chan struct{})
			m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
				close(entered)
				<-release
				return nil, errors.New("fake failure")
			}))
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			s.scan(ctx, now)
			<-entered
			pick(known.ID, "in-flight")
			close(release)
			s.workers.Wait()
			pick(unknown.ID, "failed")
		})
	}
}

func TestSamplerZeroUsageWithoutReset(t *testing.T) {
	now := time.Now()
	body := `{"seven_day":{"utilization":0,"resets_at":null},"five_hour":{"utilization":0,"resets_at":null},"iguana_necktie":{"utilization":0,"resets_at":null}}`
	h, err := parseQuotaSample("claude", []byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	fresh := &Auth{ID: "fresh", Provider: "claude"}
	fresh.Quota.ObserveResponseHeadersForProvider("claude", h, now)
	selector := &UseOrLoseSelector{}
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5-1"} {
		sample := selector.sampleFor(fresh, model, now)
		if !sample.sampled || sample.atLimit || sample.capped || sample.window.utilisation != 0 || !sample.window.reset.IsZero() {
			t.Fatalf("zero-usage sample = %+v", sample)
		}
		exhausted := useOrLoseTestAuth("exhausted", now, time.Hour, 1)
		got, err := selector.pick(context.Background(), "claude", model, []*Auth{exhausted, fresh}, now)
		if err != nil || got.ID != fresh.ID {
			t.Fatal("zero-usage account did not beat exhausted account")
		}
		known := useOrLoseTestAuth("known", now, time.Hour, .5)
		got, err = selector.pick(context.Background(), "claude", model, []*Auth{fresh, known}, now)
		if err != nil || got.ID != known.ID {
			t.Fatal("unknown reset displaced an expiring known window")
		}
	}
	if quotaSampleNeeded(fresh, now) || !quotaSampleNeeded(fresh, now.Add(quotaSampleIdleAge)) {
		t.Fatal("zero-usage sample freshness did not expire")
	}
	for _, invalid := range []string{
		`{"seven_day":{"utilization":1,"resets_at":null}}`,
		`{"seven_day":{"utilization":0,"resets_at":"invalid"}}`,
		`{"seven_day":{"resets_at":null}}`,
	} {
		if _, err := parseQuotaSample("claude", []byte(invalid), now); err == nil {
			t.Fatal("invalid zero-usage sample accepted")
		}
	}
}

func TestSamplerZeroUsageLogsSafeShapeOnceAndSharesUserAgent(t *testing.T) {
	previousHooks := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	hook := logtest.NewLocal(log.StandardLogger())
	previousLevel, previousOutput := log.GetLevel(), log.StandardLogger().Out
	log.SetLevel(log.DebugLevel)
	log.SetOutput(io.Discard)
	t.Cleanup(func() {
		log.SetLevel(previousLevel)
		log.SetOutput(previousOutput)
		log.StandardLogger().ReplaceHooks(previousHooks)
	})
	m := NewManager(nil, nil, nil)
	a := samplerRegister(t, m, &Auth{ID: "zero", Provider: "claude"})
	m.SetRoundTripperProvider(samplerTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("User-Agent") != constant.ClaudeUserAgent {
			t.Error("sampler did not use executor user agent")
		}
		return samplerResponse(`{"seven_day":{"utilization":0,"resets_at":null,"secret":"fake-hidden"},"access_token":"fake-hidden"}`), nil
	}))
	for range 2 {
		if _, err := m.fetchQuotaSample(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message == "quota sample zero-usage shape" {
			count++
			text, err := entry.String()
			if err != nil || strings.Contains(text, "fake-hidden") || !strings.Contains(text, "seven_day") || !strings.Contains(text, "resets_at") {
				t.Fatal("shape log contains unexpected fields or omitted the zero-usage window")
			}
		}
	}
	if count != 1 {
		t.Fatalf("zero-usage shape log count = %d", count)
	}
}
