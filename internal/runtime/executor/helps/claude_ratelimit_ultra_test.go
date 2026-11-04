package helps

import (
	"net/http"
	"testing"
	"time"
)

func TestClaudeRateLimitUltraWindowClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		overlay map[string]string
		shared  bool
	}{
		{name: "exact incident headers"},
		{name: "weekly warning", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "allowed_warning"}},
		{name: "weekly claim only", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d_oi-Status": "allowed", "Anthropic-Ratelimit-Unified-Representative-Claim": " Seven_Day_Overage_Included "}},
		{name: "weekly status only", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d_oi-Status": " ReJeCtEd ", "Anthropic-Ratelimit-Unified-Representative-Claim": "overage"}},
		{name: "ordinary overage claim", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d_oi-Status": "allowed", "Anthropic-Ratelimit-Unified-Representative-Claim": "overage"}, shared: true},
		{name: "similar overage claim", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d_oi-Status": "allowed", "Anthropic-Ratelimit-Unified-Representative-Claim": "seven_day_overage_included_extra"}, shared: true},
		{name: "Max weekly claim with five hour rejected", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected", "Anthropic-Ratelimit-Unified-5h-Utilization": "1.0", "Anthropic-Ratelimit-Unified-5h-Reset": "1794153600"}, shared: true},
		{name: "five hour exhausted without status", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0"}, shared: true},
		{name: "five hour healthy without status", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.0"}},
		{name: "five hour reset only", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Reset": "1794153600"}, shared: true},
		{name: "empty five hour utilization", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": ""}, shared: true},
		{name: "empty five hour status", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": ""}, shared: true},
		{name: "invalid five hour utilization", overlay: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "invalid"}, shared: true},
		{name: "weekly unknown", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "unknown"}, shared: true},
		{name: "weekly rejected", overlay: map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected"}, shared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{
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
			for name, value := range tc.overlay {
				headers.Set(name, value)
			}
			if got := ClaudeHeadersIndicateUnifiedRateLimitRejection(headers); got != tc.shared {
				t.Fatalf("shared rejection = %v, want %v", got, tc.shared)
			}
			now := time.Unix(1794153600-417173, 0)
			reset := parseClaudeRateLimitResetWithFuzz(headers, now, 0, 0)
			if tc.shared {
				if reset == nil || *reset != 417173*time.Second {
					t.Fatalf("shared reset = %v, want 417173s", reset)
				}
			} else if reset != nil {
				t.Fatalf("model-only rejection produced credential reset: %v", *reset)
			}
		})
	}
}
