package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const refusalPassthroughBody = "{\n  \"error\": {\n    \"type\": \"usage_limit_reached\",\n    \"message\": \"The usage limit has been reached\",\n    \"resets_in_seconds\": 3600\n  }\n}\n"

type refusalPassthroughError struct{}

func (refusalPassthroughError) Error() string   { return refusalPassthroughBody }
func (refusalPassthroughError) StatusCode() int { return http.StatusTooManyRequests }
func (refusalPassthroughError) RetryAfter() *time.Duration {
	retryAfter := time.Hour
	return &retryAfter
}

type refusalPassthroughExecutor struct {
	provider string
	calls    atomic.Int32
}

func (e *refusalPassthroughExecutor) Identifier() string { return e.provider }

func (e *refusalPassthroughExecutor) refuse(ctx context.Context) error {
	e.calls.Add(1)
	internallogging.SetResponseHeaders(ctx, refusalPassthroughHeaders())
	return refusalPassthroughError{}
}

func (e *refusalPassthroughExecutor) Execute(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, e.refuse(ctx)
}

func (e *refusalPassthroughExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, e.refuse(ctx)
}

func (e *refusalPassthroughExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *refusalPassthroughExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *refusalPassthroughExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func refusalPassthroughHeaders() http.Header {
	return http.Header{
		"Content-Type":                          []string{"application/json; charset=utf-8"},
		"Retry-After":                           []string{"3600"},
		"X-Codex-Primary-Used-Percent":          []string{"100"},
		"X-Codex-Primary-Reset-After-Seconds":   []string{"3600"},
		"Anthropic-Ratelimit-Unified-5h-Status": []string{"rejected"},
		"X-Ratelimit-Reset-Requests":            []string{"1h"},
	}
}

func TestProviderRefusalPassesThroughVerbatim(t *testing.T) {
	gin.SetMode(gin.TestMode)
	endpoints := []struct {
		name  string
		path  string
		route func(*handlers.BaseAPIHandler) gin.HandlerFunc
		body  func(model string, stream bool) string
	}{
		{
			name: "anthropic messages",
			path: "/v1/messages",
			route: func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
				return claude.NewClaudeCodeAPIHandler(base).ClaudeMessages
			},
			body: func(model string, stream bool) string {
				return `{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(stream) + `}`
			},
		},
		{
			name: "openai chat completions",
			path: "/v1/chat/completions",
			route: func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
				return openai.NewOpenAIAPIHandler(base).ChatCompletions
			},
			body: func(model string, stream bool) string {
				return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(stream) + `}`
			},
		},
		{
			name: "openai responses",
			path: "/v1/responses",
			route: func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
				return openai.NewOpenAIResponsesAPIHandler(base).Responses
			},
			body: func(model string, stream bool) string {
				return `{"model":"` + model + `","input":"hi","stream":` + strconv.FormatBool(stream) + `}`
			},
		},
	}
	for index, endpoint := range endpoints {
		for _, stream := range []bool{false, true} {
			name := endpoint.name + "/stream=" + strconv.FormatBool(stream)
			t.Run(name, func(t *testing.T) {
				suffix := strconv.Itoa(index) + "-" + strconv.FormatBool(stream)
				executor := &refusalPassthroughExecutor{provider: "refusal-passthrough-" + suffix}
				model := "refusal-passthrough-model-" + suffix
				manager := coreauth.NewManager(nil, &coreauth.NearestResetSelector{}, nil)
				manager.RegisterExecutor(executor)
				auth := &coreauth.Auth{ID: "refusal-passthrough-auth-" + suffix, Provider: executor.provider, Status: coreauth.StatusActive}
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

				router := gin.New()
				router.POST(endpoint.path, endpoint.route(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)))
				send := func() *httptest.ResponseRecorder {
					request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(endpoint.body(model, stream)))
					request.Header.Set("Content-Type", "application/json")
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					return recorder
				}

				for attempt, wantCalls := range []int32{1, 1} {
					recorder := send()
					if recorder.Code != http.StatusTooManyRequests {
						t.Fatalf("attempt %d status = %d, want 429; body=%q", attempt, recorder.Code, recorder.Body.String())
					}
					if got := recorder.Body.String(); got != refusalPassthroughBody {
						t.Fatalf("attempt %d body = %q, want provider body %q", attempt, got, refusalPassthroughBody)
					}
					for key, values := range refusalPassthroughHeaders() {
						if got := recorder.Header().Get(key); got != values[0] {
							t.Fatalf("attempt %d header %s = %q, want %q", attempt, key, got, values[0])
						}
					}
					if got := recorder.Header().Get(coreauth.ProxyNoticeHeader); got != coreauth.ProviderRefusalNotice {
						t.Fatalf("attempt %d %s = %q", attempt, coreauth.ProxyNoticeHeader, got)
					}
					if got := executor.calls.Load(); got != wantCalls {
						t.Fatalf("attempt %d provider calls = %d, want %d", attempt, got, wantCalls)
					}
				}
			})
		}
	}
}
