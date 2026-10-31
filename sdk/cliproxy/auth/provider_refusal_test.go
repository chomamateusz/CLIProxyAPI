package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type refusalTestError struct {
	status           int
	body             string
	retryAfter       time.Duration
	credentialScoped bool
}

func (e refusalTestError) Error() string   { return e.body }
func (e refusalTestError) StatusCode() int { return e.status }
func (e refusalTestError) RetryAfter() *time.Duration {
	if e.retryAfter <= 0 {
		return nil
	}
	return &e.retryAfter
}
func (e refusalTestError) IsCredentialScoped() bool { return e.credentialScoped }

type refusalTestReply struct {
	err     error
	headers http.Header
}

type refusalTestExecutor struct {
	provider string
	mu       sync.Mutex
	replies  map[string]refusalTestReply
	calls    []string
}

func (e *refusalTestExecutor) Identifier() string { return e.provider }

func (e *refusalTestExecutor) setReply(authID string, reply refusalTestReply) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.replies[authID] = reply
}

func (e *refusalTestExecutor) takeCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	calls := e.calls
	e.calls = nil
	return calls
}

func (e *refusalTestExecutor) reply(ctx context.Context, auth *Auth) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
	reply := e.replies[auth.ID]
	internallogging.SetResponseHeaders(ctx, reply.headers)
	return reply.err
}

func (e *refusalTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.reply(ctx, auth); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *refusalTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err := e.reply(ctx, auth); err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"ok":true}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *refusalTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (e *refusalTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *refusalTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func newRefusalTestManager(t *testing.T, selector Selector, provider, model string, auths ...*Auth) (*Manager, *refusalTestExecutor) {
	t.Helper()
	executor := &refusalTestExecutor{provider: provider, replies: make(map[string]refusalTestReply)}
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)
	for _, auth := range auths {
		auth.Provider = provider
		auth.Status = StatusActive
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
		id := auth.ID
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return manager, executor
}

func refusalTestReset(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func expireRefusalCooldowns(manager *Manager, now time.Time) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	past := now.Add(-time.Second)
	for _, auth := range manager.auths {
		if auth.NextRetryAfter.After(past) {
			auth.NextRetryAfter = past
		}
		if auth.Quota.NextRecoverAt.After(past) {
			auth.Quota.NextRecoverAt = past
		}
		for _, state := range auth.ModelStates {
			if state.NextRetryAfter.After(past) {
				state.NextRetryAfter = past
			}
			if state.Quota.NextRecoverAt.After(past) {
				state.Quota.NextRecoverAt = past
			}
		}
	}
}

func ageRefusals(manager *Manager, by time.Duration) {
	manager.refusals.mu.Lock()
	defer manager.refusals.mu.Unlock()
	for _, models := range manager.refusals.byAuth {
		for _, refusal := range models {
			refusal.at = refusal.at.Add(-by)
		}
	}
}

func quotaSelectorsUnderTest() []struct {
	name     string
	selector func() Selector
} {
	return []struct {
		name     string
		selector func() Selector
	}{
		{"nearest-reset", func() Selector { return &NearestResetSelector{} }},
		{"most-used", func() Selector { return &MostUsedSelector{} }},
	}
}

func TestQuotaReadingAtLimitNeverExcludesCredential(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		provider  string
		observed  http.Header
		refreshed map[string]string
		sampled   bool
	}{
		{
			name:     "codex weekly limit from response headers",
			provider: "codex",
			observed: http.Header{
				"X-Codex-Primary-Used-Percent":   []string{"100"},
				"X-Codex-Primary-Window-Minutes": []string{"10080"},
				"X-Codex-Primary-Reset-At":       []string{refusalTestReset(now.Add(72 * time.Hour))},
				"X-Codex-Limit-Reached":          []string{"true"},
				"X-Codex-Credits-Has-Credits":    []string{"true"},
			},
			sampled: true,
		},
		{
			name:     "claude unified rejection from response headers",
			provider: "claude",
			observed: http.Header{
				"Anthropic-Ratelimit-Unified-Status":         []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":      []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"1"},
				"Anthropic-Ratelimit-Unified-5h-Reset":       []string{refusalTestReset(now.Add(time.Hour))},
			},
			sampled: true,
		},
		{
			name:     "devin quota refresh reports nothing left",
			provider: "devin",
			refreshed: map[string]string{
				"daily_quota_remaining_percent":  "0%",
				"weekly_quota_remaining_percent": "0%",
				"daily_quota_reset_at":           now.Add(time.Hour).Format(time.RFC3339),
			},
		},
	} {
		for _, strategy := range quotaSelectorsUnderTest() {
			t.Run(strategy.name+"/"+tc.name, func(t *testing.T) {
				model := "quota-reading-" + tc.provider + "-" + strategy.name
				auth := &Auth{ID: "reading-" + tc.provider + "-" + strategy.name}
				if tc.refreshed != nil {
					auth.Quota = QuotaState{ObservedAt: now, Signals: tc.refreshed}
				}
				manager, executor := newRefusalTestManager(t, strategy.selector(), tc.provider, model, auth)
				executor.setReply(auth.ID, refusalTestReply{headers: tc.observed})
				request := cliproxyexecutor.Request{Model: model}
				if _, errExecute := manager.Execute(context.Background(), []string{tc.provider}, request, cliproxyexecutor.Options{}); errExecute != nil {
					t.Fatalf("first Execute() error = %v", errExecute)
				}
				current, _ := manager.GetByID(auth.ID)
				if sample, ok := quotaSampleFor(current, model, time.Now()); tc.sampled && (!ok || !sample.atLimit) {
					t.Fatalf("reading was not observed at limit: sample=%+v ok=%v signals=%v", sample, ok, current.Quota.Signals)
				}
				if current.Quota.Exceeded || current.Unavailable {
					t.Fatalf("reading marked the credential unavailable: quota=%+v unavailable=%v", current.Quota, current.Unavailable)
				}
				executor.takeCalls()
				for range 2 {
					if _, errExecute := manager.Execute(context.Background(), []string{tc.provider}, request, cliproxyexecutor.Options{}); errExecute != nil {
						t.Fatalf("Execute() with an at-limit reading error = %v", errExecute)
					}
				}
				if calls := executor.takeCalls(); !slices.Equal(calls, []string{auth.ID, auth.ID}) {
					t.Fatalf("executor calls = %v, want the at-limit credential twice", calls)
				}
			})
		}
	}
}

