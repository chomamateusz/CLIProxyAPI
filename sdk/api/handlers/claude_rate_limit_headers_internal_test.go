package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestClaudeRateLimitHTTPTransientBackoff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"transient 429", "transient with rejected window", "quota cooldown"} {
		t.Run(scenario, func(t *testing.T) {
			const model = "claude-sonnet-5-5"
			reset := time.Now().Truncate(time.Second).Add(10 * time.Minute)
			upstream := http.Header{"Retry-After": {"17"}, "Anthropic-Ratelimit-Unified-Status": {"allowed_warning"}, "Anthropic-Ratelimit-Unified-Reset": {"9999999999"}}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(&modelExecutionCaptureExecutor{provider: "claude"})
			auth := &coreauth.Auth{ID: "transient", Provider: "claude", Status: coreauth.StatusActive, Unavailable: true, NextRetryAfter: reset, LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}}
			claim := "seven_day"
			switch scenario {
			case "transient with rejected window":
				auth.Quota.ObservedAt = time.Now()
				auth.Quota.Signals = map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected", "Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(reset.Unix(), 10)}
				claim = "five_hour"
			case "quota cooldown":
				auth.Quota = coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: reset}
			}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			base := NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, manager)
			router := gin.New()
			router.POST("/v1/chat/completions", func(c *gin.Context) {
				err := modelExecutionStatusHeaderError{statusCode: http.StatusTooManyRequests, message: "request throttled", headers: upstream}
				msg := base.executionErrorMessage(c.Request.Context(), []string{"claude"}, coreexecutor.Request{Model: model}, coreexecutor.Options{}, err)
				base.WriteErrorResponse(c, msg)
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status=%d", recorder.Code)
			}
			if scenario == "transient 429" {
				for key, values := range upstream {
					if recorder.Header().Get(key) != values[0] {
						t.Fatalf("changed %s: %s", key, recorder.Header().Get(key))
					}
				}
				if recorder.Header().Get("Anthropic-Ratelimit-Unified-Representative-Claim") != "" {
					t.Fatal("synthesized quota claim for transient backoff")
				}
			} else {
				for key, want := range map[string]string{"Anthropic-Ratelimit-Unified-Status": "rejected", "Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(reset.Unix(), 10), "Anthropic-Ratelimit-Unified-Representative-Claim": claim} {
					if recorder.Header().Get(key) != want {
						t.Fatalf("%s=%s, want %s", key, recorder.Header().Get(key), want)
					}
				}
			}
			t.Logf("%s headers: anthropic-ratelimit-unified-status=%s anthropic-ratelimit-unified-reset=%s anthropic-ratelimit-unified-representative-claim=%q Retry-After=%s", scenario, recorder.Header().Get("Anthropic-Ratelimit-Unified-Status"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Reset"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Representative-Claim"), recorder.Header().Get("Retry-After"))
		})
	}
}
