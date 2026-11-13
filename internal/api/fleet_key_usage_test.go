package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/fleetusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type principalAccessProvider struct {
	principal string
	policy    *access.KeyPolicy
}

func (p principalAccessProvider) Identifier() string { return "fleet-usage-test" }
func (p principalAccessProvider) Authenticate(context.Context, *http.Request) (*access.Result, *access.AuthError) {
	return &access.Result{Provider: p.Identifier(), Principal: p.principal, Policy: p.policy}, nil
}

type keyNameCapture struct {
	model string
	names chan string
}

func (p *keyNameCapture) HandleUsage(ctx context.Context, record coreusage.Record) {
	if record.Model != p.model {
		return
	}
	select {
	case p.names <- fleetusage.KeyName(ctx, record):
	default:
	}
}

type noopUsagePlugin struct{}

func (noopUsagePlugin) HandleUsage(context.Context, coreusage.Record) {}

type publishingExecutor struct{ provider string }

func (e *publishingExecutor) Identifier() string { return e.provider }

func (e *publishingExecutor) publish(ctx context.Context, model string) {
	coreusage.PublishRecord(ctx, coreusage.Record{Provider: e.provider, Model: model, Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 1}})
}

func (e *publishingExecutor) Execute(ctx context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.publish(ctx, req.Model)
	return coreexecutor.Response{Payload: []byte(`{}`)}, nil
}

func (e *publishingExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.publish(ctx, req.Model)
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *publishingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *publishingExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *publishingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestClientKeyNameReachesUsagePlugin(t *testing.T) {
	const raw = "sk-fleet-usage-raw-fixture"
	sum := sha256.Sum256([]byte(raw))
	unnamed := "unnamed-" + hex.EncodeToString(sum[:])[:8]
	claudeBody := func(model string, stream bool) string {
		return `{"model":"` + model + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(stream) + `}`
	}
	chatBody := func(model string, stream bool) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(stream) + `}`
	}
	responsesBody := func(model string, stream bool) string {
		return `{"model":"` + model + `","input":"hi","stream":` + strconv.FormatBool(stream) + `}`
	}
	endpoints := []struct {
		path  string
		route func(*handlers.BaseAPIHandler) gin.HandlerFunc
		body  func(model string, stream bool) string
	}{
		{"/v1/messages", func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
			return claude.NewClaudeCodeAPIHandler(base).ClaudeMessages
		}, claudeBody},
		{"/v1/chat/completions", func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
			return openai.NewOpenAIAPIHandler(base).ChatCompletions
		}, chatBody},
		{"/v1/responses", func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
			return openai.NewOpenAIResponsesAPIHandler(base).Responses
		}, responsesBody},
	}
	identities := []struct {
		name     string
		provider access.Provider
		want     string
	}{
		{"named", principalAccessProvider{principal: raw, policy: &access.KeyPolicy{Key: raw, Name: "laptop"}}, "laptop"},
		{"unnamed", principalAccessProvider{principal: raw, policy: &access.KeyPolicy{Key: raw}}, unnamed},
		{"plain", principalAccessProvider{principal: raw}, unnamed},
		{"anonymous", nil, fleetusage.AnonymousKey},
	}
	for index, endpoint := range endpoints {
		for _, stream := range []bool{false, true} {
			for _, identity := range identities {
				suffix := strconv.Itoa(index) + "-" + strconv.FormatBool(stream) + "-" + identity.name
				t.Run(endpoint.path+"/stream="+strconv.FormatBool(stream)+"/"+identity.name, func(t *testing.T) {
					executor := &publishingExecutor{provider: "fleet-usage-" + suffix}
					model := "fleet-usage-model-" + suffix
					manager := coreauth.NewManager(nil, &coreauth.NearestResetSelector{}, nil)
					manager.RegisterExecutor(executor)
					auth := &coreauth.Auth{ID: "fleet-usage-auth-" + suffix, Provider: executor.provider, Status: coreauth.StatusActive}
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatalf("register auth: %v", errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

					capture := &keyNameCapture{model: model, names: make(chan string, 8)}
					coreusage.RegisterNamedPlugin("fleet-key-usage-test", capture)
					t.Cleanup(func() { coreusage.RegisterNamedPlugin("fleet-key-usage-test", noopUsagePlugin{}) })

					var accessManager *access.Manager
					if identity.provider != nil {
						accessManager = access.NewManager()
						accessManager.SetProviders([]access.Provider{identity.provider})
					}
					router := gin.New()
					router.Use(accessAuthMiddleware(accessManager, false))
					router.POST(endpoint.path, endpoint.route(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)))
					request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(endpoint.body(model, stream)))
					request.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(httptest.NewRecorder(), request)

					select {
					case got := <-capture.names:
						if got != identity.want {
							t.Fatalf("key name = %q, want %q", got, identity.want)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("usage record did not reach the plugin")
					}
				})
			}
		}
	}
}
