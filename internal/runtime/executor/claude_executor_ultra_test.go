package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutorUltraFableRejectionKeepsOpusSelectable(t *testing.T) {
	var fableAttempts, opusAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
			t.Errorf("decode upstream request: %v", errDecode)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch body.Model {
		case "claude-fable-5-1":
			fableAttempts.Add(1)
			for name, value := range map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Reset":                  "1794153600",
				"Anthropic-Ratelimit-Unified-7d-Status":                 "allowed",
				"Anthropic-Ratelimit-Unified-7d-Utilization":            "0.64",
				"Anthropic-Ratelimit-Unified-7d_oi-Reset":               "1794153600",
				"Anthropic-Ratelimit-Unified-7d_oi-Status":              "rejected",
				"Anthropic-Ratelimit-Unified-7d_oi-Surpassed-Threshold": "1.0",
				"Anthropic-Ratelimit-Unified-7d_oi-Utilization":         "1.0",
				"Anthropic-Ratelimit-Unified-Fallback-Percentage":       "0.5",
				"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason":   "org_level_disabled",
				"Anthropic-Ratelimit-Unified-Overage-Status":            "rejected",
				"Anthropic-Ratelimit-Unified-Representative-Claim":      "seven_day_overage_included",
				"Anthropic-Ratelimit-Unified-Reset":                     "1794153600",
				"Anthropic-Ratelimit-Unified-Status":                    "rejected",
				"Anthropic-Ratelimit-Unified-Upgrade-Paths":             "overage",
				"Retry-After": "417173",
			} {
				w.Header().Set(name, value)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`))
		case "claude-opus-5-5":
			opusAttempts.Add(1)
			_, _ = w.Write([]byte(`{"id":"msg-ultra-opus","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		default:
			t.Errorf("unexpected upstream model: %q", body.Model)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.NearestResetSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewClaudeExecutor(&config.Config{DisableCooling: false}))
	auth := &cliproxyauth.Auth{
		ID: uuid.NewString(), Provider: "claude",
		Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-fable-5-1"}, {ID: "claude-opus-5-5"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-fable-5-1", Payload: payload}, opts); errExecute == nil {
		t.Fatal("Fable request unexpectedly succeeded")
	}
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing after Fable rejection")
	}
	if updated.Quota.Reason == "credential_quota" {
		t.Fatalf("Fable-only rejection cooled the credential: %+v", updated.Quota)
	}
	state := updated.ModelStates["claude-fable-5-1"]
	if state == nil || !state.Quota.Exceeded || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("Fable model cooldown missing: %+v", state)
	}
	if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, opts); errExecute != nil {
		t.Fatalf("Opus on the same credential was not selectable: %v", errExecute)
	}
	if fableAttempts.Load() != 1 || opusAttempts.Load() != 1 {
		t.Fatalf("upstream attempts: Fable=%d Opus=%d, want 1 each", fableAttempts.Load(), opusAttempts.Load())
	}
}
