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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fleetEndFixture struct {
	handler  *Handler
	manager  *coreauth.Manager
	authDir  string
	filePath string
}

func newFleetEndFixture(t *testing.T) *fleetEndFixture {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "claude-a.json")
	if err := os.WriteFile(filePath, []byte(`{"type":"claude","email":"a@example.com","access_token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:         "claude-a.json",
		FileName:   "claude-a.json",
		Provider:   "claude",
		Attributes: map[string]string{"path": filePath},
		Metadata:   map[string]any{"type": "claude", "email": "a@example.com", "access_token": "token"},
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return &fleetEndFixture{handler: NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager), manager: manager, authDir: authDir, filePath: filePath}
}

func (f *fleetEndFixture) patch(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	f.handler.PatchAuthFileFields(ctx)
	return rec
}

func (f *fleetEndFixture) listed(t *testing.T) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
	ctx.Set(ConfigV8ContextKey, true)
	f.handler.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Files) != 1 {
		t.Fatalf("list body = %s, %v", rec.Body.String(), err)
	}
	return body.Files[0]
}

func (f *fleetEndFixture) onDisk(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(f.filePath)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPatchFleetEndsAtSetsListsAndClears(t *testing.T) {
	f := newFleetEndFixture(t)
	future := time.Now().Add(27 * 24 * time.Hour).Truncate(time.Second)
	localFuture := future.In(time.FixedZone("CET", 3600)).Format(time.RFC3339)
	wantFuture := future.UTC().Format(time.RFC3339)

	if rec := f.patch(t, `{"name":"claude-a.json","fleet_ends_at":"`+localFuture+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := f.onDisk(t)[coreauth.MetadataFleetEndsAt]; got != wantFuture {
		t.Fatalf("stored %v; want %s", got, wantFuture)
	}
	if entry := f.listed(t); entry[coreauth.MetadataFleetEndsAt] != wantFuture || entry["fleet_ended"] != false {
		t.Fatalf("listed %v / %v; want %s, not ended", entry[coreauth.MetadataFleetEndsAt], entry["fleet_ended"], wantFuture)
	}

	past := time.Now().Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if rec := f.patch(t, `{"name":"claude-a.json","fleet_ends_at":"`+past+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("set past status = %d body=%s", rec.Code, rec.Body.String())
	}
	if entry := f.listed(t); entry[coreauth.MetadataFleetEndsAt] != past || entry["fleet_ended"] != true {
		t.Fatalf("listed %v / %v; want %s, ended", entry[coreauth.MetadataFleetEndsAt], entry["fleet_ended"], past)
	}

	restarted := fileauth.NewFileTokenStore()
	restarted.SetBaseDir(f.authDir)
	loaded, err := restarted.List(context.Background())
	if err != nil || len(loaded) != 1 {
		t.Fatalf("reload = %v, %v", loaded, err)
	}
	if endsAt, ok := coreauth.FleetCredentialEndsAt(loaded[0]); !ok || coreauth.FormatFleetEndsAt(endsAt) != past {
		t.Fatalf("reloaded end = %v, %t; want %s", endsAt, ok, past)
	}

	if rec := f.patch(t, `{"name":"claude-a.json","fleet_ends_at":null}`); rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := f.onDisk(t)[coreauth.MetadataFleetEndsAt]; ok {
		t.Fatal("cleared end date still stored")
	}
	entry := f.listed(t)
	if _, ok := entry[coreauth.MetadataFleetEndsAt]; ok {
		t.Fatalf("cleared end date still listed: %v", entry)
	}
	if _, ok := entry["fleet_ended"]; ok {
		t.Fatalf("fleet_ended listed without an end date: %v", entry)
	}
	if current, _ := f.manager.GetByID("claude-a.json"); coreauth.FleetCredentialEnded(current, time.Now()) {
		t.Fatal("runtime credential still ended after clearing")
	}
}

func TestPatchFleetEndsAtRejectsInvalidValues(t *testing.T) {
	f := newFleetEndFixture(t)
	for _, body := range []string{
		`{"name":"claude-a.json","fleet_ends_at":"tomorrow"}`,
		`{"name":"claude-a.json","fleet_ends_at":"2026-12-12"}`,
		`{"name":"claude-a.json","fleet_ends_at":1765580400}`,
		`{"name":"claude-a.json","fleet_ends_at":true}`,
		`{"name":"claude-a.json","fleet_ends_at.at":"2026-12-12T23:00:00Z"}`,
	} {
		rec := f.patch(t, body)
		var payload map[string]any
		if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &payload) != nil || payload["error"] == "" {
			t.Fatalf("%s: status = %d body=%s; want a 400 JSON error", body, rec.Code, rec.Body.String())
		}
	}
	if _, ok := f.onDisk(t)[coreauth.MetadataFleetEndsAt]; ok {
		t.Fatal("rejected value was stored")
	}
}

func TestListAuthFilesFromDiskShowsFleetEnd(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	authDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(authDir, "codex-a.json"), []byte(`{"type":"codex","fleet_ends_at":"2020-01-01T00:00:00+01:00"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)
	var body struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Files) != 1 {
		t.Fatalf("list body = %s, %v", rec.Body.String(), err)
	}
	if body.Files[0][coreauth.MetadataFleetEndsAt] != "2019-12-31T23:00:00Z" || body.Files[0]["fleet_ended"] != true {
		t.Fatalf("listed %v", body.Files[0])
	}
}
