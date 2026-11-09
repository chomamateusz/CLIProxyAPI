package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type claudeUsageTransport string

func (p claudeUsageTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return p }

func (p claudeUsageTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://api.anthropic.com/api/oauth/usage" {
		return nil, errors.New("unexpected upstream request")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(p))), Request: r}, nil
}

func claudeUsagePayload(session float64, sessionReset time.Time, weekly float64, weeklyReset time.Time) claudeUsageTransport {
	return claudeUsageTransport(fmt.Sprintf(`{"five_hour":{"utilization":%v,"resets_at":%q},"seven_day":{"utilization":%v,"resets_at":%q}}`, session, sessionReset.Format(time.RFC3339), weekly, weeklyReset.Format(time.RFC3339)))
}

func sampleClaudeUsage(t *testing.T, manager *coreauth.Manager, id string, payload claudeUsageTransport) {
	t.Helper()
	manager.SetRoundTripperProvider(payload)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.StartQuotaSampler(ctx)
	defer manager.StopAutoRefresh()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if auth, ok := manager.GetByID(id); ok {
			for _, bar := range coreauth.QuotaLimitsForAuth(auth).Bars {
				if bar.ID == "weekly_all" && bar.Percent != nil && *bar.Percent == 100 && bar.Status == "rejected" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("quota sample was not applied")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func claudeSamplerRouter(manager *coreauth.Manager, path string) *gin.Engine {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	if path == "/v1/messages" {
		router.POST(path, claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	} else {
		router.POST(path, openai.NewOpenAIAPIHandler(base).ChatCompletions)
	}
	return router
}

func TestClaudeRateLimitHTTPSampledWeeklyKeepsSessionCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			const model = "claude-sonnet-5-5"
			const id = "sampled-session"
			now := time.Now().Truncate(time.Second)
			sessionReset := now.Add(2 * time.Hour)
			weeklyReset := now.Add(3 * 24 * time.Hour)
			observed := now.Add(-20 * time.Minute)
			signals := func() map[string]string {
				return map[string]string{
					"Anthropic-Ratelimit-Unified-Status":               "rejected",
					"Anthropic-Ratelimit-Unified-Reset":                strconv.FormatInt(sessionReset.Unix(), 10),
					"Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour",
					"Anthropic-Ratelimit-Unified-5h-Status":            "rejected",
					"Anthropic-Ratelimit-Unified-5h-Reset":             strconv.FormatInt(sessionReset.Unix(), 10),
					"Anthropic-Ratelimit-Unified-7d-Status":            "allowed",
					"Anthropic-Ratelimit-Unified-7d-Reset":             strconv.FormatInt(weeklyReset.Unix(), 10),
				}
			}
			executor := &claudeLimitExecutor{refusalPassthroughExecutor: refusalPassthroughExecutor{provider: "claude"}}
			manager := coreauth.NewManager(nil, &coreauth.UseOrLoseSelector{}, nil)
			manager.RegisterExecutor(executor)
			auth := &coreauth.Auth{
				ID:       id,
				Provider: "claude",
				Status:   coreauth.StatusActive,
				Metadata: map[string]any{"access_token": "fixture-token"},
				Quota:    coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: sessionReset, ObservedAt: observed, Signals: signals()},
				ModelStates: map[string]*coreauth.ModelState{
					model: {Status: coreauth.StatusActive, Quota: coreauth.QuotaState{ObservedAt: observed, Signals: signals()}},
				},
			}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			sampleClaudeUsage(t, manager, id, claudeUsagePayload(100, sessionReset, 100, weeklyReset))

			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+model+`","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			before := time.Now()
			claudeSamplerRouter(manager, path).ServeHTTP(recorder, request)
			after := time.Now()
			if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "All credentials for model") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if executor.calls.Load() != 0 {
				t.Fatal("cooldown reached executor")
			}
			for key, want := range map[string]string{
				"Anthropic-Ratelimit-Unified-Status":               "rejected",
				"Anthropic-Ratelimit-Unified-Reset":                strconv.FormatInt(sessionReset.Unix(), 10),
				"Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour",
			} {
				if got := recorder.Header().Get(key); got != want {
					t.Fatalf("%s=%s, want %s", key, got, want)
				}
			}
			retry, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
			if err != nil || retry < int(math.Ceil(sessionReset.Sub(after).Seconds())) || retry > int(math.Ceil(sessionReset.Sub(before).Seconds())) {
				t.Fatalf("Retry-After=%q, want about %s", recorder.Header().Get("Retry-After"), sessionReset.Sub(now))
			}
		})
	}
}

func TestClaudeRateLimitHTTPMinuteThrottleAfterSampledWeeklyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			const model = "claude-sonnet-5-5"
			const id = "sampled-weekly"
			now := time.Now().Truncate(time.Second)
			sessionReset := now.Add(time.Hour)
			upstream := http.Header{"Retry-After": {"17"}, "Content-Type": {"application/json"}}
			for _, window := range []string{"", "5h-"} {
				upstream.Set("Anthropic-Ratelimit-Unified-"+window+"Status", "allowed")
				upstream.Set("Anthropic-Ratelimit-Unified-"+window+"Reset", strconv.FormatInt(sessionReset.Unix(), 10))
			}
			executor := &claudeLimitExecutor{refusalPassthroughExecutor: refusalPassthroughExecutor{provider: "claude"}, headers: upstream, err: claudeMinuteLimitError{}}
			manager := coreauth.NewManager(nil, &coreauth.NearestResetSelector{}, nil)
			manager.RegisterExecutor(executor)
			if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "fixture-token"}}); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			sampleClaudeUsage(t, manager, id, claudeUsagePayload(40, sessionReset, 100, now.Add(24*time.Hour)))

			router := claudeSamplerRouter(manager, path)
			for _, attempt := range []string{"executor refusal", "guarded replay"} {
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+model+`","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusTooManyRequests || recorder.Body.String() != claudeMinuteLimitBody {
					t.Fatalf("%s status=%d body=%s", attempt, recorder.Code, recorder.Body.String())
				}
				if executor.calls.Load() != 1 {
					t.Fatalf("%s calls=%d, want 1", attempt, executor.calls.Load())
				}
				for key, values := range upstream {
					if got := recorder.Header().Values(key); len(got) != 1 || got[0] != values[0] {
						t.Fatalf("%s %s=%v, want %v", attempt, key, got, values)
					}
				}
				if claim := recorder.Header().Get("Anthropic-Ratelimit-Unified-Representative-Claim"); claim != "" {
					t.Fatalf("%s synthesized claim %s", attempt, claim)
				}
			}
		})
	}
}
