package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUseOrLoseDrainEndingCredentialsParsing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, routing string
		want          bool
	}{
		{"absent defaults on", "strategy: use-or-lose", true},
		{"explicit false", "strategy: use-or-lose, use-or-lose: {drain-ending-credentials: false}", false},
		{"explicit true", "strategy: use-or-lose, use-or-lose: {drain-ending-credentials: true}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("routing: {"+tc.routing+"}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Routing.UseOrLose.EffectiveDrainEndingCredentials(); got != tc.want {
				t.Fatalf("drain = %t; want %t", got, tc.want)
			}
		})
	}
}

func TestUseOrLoseDrainEndingResetsParsing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, routing string
		want          bool
	}{
		{"absent defaults on", "strategy: use-or-lose", true},
		{"explicit false", "strategy: use-or-lose, use-or-lose: {drain-ending-resets: false}", false},
		{"explicit true", "strategy: use-or-lose, use-or-lose: {drain-ending-resets: true}", true},
		{"independent of drain-ending-credentials", "strategy: use-or-lose, use-or-lose: {drain-ending-credentials: false}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("routing: {"+tc.routing+"}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Routing.UseOrLose.EffectiveDrainEndingResets(); got != tc.want {
				t.Fatalf("drain resets = %t; want %t", got, tc.want)
			}
		})
	}
}
