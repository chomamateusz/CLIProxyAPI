package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const fleetEndFixture = "2026-12-12T23:00:00Z"

func TestFileTokenStoreKeepsFleetEndsAtOnRefreshSave(t *testing.T) {
	t.Run("claude token storage", func(t *testing.T) {
		baseDir := t.TempDir()
		path := filepath.Join(baseDir, "claude.json")
		refreshed := &cliproxyauth.Auth{
			ID: "claude.json", FileName: "claude.json", Provider: "claude",
			Storage:  &claude.ClaudeTokenStorage{AccessToken: "new", RefreshToken: "ref-new"},
			Metadata: map[string]any{"type": "claude", "access_token": "new", cliproxyauth.MetadataFleetEndsAt: fleetEndFixture},
		}
		store := NewFileTokenStore()
		store.SetBaseDir(baseDir)
		if _, err := store.Save(context.Background(), refreshed); err != nil {
			t.Fatal(err)
		}
		if saved := readCodexRefreshFixture(t, path); saved[cliproxyauth.MetadataFleetEndsAt] != fleetEndFixture {
			t.Fatalf("refresh save dropped the end date: %#v", saved)
		}
	})

	t.Run("codex refresh patch", func(t *testing.T) {
		baseDir := t.TempDir()
		path := filepath.Join(baseDir, "codex.json")
		base := codexRefreshFixture()
		disk := base.Clone().Metadata
		disk[cliproxyauth.MetadataFleetEndsAt] = fleetEndFixture
		writeCodexRefreshFixture(t, path, disk)
		updated := base.Clone()
		updated.Metadata["access_token"] = "outer-new"
		updated.Metadata["refresh_token"] = "outer-ref-new"
		updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, updated.Metadata, true, false)
		store := NewFileTokenStore()
		store.SetBaseDir(baseDir)
		if _, err := store.Save(context.Background(), updated); err != nil {
			t.Fatal(err)
		}
		saved := readCodexRefreshFixture(t, path)
		if saved[cliproxyauth.MetadataFleetEndsAt] != fleetEndFixture || saved["access_token"] != "outer-new" {
			t.Fatalf("refresh save dropped the end date: %#v", saved)
		}
		if endsAt, ok := cliproxyauth.FleetCredentialEndsAt(updated); !ok || cliproxyauth.FormatFleetEndsAt(endsAt) != fleetEndFixture {
			t.Fatalf("runtime auth lost the end date after save: %#v", updated.Metadata)
		}
	})
}
