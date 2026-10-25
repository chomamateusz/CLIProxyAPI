package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

const clientKeyPoliciesFixture = `config-version: 8
access:
    api-keys:
        # Plain client
        - plain-fixture # Keep the plain comment
        # Laptop client
        - key: policy-fixture
          name: laptop
          models: [claude-*, gpt-*]
          excluded-models: [gpt-old]
          credentials: [claude-*]
          excluded-credentials: [claude-work.json] # Keep the policy comment
        # Other client
        - key: 'other-fixture' # Keep the key comment
          name: other
          models: ["other-*"] # Keep the models comment
server:
    port: 8317 # Keep the server comment
`

func newClientKeyPoliciesTestHandler(t *testing.T, raw string) (*Handler, *gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal("could not write config fixture")
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal("could not load config fixture")
	}
	h := &Handler{cfg: cfg, configFilePath: path, failedAttempts: make(map[string]*attemptInfo)}
	router := gin.New()
	router.GET("/client-key-policies", h.ClientKeyPolicies)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch} {
		router.Handle(method, "/client-key-policies/:name", h.ClientKeyPolicies)
	}
	return h, router, path
}

func clientKeyPoliciesRequest(t *testing.T, router http.Handler, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
	if recorder.Code != status {
		t.Fatalf("policy request returned status %d, want %d", recorder.Code, status)
	}
	for _, key := range []string{"plain-fixture", "policy-fixture", "other-fixture"} {
		if strings.Contains(recorder.Body.String(), key) {
			t.Fatal("policy response exposed a client key")
		}
	}
	if status >= 400 {
		var response struct{ Error, Message string }
		if json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response.Error == "" || response.Message == "" {
			t.Fatal("policy error did not use the management error shape")
		}
	}
	return recorder
}

