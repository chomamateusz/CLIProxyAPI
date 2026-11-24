package claude

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestClaudeKeyPolicyUsesTheExecutedModel(t *testing.T) {
	const sol, astra, fable = "gpt-6.2-sol", "gpt-6.2-astra", "claude-fable-5-2"
	policy := &access.KeyPolicy{ExcludedModels: []string{"claude-fable-*", astra}}
	executor := &policyModelExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: t.Name(), Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: sol}, {ID: astra}, {ID: fable}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(nil, manager))
	for _, mode := range []struct {
		name, path string
		stream     bool
		handle     gin.HandlerFunc
	}{
		{"messages", "/v1/messages", false, handler.ClaudeMessages},
		{"stream", "/v1/messages", true, handler.ClaudeMessages},
		{"count", "/v1/messages/count_tokens", false, handler.ClaudeCountTokens},
	} {
		for _, tc := range []struct {
			name, alias, model string
			policy             *access.KeyPolicy
			status             int
		}{
			{"sol", claudemodels.EnsureClaudeModelIDPrefix(sol), sol, policy, http.StatusOK},
			{"astra", claudemodels.EnsureClaudeModelIDPrefix(astra), astra, policy, http.StatusForbidden},
			{"legacy-exact-suffix", claudemodels.EnsureClaudeModelIDPrefix(astra) + "(high)", astra + "(high)", &access.KeyPolicy{ExcludedModels: []string{" CLAUDE-FABLE-5-DD-ARTSA-2.6-TPG "}}, http.StatusForbidden},
			{"fable", "claude-fable-5-dd-2-5-elbaf-edualc", fable, policy, http.StatusForbidden},
			{"wildcard", claudemodels.EnsureClaudeModelIDPrefix(sol), sol, &access.KeyPolicy{Models: []string{"claude-*"}}, http.StatusForbidden},
		} {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				executor.requests = nil
				body := fmt.Sprintf(`{"model":%q,"messages":[],"stream":%t}`, tc.alias, mode.stream)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, mode.path, strings.NewReader(body))
				c.Request = c.Request.WithContext(access.WithKeyPolicy(c.Request.Context(), tc.policy))
				mode.handle(c)
				if recorder.Code != tc.status {
					t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.status, recorder.Body.String())
				}
				if tc.status == http.StatusForbidden {
					if len(executor.requests) != 0 || gjson.Get(recorder.Body.String(), "error.code").String() != "model_not_allowed" ||
						gjson.Get(recorder.Body.String(), "error.message").String() != "model "+tc.model+" is not allowed for this API key" {
						t.Fatalf("expected a policy rejection with no execution; body=%s, executions=%d", recorder.Body.String(), len(executor.requests))
					}
					return
				}
				if len(executor.requests) != 1 {
					t.Fatalf("executor calls = %d, want 1", len(executor.requests))
				}
				request := executor.requests[0]
				if payloadModel := gjson.GetBytes(request.Payload, "model").String(); request.Model != tc.model || payloadModel != tc.model {
					t.Fatalf("execution model = %q, payload model = %q, want %q", request.Model, payloadModel, tc.model)
				}
			})
		}
	}
}

type policyModelExecutor struct {
	requests []coreexecutor.Request
}

func (*policyModelExecutor) Identifier() string { return "policy-model-test" }

func (e *policyModelExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.requests = append(e.requests, req)
	return coreexecutor.Response{Payload: []byte(`{}`)}, nil
}

func (e *policyModelExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *policyModelExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.requests = append(e.requests, req)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*policyModelExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*policyModelExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected HTTP execution")
}
