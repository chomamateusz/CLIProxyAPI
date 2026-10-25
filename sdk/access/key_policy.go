package access

import (
	"context"
	"path/filepath"
	"strings"
)

type KeyPolicy struct {
	Key                 string   `yaml:"key" json:"-"`
	Name                string   `yaml:"name,omitempty" json:"name,omitempty"`
	Models              []string `yaml:"models,omitempty" json:"models,omitempty"`
	ExcludedModels      []string `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
	Credentials         []string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	ExcludedCredentials []string `yaml:"excluded-credentials,omitempty" json:"excluded-credentials,omitempty"`
}

func (p *KeyPolicy) AllowsModel(model string) bool {
	return p == nil || policyAllows(p.Models, p.ExcludedModels, []string{model})
}

func (p *KeyPolicy) AllowsCredential(fileName, id, label string) bool {
	if p == nil {
		return true
	}
	names := []string{id}
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
	matches := func(patterns []string) bool {
		for _, pattern := range patterns {
			for _, name := range names {
				if wildcardMatch(pattern, name) {
					return true
				}
			}
		}
		return false
	}
	return (len(allow) == 0 || matches(allow)) && !matches(deny)
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
