package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8DrainSettingsRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		setting   string
		effective func(config.UseOrLoseConfig) bool
		other     func(config.UseOrLoseConfig) bool
	}{
		{"drain-ending-credentials", config.UseOrLoseConfig.EffectiveDrainEndingCredentials, config.UseOrLoseConfig.EffectiveDrainEndingResets},
		{"drain-ending-resets", config.UseOrLoseConfig.EffectiveDrainEndingResets, config.UseOrLoseConfig.EffectiveDrainEndingCredentials},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("config-version: 8\nrouting:\n  strategy: use-or-lose\n  use-or-lose:\n    short-window-cap-pct: 90\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.effective(cfg.Routing.UseOrLose) {
				t.Fatalf("%s is not on by default", tc.setting)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.GET("/v8/management/config/*path", h.ConfigV8)
			router.PUT("/v8/management/config/*path", h.ConfigV8)
			setting := "/v8/management/config/routing/use-or-lose/" + tc.setting
			request := func(method, url, body string) *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
				return recorder
			}
			if recorder := request(http.MethodGet, setting, ""); recorder.Code != http.StatusNotFound {
				t.Fatalf("unset GET status=%d body=%s; want 404 meaning the default", recorder.Code, recorder.Body.String())
			}
			for _, want := range []bool{false, true} {
				value, _ := json.Marshal(want)
				if recorder := request(http.MethodPut, setting, string(value)); recorder.Code != http.StatusOK {
					t.Fatalf("PUT %t status=%d body=%s", want, recorder.Code, recorder.Body.String())
				}
				recorder := request(http.MethodGet, setting, "")
				var got bool
				if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &got) != nil || got != want {
					t.Fatalf("GET after PUT %t: status=%d body=%s", want, recorder.Code, recorder.Body.String())
				}
				reloaded, errLoad := config.LoadConfig(path)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				if tc.effective(reloaded.Routing.UseOrLose) != want || tc.effective(h.cfg.Routing.UseOrLose) != want {
					t.Fatalf("saved = %t, runtime = %t; want %t", tc.effective(reloaded.Routing.UseOrLose), tc.effective(h.cfg.Routing.UseOrLose), want)
				}
				if reloaded.Routing.UseOrLose.ShortWindowCapPct != 90 || !tc.other(reloaded.Routing.UseOrLose) {
					t.Fatalf("other use-or-lose settings changed: %+v", reloaded.Routing.UseOrLose)
				}
			}
			if recorder := request(http.MethodPut, setting, `"sometimes"`); recorder.Code == http.StatusOK {
				t.Fatalf("non-boolean value accepted: body=%s", recorder.Body.String())
			}
		})
	}
}
