package api

import (
	"context"
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
		if endsAt != "2026-11-01T00:00:00Z" || credential["state"] != "ended" {
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
	ended := &coreauth.Auth{ID: "fleet-limits-models/ended", Provider: "codex", Metadata: map[string]any{coreauth.MetadataFleetEndsAt: coreauth.FormatFleetEndsAt(time.Now().Add(-time.Minute))}}
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
