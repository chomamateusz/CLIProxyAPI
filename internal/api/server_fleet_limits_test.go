package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	fleetKeyFull    = "client-full-fixture-7f3a"
	fleetKeyM1      = "client-m1-fixture-91bd"
	fleetKeyPlain   = "client-plain-fixture-c04e"
	fleetIssuerKey  = "issuer-fixture-5e21"
	fleetMgmtSecret = "management-fixture-2b88"
	fleetUltraEmail = "ultra.owner@example.com"
	fleetMaxEmail   = "max.owner@example.com"
	fleetTeamEmail  = "team.owner@example.com"
	fleetCodexEmail = "codex.owner@example.com"
	fleetOffEmail   = "off.owner@example.com"
	fleetAccountID  = "acct-7d1c9f0e-fixture"
	fleetUltraToken = "sk-ant-oat01-ultra-token-fixture"
	fleetCodexToken = "codex-access-token-fixture-9f2"
)

var fleetNow = time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)

type countingExecutor struct {
	provider string
	calls    *atomic.Int64
}

func (e countingExecutor) Identifier() string { return e.provider }
func (e countingExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, nil
}
func (e countingExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, nil
}
func (e countingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.calls.Add(1)
	return auth, nil
}
func (e countingExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, nil
}
func (e countingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	return nil, nil
}

const fleetLimitsConfigYAML = `config-version: 8
access:
  api-keys:
    - key: ` + fleetKeyFull + `
      name: macbook
    - key: ` + fleetKeyM1 + `
      name: m1
      excluded-models: ["claude-fable-*"]
      credentials: ["claude-max-*", "codex-*"]
    - ` + fleetKeyPlain + `
  key-issuer-keys:
    - key: ` + fleetIssuerKey + `
      name: orchestrator
oauth:
  excluded-models:
    claude: [claude-fable-5-1, claude-fable-*]
    codex: [gpt-6-luna, gpt-6-sol, gpt-5-codex-mini]
oauth-excluded-models-until:
  Codex:
    GPT-6-Luna: "2026-11-10T02:00:00+02:00"
`

func fleetAuthFile(dir, provider, email string) string {
	return filepath.Join(dir, provider+"-"+email+".json")
}

func newFleetLimitsServer(t *testing.T, withManagementSecret bool) (*Server, *atomic.Int64, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("MANAGEMENT_PASSWORD", "")
	previousNow := fleetLimitsNow
	fleetLimitsNow = func() time.Time { return fleetNow }
	t.Cleanup(func() { fleetLimitsNow = previousNow })

	dir := t.TempDir()
	raw := fleetLimitsConfigYAML
	if withManagementSecret {
		raw = strings.Replace(raw, "oauth:\n", "management:\n  secret-key: "+fleetMgmtSecret+"\noauth:\n", 1)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	authDir := filepath.Join(dir, "auth")
	cfg.AuthDir = authDir
	cfg.Port = 0

	manager := coreauth.NewManager(nil, nil, nil)
	calls := &atomic.Int64{}
	for _, provider := range []string{"claude", "codex"} {
		manager.RegisterExecutor(countingExecutor{provider: provider, calls: calls})
	}
	registerFleetAuths(t, manager, authDir)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("fleet-limits-claude", "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5"}, {ID: "claude-fable-5-1"}})
	reg.RegisterClient("fleet-limits-codex", "codex", []*registry.ModelInfo{{ID: "gpt-6-astra"}, {ID: "claude-opus-5-5"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fleet-limits-claude")
		reg.UnregisterClient("fleet-limits-codex")
		configaccess.Register(nil)
	})

	server := NewServer(cfg, manager, sdkaccess.NewManager(), path)
	return server, calls, authDir
}

