package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newModelExclusionEngine(t *testing.T, cfg *config.Config, policy *access.KeyPolicy, fableReset time.Time) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if cfg != nil {
		manager.SetConfig(cfg)
	}
	reset := strconv.FormatInt(fableReset.Unix(), 10)
	ultra := &coreauth.Auth{
		ID: "claude-ultra-fixture", Provider: "claude", Status: coreauth.StatusActive,
		ModelStates: map[string]*coreauth.ModelState{
			"claude-fable-5-1": {Quota: coreauth.QuotaState{ObservedAt: time.Now().Add(-time.Hour), Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d_oi-Status": "rejected",
				"Anthropic-Ratelimit-Unified-7d_oi-Reset":  reset,
				"Anthropic-Ratelimit-Unified-Status":       "rejected",
				"Anthropic-Ratelimit-Unified-Reset":        reset,
			}}},
		},
	}
	if _, err := manager.Register(context.Background(), ultra); err != nil {
		t.Fatal(err)
	}
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
	accessManager := access.NewManager()
	accessManager.SetProviders([]access.Provider{keyPolicyAccessProvider{policy: policy}})
	engine := gin.New()
	engine.Use(accessAuthMiddleware(accessManager, false))
	engine.POST("/v1/chat/completions", openai.NewOpenAIAPIHandler(base).ChatCompletions)
	engine.POST("/v1/responses", openai.NewOpenAIResponsesAPIHandler(base).Responses)
	engine.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	engine.POST("/v1/messages/count_tokens", claude.NewClaudeCodeAPIHandler(base).ClaudeCountTokens)
	return engine
}

func modelExclusionConfig(fableUntil time.Time) *config.Config {
	return &config.Config{
		OAuthExcludedModels: map[string][]string{
			"claude": {"claude-fable-5-1", "claude-fable-*"},
			"codex":  {"gpt-6-luna"},
		},
		OAuthExcludedModelsUntil: map[string]map[string]string{
			"claude": {"claude-fable-*": fableUntil.UTC().Format(time.RFC3339)},
		},
	}
}

func postModel(engine *gin.Engine, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return recorder
}

func modelRequestBody(path, model string, stream bool) string {
	encodedModel, _ := json.Marshal(model)
	if strings.HasPrefix(path, "/v1/responses") {
		return `{"model":` + string(encodedModel) + `,"input":"hi","stream":` + strconv.FormatBool(stream) + `}`
	}
	return `{"model":` + string(encodedModel) + `,"max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(stream) + `}`
}

func TestExcludedModelAnswersForbidden(t *testing.T) {
	fableReset := time.Now().Add(96 * time.Hour).Truncate(time.Second).UTC()
	configUntil := time.Now().Add(120 * time.Hour).Truncate(time.Second).UTC()
	engine := newModelExclusionEngine(t, modelExclusionConfig(configUntil), nil, fableReset)
	resetText := fableReset.Format(time.RFC3339)
	configText := configUntil.Format(time.RFC3339)
	for _, tc := range []struct {
		name, path, model string
		stream            bool
		want              string
	}{
		{"messages", "/v1/messages", "claude-fable-5-1", false, `{"type":"error","error":{"type":"model_excluded","message":"model claude-fable-5-1 is excluded on this proxy until ` + resetText + ` (oauth-excluded-models, provider claude); pick another model","until":"` + resetText + `"}}`},
		{"messages stream", "/v1/messages", "claude-fable-5-1", true, `{"type":"error","error":{"type":"model_excluded","message":"model claude-fable-5-1 is excluded on this proxy until ` + resetText + ` (oauth-excluded-models, provider claude); pick another model","until":"` + resetText + `"}}`},
		{"count tokens", "/v1/messages/count_tokens", "claude-fable-6-0", false, `{"type":"error","error":{"type":"model_excluded","message":"model claude-fable-6-0 is excluded on this proxy until ` + configText + ` (oauth-excluded-models, provider claude); pick another model","until":"` + configText + `"}}`},
		{"chat", "/v1/chat/completions", "claude-fable-5-1", false, `{"error":{"type":"model_excluded","code":"model_excluded","param":"model","message":"model claude-fable-5-1 is excluded on this proxy until ` + resetText + ` (oauth-excluded-models, provider claude); pick another model","until":"` + resetText + `"}}`},
		{"chat stream", "/v1/chat/completions", "claude-fable-5-1", true, `{"error":{"type":"model_excluded","code":"model_excluded","param":"model","message":"model claude-fable-5-1 is excluded on this proxy until ` + resetText + ` (oauth-excluded-models, provider claude); pick another model","until":"` + resetText + `"}}`},
		{"chat without until", "/v1/chat/completions", "gpt-6-luna", false, `{"error":{"type":"model_excluded","code":"model_excluded","param":"model","message":"model gpt-6-luna is excluded on this proxy until further notice (oauth-excluded-models, provider codex); pick another model","until":null}}`},
		{"responses", "/v1/responses", "gpt-6-luna", false, `{"error":{"type":"model_excluded","code":"model_excluded","param":"model","message":"model gpt-6-luna is excluded on this proxy until further notice (oauth-excluded-models, provider codex); pick another model","until":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := postModel(engine, tc.path, modelRequestBody(tc.path, tc.model, tc.stream))
			if recorder.Code != http.StatusForbidden || recorder.Body.String() != tc.want {
				t.Fatalf("status = %d body = %s\nwant %s", recorder.Code, recorder.Body.String(), tc.want)
			}
			if recorder.Header().Get("Retry-After") != "" {
				t.Fatal("excluded model answer carries Retry-After")
			}
		})
	}
}

