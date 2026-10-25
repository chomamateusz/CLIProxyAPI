package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	accessconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/access"
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"gopkg.in/yaml.v3"
)

func TestIssueRevokeClientKeys(t *testing.T) {
	raw := strings.Replace(clientKeyPoliciesFixture, "access:\n", "access:\n    key-issuer-keys: [{key: issuer-fixture, name: fleet}, {key: second-fixture, name: other}]\n", 1)
	h, _, path := newClientKeyPoliciesTestHandler(t, raw)
	h.envSecret = "management-fixture"
	router := gin.New()
	group := router.Group("/v8/management", h.Middleware())
	group.GET("/issued-keys", h.IssuedKeys)
	group.POST("/issued-keys", h.IssuedKeys)
	group.DELETE("/issued-keys/:name", h.IssuedKeys)
	group.DELETE("/issued-keys", h.IssuedKeys)
	group.GET("/client-key-policies", h.ClientKeyPolicies)
	group.PUT("/client-key-policies/:name", h.ClientKeyPolicies)
	snapshots := make(chan *config.Config, 8)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { snapshots <- cfg })
	manager := access.NewManager()
	t.Cleanup(func() { configaccess.Register(nil) })
	request := func(method, path, body, key string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/v8/management"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s status %d, want %d", method, path, rec.Code, want)
		}
		return rec
	}
	apply := func() {
		t.Helper()
		select {
		case next := <-snapshots:
			if _, err := accessconfig.ApplyAccessProviders(manager, nil, next); err != nil {
				t.Fatal("reload failed")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reload callback missing")
		}
	}
	rec := request("POST", "/issued-keys", `{"name":"worker","models":["claude-*"],"ttl":"2h"}`, "issuer-fixture", 201)
	var created struct {
		Key string `json:"key"`
		issuedKeyView
	}
	if json.Unmarshal(rec.Body.Bytes(), &created) != nil || !strings.HasPrefix(created.Key, "sk-fleet-") {
		t.Fatal("invalid create response")
	}
	start, err := time.Parse(time.RFC3339, created.CreatedAt)
	if err != nil {
		t.Fatal("invalid creation timestamp")
	}
	end, err := time.Parse(time.RFC3339, created.ExpiresAt)
	if err != nil || end.Sub(start) != 2*time.Hour {
		t.Fatal("ttl was not converted")
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("could not read saved config")
	}
	for _, segment := range []string{"# Plain client\n        - plain-fixture # Keep the plain comment", "# Other client\n        - key: 'other-fixture' # Keep the key comment\n          name: other\n          models: [\"other-*\"] # Keep the models comment", "server:\n    port: 8317 # Keep the server comment"} {
		if !strings.Contains(string(saved), segment) {
			t.Fatal("unrelated YAML changed")
		}
	}
	apply()
	authReq := httptest.NewRequest("GET", "/v1/models", nil)
	authReq.Header.Set("X-Api-Key", created.Key)
	result, authErr := manager.Authenticate(context.Background(), authReq)
	if authErr != nil || result == nil || result.Policy == nil || !result.Policy.AllowsModel("claude-test") || result.Policy.AllowsModel("gpt-test") {
		t.Fatal("issued policy did not authenticate")
	}
	rec = request("GET", "/issued-keys", "", "issuer-fixture", 200)
	if strings.Contains(rec.Body.String(), created.Key) || strings.Contains(rec.Body.String(), `"key"`) {
		t.Fatal("list exposed key")
	}
	var listed []issuedKeyView
	if json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].IssuedBy != "fleet" {
		t.Fatal("missing issued metadata")
	}
	rec = request("GET", "/client-key-policies", "", "management-fixture", 200)
	if !strings.Contains(rec.Body.String(), `"issued-by":"fleet"`) || !strings.Contains(rec.Body.String(), created.ExpiresAt) {
		t.Fatal("policy metadata missing")
	}
	request("PUT", "/client-key-policies/worker", `{"issued-by":"other"}`, "management-fixture", 400)
	request("POST", "/issued-keys", `{"name":"worker"}`, "issuer-fixture", 409)
	request("DELETE", "/issued-keys/worker", "", "second-fixture", 404)
	request("DELETE", "/issued-keys/laptop", "", "issuer-fixture", 404)
	request("DELETE", "/issued-keys/laptop", "", "management-fixture", 404)
	request("DELETE", "/issued-keys?issued-by=fleet", "", "issuer-fixture", 403)
	request("DELETE", "/issued-keys/worker", "", "issuer-fixture", 200)
	apply()
	if _, errAuth := manager.Authenticate(context.Background(), authReq); errAuth == nil {
		t.Fatal("revoked key still authenticates")
	}
	saved, err = os.ReadFile(path)
	if err != nil || string(saved) != raw {
		t.Fatal("revoke did not restore original bytes")
	}
	for _, tc := range []struct{ name, issuer string }{{"worker-one", "issuer-fixture"}, {"worker-two", "issuer-fixture"}, {"worker-other", "second-fixture"}, {"worker-admin", "management-fixture"}} {
		request("POST", "/issued-keys", `{"name":"`+tc.name+`"}`, tc.issuer, 201)
		apply()
	}
	request("DELETE", "/issued-keys?issued-by=unknown", "", "management-fixture", 200)
	request("DELETE", "/issued-keys?issued-by=fleet", "", "management-fixture", 200)
	apply()
	rec = request("GET", "/issued-keys", "", "management-fixture", 200)
	if json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed) != 2 {
		t.Fatal("bulk revocation removed wrong keys")
	}
	request("DELETE", "/issued-keys/worker-other", "", "management-fixture", 200)
	apply()
}

