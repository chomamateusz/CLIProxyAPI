package access

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

type KeyPolicy struct {
	Key                 string   `yaml:"key" json:"-"`
	IssuedBy            string   `yaml:"issued-by,omitempty" json:"issued-by,omitempty"`
	CreatedAt           string   `yaml:"created-at,omitempty" json:"created-at,omitempty"`
	ExpiresAt           string   `yaml:"expires-at,omitempty" json:"expires-at,omitempty"`
	Name                string   `yaml:"name,omitempty" json:"name,omitempty"`
	Models              []string `yaml:"models,omitempty" json:"models,omitempty"`
	ExcludedModels      []string `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
	Credentials         []string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	ExcludedCredentials []string `yaml:"excluded-credentials,omitempty" json:"excluded-credentials,omitempty"`
}

func (p *KeyPolicy) AllowsModel(model string) bool {
	return p.AllowsModelRoute(model, model)
}

func (p *KeyPolicy) AllowsModelRoute(requested, resolved string) bool {
	if p == nil {
		return true
	}
	requested = strings.TrimSpace(thinking.ParseSuffix(requested).ModelName)
	resolved = strings.TrimSpace(thinking.ParseSuffix(resolved).ModelName)
	names := []string{requested, resolved}
	resolvedNames := []string{resolved}
	if index := strings.LastIndexByte(requested, '/'); index >= 0 && index+1 < len(requested) {
		names = append(names, requested[index+1:])
	}
	if index := strings.LastIndexByte(resolved, '/'); index >= 0 && index+1 < len(resolved) {
		base := resolved[index+1:]
		names = append(names, base)
		resolvedNames = append(resolvedNames, base)
	}
	if policyMatches(p.ExcludedModels, names) {
		return false
	}
	if len(p.Models) == 0 || policyMatches(p.Models, resolvedNames) {
		return true
	}
	for _, name := range p.Models {
		if !strings.Contains(name, "*") && strings.EqualFold(name, requested) {
			return true
		}
	}
	return false
}

func (p *KeyPolicy) AllowsCredential(fileName, id, label string) bool {
	if p == nil {
		return true
	}
	names := []string{id}
	if base := filepath.Base(id); base != "" {
		names = append(names, base)
		if strings.HasSuffix(strings.ToLower(base), ".json") {
			names = append(names, base[:len(base)-5])
		}
	}
	if fileName != "" {
		base := filepath.Base(fileName)
		names = append(names, base)
		if strings.HasSuffix(strings.ToLower(base), ".json") {
			names = append(names, base[:len(base)-5])
		}
	} else if label != "" {
		names = append(names, label)
	}
	return policyAllows(p.Credentials, p.ExcludedCredentials, names)
}

func policyAllows(allow, deny, names []string) bool {
	return (len(allow) == 0 || policyMatches(allow, names)) && !policyMatches(deny, names)
}

func policyMatches(patterns, names []string) bool {
	for _, pattern := range patterns {
		for _, name := range names {
			if wildcardMatch(pattern, name) {
				return true
			}
		}
	}
	return false
}

func wildcardMatch(pattern, value string) bool {
	pattern, value = strings.ToLower(pattern), strings.ToLower(value)
	p, v, star, retry := 0, 0, -1, 0
	for v < len(value) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, retry = p, v
			p++
		case p < len(pattern) && pattern[p] == value[v]:
			p++
			v++
		case star >= 0:
			retry++
			v, p = retry, star+1
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

type keyPolicyContextKey struct{}

const KeyPolicyGinKey = "accessKeyPolicy"

func WithKeyPolicy(ctx context.Context, policy *KeyPolicy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, keyPolicyContextKey{}, policy)
}

func KeyPolicyFromContext(ctx context.Context) *KeyPolicy {
	if ctx == nil {
		return nil
	}
	policy, _ := ctx.Value(keyPolicyContextKey{}).(*KeyPolicy)
	return policy
}
