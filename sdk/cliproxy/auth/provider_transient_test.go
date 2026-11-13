package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func transientTestModes(provider string) []struct {
	name string
	run  func(*Manager, string) error
} {
	return []struct {
		name string
		run  func(*Manager, string) error
	}{
		{"execute", func(manager *Manager, model string) error {
			_, err := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			return err
		}},
		{"stream", func(manager *Manager, model string) error {
			result, err := manager.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
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
		{"count", func(manager *Manager, model string) error {
			_, err := manager.ExecuteCount(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			return err
		}},
	}
}

func markTransientTestResult(manager *Manager, id, model string, status int, retry time.Duration, quota bool) {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{strconv.Itoa(int(retry.Seconds()))}})
	manager.MarkResult(ctx, Result{
		AuthID: id, Provider: "claude", Model: model, RouteModel: model,
		Error: &Error{HTTPStatus: status, Message: `{"type":"error"}`}, RetryAfter: &retry, CredentialScope: quota,
	})
}

func assertShortTransientCooldown(t *testing.T, err error, maxSeconds int) {
	t.Helper()
	var refusal *ProviderRefusalError
	var status interface{ StatusCode() int }
	if err == nil || errors.As(err, &refusal) || !errors.As(err, &status) || status.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("error = %v (%T), want retryable 503 without a refusal", err, err)
	}
	var authErr *Error
	if !errors.As(err, &authErr) || !authErr.Retryable || authErr.Code != "auth_unavailable" {
		t.Fatalf("error = %v, want retryable auth_unavailable", err)
	}
	var headers interface{ Headers() http.Header }
	if !errors.As(err, &headers) {
		t.Fatalf("error %T has no retry hint", err)
	}
	seconds, parseErr := strconv.Atoi(headers.Headers().Get("Retry-After"))
	if parseErr != nil || seconds <= 0 || seconds > maxSeconds {
		t.Fatalf("Retry-After = %q, want 1..%d seconds", headers.Headers().Get("Retry-After"), maxSeconds)
	}
}

func TestTransientCooldownPreventsQuotaProbeAndStoredRefusal(t *testing.T) {
	for _, strategy := range quotaSelectorsUnderTest() {
		for _, mode := range transientTestModes("claude") {
			for _, aged := range []bool{false, true} {
				t.Run(strategy.name+"/"+mode.name+"/aged="+strconv.FormatBool(aged), func(t *testing.T) {
					model := "transient-cooldown-" + t.Name()
					manager, executor := newRefusalTestManager(t, strategy.selector(), "claude", model,
						&Auth{ID: "one"}, &Auth{ID: "amazingdesign"}, &Auth{ID: "three"})
					markTransientTestResult(manager, "one", model, 429, 51*time.Hour, true)
					if aged {
						ageRefusals(manager, 2*refusedCredentialRetryGuard)
					}
					markTransientTestResult(manager, "amazingdesign", model, 503, 20*time.Second, false)
					markTransientTestResult(manager, "three", model, 503, 35*time.Second, false)
					for range 3 {
						assertShortTransientCooldown(t, mode.run(manager, model), 20)
					}
					if calls := executor.takeCalls(); len(calls) != 0 {
						t.Fatalf("executor calls = %v, want no quota probe", calls)
					}
				})
			}
		}
	}
}

func TestTransientAttemptWinsOverLaterQuotaRefusal(t *testing.T) {
	for _, mode := range transientTestModes("claude") {
		for _, status := range []int{503, 529, 0} {
			t.Run(mode.name+"/"+strconv.Itoa(status), func(t *testing.T) {
				model := "transient-attempt-" + t.Name()
				manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "claude", model, &Auth{ID: "a"}, &Auth{ID: "b"})
				body := "{\n  \"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Authentication service is temporarily unavailable. Retry the request.\"}\n}\n"
				var transient error = refusalTestError{status: status, body: body, retryAfter: 25 * time.Second}
				if status == 0 {
					transient = errors.New("dial tcp: connection refused")
				}
				executor.setReply("a", refusalTestReply{err: transient, headers: http.Header{"Content-Type": []string{"application/json"}, "Retry-After": []string{"25"}}})
				executor.setReply("b", refusalTestReply{err: refusalTestError{status: 429, body: `{"type":"error","error":{"type":"rate_limit_error"}}`, retryAfter: 51 * time.Hour, credentialScoped: true}, headers: http.Header{"Retry-After": []string{"183600"}}})
				err := mode.run(manager, model)
				if status == 0 {
					assertShortTransientCooldown(t, err, int(transientErrorCooldown.Seconds()))
				} else {
					var direct *providerTransientError
					if !errors.As(err, &direct) || direct.StatusCode() != status || string(direct.ResponseBody()) != body || !direct.DirectResponse() {
						t.Fatalf("error = %v (%T), want verbatim transient %d", err, err, status)
					}
					if headers := direct.ResponseHeaders(); headers.Get("Retry-After") != "25" || headers.Get(ProxyNoticeHeader) != "" {
						t.Fatalf("transient headers = %v", headers)
					}
				}
				if calls := executor.takeCalls(); !slices.Equal(calls, []string{"a", "b"}) {
					t.Fatalf("executor calls = %v, want transient a then refused b", calls)
				}
				if status != 0 {
					for range 3 {
						assertShortTransientCooldown(t, mode.run(manager, model), int(transientErrorCooldown.Seconds()))
					}
					if calls := executor.takeCalls(); len(calls) != 0 {
						t.Fatalf("follow-up executor calls = %v, want no stored refusal or quota probe", calls)
					}
				}
			})
		}
	}
}

