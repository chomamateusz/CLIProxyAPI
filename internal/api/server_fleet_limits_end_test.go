package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func fleetEndSampledAuth(id string, used string, endsAt time.Time) *coreauth.Auth {
	auth := &coreauth.Auth{ID: id, Provider: "claude", Metadata: map[string]any{}, Quota: coreauth.QuotaState{ObservedAt: fleetNow.Add(-time.Hour), Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Utilization": used,
		"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
		"Anthropic-Ratelimit-Unified-7d-Reset":       "1794153600",
	}}}
	if !endsAt.IsZero() {
		auth.Metadata[coreauth.MetadataFleetEndsAt] = coreauth.FormatFleetEndsAt(endsAt)
		auth.Metadata[coreauth.MetadataFleetEndsAtSource] = coreauth.FleetEndSourceCodexAPI
	}
	return auth
}

func TestFleetLimitsCredentialEnd(t *testing.T) {
	labelKey := coreauth.FleetLimitsLabelKey(nil)
	for _, tc := range []struct {
		name       string
		endsAt     time.Time
		disabled   bool
		wantState  string
		wantEndsAt any
		wantUsable bool
	}{
		{"no end", time.Time{}, false, "active", nil, true},
		{"before end", fleetNow.Add(time.Second), false, "active", "2026-11-03T20:00:01Z", true},
		{"at end", fleetNow, false, "ended", "2026-11-03T20:00:00Z", false},
		{"ended and disabled", fleetNow.Add(-time.Hour), true, "ended", "2026-11-03T19:00:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := fleetEndSampledAuth("a", "0.1", tc.endsAt)
			auth.Disabled = tc.disabled
			credential := fleetLimitsCredentialFor(auth, nil, labelKey, fleetNow)
			var endsAt any
			if credential.EndsAt != nil {
				endsAt = *credential.EndsAt
			}
			if credential.State != tc.wantState || endsAt != tc.wantEndsAt || credential.usable != tc.wantUsable {
				t.Fatalf("credential = state %s, ends_at %v, usable %t; want %s, %v, %t", credential.State, endsAt, credential.usable, tc.wantState, tc.wantEndsAt, tc.wantUsable)
			}
		})
	}

	summary := make(map[string]fleetLimitsProviderSummary)
	for _, auth := range []*coreauth.Auth{fleetEndSampledAuth("ended", "0.1", fleetNow.Add(-time.Minute)), fleetEndSampledAuth("live", "0.7", time.Time{})} {
		addFleetLimitsSummary(summary, fleetLimitsCredentialFor(auth, nil, labelKey, fleetNow))
	}
	if claude := summary["claude"]; claude.UsableCredentials != 1 || claude.Bars["weekly_all"].Percent != 70 {
		t.Fatalf("summary counted the ended credential: %+v", claude)
	}
}

