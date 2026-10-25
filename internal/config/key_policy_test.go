package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"gopkg.in/yaml.v3"
)

const policyConfigYAML = `access:
  api-keys:
    - plain-fixture
    - key: policy-fixture
      name: macbook
      models: ["claude-*", "gpt-5*"]
      excluded-models: ["claude-fable-*"]
      credentials: ["claude-*.json"]
      excluded-credentials: ["claude-work.json"]
`

func assertKeyPolicies(t *testing.T, cfg *Config) {
	t.Helper()
	if !reflect.DeepEqual(cfg.APIKeys, []string{"plain-fixture", "policy-fixture"}) {
		t.Fatal("client key list differs")
	}
	want := []access.KeyPolicy{{Key: "policy-fixture", Name: "macbook", Models: []string{"claude-*", "gpt-5*"}, ExcludedModels: []string{"claude-fable-*"}, Credentials: []string{"claude-*.json"}, ExcludedCredentials: []string{"claude-work.json"}}}
	if !reflect.DeepEqual(cfg.APIKeyPolicies, want) {
		t.Fatal("client key policies differ")
	}
}

func TestAPIKeyPoliciesConfig(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(policyConfigYAML), &cfg); err != nil {
		t.Fatal(err)
	}
	assertKeyPolicies(t, &cfg)
	if err := ValidateV8Config([]byte(policyConfigYAML)); err != nil {
		t.Fatal(err)
	}
	assertKeyPolicies(t, cfg.CloneForRuntime())
	snapshot, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err = yaml.Unmarshal(snapshot, &restored); err != nil {
		t.Fatal(err)
	}
	assertKeyPolicies(t, &restored)
	if err = yaml.Unmarshal([]byte("api-keys: [legacy-fixture]\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "legacy-fixture" || len(cfg.APIKeyPolicies) != 0 {
		t.Fatal("legacy keys or policy reset failed")
	}
}

func TestAPIKeyPoliciesLoadSaveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(policyConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		assertKeyPolicies(t, cfg)
		cfg.Port = 8318
		if err = SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAPIKeyPolicyInvalidKey(t *testing.T) {
	for index, object := range []string{"{}", "{key: ''}", "{key: 123}", "{key: [fixture]}", "{key: fixture, models: {fixture: true}}"} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			var cfg Config
			err := yaml.Unmarshal([]byte("access: {api-keys: ["+object+"]}"), &cfg)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if strings.Contains(err.Error(), "fixture") {
				t.Fatal("validation error exposes policy values")
			}
		})
	}
}
