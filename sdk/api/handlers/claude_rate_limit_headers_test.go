package handlers_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type claudeLimitExecutor struct {
	refusalPassthroughExecutor
	headers http.Header
	err     error
}

func (e *claudeLimitExecutor) Execute(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	internallogging.SetResponseHeaders(ctx, e.headers)
	if e.err != nil {
		return coreexecutor.Response{}, e.err
	}
	return coreexecutor.Response{}, refusalPassthroughError{}
}

func (e *claudeLimitExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	_, err := e.Execute(ctx, auth, req, opts)
	return nil, err
}

type claudeUnavailableSelector struct{}

func (claudeUnavailableSelector) Pick(context.Context, string, string, coreexecutor.Options, []*coreauth.Auth) (*coreauth.Auth, error) {
	return nil, &coreauth.Error{Code: "auth_unavailable", Message: "no auth available", HTTPStatus: http.StatusTooManyRequests}
}

func TestClaudeRateLimitHTTPHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		path  string
		route func(*handlers.BaseAPIHandler) gin.HandlerFunc
	}{
		{"/v1/messages", func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
			return claude.NewClaudeCodeAPIHandler(base).ClaudeMessages
		}},
		{"/v1/chat/completions", func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
			return openai.NewOpenAIAPIHandler(base).ChatCompletions
		}},
		{"/v1/responses", func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
			return openai.NewOpenAIResponsesAPIHandler(base).Responses
		}},
	} {
		for _, stream := range []bool{false, true} {
			for _, origin := range []string{"refusal", "cooldown", "auth_unavailable"} {
				cooldownOnly := origin != "refusal"
				t.Run(endpoint.path+"/stream="+strconv.FormatBool(stream)+"/origin="+origin, func(t *testing.T) {
					const model = "claude-sonnet-5-5"
					reset := time.Now().Truncate(time.Second).Add(10 * time.Minute)
					stale := reset.Add(3 * time.Hour)
					executor := &claudeLimitExecutor{refusalPassthroughExecutor: refusalPassthroughExecutor{provider: "claude"}, headers: make(http.Header)}
					executor.headers.Set("Content-Type", "application/json; charset=utf-8")
					executor.headers.Set("Anthropic-Ratelimit-Unified-Status", "allowed_warning")
					executor.headers.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(stale.Unix(), 10))
					executor.headers.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day")
					executor.headers.Set("Retry-After", "10800")
					var selector coreauth.Selector = &coreauth.NearestResetSelector{}
					if origin == "auth_unavailable" {
						selector = claudeUnavailableSelector{}
					}
					manager := coreauth.NewManager(nil, selector, nil)
					manager.RegisterExecutor(executor)
					for index, id := range []string{"limit-later", "limit-earlier"} {
						auth := &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive, UpdatedAt: time.Now()}
						if index == 1 || cooldownOnly {
							until := reset
							if index == 0 {
								until = stale
							}
							auth.Quota = coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: until, ObservedAt: time.Now(), Signals: map[string]string{
								"Anthropic-Ratelimit-Unified-5h-Status": "rejected",
								"Anthropic-Ratelimit-Unified-5h-Reset":  strconv.FormatInt(until.Unix(), 10),
							}}
						}
						if origin == "auth_unavailable" {
							auth.Quota.Exceeded = false
							auth.Quota.NextRecoverAt = time.Time{}
						}
						if _, err := manager.Register(context.Background(), auth); err != nil {
							t.Fatal(err)
						}
						registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					}
					router := gin.New()
					router.POST(endpoint.path, endpoint.route(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)))
					attempts := []string{"pass-through", "replayed"}
					if cooldownOnly {
						attempts = []string{"modelCooldownError"}
						if origin == "auth_unavailable" {
							attempts = []string{origin}
						}
					}
					for _, scenario := range attempts {
						body := `{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"input":"hi","stream":` + strconv.FormatBool(stream) + `}`
						request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(body))
						request.Header.Set("Content-Type", "application/json")
						recorder := httptest.NewRecorder()
						before := time.Now()
						router.ServeHTTP(recorder, request)
						after := time.Now()
						if recorder.Code != http.StatusTooManyRequests {
							t.Fatalf("%s status=%d body=%s", scenario, recorder.Code, recorder.Body.String())
						}
						if cooldownOnly {
							expected := "All credentials for model"
							if origin == "auth_unavailable" {
								expected = "no auth available"
							}
							if !strings.Contains(recorder.Body.String(), expected) {
								t.Fatalf("expected cooldown, got %s", recorder.Body.String())
							}
							if executor.calls.Load() != 0 {
								t.Fatal("cooldown reached executor")
							}
						} else {
							if recorder.Body.String() != refusalPassthroughBody {
								t.Fatalf("provider body changed: %q", recorder.Body.String())
							}
							if executor.calls.Load() != 1 {
								t.Fatalf("%s calls=%d, want 1", scenario, executor.calls.Load())
							}
							if recorder.Header().Get(coreauth.ProxyNoticeHeader) != coreauth.ProviderRefusalNotice {
								t.Fatal("missing proxy notice")
							}
							if recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
								t.Fatal("content type changed")
							}
						}
						for key, want := range map[string]string{"Anthropic-Ratelimit-Unified-Status": "rejected", "Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(reset.Unix(), 10), "Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour"} {
							if got := recorder.Header().Get(key); got != want {
								t.Fatalf("%s %s=%s, want %s", scenario, key, got, want)
							}
							if len(recorder.Header().Values(key)) != 1 {
								t.Fatalf("duplicate header %s", key)
							}
						}
						retry, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
						if err != nil || retry < 1 || retry < int(math.Ceil(reset.Sub(after).Seconds())) || retry > int(math.Ceil(reset.Sub(before).Seconds())) {
							t.Fatalf("Retry-After=%q", recorder.Header().Get("Retry-After"))
						}
						if len(recorder.Header().Values("Retry-After")) != 1 {
							t.Fatal("duplicate Retry-After")
						}
						t.Logf("%s headers: anthropic-ratelimit-unified-status=%s anthropic-ratelimit-unified-reset=%s anthropic-ratelimit-unified-representative-claim=%s Retry-After=%s", scenario, recorder.Header().Get("Anthropic-Ratelimit-Unified-Status"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Reset"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Representative-Claim"), recorder.Header().Get("Retry-After"))
					}
				})
			}
		}
	}
}

