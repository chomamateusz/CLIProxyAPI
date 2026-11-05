package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

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
	for _, value := range []float64{0, -1, 101, math.NaN(), math.Inf(1), math.Inf(-1), .1, 80.5, 95, 100} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			want := float64(95)
			if value > 0 && value <= 100 {
				want = value
			}
			if got := (UseOrLoseConfig{ShortWindowCapPct: value}).EffectiveShortWindowCapPct(); got != want {
				t.Fatalf("cap = %g; want %g", got, want)
			}
		})
	}
}
