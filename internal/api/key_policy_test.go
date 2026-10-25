package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	codexlive "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/live"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type keyPolicyAccessProvider struct{ policy *access.KeyPolicy }

func (p keyPolicyAccessProvider) Identifier() string { return "policy-test" }
func (p keyPolicyAccessProvider) Authenticate(context.Context, *http.Request) (*access.Result, *access.AuthError) {
	return &access.Result{Provider: p.Identifier(), Policy: p.policy}, nil
}

func TestKeyPolicyModelEndpoints(t *testing.T) {
	const allowed, denied = "claude-policy-allowed", "claude-policy-denied"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("policy-catalog", "claude", []*registry.ModelInfo{{ID: allowed}, {ID: denied}})
	t.Cleanup(func() { reg.UnregisterClient("policy-catalog") })
	for _, useHome := range []bool{false, true} {
		name := "local"
		if useHome {
			name = "home"
		}
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Home.Enabled = useHome
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, nil)
			server := &Server{cfg: cfg, handlers: base}
			if useHome {
				previous := home.Current()
				home.SetCurrent(newHomeCatalogClient(t, `{"claude":[{"id":"claude-policy-allowed"},{"id":"claude-policy-denied"}]}`))
				t.Cleanup(func() { home.SetCurrent(previous) })
			}
			for _, tc := range []struct{ name, path, header, field string }{
				{"openai", "/v1/models", "", "data"},
				{"anthropic", "/v1/models", "2023-06-01", "data"},
				{"gemini", "/v1beta/models", "", "models"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					manager := access.NewManager()
					engine := gin.New()
					engine.Use(accessAuthMiddleware(manager, false))
					engine.GET("/v1/models", server.unifiedModelsHandler(openai.NewOpenAIAPIHandler(base), claude.NewClaudeCodeAPIHandler(base)))
					engine.GET("/v1beta/models", server.geminiModelsHandler(gemini.NewGeminiAPIHandler(base)))
					for _, policy := range []*access.KeyPolicy{nil, {Models: []string{"claude-policy-*"}, ExcludedModels: []string{denied}}, nil} {
						manager.SetProviders([]access.Provider{keyPolicyAccessProvider{policy: policy}})
						request := httptest.NewRequest("GET", tc.path, nil)
						request.Header.Set("Anthropic-Version", tc.header)
						recorder := httptest.NewRecorder()
						engine.ServeHTTP(recorder, request)
						if recorder.Code != http.StatusOK {
							t.Fatalf("model list status = %d", recorder.Code)
						}
						var response map[string]json.RawMessage
						if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						var models []map[string]any
						if err := json.Unmarshal(response[tc.field], &models); err != nil {
							t.Fatal(err)
						}
						foundAllowed, foundDenied := false, false
						for _, model := range models {
							id, _ := model["id"].(string)
							if id == "" {
								id, _ = model["name"].(string)
								id = strings.TrimPrefix(id, "models/")
							}
							foundAllowed = foundAllowed || id == allowed
							foundDenied = foundDenied || id == denied
							if policy != nil && !policy.AllowsModel(id) {
								t.Errorf("disallowed model listed: %s", id)
							}
						}
						if !foundAllowed || foundDenied != (policy == nil) {
							t.Fatal("catalog does not match policy, or shared catalog was mutated")
						}
					}
				})
			}
		})
	}
}

func TestKeyPolicyMiddlewareContext(t *testing.T) {
	policy := &access.KeyPolicy{Name: "macbook"}
	manager := access.NewManager()
	manager.SetProviders([]access.Provider{keyPolicyAccessProvider{policy: policy}})
	engine := gin.New()
	engine.Use(accessAuthMiddleware(manager, false))
	engine.GET("/", func(c *gin.Context) {
		if value, _ := c.Get(access.KeyPolicyGinKey); value != policy {
			t.Error("gin policy missing")
		}
		if access.KeyPolicyFromContext(c.Request.Context()) != policy {
			t.Error("request policy missing")
		}
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}

func TestKeyPolicyExecutionEndpoints(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	manager := access.NewManager()
	manager.SetProviders([]access.Provider{keyPolicyAccessProvider{policy: &access.KeyPolicy{Models: []string{"allowed"}}}})
	engine := gin.New()
	engine.Use(accessAuthMiddleware(manager, false))
	engine.POST("/v1/chat/completions", openai.NewOpenAIAPIHandler(base).ChatCompletions)
	engine.POST("/v1/responses", openai.NewOpenAIResponsesAPIHandler(base).Responses)
	engine.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	engine.POST("/v1/messages/count_tokens", claude.NewClaudeCodeAPIHandler(base).ClaudeCountTokens)
	engine.POST("/v1beta/models/*action", gemini.NewGeminiAPIHandler(base).GeminiHandler)
	for _, tc := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"denied","messages":[]}`},
		{"chat stream", "/v1/chat/completions", `{"model":"denied","messages":[],"stream":true}`},
		{"responses", "/v1/responses", `{"model":"denied","input":"hi"}`},
		{"responses stream", "/v1/responses", `{"model":"denied","input":"hi","stream":true}`},
		{"claude", "/v1/messages", `{"model":"denied","messages":[]}`},
		{"claude stream", "/v1/messages", `{"model":"denied","messages":[],"stream":true}`},
		{"claude count", "/v1/messages/count_tokens", `{"model":"denied","messages":[]}`},
		{"gemini", "/v1beta/models/denied:generateContent", `{"contents":[]}`},
		{"gemini stream", "/v1beta/models/denied:streamGenerateContent", `{"contents":[]}`},
		{"gemini count", "/v1beta/models/denied:countTokens", `{"contents":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			var response handlers.ErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusForbidden || response.Error.Type != "permission_error" || response.Error.Code != "model_not_allowed" {
				t.Fatalf("unexpected denial: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCodexAlphaSearchRejectsDeniedModel(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	server := &Server{handlers: base}
	manager := access.NewManager()
	manager.SetProviders([]access.Provider{keyPolicyAccessProvider{policy: &access.KeyPolicy{Models: []string{"allowed"}}}})
	router := gin.New()
	router.Use(accessAuthMiddleware(manager, false))
	for _, path := range []string{"/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		router.POST(path, server.codexAlphaSearch)
	}
	for _, path := range []string{"/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		for _, body := range []string{`{"model":"denied","query":"test"}`, `{"query":"test"}`} {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest("POST", path, strings.NewReader(body)))
			if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "model_not_allowed") {
				t.Fatalf("%s: status = %d", path, recorder.Code)
			}
		}
	}
	base.SetModelRouterHost(&codexSearchModelRouter{response: pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "codex", TargetModel: "denied"}, handled: true})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/alpha/search", strings.NewReader(`{"model":"allowed","query":"test"}`)))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("routed target status = %d", recorder.Code)
	}
}