func TestExcludedModelNameIsEncoded(t *testing.T) {
	engine := newModelExclusionEngine(t, modelExclusionConfig(time.Now().Add(time.Hour)), nil, time.Now().Add(time.Hour))
	model := `claude-fable-x","code":"forged`
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		recorder := postModel(engine, path, modelRequestBody(path, model, false))
		var response struct {
			Error struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s: invalid JSON %s", path, recorder.Body.String())
		}
		if recorder.Code != http.StatusForbidden || response.Error.Type != "model_excluded" || response.Error.Code == "forged" || !strings.Contains(response.Error.Message, model) {
			t.Fatalf("%s: status = %d body = %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestUnknownModelAnswerIsUnchanged(t *testing.T) {
	reset := time.Now().Add(time.Hour)
	withExclusions := newModelExclusionEngine(t, modelExclusionConfig(reset), nil, reset)
	withoutExclusions := newModelExclusionEngine(t, nil, nil, reset)
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens", "/v1/responses"} {
		for _, stream := range []bool{false, true} {
			if path == "/v1/messages/count_tokens" && stream {
				continue
			}
			body := modelRequestBody(path, "no-such-model-xyz", stream)
			got, want := postModel(withExclusions, path, body), postModel(withoutExclusions, path, body)
			if got.Code != http.StatusBadRequest || got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Fatalf("%s stream=%v: got %d %s, want %d %s", path, stream, got.Code, got.Body.String(), want.Code, want.Body.String())
			}
		}
	}
	chat := postModel(withExclusions, "/v1/chat/completions", modelRequestBody("/v1/chat/completions", "no-such-model-xyz", false))
	if want := `{"error":{"message":"unknown provider for model no-such-model-xyz","type":"invalid_request_error","code":"model_not_found","param":"model"}}`; chat.Body.String() != want {
		t.Fatalf("chat 400 body = %s", chat.Body.String())
	}
}

func TestKeyPolicyDenialPrecedesExclusion(t *testing.T) {
	reset := time.Now().Add(time.Hour)
	engine := newModelExclusionEngine(t, modelExclusionConfig(reset), &access.KeyPolicy{ExcludedModels: []string{"claude-fable-*"}}, reset)
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		recorder := postModel(engine, path, modelRequestBody(path, "claude-fable-5-1", false))
		var response handlers.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusForbidden || response.Error.Code != "model_not_allowed" {
			t.Fatalf("%s: status = %d body = %s", path, recorder.Code, recorder.Body.String())
		}
	}
}