func TestClientKeyPoliciesGet(t *testing.T) {
	_, router, path := newClientKeyPoliciesTestHandler(t, clientKeyPoliciesFixture)
	response := clientKeyPoliciesRequest(t, router, "GET", "/client-key-policies", "", 200)
	var entries []clientKeyPolicyEntry
	if err := json.Unmarshal(response.Body.Bytes(), &entries); err != nil || len(entries) != 3 {
		t.Fatal("unexpected policy list")
	}
	if entries[0].Index != 0 || entries[0].Name != "" || entries[0].HasPolicy || entries[0].Models == nil {
		t.Fatal("plain entry was not listed correctly")
	}
	if entries[1].Index != 1 || entries[1].Name != "laptop" || !entries[1].HasPolicy || !reflect.DeepEqual(entries[1].ExcludedCredentials, []string{"claude-work.json"}) {
		t.Fatal("named policy was not listed correctly")
	}
	for _, segment := range []string{"laptop", "1", "0"} {
		response = clientKeyPoliciesRequest(t, router, "GET", "/client-key-policies/"+segment, "", 200)
		var entry clientKeyPolicyEntry
		if json.Unmarshal(response.Body.Bytes(), &entry) != nil {
			t.Fatal("could not decode policy")
		}
		want := entries[1]
		if segment == "0" {
			want = entries[0]
		}
		if !reflect.DeepEqual(entry, want) {
			t.Fatal("single policy differs from list entry")
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != clientKeyPoliciesFixture {
		t.Fatal("GET changed the config file")
	}
}

func TestClientKeyPoliciesPutAndPatch(t *testing.T) {
	h, router, path := newClientKeyPoliciesTestHandler(t, clientKeyPoliciesFixture)
	reloads := make(chan *config.Config, 1)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloads <- cfg })
	for _, tc := range []struct {
		name, method, body string
		want               clientKeyPolicyLists
	}{
		{"replace", "PUT", `{"models":["new-*"],"excluded-models":["new-old"],"credentials":["new-*.json"],"excluded-credentials":["new-work.json"]}`, clientKeyPolicyLists{[]string{"new-*"}, []string{"new-old"}, []string{"new-*.json"}, []string{"new-work.json"}}},
		{"missing fields clear", "PUT", `{"models":["claude-*"]}`, clientKeyPolicyLists{Models: []string{"claude-*"}}},
		{"add", "PATCH", `{"add":{"excluded-credentials":["claude-work.json","claude-personal.json","claude-work.json"]}}`, clientKeyPolicyLists{Models: []string{"claude-*"}, ExcludedCredentials: []string{"claude-work.json", "claude-personal.json"}}},
		{"remove", "PATCH", `{"remove":{"excluded-credentials":["claude-work.json","absent.json"]}}`, clientKeyPolicyLists{Models: []string{"claude-*"}, ExcludedCredentials: []string{"claude-personal.json"}}},
		{"round trip", "PATCH", `{"remove":{"excluded-credentials":["claude-personal.json"]}}`, clientKeyPolicyLists{Models: []string{"claude-*"}}},
		{"all fields", "PATCH", `{"add":{"models":["gpt-*","claude-*"],"excluded-models":["old"],"credentials":["work"],"excluded-credentials":["blocked"]},"remove":{"models":["claude-*"],"credentials":["work"]}}`, clientKeyPolicyLists{Models: []string{"gpt-*"}, ExcludedModels: []string{"old"}, ExcludedCredentials: []string{"blocked"}}},
		{"empty replace", "PUT", `{}`, clientKeyPolicyLists{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := clientKeyPoliciesRequest(t, router, tc.method, "/client-key-policies/laptop", tc.body, 200)
			var result struct {
				Status string `json:"status"`
				clientKeyPolicyEntry
			}
			tc.want.emptyLists()
			if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Status != "ok" || !result.HasPolicy || result.Name != "laptop" || result.Index != 1 || !reflect.DeepEqual(result.clientKeyPolicyLists, tc.want) {
				t.Fatal("updated policy response does not match the requested lists")
			}
			loaded, err := config.LoadConfig(path)
			if err != nil || len(loaded.APIKeyPolicies) != 2 {
				t.Fatal("could not reload saved policies")
			}
			policy := loaded.APIKeyPolicies[0]
			actual := clientKeyPolicyLists{policy.Models, policy.ExcludedModels, policy.Credentials, policy.ExcludedCredentials}
			actual.emptyLists()
			if !reflect.DeepEqual(actual, tc.want) {
				t.Fatal("saved policy differs from the response")
			}
			select {
			case snapshot := <-reloads:
				if !reflect.DeepEqual(snapshot.APIKeyPolicies, loaded.APIKeyPolicies) {
					t.Fatal("management reload received stale policies")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("management save did not trigger reload")
			}
		})
	}
}

func TestClientKeyPoliciesPlainConversionPreservesOtherEntries(t *testing.T) {
	_, router, path := newClientKeyPoliciesTestHandler(t, clientKeyPoliciesFixture)
	clientKeyPoliciesRequest(t, router, "PATCH", "/client-key-policies/0", `{"add":{"excluded-credentials":["claude-work.json"]}}`, 200)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("could not read saved config")
	}
	marker := "        # Laptop client\n"
	_, before, okBefore := strings.Cut(clientKeyPoliciesFixture, marker)
	_, after, okAfter := strings.Cut(string(saved), marker)
	if !okBefore || !okAfter || before != after {
		t.Fatal("other entries or their comments changed")
	}
	if !strings.Contains(string(saved), "# Plain client") || !strings.Contains(string(saved), "# Keep the plain comment") {
		t.Fatal("converted entry lost its comments")
	}
	var doc yaml.Node
	if yaml.Unmarshal(saved, &doc) != nil {
		t.Fatal("saved config is invalid YAML")
	}
	keys := configV8Node(doc.Content[0], []string{"access", "api-keys"})
	if keys == nil || keys.Content[0].Kind != yaml.MappingNode || yamlMapScalar(keys.Content[0], "key") != "plain-fixture" {
		t.Fatal("plain entry was not converted to a policy object")
	}
}