func registerFleetAuths(t *testing.T, manager *coreauth.Manager, authDir string) {
	t.Helper()
	fableReset := time.Date(2026, 11, 8, 16, 0, 30, 0, time.UTC)
	ultraOpus := map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":             "1794153600",
		"Anthropic-Ratelimit-Unified-7d-Status":            "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization":       "0.64",
		"Anthropic-Ratelimit-Unified-Representative-Claim": "seven_day",
		"Anthropic-Ratelimit-Unified-Status":               "allowed",
		"Anthropic-Ratelimit-Unified-Overage-Status":       "rejected",
	}
	ultraFable := map[string]string{
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
	}
	codexPro := map[string]string{
		"X-Codex-Active-Limit":                         "premium",
		"X-Codex-Credits-Balance":                      "14090.3601385000",
		"X-Codex-Credits-Has-Credits":                  "True",
		"X-Codex-Credits-Unlimited":                    "False",
		"X-Codex-Plan-Type":                            "pro",
		"X-Codex-Primary-Over-Secondary-Limit-Percent": "0",
		"X-Codex-Primary-Reset-After-Seconds":          "38819",
		"X-Codex-Primary-Reset-At":                     "1793775332",
		"X-Codex-Primary-Used-Percent":                 "100",
		"X-Codex-Primary-Window-Minutes":               "10080",
		"X-Codex-Secondary-Reset-After-Seconds":        "0",
		"X-Codex-Secondary-Used-Percent":               "0",
		"X-Codex-Secondary-Window-Minutes":             "0",
	}
	auths := []*coreauth.Auth{
		{
			ID: "claude-" + fleetUltraEmail + ".json", FileName: fleetAuthFile(authDir, "claude", fleetUltraEmail), Provider: "claude", Label: fleetUltraEmail, Status: coreauth.StatusActive,
			Metadata:       map[string]any{"type": "claude", "email": fleetUltraEmail, "account_id": fleetAccountID, "access_token": fleetUltraToken},
			Unavailable:    true,
			NextRetryAfter: fableReset,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: fableReset, ObservedAt: fleetNow.Add(-time.Hour), Signals: ultraOpus},
			ModelStates: map[string]*coreauth.ModelState{
				"claude-fable-5-1": {
					Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: fableReset,
					LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
					Quota:     coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: fableReset, ObservedAt: fleetNow.Add(-2 * time.Hour), Signals: ultraFable},
				},
			},
		},
		{
			ID: "claude-max-" + fleetMaxEmail + ".json", FileName: fleetAuthFile(authDir, "claude-max", fleetMaxEmail), Provider: "claude", Status: coreauth.StatusActive,
			Metadata: map[string]any{"type": "claude", "email": fleetMaxEmail},
			Quota: coreauth.QuotaState{ObservedAt: fleetNow.Add(-6 * time.Hour), Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.82",
				"Anthropic-Ratelimit-Unified-5h-Status":      "allowed_warning",
				"Anthropic-Ratelimit-Unified-5h-Reset":       "1794168000",
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.3",
				"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
				"Anthropic-Ratelimit-Unified-7d-Reset":       "1794153600",
			}},
		},
		{
			ID: "claude-team-" + fleetTeamEmail + ".json", FileName: fleetAuthFile(authDir, "claude-team", fleetTeamEmail), Provider: "claude", Status: coreauth.StatusActive,
			Metadata: map[string]any{"type": "claude", "email": fleetTeamEmail},
			Quota: coreauth.QuotaState{ObservedAt: fleetNow.Add(-time.Hour), Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.5",
				"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
				"Anthropic-Ratelimit-Unified-7d-Reset":       "1794240000",
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.4",
				"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
				"Anthropic-Ratelimit-Unified-5h-Reset":       "1794178800",
			}},
			ModelStates: map[string]*coreauth.ModelState{
				"claude-opus-5-5": {
					Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: fleetNow.Add(30 * time.Minute),
					LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
					Quota:     coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: fleetNow.Add(30 * time.Minute)},
				},
			},
		},
		{
			ID: "claude-" + fleetOffEmail + ".json", FileName: fleetAuthFile(authDir, "claude", fleetOffEmail), Provider: "claude", Disabled: true, Status: coreauth.StatusDisabled,
			Metadata: map[string]any{"type": "claude", "email": fleetOffEmail},
			Quota: coreauth.QuotaState{ObservedAt: fleetNow.Add(-time.Hour), Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.01",
				"Anthropic-Ratelimit-Unified-7d-Reset":       "1794240000",
			}},
		},
		{
			ID: "codex-" + fleetCodexEmail + "-pro.json", FileName: fleetAuthFile(authDir, "codex", fleetCodexEmail+"-pro"), Provider: "codex", Status: coreauth.StatusActive,
			Metadata: map[string]any{"type": "codex", "email": fleetCodexEmail, "account_id": fleetAccountID, "access_token": fleetCodexToken},
			Quota:    coreauth.QuotaState{ObservedAt: time.Unix(1793775332-38819, 0), Signals: codexPro},
			ModelStates: map[string]*coreauth.ModelState{
				"gpt-6-sol": {
					Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
					Quota: coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)},
				},
			},
		},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.Provider, err)
		}
	}
}

