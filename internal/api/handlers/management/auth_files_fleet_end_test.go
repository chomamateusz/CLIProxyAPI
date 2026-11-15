package management

import (
	"context"
	"encoding/json"
	"io"
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
	if entry := f.listed(t); entry[coreauth.MetadataFleetEndsAt] != wantFuture || entry["fleet_ended"] != false || entry["fleet_end_state"] != "scheduled" || entry["fleet_ends_at_source"] != "manual" {
		t.Fatalf("listed %v; want %s, scheduled by hand", entry, wantFuture)
	}
	if got := f.onDisk(t)[coreauth.MetadataFleetEndsAtSource]; got != coreauth.FleetEndSourceManual {
		t.Fatalf("stored source %v; want manual", got)
	}

	past := time.Now().Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if rec := f.patch(t, `{"name":"claude-a.json","fleet_ends_at":"`+past+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("set past status = %d body=%s", rec.Code, rec.Body.String())
	}
	if entry := f.listed(t); entry[coreauth.MetadataFleetEndsAt] != past || entry["fleet_ended"] != false || entry["fleet_end_state"] != "overdue" {
		t.Fatalf("listed %v; want %s, overdue", entry, past)
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
	if stored := f.onDisk(t); stored[coreauth.MetadataFleetEndsAt] != nil || stored[coreauth.MetadataFleetEndsAtSource] != nil {
		t.Fatalf("cleared end still stored: %v", stored)
	}
	entry := f.listed(t)
	if _, ok := entry[coreauth.MetadataFleetEndsAt]; ok {
		t.Fatalf("cleared end date still listed: %v", entry)
	}
	if _, ok := entry["fleet_ended"]; ok || entry["fleet_end_state"] != "none" {
		t.Fatalf("fleet end listed without an end date: %v", entry)
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
	if err := os.WriteFile(filepath.Join(authDir, "codex-a.json"), []byte(`{"type":"codex","fleet_ends_at":"2020-01-01T00:00:00+01:00","fleet_ends_at_source":"codex-api"}`), 0o600); err != nil {
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
	if body.Files[0][coreauth.MetadataFleetEndsAt] != "2019-12-31T23:00:00Z" || body.Files[0]["fleet_ended"] != true || body.Files[0]["fleet_end_state"] != "ended" || body.Files[0]["fleet_ends_at_source"] != "codex-api" {
		t.Fatalf("listed %v", body.Files[0])
	}
}

type fleetProfileTransport struct {
	body   string
	status int
	calls  int
}

func (p *fleetProfileTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.calls++
	if r.URL.String() != "https://api.anthropic.com/api/oauth/profile" {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: make(http.Header)}, nil
	}
	return &http.Response{StatusCode: p.status, Body: io.NopCloser(strings.NewReader(p.body)), Header: make(http.Header)}, nil
}

func (p *fleetProfileTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return p }

func fleetProfile(createdAt string) *fleetProfileTransport {
	return &fleetProfileTransport{status: http.StatusOK, body: `{"account":{"uuid":"u","has_claude_max":true},"organization":{"uuid":"o","subscription_status":"active","subscription_created_at":"` + createdAt + `","organization_type":"claude_max"}}`}
}

func nextAnniversaryOf(t *testing.T, anchor string, after time.Time) string {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, anchor)
	if err != nil {
		t.Fatal(err)
	}
	return coreauth.FormatFleetEndsAt(coreauth.NextBillingAnniversary(parsed, after))
}

func TestPatchFleetCancelledSetsBillingAnniversary(t *testing.T) {
	f := newFleetEndFixture(t)
	profile := fleetProfile("2026-10-07T21:09:56.274004Z")
	f.manager.SetRoundTripperProvider(profile)

	before := time.Now()
	if rec := f.patch(t, `{"name":"claude-a.json","fleet_cancelled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", rec.Code, rec.Body.String())
	}
	want := nextAnniversaryOf(t, "2026-10-07T21:09:56Z", before)
	stored := f.onDisk(t)
	if stored[coreauth.MetadataFleetEndsAt] != want || stored[coreauth.MetadataFleetEndsAtSource] != coreauth.FleetEndSourceClaudeAnniversary || stored[coreauth.MetadataFleetCancelledAt] == nil {
		t.Fatalf("stored %v; want end %s by billing anniversary with a cancel marker", stored, want)
	}
	entry := f.listed(t)
	if entry["fleet_end_state"] != "scheduled" || entry["fleet_ends_at_source"] != "claude-anniversary" || entry["fleet_billing_anchor"] != "2026-10-07T21:09:56Z" || entry["fleet_end_checked_at"] == nil || entry["fleet_cancelled_at"] == nil {
		t.Fatalf("listed %v", entry)
	}
	if warnings, ok := entry["fleet_end_warnings"].([]any); !ok || len(warnings) != 0 {
		t.Fatalf("warnings = %#v; want an empty array", entry["fleet_end_warnings"])
	}

	if rec := f.patch(t, `{"name":"claude-a.json","fleet_end_action":"next_period"}`); rec.Code != http.StatusOK {
		t.Fatalf("next period status = %d body=%s", rec.Code, rec.Body.String())
	}
	endsAt, _ := time.Parse(time.RFC3339, want)
	if got := f.onDisk(t)[coreauth.MetadataFleetEndsAt]; got != nextAnniversaryOf(t, "2026-10-07T21:09:56Z", endsAt) {
		t.Fatalf("next period stored %v", got)
	}
	if profile.calls != 1 {
		t.Fatalf("profile fetched %d times; want once, then reused", profile.calls)
	}

	if rec := f.patch(t, `{"name":"claude-a.json","fleet_cancelled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("uncancel status = %d body=%s", rec.Code, rec.Body.String())
	}
	stored = f.onDisk(t)
	for _, key := range []string{coreauth.MetadataFleetEndsAt, coreauth.MetadataFleetEndsAtSource, coreauth.MetadataFleetCancelledAt} {
		if _, ok := stored[key]; ok {
			t.Fatalf("uncancel kept %s: %v", key, stored)
		}
	}
}

func TestPatchFleetCancelledWithoutBillingAnchor(t *testing.T) {
	for name, profile := range map[string]*fleetProfileTransport{
		"profile without anchor": fleetProfile(""),
		"profile refused":        {status: http.StatusForbidden, body: `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFleetEndFixture(t)
			f.manager.SetRoundTripperProvider(profile)
			rec := f.patch(t, `{"name":"claude-a.json","fleet_cancelled":true}`)
			var payload map[string]any
			if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &payload) != nil || payload["error"] != "no_billing_anchor" {
				t.Fatalf("status = %d body=%s; want 400 no_billing_anchor", rec.Code, rec.Body.String())
			}
			if stored := f.onDisk(t); stored[coreauth.MetadataFleetEndsAt] != nil || stored[coreauth.MetadataFleetCancelledAt] != nil {
				t.Fatalf("rejected cancel changed the file: %v", stored)
			}
		})
	}
}

func TestPatchFleetEndNextPeriodAndValidation(t *testing.T) {
	f := newFleetEndFixture(t)
	f.manager.SetRoundTripperProvider(&fleetProfileTransport{status: http.StatusForbidden})
	if rec := f.patch(t, `{"name":"claude-a.json","fleet_end_action":"next_period"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("next period without an end: status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := f.patch(t, `{"name":"claude-a.json","fleet_ends_at":"2027-01-31T10:00:00Z"}`); rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := f.patch(t, `{"name":"claude-a.json","fleet_end_action":"next_period"}`); rec.Code != http.StatusOK {
		t.Fatalf("manual next period status = %d body=%s", rec.Code, rec.Body.String())
	}
	if stored := f.onDisk(t); stored[coreauth.MetadataFleetEndsAt] != "2027-02-28T10:00:00Z" || stored[coreauth.MetadataFleetEndsAtSource] != coreauth.FleetEndSourceManual {
		t.Fatalf("manual next period stored %v", stored)
	}
	for _, body := range []string{
		`{"name":"claude-a.json","fleet_cancelled":"yes"}`,
		`{"name":"claude-a.json","fleet_cancelled":null}`,
		`{"name":"claude-a.json","fleet_end_action":"later"}`,
		`{"name":"claude-a.json","fleet_ends_at_source":"codex-api"}`,
		`{"name":"claude-a.json","fleet_cancelled_at":"2026-11-01T00:00:00Z"}`,
		`{"name":"claude-a.json","fleet_cancelled":false,"fleet_ends_at":"2027-01-01T00:00:00Z"}`,
		`{"name":"claude-a.json","fleet_cancelled.at":true}`,
	} {
		if rec := f.patch(t, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s; want 400", body, rec.Code, rec.Body.String())
		}
	}
	if stored := f.onDisk(t); stored[coreauth.MetadataFleetEndsAt] != "2027-02-28T10:00:00Z" {
		t.Fatalf("rejected patches changed the end: %v", stored)
	}
}
