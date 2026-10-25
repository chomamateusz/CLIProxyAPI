package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestAPIKeyPolicyBlocksLegacyWrites(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"fixture"}, APIKeyPolicies: []access.KeyPolicy{{Key: "fixture", Name: "macbook"}}}}
	handler := &Handler{cfg: cfg}
	for _, tc := range []struct {
		method string
		handle gin.HandlerFunc
	}{
		{"PUT", handler.PutAPIKeys}, {"PATCH", handler.PatchAPIKeys}, {"DELETE", handler.DeleteAPIKeys},
	} {
		t.Run(tc.method, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(tc.method, "/api-keys?index=0", strings.NewReader(`[]`))
			tc.handle(c)
			if recorder.Code != http.StatusConflict || recorder.Body.String() != `{"error":"api_key_policies_present"}` {
				t.Fatal("legacy write did not return policy conflict")
			}
			if len(cfg.APIKeys) != 1 || len(cfg.APIKeyPolicies) != 1 {
				t.Fatal("legacy write changed policies")
			}
		})
	}
}

func TestAPIKeyPolicyGuardAfterReload(t *testing.T) {
	for _, method := range []string{"PUT", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			handler := &Handler{cfg: &config.Config{}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := `[]`
			if method == "PATCH" {
				body = `{"old":"fixture","new":"replacement"}`
			}
			c.Request = httptest.NewRequest(method, "/api-keys", nil)
			c.Request.Body = &keyPolicyReloadReader{Reader: strings.NewReader(body), reload: func() {
				handler.mu.Lock()
				defer handler.mu.Unlock()
				handler.cfg = &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"fixture"}, APIKeyPolicies: []access.KeyPolicy{{Key: "fixture"}}}}
			}}
			if method == "PUT" {
				handler.PutAPIKeys(c)
			} else {
				handler.PatchAPIKeys(c)
			}
			if recorder.Code != http.StatusConflict || len(handler.cfg.APIKeys) != 1 {
				t.Fatal("reload bypassed policy write guard")
			}
		})
	}
}

type keyPolicyReloadReader struct {
	*strings.Reader
	reload func()
}

func (r *keyPolicyReloadReader) Read(p []byte) (int, error) {
	if r.reload != nil {
		r.reload()
		r.reload = nil
	}
	return r.Reader.Read(p)
}

func (r *keyPolicyReloadReader) Close() error { return nil }
