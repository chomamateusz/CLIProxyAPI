package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestOAuthOrganizationAutoDisablePersistsAndManagementReenables(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	const body = `{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."}}`
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, model := range []string{"", "claude-health-test"} {
			t.Run(http.StatusText(status)+"/"+model, func(t *testing.T) {
				authDir := t.TempDir()
				fileName := "claude-health.json"
				filePath := filepath.Join(authDir, fileName)
				store := fileauth.NewFileTokenStore()
				store.SetBaseDir(authDir)
				manager := coreauth.NewManager(store, nil, nil)
				if _, err := manager.Register(context.Background(), &coreauth.Auth{
					ID: fileName, FileName: fileName, Provider: "claude", Status: coreauth.StatusActive,
					Attributes: map[string]string{"path": filePath},
					Metadata:   map[string]any{"type": "claude", "access_token": "test-token", "refresh_token": "test-refresh"},
				}); err != nil {
					t.Fatal(err)
				}
				assertPersistedDisabled := func(want bool) {
					t.Helper()
					raw, err := os.ReadFile(filePath)
					if err != nil {
						t.Fatal(err)
					}
					var data map[string]any
					if err := json.Unmarshal(raw, &data); err != nil {
						t.Fatal(err)
					}
					if data["disabled"] != want {
						t.Fatalf("persisted disabled = %v, want %v", data["disabled"], want)
					}
				}
				events := 0
				manager.SetCredentialHealthHook(func(ctx context.Context, event coreauth.CredentialHealthEvent) {
					events++
					assertPersistedDisabled(true)
					auth, _ := manager.GetByID(fileName)
					if auth == nil || !auth.Disabled || event.AuthID != fileName || event.Provider != "claude" || event.HTTPStatus != status || event.PersistenceError != nil {
						t.Fatalf("invalid health event or unpublished state: event=%+v auth=%+v", event, auth)
					}
					if _, committed, err := manager.Modify(ctx, fileName, auth.RegistrationEpoch, func(current *coreauth.Auth) error {
						current.Metadata["health_observed"] = true
						return nil
					}); err != nil || !committed {
						t.Fatalf("health callback could not reenter manager: committed=%v err=%v", committed, err)
					}
				})
				ctx, cancel := context.WithCancel(coreauth.WithSkipPersist(context.Background()))
				cancel()
				manager.MarkResult(ctx, coreauth.Result{AuthID: fileName, Provider: "claude", Model: model, Error: &coreauth.Error{HTTPStatus: status, Message: body}})
				if events != 1 {
					t.Fatalf("health events = %d, want 1", events)
				}
				assertPersistedDisabled(true)

				reloaded := coreauth.NewManager(store, nil, nil)
				if err := reloaded.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
				auth, _ := reloaded.GetByID(fileName)
				if auth == nil || !auth.Disabled || auth.Status != coreauth.StatusDisabled {
					t.Fatalf("restart lost disabled state: %+v", auth)
				}
				h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, reloaded)
				rec := httptest.NewRecorder()
				listCtx, _ := gin.CreateTestContext(rec)
				listCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
				h.ListAuthFiles(listCtx)
				var listing struct {
					Files []struct {
						Name     string `json:"name"`
						Disabled bool   `json:"disabled"`
					} `json:"files"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
					t.Fatal(err)
				}
				if rec.Code != http.StatusOK || len(listing.Files) != 1 || listing.Files[0].Name != fileName || !listing.Files[0].Disabled {
					t.Fatalf("management listing does not show disabled auth: %s", rec.Body.String())
				}

				for _, target := range []*coreauth.Manager{manager, reloaded} {
					h = NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, target)
					rec = httptest.NewRecorder()
					patchCtx, _ := gin.CreateTestContext(rec)
					patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"claude-health.json","disabled":false}`))
					patchCtx.Request.Header.Set("Content-Type", "application/json")
					h.PatchAuthFileStatus(patchCtx)
					if rec.Code != http.StatusOK {
						t.Fatalf("re-enable failed: %s", rec.Body.String())
					}
					assertPersistedDisabled(false)
					auth, _ = target.GetByID(fileName)
					if auth.Disabled || auth.Status != coreauth.StatusActive || auth.StatusMessage != "" || auth.Unavailable || !auth.NextRetryAfter.IsZero() || coreauth.HasUnauthorizedAuthFailure(auth) {
						t.Fatalf("re-enable did not restore availability: %+v", auth)
					}
				}
			})
		}
	}
}