func fleetLimitsRequest(server *Server, header, value string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/fleet/limits", nil)
	request.RemoteAddr = "127.0.0.1:4100"
	if header != "" {
		request.Header.Set(header, value)
	}
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, request)
	return recorder
}

func decodeFleetLimits(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestFleetLimitsAuthentication(t *testing.T) {
	for _, withSecret := range []bool{true, false} {
		name := "with management secret"
		if !withSecret {
			name = "without management secret"
		}
		t.Run(name, func(t *testing.T) {
			server, _, _ := newFleetLimitsServer(t, withSecret)
			for _, tc := range []struct {
				name, header, value string
				want                int
			}{
				{"bearer client key", "Authorization", "Bearer " + fleetKeyFull, http.StatusOK},
				{"x-api-key client key", "X-Api-Key", fleetKeyM1, http.StatusOK},
				{"plain client key", "Authorization", "Bearer " + fleetKeyPlain, http.StatusOK},
				{"invalid key", "Authorization", "Bearer not-a-configured-key", http.StatusUnauthorized},
				{"no key", "", "", http.StatusUnauthorized},
				{"management header", "X-Management-Key", fleetMgmtSecret, http.StatusUnauthorized},
				{"management bearer", "Authorization", "Bearer " + fleetMgmtSecret, http.StatusUnauthorized},
				{"issuer key", "Authorization", "Bearer " + fleetIssuerKey, http.StatusUnauthorized},
			} {
				if got := fleetLimitsRequest(server, tc.header, tc.value).Code; got != tc.want {
					t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
				}
			}
		})
	}
}

func TestFleetLimitsContent(t *testing.T) {
	server, calls, _ := newFleetLimitsServer(t, true)
	body := decodeFleetLimits(t, fleetLimitsRequest(server, "Authorization", "Bearer "+fleetKeyFull))

	if body["object"] != "fleet.limits" || body["generated_at"] != "2026-11-03T20:00:00Z" || body["proxy_version"] != buildinfo.Version {
		t.Fatalf("header fields = %v %v %v", body["object"], body["generated_at"], body["proxy_version"])
	}
	if client := body["client"].(map[string]any); client["name"] != "macbook" {
		t.Fatalf("client = %v", client)
	}

	credentials := body["credentials"].([]any)
	if len(credentials) != 5 {
		t.Fatalf("credentials = %d, want 5", len(credentials))
	}
	byState := map[string][]map[string]any{}
	labels := map[string]bool{}
	for _, raw := range credentials {
		credential := raw.(map[string]any)
		byState[credential["state"].(string)] = append(byState[credential["state"].(string)], credential)
		label := credential["label"].(string)
		if !strings.HasPrefix(label, credential["provider"].(string)+"-") || len(label) != len(credential["provider"].(string))+9 || labels[label] {
			t.Fatalf("label %q is not a unique provider-prefixed short id", label)
		}
		labels[label] = true
		_, hasCredits := credential["credits"]
		if hasCredits != (credential["provider"] == "codex") {
			t.Fatalf("credits presence for %s = %v", credential["provider"], hasCredits)
		}
	}

	if len(byState["cooldown"]) != 1 || len(byState["disabled"]) != 1 || len(byState["active"]) != 3 {
		t.Fatalf("states = %v", byState)
	}
	ultra := byState["cooldown"][0]
	bars := barsByID(ultra)
	if bar := bars["weekly_all"]; bar == nil || bar["percent"] != 64.0 || bar["status"] != "allowed" || bar["resets_at"] != "2026-11-08T16:00:00Z" || bar["sampled_at"] != "2026-11-03T19:00:00Z" || bar["reset_passed"] != false {
		t.Fatalf("ultra weekly_all = %v", bar)
	}
	if bar := bars["weekly_fable"]; bar == nil || bar["percent"] != 100.0 || bar["status"] != "rejected" || bar["resets_at"] != "2026-11-08T16:00:00Z" {
		t.Fatalf("ultra weekly_fable = %v", bar)
	}
	if _, ok := bars["session_5h"]; ok {
		t.Fatal("Ultra credential reported session_5h")
	}
	if ultra["plan"] != nil {
		t.Fatalf("claude plan = %v", ultra["plan"])
	}
	cooldowns := ultra["cooldowns"].([]any)
	if len(cooldowns) != 2 {
		t.Fatalf("ultra cooldowns = %v", cooldowns)
	}
	credentialCooldown, modelCooldown := cooldowns[0].(map[string]any), cooldowns[1].(map[string]any)
	if credentialCooldown["scope"] != "credential" || credentialCooldown["model"] != nil || credentialCooldown["reason"] != "credential_quota" || credentialCooldown["until"] != "2026-11-08T16:00:30Z" {
		t.Fatalf("credential cooldown = %v", credentialCooldown)
	}
	if modelCooldown["scope"] != "model" || modelCooldown["model"] != "claude-fable-5-1" || modelCooldown["reason"] != "quota" || modelCooldown["until"] != "2026-11-08T16:00:30Z" || modelCooldown["http_status"] != 429.0 {
		t.Fatalf("model cooldown = %v", modelCooldown)
	}

	var maxCredential, teamCredential, codexCredential map[string]any
	for _, credential := range byState["active"] {
		switch {
		case credential["provider"] == "codex":
			codexCredential = credential
		case barsByID(credential)["session_5h"]["percent"] == 82.0:
			maxCredential = credential
		default:
			teamCredential = credential
		}
	}
	if maxCredential == nil || teamCredential == nil || codexCredential == nil {
		t.Fatal("active credentials not found")
	}
	if session := barsByID(maxCredential)["session_5h"]; session["status"] != "allowed_warning" || session["reset_passed"] != false {
		t.Fatalf("max session_5h = %v", session)
	}
	teamCooldowns := teamCredential["cooldowns"].([]any)
	if len(teamCooldowns) != 1 || teamCooldowns[0].(map[string]any)["scope"] != "model" || teamCooldowns[0].(map[string]any)["model"] != "claude-opus-5-5" {
		t.Fatalf("team cooldowns = %v", teamCooldowns)
	}
	codexBars := codexCredential["bars"].([]any)
	if len(codexBars) != 1 {
		t.Fatalf("codex bars = %v, want the zero-minute secondary skipped", codexBars)
	}
	if bar := codexBars[0].(map[string]any); bar["id"] != "weekly_all" || bar["percent"] != 100.0 || bar["resets_at"] != "2026-11-04T06:55:32Z" || bar["status"] != nil || bar["reset_passed"] != false {
		t.Fatalf("codex weekly_all = %v", bar)
	}
	if codexCredential["plan"] != "pro" {
		t.Fatalf("codex plan = %v", codexCredential["plan"])
	}
	if credits := codexCredential["credits"].(map[string]any); credits["has_credits"] != true || credits["unlimited"] != false || credits["balance"] != 14090.36 {
		t.Fatalf("codex credits = %v", credits)
	}

	summary := body["summary"].(map[string]any)
	claudeSummary := summary["claude"].(map[string]any)
	if claudeSummary["usable_credentials"] != 2.0 {
		t.Fatalf("claude usable = %v", claudeSummary["usable_credentials"])
	}
	claudeBars := claudeSummary["bars"].(map[string]any)
	if weekly := claudeBars["weekly_all"].(map[string]any); weekly["percent"] != 30.0 || weekly["resets_at"] != "2026-11-08T16:00:00Z" {
		t.Fatalf("claude summary weekly_all = %v", weekly)
	}
	if session := claudeBars["session_5h"].(map[string]any); session["percent"] != 40.0 || session["resets_at"] != "2026-11-08T23:00:00Z" {
		t.Fatalf("claude summary session_5h = %v", session)
	}
	if _, ok := claudeBars["weekly_fable"]; ok {
		t.Fatal("cooldown credential counted in summary")
	}
	codexSummary := summary["codex"].(map[string]any)
	if codexSummary["usable_credentials"] != 1.0 || codexSummary["bars"].(map[string]any)["weekly_all"].(map[string]any)["percent"] != 100.0 {
		t.Fatalf("codex summary = %v", codexSummary)
	}

	exclusions := map[string]map[string]any{}
	for _, raw := range body["exclusions"].([]any) {
		exclusion := raw.(map[string]any)
		exclusions[exclusion["pattern"].(string)] = exclusion
	}
	for pattern, want := range map[string][2]any{
		"claude-fable-5-1": {"2026-11-08T16:00:00Z", "quota_reset"},
		"claude-fable-*":   {"2026-11-08T16:00:00Z", "quota_reset"},
		"gpt-6-luna":       {"2026-11-10T00:00:00Z", "config"},
		"gpt-6-sol":        {"2026-11-05T00:00:00Z", "cooldown"},
		"gpt-5-codex-mini": {nil, nil},
	} {
		exclusion := exclusions[pattern]
		if exclusion == nil || exclusion["source"] != "oauth-excluded-models" || exclusion["until"] != want[0] || exclusion["until_source"] != want[1] {
			t.Errorf("exclusion %s = %v, want until %v (%v)", pattern, exclusion, want[0], want[1])
		}
	}
	if len(exclusions) != 5 {
		t.Fatalf("exclusions = %v", exclusions)
	}

	if calls.Load() != 0 {
		t.Fatalf("executors received %d calls", calls.Load())
	}
}

func TestFleetLimitsResetPassedCountsAsZero(t *testing.T) {
	server, _, _ := newFleetLimitsServer(t, true)
	fleetLimitsNow = func() time.Time { return time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC) }
	body := decodeFleetLimits(t, fleetLimitsRequest(server, "Authorization", "Bearer "+fleetKeyFull))
	passed := 0
	for _, raw := range body["credentials"].([]any) {
		bars := barsByID(raw.(map[string]any))
		if session := bars["session_5h"]; session != nil && session["percent"] == 82.0 {
			if session["reset_passed"] != true || bars["weekly_all"]["reset_passed"] != true {
				t.Fatalf("bars after reset = %v", bars)
			}
			passed++
		}
	}
	if passed != 1 {
		t.Fatalf("max credential not found")
	}
	claudeBars := body["summary"].(map[string]any)["claude"].(map[string]any)["bars"].(map[string]any)
	if session := claudeBars["session_5h"].(map[string]any); session["percent"] != 0.0 {
		t.Fatalf("summary session_5h after reset = %v", session)
	}
	if weekly := claudeBars["weekly_all"].(map[string]any); weekly["percent"] != 0.0 || weekly["resets_at"] != "2026-11-08T16:00:00Z" {
		t.Fatalf("summary weekly_all after reset = %v", weekly)
	}
}

