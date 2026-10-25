package access

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestReloadKeyIssuers(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "management-fixture")
	t.Cleanup(func() { configaccess.Register(nil) })
	path := filepath.Join(t.TempDir(), "config.yaml")
	manager := sdkaccess.NewManager()
	var previous *config.Config
	var handler *management.Handler
	var router *gin.Engine
	for _, tc := range []struct {
		name, entries        string
		oldStatus, newStatus int
	}{
		{"initial", "[{key: first-fixture, name: fleet}]", 200, 401},
		{"rotated", "[{key: second-fixture, name: fleet}]", 401, 200},
		{"removed", "[]", 401, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "config-version: 8\naccess:\n  api-keys: [client-fixture]\n  key-issuer-keys: " + tc.entries + "\n"
			if os.WriteFile(path, []byte(raw), 0600) != nil {
				t.Fatal("could not write fixture")
			}
			next, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal("could not load fixture")
			}
			if _, err = ApplyAccessProviders(manager, previous, next); err != nil {
				t.Fatal("reconcile failed")
			}
			if handler == nil {
				handler = management.NewHandler(next, path, nil)
				router = gin.New()
				router.GET("/v8/management/issued-keys", handler.Middleware(), handler.IssuedKeys)
			} else {
				handler.SetConfig(next)
			}
			for _, credential := range []struct {
				key    string
				status int
			}{{"first-fixture", tc.oldStatus}, {"second-fixture", tc.newStatus}} {
				req := httptest.NewRequest("GET", "/v8/management/issued-keys", nil)
				req.RemoteAddr = "127.0.0.1:1234"
				req.Header.Set("Authorization", "Bearer "+credential.key)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != credential.status {
					t.Fatalf("management reload status %d, want %d", rec.Code, credential.status)
				}
				if _, errAuth := manager.Authenticate(context.Background(), req); errAuth == nil {
					t.Fatal("issuer authenticated as client")
				}
			}
			previous = next
		})
	}
}