func TestClaudeRateLimitHTTPFilteredEligibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		for _, scenario := range []string{"other provider excluded", "mapped model excluded"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				const model = "claude-sonnet-5-5"
				reset := time.Now().Truncate(time.Second).Add(10 * time.Minute)
				manager := coreauth.NewManager(nil, claudeUnavailableSelector{}, nil)
				manager.RegisterExecutor(&claudeLimitExecutor{refusalPassthroughExecutor: refusalPassthroughExecutor{provider: "claude"}})
				auth := &coreauth.Auth{ID: "eligible", Provider: "claude", Status: coreauth.StatusActive, Quota: coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: reset}}
				var policy *access.KeyPolicy
				register := func(a *coreauth.Auth) {
					t.Helper()
					if _, err := manager.Register(context.Background(), a); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				}
				claim := "seven_day"
				switch scenario {
				case "other provider excluded":
					manager.RegisterExecutor(&refusalPassthroughExecutor{provider: "antigravity"})
					register(&coreauth.Auth{ID: "excluded", Provider: "antigravity", Status: coreauth.StatusActive})
					policy = &access.KeyPolicy{Credentials: []string{"eligible"}}
				case "mapped model excluded":
					manager.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{
						{APIKey: "fixture-allowed", Models: []internalconfig.ClaudeModel{{Name: "claude-allowed", Alias: model}}},
						{APIKey: "fixture-denied", Models: []internalconfig.ClaudeModel{{Name: "claude-denied", Alias: model}}},
					}})
					attributes := func(key string) map[string]string {
						return map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey, coreauth.AttributeAPIKey: key}
					}
					auth.Attributes = attributes("fixture-allowed")
					register(&coreauth.Auth{ID: "denied-mapping", Provider: "claude", Status: coreauth.StatusActive, Attributes: attributes("fixture-denied")})
					policy = &access.KeyPolicy{ExcludedModels: []string{"claude-denied"}}
				}
				register(auth)
				base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
				router := gin.New()
				if path == "/v1/messages" {
					router.POST(path, claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
				} else {
					router.POST(path, openai.NewOpenAIAPIHandler(base).ChatCompletions)
				}
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+model+`","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
				request = request.WithContext(access.WithKeyPolicy(request.Context(), policy))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusTooManyRequests {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				for key, want := range map[string]string{"Anthropic-Ratelimit-Unified-Status": "rejected", "Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(reset.Unix(), 10), "Anthropic-Ratelimit-Unified-Representative-Claim": claim} {
					if recorder.Header().Get(key) != want {
						t.Fatalf("%s=%s, want %s", key, recorder.Header().Get(key), want)
					}
				}
				t.Logf("%s headers: anthropic-ratelimit-unified-status=%s anthropic-ratelimit-unified-reset=%s anthropic-ratelimit-unified-representative-claim=%q Retry-After=%s", scenario, recorder.Header().Get("Anthropic-Ratelimit-Unified-Status"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Reset"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Representative-Claim"), recorder.Header().Get("Retry-After"))
			})
		}
	}
}

const claudeMinuteLimitBody = `{"error":{"type":"rate_limit_error","message":"requests per minute exceeded"}}`

type claudeMinuteLimitError struct{}

func (claudeMinuteLimitError) Error() string   { return claudeMinuteLimitBody }
func (claudeMinuteLimitError) StatusCode() int { return http.StatusTooManyRequests }
func (claudeMinuteLimitError) RetryAfter() *time.Duration {
	retry := 17 * time.Second
	return &retry
}

func TestClaudeRateLimitHTTPMinuteThrottle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		for _, stream := range []bool{false, true} {
			for _, scenario := range []struct {
				name    string
				status  string
				windows []string
				claim   string
			}{
				{name: "absent"},
				{name: "allowed", status: "allowed", windows: []string{"5h-", "7d-"}},
				{name: "weekly warning claim", status: "allowed_warning", windows: []string{"7d-"}, claim: "seven_day"},
				{name: "session allowed claim", status: "allowed", windows: []string{"5h-"}, claim: "five_hour"},
				{name: "session rejected", status: "rejected", windows: []string{"5h-"}},
			} {
				t.Run(path+"/stream="+strconv.FormatBool(stream)+"/signals="+scenario.name, func(t *testing.T) {
					const model = "claude-sonnet-5-5"
					upstream := http.Header{"Retry-After": {"17"}, "Content-Type": {"application/json"}}
					reset := strconv.FormatInt(time.Now().Truncate(time.Second).Add(time.Hour).Unix(), 10)
					if scenario.status != "" {
						for _, window := range append([]string{""}, scenario.windows...) {
							upstream.Set("Anthropic-Ratelimit-Unified-"+window+"Status", scenario.status)
							upstream.Set("Anthropic-Ratelimit-Unified-"+window+"Reset", reset)
						}
					}
					if scenario.claim != "" {
						upstream.Set("Anthropic-Ratelimit-Unified-Representative-Claim", scenario.claim)
					}
					executor := &claudeLimitExecutor{refusalPassthroughExecutor: refusalPassthroughExecutor{provider: "claude"}, headers: upstream, err: claudeMinuteLimitError{}}
					manager := coreauth.NewManager(nil, &coreauth.NearestResetSelector{}, nil)
					manager.RegisterExecutor(executor)
					if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "minute-throttle", Provider: "claude", Status: coreauth.StatusActive}); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient("minute-throttle", "claude", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("minute-throttle") })
					base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
					router := gin.New()
					if path == "/v1/messages" {
						router.POST(path, claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
					} else {
						router.POST(path, openai.NewOpenAIAPIHandler(base).ChatCompletions)
					}
					for _, attempt := range []string{"executor refusal", "guarded replay"} {
						body := `{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(stream) + `}`
						request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
						request.Header.Set("Content-Type", "application/json")
						recorder := httptest.NewRecorder()
						router.ServeHTTP(recorder, request)
						if recorder.Code != http.StatusTooManyRequests || recorder.Body.String() != claudeMinuteLimitBody {
							t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
						}
						stored, ok := manager.GetByID("minute-throttle")
						if !ok || stored.ModelStates[model] == nil || !stored.ModelStates[model].Quota.Exceeded || stored.ModelStates[model].Quota.Reason != "quota" {
							t.Fatalf("executor result did not record model quota cooldown: %+v", stored)
						}
						if executor.calls.Load() != 1 {
							t.Fatalf("calls=%d, want 1", executor.calls.Load())
						}
						if scenario.status == "rejected" {
							for key, want := range map[string]string{
								"Anthropic-Ratelimit-Unified-Status":               "rejected",
								"Anthropic-Ratelimit-Unified-Reset":                reset,
								"Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour",
							} {
								if got := recorder.Header().Get(key); got != want {
									t.Fatalf("%s=%s, want %s", key, got, want)
								}
							}
							retry, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
							if err != nil || retry < 1 {
								t.Fatalf("invalid Retry-After: %q", recorder.Header().Get("Retry-After"))
							}
						} else {
							for key, values := range upstream {
								if got := recorder.Header().Values(key); len(got) != 1 || got[0] != values[0] {
									t.Fatalf("%s changed: %v, want %v", key, got, values)
								}
							}
							for _, key := range []string{"Anthropic-Ratelimit-Unified-Status", "Anthropic-Ratelimit-Unified-Reset", "Anthropic-Ratelimit-Unified-Representative-Claim"} {
								if recorder.Header().Get(key) != upstream.Get(key) {
									t.Fatalf("synthesized %s: %s", key, recorder.Header().Get(key))
								}
							}
						}
						t.Logf("%s headers: anthropic-ratelimit-unified-status=%q anthropic-ratelimit-unified-reset=%q anthropic-ratelimit-unified-representative-claim=%q Retry-After=%s", attempt, recorder.Header().Get("Anthropic-Ratelimit-Unified-Status"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Reset"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Representative-Claim"), recorder.Header().Get("Retry-After"))
					}
				})
			}
		}
	}
}
