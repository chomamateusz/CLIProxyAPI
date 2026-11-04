package auth

import (
	"net/http"
	"testing"
	"time"
)

func claudeUltraIncidentHeaders() http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-7d-Reset":                  {"1794153600"},
		"Anthropic-Ratelimit-Unified-7d-Status":                 {"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":            {"0.64"},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":               {"1794153600"},
		"Anthropic-Ratelimit-Unified-7d_oi-Status":              {"rejected"},
		"Anthropic-Ratelimit-Unified-7d_oi-Surpassed-Threshold": {"1.0"},
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization":         {"1.0"},
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":       {"0.5"},
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason":   {"org_level_disabled"},
		"Anthropic-Ratelimit-Unified-Overage-Status":            {"rejected"},
		"Anthropic-Ratelimit-Unified-Representative-Claim":      {"seven_day_overage_included"},
		"Anthropic-Ratelimit-Unified-Reset":                     {"1794153600"},
		"Anthropic-Ratelimit-Unified-Status":                    {"rejected"},
		"Anthropic-Ratelimit-Unified-Upgrade-Paths":             {"overage"},
		"Retry-After": {"417173"},
	}
}

func TestQuotaClaudeSharedAtLimitUltra(t *testing.T) {
	for _, tc := range []struct {
		name    string
		overlay map[string]string
		shared  bool
	}{
		{name: "exact incident headers"},
		{name: "weekly warning", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "allowed_warning"}},
		{name: "five hour rejected", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected"}, shared: true},
		{name: "five hour exhausted without status", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0"}, shared: true},
		{name: "five hour healthy without status", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.0"}},
		{name: "five hour reset only", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Reset": "1794153600"}, shared: true},
		{name: "invalid five hour utilization", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "invalid"}, shared: true},
		{name: "weekly unknown", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "unknown"}, shared: true},
		{name: "weekly rejected", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected"}, shared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := claudeUltraIncidentHeaders()
			for name, value := range tc.overlay {
				headers.Set(name, value)
			}
			signals := collectQuotaSignals("claude", headers)
			if got := quotaClaudeSharedAtLimit(signals); got != tc.shared {
				t.Fatalf("shared at limit = %v, want %v; signals=%v", got, tc.shared, signals)
			}
		})
	}
}

func TestQuotaObservationPreservesFiveHourPresenceAndReplacesSnapshot(t *testing.T) {
	var quota QuotaState
	headers := claudeUltraIncidentHeaders()
	fiveHour := map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Status":      "rejected",
		"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0",
		"Anthropic-Ratelimit-Unified-5h-Reset":       "1794153600",
	}
	for name, value := range fiveHour {
		headers.Set(name, value)
	}
	if !quota.ObserveResponseHeadersForProvider("claude", headers, time.Unix(123, 0)) {
		t.Fatal("five-hour observation was not captured")
	}
	for name, want := range fiveHour {
		if got, present := quota.Signals[name]; !present || got != want {
			t.Fatalf("%s = %q, present=%v, want %q", name, got, present, want)
		}
	}
	if !quota.ObserveResponseHeadersForProvider("claude", claudeUltraIncidentHeaders(), time.Unix(124, 0)) {
		t.Fatal("Ultra observation was not captured")
	}
	for name := range fiveHour {
		if _, present := quota.Signals[name]; present {
			t.Fatalf("old five-hour signal survived replacement: %s", name)
		}
	}
	if quotaClaudeSharedAtLimit(quota.Signals) {
		t.Fatal("Ultra Fable-only rejection exhausted the shared quota")
	}
}