func TestRevokeFinalIssuedKeyKeepsAuthenticationRequired(t *testing.T) {
	h, _, path := newClientKeyPoliciesTestHandler(t, "config-version: 8\naccess:\n  api-keys: []\n")
	h.envSecret = "management-fixture"
	router := gin.New()
	group := router.Group("/v8/management", h.Middleware())
	group.POST("/issued-keys", h.IssuedKeys)
	group.DELETE("/issued-keys/:name", h.IssuedKeys)
	snapshots := make(chan *config.Config, 2)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { snapshots <- cfg })
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/v8/management"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer management-fixture")
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("status = %d, want %d", rec.Code, want)
		}
		return rec
	}
	created := request("POST", "/issued-keys", `{"name":"only"}`, 201)
	var issued struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(created.Body.Bytes(), &issued) != nil || issued.Key == "" {
		t.Fatal("missing issued key")
	}
	select {
	case <-snapshots:
	case <-time.After(5 * time.Second):
		t.Fatal("create reload callback missing")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	denied := request("DELETE", "/issued-keys/only", "", http.StatusConflict)
	if denied.Body.String() != `{"error":"last_client_key","message":"revoking the last client key would disable authentication"}` {
		t.Fatalf("unexpected refusal: %s", denied.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused revocation changed config file")
	}
	select {
	case <-snapshots:
		t.Fatal("refused revocation triggered reload")
	default:
	}
}

func TestIssuedKeysYAMLPreservation(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"block", "config-version: 8\naccess:\n  api-keys:\n    - 'plain-fixture' # client\nserver: {port: 8317} # server\n"},
		{"separate dash", "config-version: 8\naccess:\n  api-keys:\n    -\n      key: plain-fixture\n      name: existing\nserver: {port: 8317}\n"},
		{"flow", "config-version: 8\naccess: {api-keys: ['plain-fixture']} # access\nserver: {port: 8317}\n"},
		{"trailing comma", "config-version: 8\naccess: {api-keys: [plain-fixture,]}\n"},
		{"missing trailing comma keys", "config-version: 8\naccess: {key-issuer-keys: [issuer-fixture],}\n"},
		{"empty", "config-version: 8\naccess: {api-keys: []}\n"},
		{"null", "config-version: 8\naccess:\n  api-keys: null # empty\n"},
		{"missing keys", "config-version: 8\naccess:\n  key-issuer-keys: [issuer-fixture]\n"},
		{"missing flow keys", "config-version: 8\naccess: {key-issuer-keys: [issuer-fixture]}\n"},
		{"missing access", "config-version: 8\nserver: {port: 8317} # server\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := access.KeyPolicy{Key: "generated-fixture", Name: "worker", IssuedBy: "fleet"}
			var node yaml.Node
			if node.Encode(p) != nil {
				t.Fatal("encode failed")
			}
			data, err := issuedKeysYAML([]byte(tc.raw), &node, nil)
			if err != nil {
				t.Fatal("insert failed")
			}
			cfg, err := config.ParseConfigBytes(data)
			if err != nil || len(cfg.APIKeyPolicies) == 0 || cfg.APIKeyPolicies[len(cfg.APIKeyPolicies)-1].Name != "worker" {
				t.Fatal("invalid inserted policy")
			}
			data, err = issuedKeysYAML(data, nil, []access.KeyPolicy{p})
			if err != nil {
				t.Fatal("remove failed")
			}
			cfg, err = config.ParseConfigBytes(data)
			if err != nil {
				t.Fatal("could not parse after removal")
			}
			for _, policy := range cfg.APIKeyPolicies {
				if policy.Name == "worker" {
					t.Fatal("policy was not removed")
				}
			}
		})
	}
}

