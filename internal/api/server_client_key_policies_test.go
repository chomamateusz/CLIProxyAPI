package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestManagementV8ClientKeyPolicyRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("MANAGEMENT_PASSWORD", "management-fixture")
	for _, tc := range []struct {
		name                string
		enabled, authorized bool
		want                int
	}{
		{"missing management key", true, false, http.StatusUnauthorized},
		{"authorized", true, true, http.StatusOK},
		{"disabled", false, true, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := "config-version: 8\naccess:\n  api-keys:\n    - key: client-fixture\n      name: laptop\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal("could not write config fixture")
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal("could not load config fixture")
			}
			s := &Server{cfg: cfg, engine: gin.New(), mgmt: management.NewHandler(cfg, path, nil)}
			s.managementRoutesEnabled.Store(tc.enabled)
			s.registerManagementV8Routes()
			for _, route := range []struct{ method, suffix string }{
				{"GET", ""}, {"GET", "/laptop"}, {"PUT", "/laptop"}, {"PATCH", "/laptop"},
			} {
				req := httptest.NewRequest(route.method, "/v8/management/client-key-policies"+route.suffix, strings.NewReader(`{}`))
				req.RemoteAddr = "127.0.0.1:1234"
				if tc.authorized {
					req.Header.Set("X-Management-Key", "management-fixture")
				}
				recorder := httptest.NewRecorder()
				s.engine.ServeHTTP(recorder, req)
				if recorder.Code != tc.want {
					t.Fatalf("%s route returned status %d, want %d", route.method, recorder.Code, tc.want)
				}
				if strings.Contains(recorder.Body.String(), "client-fixture") {
					t.Fatal("route exposed a client key")
				}
			}
		})
	}
}
