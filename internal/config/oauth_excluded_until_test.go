package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOAuthExcludedModelsUntilLoadsAndNormalizes(t *testing.T) {
	for _, layout := range []string{"legacy", "v8"} {
		t.Run(layout, func(t *testing.T) {
			raw := "oauth-excluded-models:\n  claude: [claude-fable-5-1, claude-fable-*]\n"
			if layout == "v8" {
				raw = "config-version: 8\noauth:\n  excluded-models:\n    claude: [claude-fable-5-1, claude-fable-*]\n"
			}
			raw += "oauth-excluded-models-until:\n" +
				"  \" Claude \":\n" +
				"    \"Claude-Fable-*\": \"2026-11-08T17:00:00+01:00\"\n" +
				"    claude-fable-5-1: \"next week\"\n" +
				"    \" \": \"2026-11-08T16:00:00Z\"\n" +
				"  \"\":\n" +
				"    claude-fable-*: \"2026-11-08T16:00:00Z\"\n" +
				"  codex:\n" +
				"    gpt-6-luna: \"not a time\"\n"
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			want := map[string]map[string]string{"claude": {"claude-fable-*": "2026-11-08T16:00:00Z"}}
			if !reflect.DeepEqual(cfg.OAuthExcludedModelsUntil, want) {
				t.Fatalf("until = %#v, want %#v", cfg.OAuthExcludedModelsUntil, want)
			}
			if !reflect.DeepEqual(cfg.OAuthExcludedModels, map[string][]string{"claude": {"claude-fable-5-1", "claude-fable-*"}}) {
				t.Fatalf("exclusions changed: %#v", cfg.OAuthExcludedModels)
			}
			if cloned := cfg.CloneForRuntime(); !reflect.DeepEqual(cloned.OAuthExcludedModelsUntil, want) {
				t.Fatalf("runtime clone = %#v", cloned.OAuthExcludedModelsUntil)
			}
		})
	}
}

func TestNormalizeOAuthExcludedModelsUntilEmpty(t *testing.T) {
	if got := NormalizeOAuthExcludedModelsUntil(map[string]map[string]string{"codex": {"gpt-6-luna": "bad"}}); got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
	if got := NormalizeOAuthExcludedModelsUntil(nil); got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}