func TestClientKeyPoliciesLookupErrors(t *testing.T) {
	for _, method := range []string{"GET", "PUT", "PATCH"} {
		for _, tc := range []struct {
			name, segment, raw string
			status             int
		}{
			{"unknown", "missing", clientKeyPoliciesFixture, 404},
			{"case sensitive", "Laptop", clientKeyPoliciesFixture, 404},
			{"out of range", "3", clientKeyPoliciesFixture, 404},
			{"negative index", "-1", clientKeyPoliciesFixture, 404},
			{"ambiguous", "laptop", strings.Replace(clientKeyPoliciesFixture, "name: other", "name: laptop", 1), 409},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				_, router, path := newClientKeyPoliciesTestHandler(t, tc.raw)
				clientKeyPoliciesRequest(t, router, method, "/client-key-policies/"+tc.segment, `{}`, tc.status)
				saved, err := os.ReadFile(path)
				if err != nil || string(saved) != tc.raw {
					t.Fatal("failed lookup changed the config")
				}
			})
		}
	}
}

func TestClientKeyPoliciesNumericNamePrecedesIndex(t *testing.T) {
	_, router, _ := newClientKeyPoliciesTestHandler(t, strings.Replace(clientKeyPoliciesFixture, "name: laptop", "name: '0'", 1))
	response := clientKeyPoliciesRequest(t, router, "GET", "/client-key-policies/0", "", 200)
	var entry clientKeyPolicyEntry
	if json.Unmarshal(response.Body.Bytes(), &entry) != nil || entry.Index != 1 || entry.Name != "0" {
		t.Fatal("numeric name did not take precedence over index")
	}
}

func TestClientKeyPoliciesInvalidBody(t *testing.T) {
	for i, tc := range []struct{ method, body string }{
		{"PUT", ""}, {"PUT", `null`}, {"PUT", `[]`}, {"PUT", `{"models":"bad"}`},
		{"PUT", `{"credentials":[1]}`}, {"PUT", `{"key":"plain-fixture"}`}, {"PUT", `{} {}`},
		{"PATCH", `null`}, {"PATCH", `{"add":[]}`}, {"PATCH", `{"remove":{"models":{}}}`},
		{"PATCH", `{"models":[]}`}, {"PATCH", `{"add":{"key":"policy-fixture"}}`},
	} {
		t.Run(tc.method+"/"+strconv.Itoa(i), func(t *testing.T) {
			_, router, path := newClientKeyPoliciesTestHandler(t, clientKeyPoliciesFixture)
			clientKeyPoliciesRequest(t, router, tc.method, "/client-key-policies/laptop", tc.body, 400)
			saved, err := os.ReadFile(path)
			if err != nil || string(saved) != clientKeyPoliciesFixture {
				t.Fatal("invalid body changed the config")
			}
		})
	}
}

func TestClientKeyPoliciesManagementAuthentication(t *testing.T) {
	h, _, _ := newClientKeyPoliciesTestHandler(t, clientKeyPoliciesFixture)
	h.envSecret = "management-fixture"
	router := gin.New()
	group := router.Group("/v8/management", h.Middleware())
	group.GET("/client-key-policies", h.ClientKeyPolicies)
	for _, method := range []string{"GET", "PUT", "PATCH"} {
		group.Handle(method, "/client-key-policies/:name", h.ClientKeyPolicies)
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/client-key-policies"}, {"GET", "/client-key-policies/laptop"},
		{"PUT", "/client-key-policies/laptop"}, {"PATCH", "/client-key-policies/laptop"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/v8/management"+tc.path, strings.NewReader(`{}`))
			req.RemoteAddr = "127.0.0.1:1234"
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("missing management key returned status %d", recorder.Code)
			}
		})
	}
}

