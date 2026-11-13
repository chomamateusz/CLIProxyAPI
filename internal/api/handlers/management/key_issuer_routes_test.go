package management_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestKeyIssuerAuthorizationMatrix(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "management-fixture")
	t.Cleanup(func() { configaccess.Register(nil) })
	for _, principal := range []struct{ name, key string }{{"management", "management-fixture"}, {"issuer", "issuer-fixture"}, {"client", "client-fixture"}, {"none", ""}} {
		for _, route := range []struct {
			method, path, body string
			allowed            int
			issuer             bool
		}{
			{"GET", "/v8/management/config", "", 200, false},
			{"GET", "/v8/management/credentials/download?name=missing.json", "", 404, false},
			{"GET", "/v8/management/client-key-policies", "", 200, false},
			{"GET", "/v8/management/issued-keys", "", 200, true},
			{"POST", "/v8/management/issued-keys", `{"name":"worker"}`, 201, true},
			{"DELETE", "/v8/management/issued-keys/existing", "", 200, true},
			{"DELETE", "/v8/management/issued-keys?issued-by=fleet", "", 200, false},
			{"GET", "/v8/management/usage/client-keys", "", 200, true},
			{"GET", "/v8/management/usage/client-keys?from=bad", "", 400, true},
			{"GET", "/v8/management/observability/usage/api-keys", "", 200, false},
			{"PUT", "/v0/management/api-keys", `["client-fixture", "issued-fixture"]`, 200, false},
			{"PUT", "/v8/management/config/access/key-issuer-keys", `[]`, 200, false},
			{"PUT", "/v8/management/config/management/secret-key", `"replacement-fixture"`, 200, false},
			{"PUT", "/v8/management/config/access/secret-key", `"replacement-fixture"`, 400, false},
		} {
			t.Run(principal.name+"/"+route.method+route.path, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.yaml")
				raw := "config-version: 8\naccess:\n  key-issuer-keys: [{key: issuer-fixture, name: fleet}]\n  api-keys:\n    - client-fixture\n    - {key: issued-fixture, name: existing, issued-by: fleet}\n"
				if route.path == "/v0/management/api-keys" {
					raw = strings.Replace(raw, "    - {key: issued-fixture, name: existing, issued-by: fleet}", "    - issued-fixture", 1)
				}
				if os.WriteFile(path, []byte(raw), 0600) != nil {
					t.Fatal("could not write fixture")
				}
				cfg, err := config.LoadConfig(path)
				if err != nil {
					t.Fatal("could not load fixture")
				}
				cfg.AuthDir = filepath.Dir(path)
				server := api.NewServer(cfg, auth.NewManager(nil, nil, nil), access.NewManager(), path)
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				req.RemoteAddr = "127.0.0.1:1234"
				if principal.key != "" {
					req.Header.Set("X-Management-Key", principal.key)
				}
				rec := httptest.NewRecorder()
				server.Handler().ServeHTTP(rec, req)
				want := 401
				if principal.name == "management" {
					want = route.allowed
				} else if principal.name == "issuer" {
					want = 403
					if route.issuer {
						want = route.allowed
					}
				}
				if rec.Code != want {
					t.Fatalf("status %d, want %d", rec.Code, want)
				}
				if want == 403 && rec.Body.String() != `{"error":"forbidden","message":"issuer keys may only manage client keys"}` {
					t.Fatal("wrong forbidden response")
				}
			})
		}
	}
}

func TestIssuerKeyRejectedByModelsRoute(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	t.Cleanup(func() { configaccess.Register(nil) })
	for _, clients := range []string{"[]", "[client-fixture]"} {
		t.Run(clients, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := "config-version: 8\naccess:\n  key-issuer-keys: [{key: issuer-fixture, name: fleet}]\n  api-keys: " + clients + "\n"
			if os.WriteFile(path, []byte(raw), 0600) != nil {
				t.Fatal("could not write fixture")
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal("could not load fixture")
			}
			cfg.AuthDir = filepath.Dir(path)
			server := api.NewServer(cfg, auth.NewManager(nil, nil, nil), access.NewManager(), path)
			for _, tc := range []struct {
				path string
				want int
			}{{"/v1/models", 401}, {"/v8/management/issued-keys", 200}, {"/v8/management/config", 403}} {
				req := httptest.NewRequest("GET", tc.path, nil)
				req.RemoteAddr = "127.0.0.1:1234"
				req.Header.Set("Authorization", "Bearer issuer-fixture")
				rec := httptest.NewRecorder()
				server.Handler().ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Fatalf("%s status %d, want %d", tc.path, rec.Code, tc.want)
				}
			}
		})
	}
}