func TestRealtimeClientSecretRechecksIssuerPolicy(t *testing.T) {
	configaccess.Register(&config.SDKConfig{APIKeys: []string{"client-fixture"}, APIKeyPolicies: []access.KeyPolicy{{Key: "client-fixture", Models: []string{"gpt-realtime"}}}})
	t.Cleanup(func() { configaccess.Register(nil) })
	manager := access.NewManager()
	manager.SetProviders(access.RegisteredProviders())
	liveHandler := codexlive.NewHandler(coreauth.NewManager(nil, nil, nil), nil)
	router := gin.New()
	router.POST("/mint", accessAuthMiddleware(manager, true), liveHandler.CreateClientSecret)
	router.GET("/check", realtimeAuthMiddleware(manager, liveHandler), func(c *gin.Context) {
		policy := access.KeyPolicyFromContext(c.Request.Context())
		if policy == nil || !policy.AllowsModel("gpt-realtime") || policy.AllowsModel("other") {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest("POST", "/mint", strings.NewReader(`{"session":{"type":"realtime","model":"gpt-realtime"}}`))
	req.Header.Set("Authorization", "Bearer client-fixture")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint status = %d", rec.Code)
	}
	var minted struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &minted) != nil {
		t.Fatal("invalid mint response")
	}
	check := func(want int) {
		req := httptest.NewRequest("GET", "/check", nil)
		req.Header.Set("Authorization", "Bearer "+minted.Value)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("secret status = %d, want %d", rec.Code, want)
		}
	}
	check(http.StatusOK)
	configaccess.Register(nil)
	manager.SetProviders(nil)
	check(http.StatusUnauthorized)
}

func TestRealtimeClientSecretFromPlainKeySurvivesIssuerRemoval(t *testing.T) {
	configaccess.Register(&config.SDKConfig{APIKeys: []string{"plain-fixture"}})
	t.Cleanup(func() { configaccess.Register(nil) })
	manager := access.NewManager()
	manager.SetProviders(access.RegisteredProviders())
	liveHandler := codexlive.NewHandler(coreauth.NewManager(nil, nil, nil), nil)
	router := gin.New()
	router.POST("/mint", accessAuthMiddleware(manager, true), liveHandler.CreateClientSecret)
	router.GET("/check", realtimeAuthMiddleware(manager, liveHandler), func(c *gin.Context) {
		if access.KeyPolicyFromContext(c.Request.Context()) != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})
	mintRequest := httptest.NewRequest(http.MethodPost, "/mint", strings.NewReader(`{"session":{"type":"realtime","model":"gpt-realtime"}}`))
	mintRequest.Header.Set("Authorization", "Bearer plain-fixture")
	mintResponse := httptest.NewRecorder()
	router.ServeHTTP(mintResponse, mintRequest)
	if mintResponse.Code != http.StatusOK {
		t.Fatalf("mint status = %d", mintResponse.Code)
	}
	var minted struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(mintResponse.Body.Bytes(), &minted); err != nil || minted.Value == "" {
		t.Fatal("invalid mint response")
	}
	configaccess.Register(nil)
	manager.SetProviders(nil)
	checkRequest := httptest.NewRequest(http.MethodGet, "/check", nil)
	checkRequest.Header.Set("Authorization", "Bearer "+minted.Value)
	checkResponse := httptest.NewRecorder()
	router.ServeHTTP(checkResponse, checkRequest)
	if checkResponse.Code != http.StatusOK {
		t.Fatalf("secret status = %d, want %d", checkResponse.Code, http.StatusOK)
	}
}