func TestClientKeyPoliciesPreserveSurroundingYAML(t *testing.T) {
	for _, tc := range []struct{ name, prefix, target, suffix string }{
		{
			"two space block",
			"config-version: 8\naccess:\n  api-keys:\n    - 'before-fixture'   # Before\n    # Target\n",
			"    - plain-fixture # Target comment\n",
			"    # After\n    - {key: 'other-fixture', name: other}  # After comment\nserver: {port: 8317}\n",
		},
		{
			"flow with unicode and delimiters",
			"config-version: 8\naccess: {api-keys: ['before-☃-fixture', ",
			"'plain-fixture'",
			", {key: 'other-fixture', name: other, models: ['a,b']}]}\n",
		},
		{
			"flow plain punctuation",
			"config-version: 8\naccess: {api-keys: ['before-fixture', ",
			"plain'punctuation#fixture",
			", 'other-fixture']}\n",
		},
		{
			"flow object",
			"config-version: 8\naccess: {api-keys: ['before-fixture', ",
			"{key: policy-fixture, name: laptop, models: ['a,b', 'a]b', 'it''s', \"a\\\"b\"]}",
			", 'other-fixture']} # End\n",
		},
		{
			"block mapping",
			"config-version: 8\naccess:\n  api-keys:\n    - before-fixture\n    # Target\n",
			"    - key: policy-fixture\n      name: laptop\n      models: [claude-*] # Models comment\n",
			"    # After\n    - other-fixture\n",
		},
		{
			"last block entry",
			"config-version: 8\naccess:\n  api-keys:\n    - before-fixture\n",
			"    - plain-fixture\n",
			"# Server\nserver:\n  port: 8317\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, router, path := newClientKeyPoliciesTestHandler(t, tc.prefix+tc.target+tc.suffix)
			clientKeyPoliciesRequest(t, router, "PATCH", "/client-key-policies/1", `{"add":{"excluded-credentials":["claude-work.json"]}}`, 200)
			saved, err := os.ReadFile(path)
			if err != nil || !strings.HasPrefix(string(saved), tc.prefix) || !strings.HasSuffix(string(saved), tc.suffix) {
				t.Fatal("policy edit changed surrounding YAML bytes")
			}
			if _, err = config.LoadConfig(path); err != nil {
				t.Fatal("saved YAML cannot be loaded")
			}
		})
	}
}

func TestClientKeyPoliciesPreserveListComments(t *testing.T) {
	raw := "config-version: 8\naccess:\n  api-keys:\n    - key: policy-fixture\n      name: laptop\n      excluded-credentials:\n        # Work account\n        - claude-work.json # Temporarily blocked\n    - other-fixture\n"
	_, router, path := newClientKeyPoliciesTestHandler(t, raw)
	clientKeyPoliciesRequest(t, router, "PATCH", "/client-key-policies/laptop", `{"add":{"excluded-credentials":["claude-personal.json"]}}`, 200)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("could not read saved config")
	}
	for _, comment := range []string{"# Work account", "# Temporarily blocked"} {
		if strings.Count(string(saved), comment) != 1 {
			t.Fatal("policy edit lost or duplicated a list comment")
		}
	}
}

func TestClientKeyPoliciesWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write read-only files")
	}
	h, router, path := newClientKeyPoliciesTestHandler(t, clientKeyPoliciesFixture)
	previous := h.cfg
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal("could not make config read-only")
	}
	clientKeyPoliciesRequest(t, router, "PATCH", "/client-key-policies/laptop", `{"add":{"excluded-credentials":["blocked.json"]}}`, 500)
	if h.cfg != previous || h.reloadGeneration != 0 {
		t.Fatal("failed write published a new runtime configuration")
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != clientKeyPoliciesFixture {
		t.Fatal("failed write changed the config")
	}
}

func TestClientKeyPoliciesEmptyAndLegacy(t *testing.T) {
	for _, raw := range []string{"config-version: 8\n", "config-version: 8\naccess: {api-keys: []}\n", "config-version: 8\naccess: {api-keys: null}\n"} {
		_, router, _ := newClientKeyPoliciesTestHandler(t, raw)
		response := clientKeyPoliciesRequest(t, router, "GET", "/client-key-policies", "", 200)
		if response.Body.String() != "[]" {
			t.Fatal("empty policy list must be an array")
		}
	}
	_, router, path := newClientKeyPoliciesTestHandler(t, "api-keys: [plain-fixture]\n")
	clientKeyPoliciesRequest(t, router, "PATCH", "/client-key-policies/0", `{"add":{"excluded-credentials":["blocked.json"]}}`, 200)
	saved, err := os.ReadFile(path)
	if err != nil || config.ValidateV8Config(saved) != nil || !strings.Contains(string(saved), "config-version: 8") {
		t.Fatal("legacy policy update did not migrate to valid v8 config")
	}
}