func TestFleetLimitsKeyPolicy(t *testing.T) {
	server, calls, _ := newFleetLimitsServer(t, true)
	modelIDs := func(key string) []string {
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		request.Header.Set("Authorization", "Bearer "+key)
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, request)
		var listing struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(listing.Data))
		for _, model := range listing.Data {
			ids = append(ids, model.ID)
		}
		sort.Strings(ids)
		return ids
	}
	for _, key := range []string{fleetKeyFull, fleetKeyM1, fleetKeyPlain} {
		body := decodeFleetLimits(t, fleetLimitsRequest(server, "X-Api-Key", key))
		var got []string
		for _, raw := range body["models"].([]any) {
			model := raw.(map[string]any)
			got = append(got, model["id"].(string))
			if model["id"] == "claude-opus-5-5" {
				if providers := model["providers"].([]any); len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" {
					t.Fatalf("claude-opus-5-5 providers = %v", providers)
				}
			}
		}
		if want := modelIDs(key); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("models for %s-key = %v, /v1/models = %v", body["client"], got, want)
		}
		hasFable := strings.Contains(strings.Join(got, ","), "claude-fable-5-1")
		if hasFable != (key != fleetKeyM1) {
			t.Fatalf("fable visibility for %v = %v", body["client"], hasFable)
		}
	}

	m1 := decodeFleetLimits(t, fleetLimitsRequest(server, "Authorization", "Bearer "+fleetKeyM1))
	if m1["client"].(map[string]any)["name"] != "m1" {
		t.Fatalf("client = %v", m1["client"])
	}
	credentials := m1["credentials"].([]any)
	if len(credentials) != 2 {
		t.Fatalf("m1 credentials = %v", credentials)
	}
	providers := []string{credentials[0].(map[string]any)["provider"].(string), credentials[1].(map[string]any)["provider"].(string)}
	if providers[0] != "claude" || providers[1] != "codex" {
		t.Fatalf("m1 providers = %v", providers)
	}
	if claude := m1["summary"].(map[string]any)["claude"].(map[string]any); claude["usable_credentials"] != 1.0 {
		t.Fatalf("m1 claude summary = %v", claude)
	}
	var keyPolicyExclusion map[string]any
	for _, raw := range m1["exclusions"].([]any) {
		if exclusion := raw.(map[string]any); exclusion["source"] == "key-policy" {
			keyPolicyExclusion = exclusion
		}
	}
	if keyPolicyExclusion == nil || keyPolicyExclusion["pattern"] != "claude-fable-*" || keyPolicyExclusion["provider"] != nil || keyPolicyExclusion["until"] != nil || keyPolicyExclusion["until_source"] != nil {
		t.Fatalf("key-policy exclusion = %v", keyPolicyExclusion)
	}

	plain := decodeFleetLimits(t, fleetLimitsRequest(server, "Authorization", "Bearer "+fleetKeyPlain))
	if name, exists := plain["client"].(map[string]any)["name"]; !exists || name != nil {
		t.Fatalf("plain key client = %v", plain["client"])
	}
	if calls.Load() != 0 {
		t.Fatalf("executors received %d calls", calls.Load())
	}
}

