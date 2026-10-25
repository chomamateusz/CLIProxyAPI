package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestClientKeyExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, expiry string
		allowed      bool
	}{
		{"no expiry", "", true}, {"expired", "2000-01-01T00:00:00Z", false}, {"future", "2999-01-01T00:00:00Z", true}, {"invalid", "invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPolicyProvider("", []string{"client-fixture"}, []access.KeyPolicy{{Key: "client-fixture", ExpiresAt: tc.expiry}})
			req := httptest.NewRequest("GET", "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer client-fixture")
			result, err := p.Authenticate(context.Background(), req)
			if (err == nil && result != nil) != tc.allowed {
				t.Fatal("unexpected expiry authentication")
			}
		})
	}
}

func TestIssuerKeyCannotBecomeClientKey(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{APIKeys: []string{"issuer-fixture"}, KeyIssuerKeys: []config.KeyIssuerKey{{Key: "issuer-fixture", Name: "fleet"}}}
	Register(cfg)
	t.Cleanup(func() { Register(nil) })
	manager := access.NewManager()
	manager.SetProviders(access.RegisteredProviders())
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer issuer-fixture")
	if _, err := manager.Authenticate(context.Background(), req); err == nil {
		t.Fatal("issuer authenticated as client")
	}
}
