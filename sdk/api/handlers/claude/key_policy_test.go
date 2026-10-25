package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestKeyPolicyChecksClientModelBeforeCloakingAlias(t *testing.T) {
	const target = "policy-model"
	alias := claudemodels.EnsureClaudeModelIDPrefix(target)
	for _, mode := range []string{"messages", "stream", "count"} {
		for _, allowAlias := range []bool{false, true} {
			name := mode + "/denied"
			allowed := target
			if allowAlias {
				name = mode + "/allowed"
				allowed = alias
			}
			t.Run(name, func(t *testing.T) {
				base := handlers.NewBaseAPIHandlers(nil, coreauth.NewManager(nil, nil, nil))
				handler := NewClaudeCodeAPIHandler(base)
				body := map[string]any{"model": alias, "messages": []any{}, "stream": mode == "stream"}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				ctx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Models: []string{allowed}})
				c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(raw))).WithContext(ctx)
				if mode == "count" {
					handler.ClaudeCountTokens(c)
				} else {
					handler.ClaudeMessages(c)
				}
				if allowAlias {
					if recorder.Code == http.StatusForbidden {
						t.Fatal("client alias was checked after resolution")
					}
				} else {
					var response handlers.ErrorResponse
					if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if recorder.Code != http.StatusForbidden || response.Error.Code != "model_not_allowed" || response.Error.Message != "model "+alias+" is not allowed for this API key" {
						t.Fatalf("unexpected policy response: %s", recorder.Body.String())
					}
				}
			})
		}
	}
}