func TestFleetLimitsPrivacy(t *testing.T) {
	server, _, authDir := newFleetLimitsServer(t, true)
	for _, key := range []string{fleetKeyFull, fleetKeyM1, fleetKeyPlain} {
		recorder := fleetLimitsRequest(server, "Authorization", "Bearer "+key)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		body := strings.ToLower(recorder.Body.String())
		forbidden := []string{fleetKeyFull, fleetKeyM1, fleetKeyPlain, fleetIssuerKey, fleetMgmtSecret, fleetAccountID, fleetUltraToken, fleetCodexToken, authDir, ".json", "@", "owner"}
		for _, email := range []string{fleetUltraEmail, fleetMaxEmail, fleetTeamEmail, fleetCodexEmail, fleetOffEmail} {
			forbidden = append(forbidden, email, strings.Split(email, "@")[0])
		}
		for _, value := range forbidden {
			if strings.Contains(body, strings.ToLower(value)) {
				t.Fatalf("response exposes %q", value)
			}
		}
	}
}

func TestFleetLimitsRouteIsNotShadowed(t *testing.T) {
	server, _, _ := newFleetLimitsServer(t, true)
	for _, header := range []string{"", "2023-06-01"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/fleet/limits", nil)
		request.Header.Set("Authorization", "Bearer "+fleetKeyFull)
		if header != "" {
			request.Header.Set("Anthropic-Version", header)
			request.Header.Set("User-Agent", "claude-cli/2.0")
		}
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, request)
		if body := decodeFleetLimits(t, recorder); body["object"] != "fleet.limits" {
			t.Fatalf("route served %v", body["object"])
		}
	}
}

func barsByID(credential map[string]any) map[string]map[string]any {
	bars := map[string]map[string]any{}
	for _, raw := range credential["bars"].([]any) {
		bar := raw.(map[string]any)
		bars[bar["id"].(string)] = bar
	}
	return bars
}