func TestRefusedCredentialSkippedDuringCooldownAndRestoredAfter(t *testing.T) {
	for _, strategy := range quotaSelectorsUnderTest() {
		t.Run(strategy.name, func(t *testing.T) {
			model := "refusal-cooldown-" + strategy.name
			manager, executor := newRefusalTestManager(t, strategy.selector(), "codex", model, &Auth{ID: "cooldown-a"}, &Auth{ID: "cooldown-b"})
			executor.setReply("cooldown-a", refusalTestReply{err: refusalTestError{status: http.StatusTooManyRequests, body: `{"error":"slow down"}`, retryAfter: time.Hour}})
			request := cliproxyexecutor.Request{Model: model}
			run := func() {
				t.Helper()
				if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{}); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
			}

			run()
			if calls := executor.takeCalls(); !slices.Equal(calls, []string{"cooldown-a", "cooldown-b"}) {
				t.Fatalf("first request calls = %v, want refusal on a then b", calls)
			}
			run()
			if calls := executor.takeCalls(); !slices.Equal(calls, []string{"cooldown-b"}) {
				t.Fatalf("calls during cooldown = %v, want only b", calls)
			}

			executor.setReply("cooldown-a", refusalTestReply{})
			expireRefusalCooldowns(manager, time.Now())
			run()
			if calls := executor.takeCalls(); !slices.Equal(calls, []string{"cooldown-a"}) {
				t.Fatalf("calls after cooldown = %v, want a back in rotation", calls)
			}
		})
	}
}

