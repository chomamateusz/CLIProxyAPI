package management

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type resetGrantTransport struct{}

func (resetGrantTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return resetGrantTransport{}
}

func (resetGrantTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"organization":{"subscription_status":"active","organization_type":"claude_max"}}`
	if r.URL.Path == "/api/oauth/usage" {
		body = `{"cedar_ember":{"eligible":true,"grants":[{"id":"made_up","resets_total":1,"resets_left":1,"clears":["seven_day"],"ends_at":"2099-01-01T00:00:00Z"}]}}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func TestListAuthFilesShowsFreshResetGrants(t *testing.T) {
	f := newFleetEndFixture(t)
	if entry := f.listed(t); entry["fleet_reset_grants"] != nil {
		t.Fatalf("grants listed before a sample: %v", entry)
	}
	f.manager.SetRoundTripperProvider(resetGrantTransport{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.manager.StartSubscriptionSampler(ctx)
	deadline := time.Now().Add(5 * time.Second)
	var entry map[string]any
	for entry = f.listed(t); entry["fleet_reset_grants"] == nil; entry = f.listed(t) {
		if time.Now().After(deadline) {
			t.Fatalf("grants never listed: %v", entry)
		}
		time.Sleep(5 * time.Millisecond)
	}
	grants, _ := entry["fleet_reset_grants"].(map[string]any)
	if grants["weekly"] != float64(1) || grants["total"] != float64(1) || grants["expires_at"] != "2099-01-01T00:00:00Z" || grants["sampled_at"] == nil {
		t.Fatalf("listed grants = %v", grants)
	}
	auth, _ := f.manager.GetByID("claude-a.json")
	stale := gin.H{}
	addAuthFleetEndFields(stale, auth, time.Now().Add(25*time.Hour))
	if _, ok := stale["fleet_reset_grants"]; ok {
		t.Fatalf("grants listed from a sample older than 24 hours: %v", stale)
	}
}
