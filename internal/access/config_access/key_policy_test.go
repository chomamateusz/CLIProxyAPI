package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAuthenticateKeyPolicy(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{APIKeys: []string{"plain-fixture", "policy-fixture"}, APIKeyPolicies: []access.KeyPolicy{{Key: "policy-fixture", Name: "macbook", Models: []string{"claude-*"}}}}
	Register(cfg)
	t.Cleanup(func() { Register(nil) })
	manager := access.NewManager()
	manager.SetProviders(access.RegisteredProviders())
	for _, tc := range []struct {
		name, key string
		policy    bool
	}{
		{"plain", cfg.APIKeys[0], false}, {"object", cfg.APIKeys[1], true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer "+tc.key)
			result, err := manager.Authenticate(context.Background(), req)
			if err != nil || result == nil {
				t.Fatal("authentication failed")
			}
			if (result.Policy != nil) != tc.policy {
				t.Fatal("unexpected policy presence")
			}
			if result.Policy != nil && (result.Policy.Name != "macbook" || result.Policy.AllowsModel("gpt-5")) {
				t.Fatal("policy was not attached")
			}
		})
	}
	cfg.APIKeyPolicies[0].Models[0] = "gpt-*"
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("X-Api-Key", cfg.APIKeys[1])
	result, _ := manager.Authenticate(context.Background(), req)
	if !result.Policy.AllowsModel("claude-opus") {
		t.Fatal("provider shares mutable policy slices")
	}
	Register(cfg)
	manager.SetProviders(access.RegisteredProviders())
	result, _ = manager.Authenticate(context.Background(), req)
	if result.Policy.AllowsModel("claude-opus") {
		t.Fatal("provider did not reload policy")
	}
}