func TestFleetLimitsEndpointReportsEndsAt(t *testing.T) {
	server, calls, _ := newFleetLimitsServer(t, true)
	maxID := "claude-max-" + fleetMaxEmail + ".json"
	auth, ok := server.handlers.AuthManager.GetByID(maxID)
	if !ok {
		t.Fatal("max credential missing")
	}
	auth.Metadata[coreauth.MetadataFleetEndsAt] = "2026-11-01T00:00:00Z"
	auth.Metadata[coreauth.MetadataFleetEndsAtSource] = coreauth.FleetEndSourcePlanEnded
	if _, err := server.handlers.AuthManager.Update(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	body := decodeFleetLimits(t, fleetLimitsRequest(server, "Authorization", "Bearer "+fleetKeyFull))
	ended := 0
	for _, raw := range body["credentials"].([]any) {
		credential := raw.(map[string]any)
		endsAt, present := credential["ends_at"]
		if !present {
			t.Fatalf("credential without ends_at key: %v", credential)
		}
		if endsAt == nil {
			continue
		}
		if endsAt != "2026-11-01T00:00:00Z" || credential["state"] != "ended" || credential["end_state"] != "ended" || credential["ends_at_source"] != "plan-ended" {
			t.Fatalf("ended credential = %v", credential)
		}
		ended++
	}
	if ended != 1 {
		t.Fatalf("ended credentials = %d; want 1", ended)
	}
	if calls.Load() != 0 {
		t.Fatalf("limits called upstream %d times", calls.Load())
	}
}

func TestFleetLimitsModelsOmitsModelOfEndedCredential(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	ended := &coreauth.Auth{ID: "fleet-limits-models/ended", Provider: "codex", Metadata: map[string]any{coreauth.MetadataFleetEndsAt: coreauth.FormatFleetEndsAt(time.Now().Add(-time.Minute)), coreauth.MetadataFleetEndsAtSource: coreauth.FleetEndSourceCodexAPI}}
	live := &coreauth.Auth{ID: "fleet-limits-models/live", Provider: "codex", Metadata: map[string]any{}}
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*coreauth.Auth{ended, live} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	}
	reg.RegisterClient(ended.ID, "codex", []*registry.ModelInfo{{ID: "fleet-limits-ended-only"}, {ID: "fleet-limits-shared"}})
	reg.RegisterClient(live.ID, "codex", []*registry.ModelInfo{{ID: "fleet-limits-shared"}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.StartQuotaSampler(ctx)

	listed := func() map[string]bool {
		ids := make(map[string]bool)
		for _, model := range fleetLimitsModels(nil) {
			ids[model.ID] = true
		}
		return ids
	}
	deadline := time.Now().Add(5 * time.Second)
	for listed()["fleet-limits-ended-only"] {
		if time.Now().After(deadline) {
			t.Fatal("fleetLimitsModels still lists a model served only by an ended credential")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !listed()["fleet-limits-shared"] {
		t.Fatal("fleetLimitsModels dropped a model a live credential still serves")
	}
}

func TestFleetLimitsOverdueCredentialStaysUsable(t *testing.T) {
	auth := fleetEndSampledAuth("overdue", "0.1", fleetNow.Add(-time.Hour))
	auth.Metadata[coreauth.MetadataFleetEndsAtSource] = coreauth.FleetEndSourceManual
	credential := fleetLimitsCredentialFor(auth, nil, coreauth.FleetLimitsLabelKey(nil), fleetNow)
	if credential.State != "active" || credential.EndState != "overdue" || credential.EndsAtSource == nil || *credential.EndsAtSource != "manual" || !credential.usable || credential.EndWarnings == nil {
		t.Fatalf("overdue credential = %+v", credential)
	}
	none := fleetLimitsCredentialFor(fleetEndSampledAuth("none", "0.1", time.Time{}), nil, coreauth.FleetLimitsLabelKey(nil), fleetNow)
	if none.EndState != "none" || none.EndsAtSource != nil || none.EndsAt != nil || none.EndWarnings == nil {
		t.Fatalf("credential without an end = %+v", none)
	}
}

type fleetSubscriptionTransport string

func (p fleetSubscriptionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "chatgpt.com" || r.URL.Path != "/backend-api/subscriptions" {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: make(http.Header)}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(p))), Header: make(http.Header)}, nil
}

func (p fleetSubscriptionTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return p }

func TestFleetLimitsReportsProviderEndWarnings(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRoundTripperProvider(fleetSubscriptionTransport(`{"will_renew":true,"is_active":true,"grace_period_end_timestamp":"2099-01-05T00:00:00Z","entitlement":{"cancels_at":null,"has_active_subscription":true}}`))
	endsAt := coreauth.FormatFleetEndsAt(time.Now().Add(72 * time.Hour))
	auth := &coreauth.Auth{ID: "codex-warned", Provider: "codex", Metadata: map[string]any{"access_token": "token", "account_id": "acct", coreauth.MetadataFleetEndsAt: endsAt, coreauth.MetadataFleetEndsAtSource: coreauth.FleetEndSourceManual}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); manager.StopAutoRefresh() })
	manager.StartSubscriptionSampler(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, _ := manager.GetByID(auth.ID)
		credential := fleetLimitsCredentialFor(current, nil, coreauth.FleetLimitsLabelKey(nil), time.Now())
		if len(credential.EndWarnings) == 2 {
			if credential.EndWarnings[0].Code != "provider_renews" || credential.EndWarnings[1].Code != "grace_period" || credential.EndWarnings[1].Until == nil || *credential.EndWarnings[1].Until != "2099-01-05T00:00:00Z" {
				t.Fatalf("warnings = %+v", credential.EndWarnings)
			}
			if credential.EndState != "scheduled" || *credential.EndsAt != endsAt {
				t.Fatalf("manual end changed by a renewing provider: %+v", credential)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("warnings never appeared: %+v", credential)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
