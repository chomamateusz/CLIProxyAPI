package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"gopkg.in/yaml.v3"
)

func splitAPIKeyPolicies(root *yaml.Node) ([]access.KeyPolicy, error) {
	keys := yamlPath(root, "api-keys")
	if keys == nil || keys.Kind != yaml.SequenceNode {
		return nil, nil
	}
	var policies []access.KeyPolicy
	for i, entry := range keys.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		var policy access.KeyPolicy
		if err := entry.Decode(&policy); err != nil {
			return nil, fmt.Errorf("api-keys[%d]: invalid policy", i)
		}
		key := yamlPath(entry, "key")
		if key == nil || key.Kind != yaml.ScalarNode || key.Tag != "!!str" || strings.TrimSpace(policy.Key) == "" {
			return nil, fmt.Errorf("api-keys[%d]: policy requires a non-empty string key", i)
		}
		if policy.ExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339, policy.ExpiresAt); err != nil {
				return nil, fmt.Errorf("api-keys[%d]: invalid expires-at", i)
			}
		}
		policies = append(policies, policy)
		keys.Content[i] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: policy.Key}
	}
	return policies, nil
}

func restoreAPIKeyPolicies(root *yaml.Node, cfg *Config) error {
	keys := yamlPath(root, "api-keys")
	if keys != nil {
		policies := make(map[string]access.KeyPolicy, len(cfg.APIKeyPolicies))
		for _, policy := range cfg.APIKeyPolicies {
			policies[policy.Key] = policy
		}
		for i, entry := range keys.Content {
			if policy, ok := policies[entry.Value]; ok {
				var object yaml.Node
				if err := object.Encode(policy); err != nil {
					return err
				}
				keys.Content[i] = &object
			}
		}
	}
	return nil
}