func TestIssuedKeysInvalidRequests(t *testing.T) {
	h, _, _ := newClientKeyPoliciesTestHandler(t, clientKeyPoliciesFixture)
	router := gin.New()
	router.POST("/issued-keys", func(c *gin.Context) {
		c.Set(managementPrincipalKey, managementPrincipal{Kind: "issuer", Name: "fleet"})
	}, h.IssuedKeys)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing name", `{}`, 400},
		{"invalid name", `{"name":"bad/name"}`, 400},
		{"long name", `{"name":"` + strings.Repeat("a", 65) + `"}`, 400},
		{"existing name", `{"name":"laptop"}`, 409},
		{"bad ttl", `{"name":"worker","ttl":"forever"}`, 400},
		{"negative ttl", `{"name":"worker","ttl":"-1h"}`, 400},
		{"zero ttl", `{"name":"worker","ttl":"0s"}`, 400},
		{"bad expiry", `{"name":"worker","expires-at":"tomorrow"}`, 400},
		{"past expiry", `{"name":"worker","expires-at":"2000-01-01T00:00:00Z"}`, 400},
		{"ambiguous expiry", `{"name":"worker","ttl":"1h","expires-at":"2999-01-01T00:00:00Z"}`, 400},
		{"issuer override", `{"name":"worker","issued-by":"other"}`, 400},
		{"expiry", `{"name":"worker","expires-at":"2999-01-01T00:00:00Z"}`, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest("POST", "/issued-keys", strings.NewReader(tc.body)))
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
		})
	}
}

func TestIssuedKeysRemoveSequenceEntries(t *testing.T) {
	for _, layout := range []string{
		"access:\n  api-keys: [{key: first-fixture, name: first, issued-by: fleet}, {key: second-fixture, name: second, issued-by: fleet}, {key: third-fixture, name: third, issued-by: fleet}]\n",
		"access:\n  api-keys:\n    # First\n    - {key: first-fixture, name: first, issued-by: fleet}\n    # Second\n    - {key: second-fixture, name: second, issued-by: fleet}\n    # Third\n    - {key: third-fixture, name: third, issued-by: fleet}\n",
	} {
		for _, name := range []string{"first", "second", "third"} {
			t.Run(name, func(t *testing.T) {
				raw := []byte("config-version: 8\n" + layout)
				p := access.KeyPolicy{Key: name + "-fixture", Name: name, IssuedBy: "fleet"}
				data, err := issuedKeysYAML(raw, nil, []access.KeyPolicy{p})
				if err != nil {
					t.Fatal("removal failed")
				}
				cfg, err := config.ParseConfigBytes(data)
				if err != nil || len(cfg.APIKeys) != 2 {
					t.Fatal("invalid remaining keys")
				}
				for _, policy := range cfg.APIKeyPolicies {
					if policy.Name == name {
						t.Fatal("key was not removed")
					}
				}
			})
		}
	}
}

func TestIssuedKeysRemovalMatchesOwner(t *testing.T) {
	raw := []byte("config-version: 8\naccess:\n  api-keys: [{key: duplicate-fixture, name: other, issued-by: other}, {key: duplicate-fixture, name: owned, issued-by: fleet}]\n")
	data, err := issuedKeysYAML(raw, nil, []access.KeyPolicy{{Key: "duplicate-fixture", Name: "owned", IssuedBy: "fleet"}})
	if err != nil {
		t.Fatal("could not revoke owned entry")
	}
	cfg, err := config.ParseConfigBytes(data)
	if err != nil || len(cfg.APIKeyPolicies) != 1 || cfg.APIKeyPolicies[0].IssuedBy != "other" {
		t.Fatal("revocation changed another issuer's entry")
	}
}
