package access

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestApplyAccessProvidersReloadsKeyPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	manager := sdkaccess.NewManager()
	t.Cleanup(func() { configaccess.Register(nil) })
	var previous *config.Config
	for _, tc := range []struct {
		name, fields string
		want         *sdkaccess.KeyPolicy
	}{
		{"initial", "name: laptop\nmodels: [claude-*]\nexcluded-credentials: [claude-work.json]", &sdkaccess.KeyPolicy{Name: "laptop", Models: []string{"claude-*"}, ExcludedCredentials: []string{"claude-work.json"}}},
		{"excluded credentials only", "name: laptop\nmodels: [claude-*]\nexcluded-credentials: [claude-personal.json]", &sdkaccess.KeyPolicy{Name: "laptop", Models: []string{"claude-*"}, ExcludedCredentials: []string{"claude-personal.json"}}},
		{"all fields", "name: desktop\nmodels: [gpt-*]\nexcluded-models: [gpt-old]\ncredentials: [codex-*]\nexcluded-credentials: [codex-work.json]", &sdkaccess.KeyPolicy{Name: "desktop", Models: []string{"gpt-*"}, ExcludedModels: []string{"gpt-old"}, Credentials: []string{"codex-*"}, ExcludedCredentials: []string{"codex-work.json"}}},
		{"empty policy", "", &sdkaccess.KeyPolicy{}},
		{"plain key", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "config-version: 8\naccess:\n  api-keys:\n    - key: reload-fixture\n"
			if tc.fields != "" {
				raw += "      " + strings.ReplaceAll(tc.fields, "\n", "\n      ") + "\n"
			}
			if tc.want == nil {
				raw = "config-version: 8\naccess:\n  api-keys: [reload-fixture]\n"
			}
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal("could not write config fixture")
			}
			next, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal("could not load config fixture")
			}
			if previous != nil && !reflect.DeepEqual(previous.APIKeys, next.APIKeys) {
				t.Fatal("reload must change only policy fields")
			}
			if _, err = ApplyAccessProviders(manager, previous, next); err != nil {
				t.Fatal("access reconciliation failed")
			}
			req := httptest.NewRequest("GET", "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer "+next.APIKeys[0])
			result, errAuth := manager.Authenticate(context.Background(), req)
			if errAuth != nil || result == nil {
				t.Fatal("authentication failed after reconciliation")
			}
			if tc.want != nil {
				tc.want.Key = next.APIKeys[0]
			}
			if !reflect.DeepEqual(result.Policy, tc.want) {
				t.Fatal("authenticated policy does not match the reloaded configuration")
			}
			previous = next
		})
	}
}
