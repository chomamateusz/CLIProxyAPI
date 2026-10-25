package configaccess

import (
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func keyPolicyExpired(policy *access.KeyPolicy) bool {
	if policy == nil || policy.ExpiresAt == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, policy.ExpiresAt)
	return err != nil || !time.Now().Before(expires)
}

func newIssuerPolicyProvider(name string, keys []string, cfg *sdkconfig.SDKConfig) *provider {
	p := newPolicyProvider(name, keys, cfg.APIKeyPolicies)
	for _, issuer := range cfg.KeyIssuerKeys {
		delete(p.keys, strings.TrimSpace(issuer.Key))
	}
	return p
}
