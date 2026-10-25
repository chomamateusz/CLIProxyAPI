package configaccess

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func newPolicyProvider(name string, keys []string, policies []access.KeyPolicy) *provider {
	p := newProvider(name, keys)
	p.policies = make(map[string]*access.KeyPolicy, len(policies))
	for _, policy := range policies {
		policy.Models = append([]string(nil), policy.Models...)
		policy.ExcludedModels = append([]string(nil), policy.ExcludedModels...)
		policy.Credentials = append([]string(nil), policy.Credentials...)
		policy.ExcludedCredentials = append([]string(nil), policy.ExcludedCredentials...)
		p.policies[strings.TrimSpace(policy.Key)] = &policy
	}
	return p
}
