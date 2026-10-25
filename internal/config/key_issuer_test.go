package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestKeyIssuerConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, entries string
		valid         bool
	}{
		{"plain", "[issuer-fixture]", true},
		{"object", "[{key: issuer-fixture, name: fleet}]", true},
		{"missing name", "[{key: issuer-fixture}]", false},
		{"empty key", "[{key: '', name: fleet}]", false},
		{"duplicate name", "[{key: issuer-fixture, name: fleet}, {key: other-fixture, name: fleet}]", false},
		{"client collision", "[{key: client-fixture, name: fleet}]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte("config-version: 8\naccess:\n  api-keys: [client-fixture]\n  key-issuer-keys: " + tc.entries + "\n")
			cfg, err := ParseConfigBytes(raw)
			if (err == nil) != tc.valid {
				t.Fatal("unexpected parse result")
			}
			if !tc.valid {
				if err != nil && strings.Contains(err.Error(), "fixture") {
					t.Fatal("error exposed key material")
				}
				return
			}
			if len(cfg.KeyIssuerKeys) != 1 || cfg.KeyIssuerKeys[0].IssuerName() == "" {
				t.Fatal("issuer missing")
			}
			encoded, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal("could not marshal config")
			}
			next, err := ParseConfigBytes(encoded)
			if err != nil || next.KeyIssuerKeys[0] != cfg.KeyIssuerKeys[0] {
				t.Fatal("issuer lost during snapshot")
			}
			if ValidateV8Config(raw) != nil {
				t.Fatal("validation rejected issuer")
			}
		})
	}
}
