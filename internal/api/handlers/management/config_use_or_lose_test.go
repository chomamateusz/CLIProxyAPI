package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestUseOrLosePutRoutingStrategyRespectsExplicitAffinity(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		want          bool
	}{
		{"panel false", "  session-affinity: false\n", false},
		{"explicit true", "  session-affinity: true\n", true},
		{"absent", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("routing:\n  strategy: nearest-reset\n"+tc.setting), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.PUT("/routing/strategy", h.PutRoutingStrategy)
			for range 2 {
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/routing/strategy", strings.NewReader(`{"value":"use-or-lose"}`)))
				if recorder.Code != http.StatusOK {
					t.Fatalf("PUT routing/strategy: status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				if h.cfg.Routing.Strategy != "use-or-lose" || h.cfg.Routing.SessionAffinity != tc.want {
					t.Fatalf("runtime routing = %+v; want use-or-lose with affinity %t", h.cfg.Routing, tc.want)
				}
				reloaded, err := config.LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				if reloaded.Routing.Strategy != "use-or-lose" || reloaded.Routing.SessionAffinity != tc.want {
					t.Fatalf("saved routing = %+v; want use-or-lose with affinity %t", reloaded.Routing, tc.want)
				}
				h.SetConfig(reloaded)
			}
		})
	}
}
