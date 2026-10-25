package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

type KeyIssuerKey struct {
	Key  string `yaml:"key" json:"-"`
	Name string `yaml:"name" json:"name"`
}

func (k KeyIssuerKey) IssuerName() string {
	if k.Name != "" {
		return k.Name
	}
	digest := sha256.Sum256([]byte(k.Key))
	return "issuer-" + hex.EncodeToString(digest[:16])
}

func (k *KeyIssuerKey) UnmarshalYAML(node *yaml.Node) error {
	var next KeyIssuerKey
	switch {
	case node.Kind == yaml.ScalarNode && node.Tag == "!!str":
		next.Key = node.Value
	case node.Kind == yaml.MappingNode:
		key, name := yamlPath(node, "key"), yamlPath(node, "name")
		if key == nil || key.Kind != yaml.ScalarNode || key.Tag != "!!str" || name == nil || name.Kind != yaml.ScalarNode || name.Tag != "!!str" {
			return errors.New("key-issuer-keys: object entries require string key and name fields")
		}
		type entry KeyIssuerKey
		if node.Decode((*entry)(&next)) != nil || strings.TrimSpace(next.Name) == "" {
			return errors.New("key-issuer-keys: object entries require a key and name")
		}
	default:
		return errors.New("key-issuer-keys: expected a string or named key object")
	}
	next.Key = strings.TrimSpace(next.Key)
	next.Name = strings.TrimSpace(next.Name)
	if next.Key == "" {
		return errors.New("key-issuer-keys: key must not be empty")
	}
	*k = next
	return nil
}

func (k KeyIssuerKey) MarshalYAML() (any, error) {
	if k.Name == "" {
		return k.Key, nil
	}
	type entry KeyIssuerKey
	return entry(k), nil
}

func validateKeyIssuers(issuers []KeyIssuerKey, clients []string) error {
	names, keys := map[string]bool{"management": true}, map[string]bool{}
	for _, key := range clients {
		keys[strings.TrimSpace(key)] = true
	}
	for _, issuer := range issuers {
		if names[issuer.IssuerName()] || keys[issuer.Key] {
			return errors.New("key-issuer-keys: names and keys must be unique and distinct from client keys")
		}
		names[issuer.IssuerName()], keys[issuer.Key] = true, true
	}
	return nil
}