func TestTransientAttemptAfterQuotaRefusalWins(t *testing.T) {
	for _, mode := range transientTestModes("claude") {
		t.Run(mode.name, func(t *testing.T) {
			model := "later-transient-" + t.Name()
			manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "claude", model, &Auth{ID: "a"}, &Auth{ID: "b"})
			body := `{"type":"error","error":{"type":"api_error","message":"credential validation failed"}}`
			executor.setReply("a", refusalTestReply{err: refusalTestError{status: 429, body: `{"error":"quota"}`, retryAfter: 51 * time.Hour, credentialScoped: true}})
			executor.setReply("b", refusalTestReply{err: refusalTestError{status: 503, body: body}})
			var transient *providerTransientError
			if err := mode.run(manager, model); !errors.As(err, &transient) || transient.StatusCode() != 503 || string(transient.ResponseBody()) != body {
				t.Fatalf("error = %v, want the later provider 503 verbatim", err)
			}
			if calls := executor.takeCalls(); !slices.Equal(calls, []string{"a", "b"}) {
				t.Fatalf("executor calls = %v, want quota a then transient b", calls)
			}
		})
	}
}

func TestPreexistingTransientCooldownWinsOverNewQuotaRefusal(t *testing.T) {
	for _, mode := range transientTestModes("claude") {
		t.Run(mode.name, func(t *testing.T) {
			model := "preexisting-transient-" + t.Name()
			manager, executor := newRefusalTestManager(t, &RoundRobinSelector{}, "claude", model, &Auth{ID: "a"}, &Auth{ID: "b"})
			manager.SetRetryConfig(0, 0, 1)
			markTransientTestResult(manager, "a", model, 503, 20*time.Second, false)
			executor.setReply("b", refusalTestReply{err: refusalTestError{status: 429, body: `{"error":"quota"}`, retryAfter: 51 * time.Hour, credentialScoped: true}})
			assertShortTransientCooldown(t, mode.run(manager, model), 20)
			if calls := executor.takeCalls(); !slices.Equal(calls, []string{"b"}) {
				t.Fatalf("executor calls = %v, want only b", calls)
			}
		})
	}
}

func TestAllClaudeQuotaRefusedStillPassesThroughAndProbesSoonest(t *testing.T) {
	for _, mode := range transientTestModes("claude") {
		t.Run(mode.name, func(t *testing.T) {
			model := "all-quota-" + t.Name()
			manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "claude", model, &Auth{ID: "a"}, &Auth{ID: "b"}, &Auth{ID: "disabled", Disabled: true})
			markTransientTestResult(manager, "a", model, 429, 51*time.Hour, true)
			markTransientTestResult(manager, "a", model, 503, 20*time.Second, false)
			markTransientTestResult(manager, "disabled", model, 503, 20*time.Second, false)
			markTransientTestResult(manager, "b", model, 429, 2*time.Hour, true)
			var refusal *ProviderRefusalError
			if err := mode.run(manager, model); !errors.As(err, &refusal) || refusal.StatusCode() != 429 || refusal.ResponseHeaders().Get(ProxyNoticeHeader) != ProviderRefusalNotice {
				t.Fatalf("error = %v, want stored quota refusal and proxy notice", err)
			}
			if calls := executor.takeCalls(); len(calls) != 0 {
				t.Fatalf("executor calls inside guard = %v", calls)
			}
			ageRefusals(manager, 2*refusedCredentialRetryGuard)
			if err := mode.run(manager, model); err != nil {
				t.Fatalf("soonest quota probe error = %v", err)
			}
			if calls := executor.takeCalls(); !slices.Equal(calls, []string{"b"}) {
				t.Fatalf("executor calls after guard = %v, want soonest b", calls)
			}
		})
	}
}

