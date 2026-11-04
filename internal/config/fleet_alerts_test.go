package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestFleetAlertsConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yaml     string
		wantErr  bool
		stuck    time.Duration
		interval time.Duration
	}{
		{"absent", "config-version: 8\n", false, 0, 0},
		{"off", "fleet-alerts: {webhook-url: '', stuck-after: invalid}\n", false, 0, 0},
		{"defaults", "fleet-alerts: {webhook-url: 'http://127.0.0.1:9123/api/webhook/test'}\n", false, time.Hour, time.Minute},
		{"custom", "config-version: 8\nfleet-alerts: {webhook-url: 'https://example.com/hook', stuck-after: 2h, check-interval: 30s}\n", false, 2 * time.Hour, 30 * time.Second},
		{"zero threshold", "fleet-alerts: {webhook-url: 'https://example.com/hook', stuck-after: 0s}\n", true, 0, 0},
		{"negative interval", "fleet-alerts: {webhook-url: 'https://example.com/hook', check-interval: -1m}\n", true, 0, 0},
		{"bad duration", "fleet-alerts: {webhook-url: 'https://example.com/hook', stuck-after: bad}\n", true, 0, 0},
		{"relative url", "fleet-alerts: {webhook-url: '/api/webhook/test'}\n", true, 0, 0},
		{"unsupported scheme", "fleet-alerts: {webhook-url: 'file:///private/test'}\n", true, 0, 0},
		{"credentials", "fleet-alerts: {webhook-url: 'http://user:fixture-secret@example.com/hook'}\n", true, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			err := yaml.Unmarshal([]byte(tc.yaml), &cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				if strings.Contains(err.Error(), "fixture-secret") {
					t.Fatal("validation error exposed URL credentials")
				}
				return
			}
			stuck, interval, err := cfg.FleetAlerts.Durations()
			if err != nil || stuck != tc.stuck || interval != tc.interval {
				t.Fatalf("durations = %s, %s, %v", stuck, interval, err)
			}
			encoded, err := yaml.Marshal(&cfg)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip Config
			if err := yaml.Unmarshal(encoded, &roundTrip); err != nil || roundTrip.FleetAlerts != cfg.FleetAlerts {
				t.Fatalf("round trip = %+v, %v", roundTrip.FleetAlerts, err)
			}
		})
	}
}
