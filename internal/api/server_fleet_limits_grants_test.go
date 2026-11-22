package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type resetGrantTransport struct{}

func (resetGrantTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return resetGrantTransport{}
}

func (resetGrantTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"organization":{"subscription_status":"active","organization_type":"claude_max"}}`
	if r.URL.Path == "/api/oauth/usage" {
		body = `{"cedar_ember":{"eligible":true,"grants":[{"id":"weekly","resets_total":2,"resets_left":2,"clears":["seven_day"]},{"id":"session","resets_total":1,"resets_left":1,"clears":["five_hour"]}]}}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func resetGrantsJSON(t *testing.T, credential fleetLimitsCredential) (map[string]any, bool) {
	t.Helper()
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	grants, ok := decoded["reset_grants"]
	if !ok {
		return nil, false
	}
	object, _ := grants.(map[string]any)
	return object, true
}

func TestFleetLimitsResetGrants(t *testing.T) {
	labelKey := coreauth.FleetLimitsLabelKey(nil)
	m := coreauth.NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &coreauth.Auth{ID: "claude-grants", Provider: "claude", Metadata: map[string]any{"access_token": "made-up-claude"}}); err != nil {
		t.Fatal(err)
	}
	claude, _ := m.GetByID("claude-grants")
	if _, ok := resetGrantsJSON(t, fleetLimitsCredentialFor(claude, nil, labelKey, time.Now())); ok {
		t.Fatal("reset_grants present before a sample")
	}
	m.SetRoundTripperProvider(resetGrantTransport{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.StartSubscriptionSampler(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		claude, _ = m.GetByID("claude-grants")
		if coreauth.FleetResetGrantsFor(claude, time.Now()) != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grants never sampled")
		}
		time.Sleep(5 * time.Millisecond)
	}
	grants, ok := resetGrantsJSON(t, fleetLimitsCredentialFor(claude, nil, labelKey, time.Now()))
	if !ok || grants["weekly"] != float64(2) || grants["total"] != float64(3) || grants["expires_at"] != nil || grants["sampled_at"] == nil {
		t.Fatalf("reset_grants = %v, present %t", grants, ok)
	}
	if _, hasExpiry := grants["expires_at"]; !hasExpiry {
		t.Fatal("expires_at missing instead of null")
	}
	if _, ok = resetGrantsJSON(t, fleetLimitsCredentialFor(claude, nil, labelKey, time.Now().Add(25*time.Hour))); ok {
		t.Fatal("reset_grants present for a sample older than 24 hours")
	}
	codex := &coreauth.Auth{ID: "codex-none", Provider: "codex", Metadata: map[string]any{"access_token": "made-up-codex", "account_id": "acct-example"}}
	if _, ok = resetGrantsJSON(t, fleetLimitsCredentialFor(codex, nil, labelKey, time.Now())); ok {
		t.Fatal("reset_grants present for a provider without grants")
	}
}
