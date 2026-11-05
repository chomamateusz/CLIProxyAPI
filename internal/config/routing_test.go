package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"gopkg.in/yaml.v3"
)

func TestUseOrLoseConfigDefaultsAndRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, routing string
		affinity      bool
		cap           float64
	}{
		{"default", "strategy: use-or-lose", true, 95},
		{"normalized strategy", "strategy: ' USE-OR-LOSE '", true, 95},
		{"explicit opt out", "strategy: use-or-lose, session-affinity: false", false, 95},
		{"custom threshold", "strategy: use-or-lose, use-or-lose: {short-window-cap-pct: 80.5}", true, 80.5},
		{"invalid threshold", "strategy: use-or-lose, use-or-lose: {short-window-cap-pct: 101}", true, 95},
		{"other strategy unchanged", "strategy: round-robin", false, 0},
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
			if cfg.Routing.SessionAffinity != tc.affinity || cfg.Routing.UseOrLose.ShortWindowCapPct != tc.cap {
				t.Fatalf("routing = %+v; want affinity %t, cap %g", cfg.Routing, tc.affinity, tc.cap)
			}
			data, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip Config
			if err = yaml.Unmarshal(data, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if roundTrip.Routing != cfg.Routing {
				t.Fatalf("routing changed after round trip: %+v -> %+v", cfg.Routing, roundTrip.Routing)
			}
		})
	}
}

func TestUseOrLoseShortWindowCapNormalization(t *testing.T) {
	t.Parallel()
	for _, value := range []float64{0, -1, 101, math.NaN(), math.Inf(1), math.Inf(-1), .1, .95, 1, 1.01, 80.5, 95, 100} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			want := float64(95)
			if value > 1 && value <= 100 {
				want = value
			}
			if got := (UseOrLoseConfig{ShortWindowCapPct: value}).EffectiveShortWindowCapPct(); got != want {
				t.Fatalf("cap = %g; want %g", got, want)
			}
		})
	}
}

func TestUseOrLoseInvalidCapWarnsInPercentUnits(t *testing.T) {
	logger := log.StandardLogger()
	originalHooks := logger.ReplaceHooks(make(log.LevelHooks))
	defer logger.ReplaceHooks(originalHooks)
	hook := logtest.NewGlobal()
	for _, value := range []string{"-1", "0", "0.95", "1", "101", ".nan", ".inf"} {
		t.Run(value, func(t *testing.T) {
			hook.Reset()
			var cfg Config
			if err := yaml.Unmarshal([]byte("routing: {strategy: use-or-lose, use-or-lose: {short-window-cap-pct: "+value+"}}"), &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.Routing.UseOrLose.ShortWindowCapPct != 95 {
				t.Fatalf("cap = %g; want 95", cfg.Routing.UseOrLose.ShortWindowCapPct)
			}
			entries := hook.AllEntries()
			if len(entries) != 1 || entries[0].Level != log.WarnLevel || !strings.Contains(entries[0].Message, "percentage greater than 1 and at most 100") || !strings.Contains(entries[0].Message, "using 95") {
				t.Fatalf("missing percentage/default warning: %+v", entries)
			}
		})
	}
	for _, routing := range []string{"strategy: use-or-lose", "strategy: use-or-lose, use-or-lose: {short-window-cap-pct: 80.5}"} {
		hook.Reset()
		var cfg Config
		if err := yaml.Unmarshal([]byte("routing: {"+routing+"}"), &cfg); err != nil {
			t.Fatal(err)
		}
		if len(hook.AllEntries()) != 0 {
			t.Fatal("valid or omitted cap must not warn")
		}
	}
}
