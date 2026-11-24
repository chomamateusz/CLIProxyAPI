package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
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

func TestLoadConfigWarnsAboutCloakedKeyPolicyPatterns(t *testing.T) {
	logger := log.StandardLogger()
	originalHooks := logger.ReplaceHooks(make(log.LevelHooks))
	t.Cleanup(func() { logger.ReplaceHooks(originalHooks) })
	hook := logtest.NewGlobal()
	const allow, deny = "claude-fable-5-dd-los-2.6-tpg", "CLAUDE-FABLE-5-DD-*"
	raw := `config-version: 8
access:
  api-keys:
    - key: client-fixture
      name: test
      models: [gpt-6.2-sol, ` + allow + `]
      excluded-models: [claude-fable-*, "` + deny + `"]
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeyPolicies) != 1 {
		t.Fatal("config load did not preserve the client key policy")
	}
	policy := cfg.APIKeyPolicies[0]
	if !reflect.DeepEqual(policy.Models, []string{"gpt-6.2-sol", allow}) || !reflect.DeepEqual(policy.ExcludedModels, []string{"claude-fable-*", deny}) {
		t.Fatal("config load changed model policy patterns")
	}
	warned := make(map[string]any)
	for _, entry := range hook.AllEntries() {
		field, _ := entry.Data["field"].(string)
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "use the real model ID") {
			warned[field] = entry.Data["pattern"]
		}
	}
	if !reflect.DeepEqual(warned, map[string]any{"models": allow, "excluded-models": deny}) {
		t.Fatalf("warned patterns = %v", warned)
	}
}