func TestAllRefusedCredentialsRetrySoonestAfterGuard(t *testing.T) {
	refusalHeaders := func(id string) http.Header {
		return http.Header{
			"Content-Type":                   []string{"application/json"},
			"Retry-After":                    []string{"3600"},
			"X-Codex-Primary-Used-Percent":   []string{"100"},
			"X-Codex-Primary-Reset-At":       []string{refusalTestReset(time.Now().Add(time.Hour))},
			"X-Ratelimit-Remaining-Requests": []string{"0"},
			"X-Request-Id":                   []string{id},
		}
	}
	modes := []struct {
		name string
		run  func(*Manager, string) error
	}{
		{"non-stream", func(manager *Manager, model string) error {
			_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			return err
		}},
		{"stream", func(manager *Manager, model string) error {
			result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			if err != nil {
				return err
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					return chunk.Err
				}
			}
			return nil
		}},
	}
	retries := []struct {
		name  string
		retry int
	}{{"no retry", 0}, {"default retry", 3}}
	for _, scope := range []struct {
		name             string
		credentialScoped bool
	}{{"model scoped", false}, {"credential scoped", true}} {
		for _, mode := range modes {
			for _, retry := range retries {
				t.Run(scope.name+"/"+mode.name+"/"+retry.name, func(t *testing.T) {
					model := "refusal-guard-" + mode.name + "-" + strconv.FormatBool(scope.credentialScoped) + "-" + strconv.Itoa(retry.retry)
					manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "codex", model, &Auth{ID: "guard-a"}, &Auth{ID: "guard-b"})
					manager.SetRetryConfig(retry.retry, 30*time.Second, 0)
					refuse := func(id, body string, retryAfter time.Duration) {
						executor.setReply(id, refusalTestReply{
							err:     refusalTestError{status: http.StatusTooManyRequests, body: body, retryAfter: retryAfter, credentialScoped: scope.credentialScoped},
							headers: refusalHeaders(id),
						})
					}
					bodyA := `{"error": {"type": "usage_limit_reached", "message": "a"}}`
					bodyB := "{\n  \"error\": {\"type\": \"usage_limit_reached\", \"message\": \"b\"}\n}\n"
					refuse("guard-a", bodyA, 2*time.Hour)
					refuse("guard-b", bodyB, time.Hour)
					assertRefusal := func(err error, wantBody string) {
						t.Helper()
						var refusal *ProviderRefusalError
						if !errors.As(err, &refusal) {
							t.Fatalf("error = %v (%T), want provider refusal pass-through", err, err)
						}
						if refusal.StatusCode() != http.StatusTooManyRequests || string(refusal.ResponseBody()) != wantBody {
							t.Fatalf("refusal = %d %q, want 429 %q", refusal.StatusCode(), refusal.ResponseBody(), wantBody)
						}
						headers := refusal.ResponseHeaders()
						if headers.Get(ProxyNoticeHeader) != ProviderRefusalNotice || headers.Get("Retry-After") != "3600" ||
							headers.Get("X-Codex-Primary-Used-Percent") != "100" || headers.Get("X-Ratelimit-Remaining-Requests") != "0" {
							t.Fatalf("refusal headers = %v", headers)
						}
						if headers.Get("X-Request-Id") != "" {
							t.Fatalf("unrelated provider header leaked: %v", headers)
						}
						if headers.Get("X-Codex-Primary-Reset-At") == "" {
							t.Fatalf("reset header missing: %v", headers)
						}
					}

					assertRefusal(mode.run(manager, model), bodyB)
					if calls := executor.takeCalls(); !slices.Equal(calls, []string{"guard-a", "guard-b"}) {
						t.Fatalf("first request calls = %v", calls)
					}

					assertRefusal(mode.run(manager, model), bodyB)
					if calls := executor.takeCalls(); len(calls) != 0 {
						t.Fatalf("request inside the guard reached the provider: %v", calls)
					}

					ageRefusals(manager, refusedCredentialRetryGuard+time.Second)
					bodyRetry := `{"error":{"type":"usage_limit_reached","message":"b again"}}`
					refuse("guard-b", bodyRetry, time.Hour)
					assertRefusal(mode.run(manager, model), bodyRetry)
					if calls := executor.takeCalls(); !slices.Equal(calls, []string{"guard-b"}) {
						t.Fatalf("calls after the guard = %v, want only the soonest-ending credential", calls)
					}

					ageRefusals(manager, refusedCredentialRetryGuard+time.Second)
					executor.setReply("guard-b", refusalTestReply{})
					if err := mode.run(manager, model); err != nil {
						t.Fatalf("soonest-ending credential served from credits but request failed: %v", err)
					}
					if calls := executor.takeCalls(); !slices.Equal(calls, []string{"guard-b"}) {
						t.Fatalf("calls when provider accepts = %v", calls)
					}
				})
			}
		}
	}
}

func TestCooldownWithoutProviderResponse(t *testing.T) {
	seedCooldown := func(manager *Manager, id, model string, refusal bool) {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		state := ensureModelState(manager.auths[id], model)
		state.Unavailable = true
		state.NextRetryAfter = time.Now().Add(time.Hour)
		if refusal {
			state.Quota = QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: state.NextRetryAfter}
		}
	}

	t.Run("restored refusal cooldown without stored response tries the provider", func(t *testing.T) {
		model := "cooldown-restored"
		manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "codex", model, &Auth{ID: "restored-a"})
		seedCooldown(manager, "restored-a", model, true)
		if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if calls := executor.takeCalls(); !slices.Equal(calls, []string{"restored-a"}) {
			t.Fatalf("calls = %v", calls)
		}
	})

	t.Run("non-refusal cooldown keeps the cooling label", func(t *testing.T) {
		model := "cooldown-transient"
		manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "codex", model, &Auth{ID: "transient-a"})
		seedCooldown(manager, "transient-a", model, false)
		_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		var refusal *ProviderRefusalError
		if errors.As(err, &refusal) || !isAuthUnavailableError(err) {
			t.Fatalf("error = %v, want auth unavailable label", err)
		}
		if calls := executor.takeCalls(); len(calls) != 0 {
			t.Fatalf("calls = %v", calls)
		}
	})

	t.Run("no credential configured keeps the auth label", func(t *testing.T) {
		manager := NewManager(nil, &NearestResetSelector{}, nil)
		manager.RegisterExecutor(&refusalTestExecutor{provider: "codex", replies: map[string]refusalTestReply{}})
		_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "cooldown-none"}, cliproxyexecutor.Options{})
		var refusal *ProviderRefusalError
		var authErr *Error
		if errors.As(err, &refusal) || !errors.As(err, &authErr) || authErr.Code != "auth_not_found" {
			t.Fatalf("error = %v, want auth_not_found label", err)
		}
	})
}