func TestCredentialAuthCooldownDoesNotBecomeTransient(t *testing.T) {
	for _, status := range []int{401, 402, 403} {
		for _, mode := range transientTestModes("claude") {
			t.Run(strconv.Itoa(status)+"/"+mode.name, func(t *testing.T) {
				model := "auth-cooldown-" + t.Name()
				manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "claude", model, &Auth{ID: "a"}, &Auth{ID: "b"})
				markTransientTestResult(manager, "a", model, 429, 51*time.Hour, true)
				markTransientTestResult(manager, "b", model, status, 30*time.Minute, false)
				var refusal *ProviderRefusalError
				if err := mode.run(manager, model); !errors.As(err, &refusal) || refusal.StatusCode() != 429 {
					t.Fatalf("error = %v, want unchanged quota refusal beside an auth cooldown", err)
				}
				if calls := executor.takeCalls(); len(calls) != 0 {
					t.Fatalf("executor calls inside guard = %v", calls)
				}
			})
		}
	}
}

func TestCodexTransientDoesNotChangeRefusalPassThrough(t *testing.T) {
	for _, mode := range transientTestModes("codex") {
		t.Run(mode.name, func(t *testing.T) {
			model := "codex-transient-" + t.Name()
			manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "codex", model, &Auth{ID: "a"}, &Auth{ID: "b"})
			executor.setReply("a", refusalTestReply{err: refusalTestError{status: 503, body: `{"error":"temporary"}`}})
			executor.setReply("b", refusalTestReply{err: refusalTestError{status: 429, body: `{"error":"quota"}`, retryAfter: 51 * time.Hour, credentialScoped: true}})
			for range 2 {
				var refusal *ProviderRefusalError
				if err := mode.run(manager, model); !errors.As(err, &refusal) || refusal.StatusCode() != 429 {
					t.Fatalf("error = %v, want unchanged Codex quota refusal", err)
				}
			}
			if calls := executor.takeCalls(); !slices.Equal(calls, []string{"a", "b"}) {
				t.Fatalf("executor calls = %v, want no follow-up probe inside guard", calls)
			}
		})
	}
}

func TestTransientResponseSurvivesLaterNetworkFailureAndQuotaRefusal(t *testing.T) {
	for _, mode := range transientTestModes("claude") {
		for _, networkErr := range []error{errors.New("dial tcp: connection refused"), errors.New("unexpected EOF")} {
			t.Run(mode.name+"/"+networkErr.Error(), func(t *testing.T) {
				model := "transient-network-" + t.Name()
				manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "claude", model, &Auth{ID: "a"}, &Auth{ID: "b"}, &Auth{ID: "c"})
				manager.SetRetryConfig(0, 0, 3)
				body := `{"error":{"type":"overloaded_error","message":"Retry the request."}}`
				executor.setReply("a", refusalTestReply{err: refusalTestError{status: 503, body: body}, headers: http.Header{"Retry-After": []string{"15"}}})
				executor.setReply("b", refusalTestReply{err: networkErr})
				executor.setReply("c", refusalTestReply{err: refusalTestError{status: 429, body: `{"error":"quota"}`, retryAfter: 51 * time.Hour, credentialScoped: true}})
				var transient *providerTransientError
				if err := mode.run(manager, model); !errors.As(err, &transient) || transient.StatusCode() != 503 || string(transient.ResponseBody()) != body || transient.ResponseHeaders().Get("Retry-After") != "15" {
					t.Fatalf("error = %v, want the provider's transient response, not a generated error", err)
				}
				if calls := executor.takeCalls(); !slices.Equal(calls, []string{"a", "b", "c"}) {
					t.Fatalf("executor calls = %v", calls)
				}
			})
		}
	}
}

func TestNetworkAttemptWinsOverQuotaRefusal(t *testing.T) {
	for _, mode := range transientTestModes("claude") {
		t.Run(mode.name, func(t *testing.T) {
			model := "network-refusal-" + t.Name()
			manager, executor := newRefusalTestManager(t, &NearestResetSelector{}, "claude", model, &Auth{ID: "a"}, &Auth{ID: "b"})
			executor.setReply("a", refusalTestReply{err: errors.New("unexpected EOF")})
			executor.setReply("b", refusalTestReply{err: refusalTestError{status: 429, body: `{"error":"quota"}`, retryAfter: 51 * time.Hour, credentialScoped: true}})
			assertShortTransientCooldown(t, mode.run(manager, model), int(transientErrorCooldown.Seconds()))
			if calls := executor.takeCalls(); !slices.Equal(calls, []string{"a", "b"}) {
				t.Fatalf("executor calls = %v", calls)
			}
		})
	}
}
