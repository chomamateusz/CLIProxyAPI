package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestDeniedModelKeyPolicy(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-response", "claude", "gemini"} {
		for _, mode := range []string{"execute", "count", "stream"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				policy := &access.KeyPolicy{Models: []string{"allowed"}}
				c.Request = httptest.NewRequest("POST", "/", nil).WithContext(access.WithKeyPolicy(context.Background(), policy))
				ctx, cancel := h.GetContextWithCancel(nil, c, context.Background())
				defer cancel()
				var errMsg *interfaces.ErrorMessage
				switch mode {
				case "execute":
					_, _, errMsg = h.ExecuteWithAuthManager(ctx, protocol, "denied(high)", nil, "")
				case "count":
					_, _, errMsg = h.ExecuteCountWithAuthManager(ctx, protocol, "denied(high)", nil, "")
				case "stream":
					data, _, errs := h.ExecuteStreamWithAuthManager(ctx, protocol, "denied(high)", nil, "")
					if data != nil {
						t.Fatal("denied stream returned data")
					}
					errMsg = <-errs
				}
				if errMsg == nil {
					t.Fatal("denied model reached nil auth manager")
				}
				h.WriteErrorResponse(c, errMsg)
				var body ErrorResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if recorder.Code != http.StatusForbidden || body.Error.Type != "permission_error" || body.Error.Code != "model_not_allowed" || body.Error.Message != "model denied(high) is not allowed for this API key" {
					t.Fatalf("unexpected denied response: status=%d body=%s", recorder.Code, recorder.Body.String())
				}
			})
		}
	}
}

func TestAllowedModelKeyPolicyCredentialFilter(t *testing.T) {
	const model = "policy-test-model"
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	calls := 0
	executor := &modelExecutionCaptureExecutor{provider: "gemini", execute: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
		calls++
		if auth.ID != "policy-allowed" {
			t.Error("disallowed credential executed")
		}
		return coreexecutor.Response{Payload: []byte(`{}`)}, nil
	}}
	manager.RegisterExecutor(executor)
	for _, candidate := range []*coreauth.Auth{
		{ID: "policy-blocked", Provider: "gemini", FileName: "/auths/work.json", Attributes: map[string]string{"priority": "10"}},
		{ID: "policy-allowed", Provider: "gemini", FileName: "/auths/personal.json"},
	} {
		if _, err := manager.Register(context.Background(), candidate); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(candidate.ID, "gemini", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
	}
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	ctx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Models: []string{model}, Credentials: []string{"personal"}})
	if _, _, errMsg := h.ExecuteWithAuthManager(ctx, "gemini", model, []byte(`{}`), ""); errMsg != nil {
		t.Fatalf("allowed execution failed: %v", errMsg.Error)
	}
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
	for _, mode := range []string{"count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			ctx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Credentials: []string{"missing"}})
			var errMsg *interfaces.ErrorMessage
			if mode == "count" {
				_, _, errMsg = h.ExecuteCountWithAuthManager(ctx, "gemini", model, []byte(`{}`), "")
			} else {
				_, _, errs := h.ExecuteStreamWithAuthManager(ctx, "gemini", model, []byte(`{}`), "")
				errMsg = <-errs
			}
			if errMsg == nil {
				t.Fatal("credential filter was not applied")
			}
		})
	}
}

func TestKeyPolicyModelListCatalogs(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"openai", `{"data":[{"id":"allowed"},{"id":"denied"}]}`, `{"data":[{"id":"allowed"}]}`},
		{"claude", `{"data":[{"id":"denied"},{"id":"allowed"}],"first_id":"denied","last_id":"allowed"}`, `{"data":[{"id":"allowed"}],"first_id":"allowed","last_id":"allowed"}`},
		{"gemini", `{"models":[{"name":"models/allowed"},{"name":"models/denied"}]}`, `{"models":[{"name":"models/allowed"}]}`},
		{"codex", `{"models":[{"slug":"allowed"},{"slug":"denied"}]}`, `{"models":[{"slug":"allowed"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(access.KeyPolicyGinKey, &access.KeyPolicy{Models: []string{"allowed"}})
			got, err := filterKeyPolicyModelList(c, []byte(tc.body))
			if err != nil || string(got) != tc.want {
				t.Fatalf("filtered catalog = %s, error %v", got, err)
			}
			c.Set(access.KeyPolicyGinKey, (*access.KeyPolicy)(nil))
			got, err = filterKeyPolicyModelList(c, []byte(tc.body))
			if err != nil || string(got) != tc.body {
				t.Fatal("unrestricted catalog changed")
			}
		})
	}
}
