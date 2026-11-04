package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestReloadConfigPicksUpOAuthExcludedModelsUntil(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	raw := "auth-dir: " + authDir + "\n" +
		"oauth-excluded-models:\n  claude: [claude-fable-*]\n" +
		"oauth-excluded-models-until:\n  Claude:\n    Claude-Fable-*: \"2026-11-08T16:00:00Z\"\n"
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	var reloaded *config.Config
	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(cfg *config.Config) { reloaded = cfg },
	}
	w.SetConfig(&config.Config{AuthDir: authDir, CredentialInFlight: config.DefaultCredentialInFlightConfig(), OAuthExcludedModels: map[string][]string{"claude": {"claude-fable-*"}}})

	if ok := w.reloadConfig(); !ok {
		t.Fatal("expected reloadConfig to succeed")
	}
	if reloaded == nil || reloaded.OAuthExcludedModelsUntil["claude"]["claude-fable-*"] != "2026-11-08T16:00:00Z" {
		t.Fatalf("reloaded until = %#v", reloaded)
	}
}
