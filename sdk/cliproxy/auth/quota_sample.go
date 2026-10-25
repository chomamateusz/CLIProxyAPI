package auth

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type quotaSample struct {
	utilisation float64
	resetAt     time.Time
	atLimit     bool
}

func quotaSampleFor(auth *Auth, model string, now time.Time) (quotaSample, bool) {
	if auth == nil {
		return quotaSample{}, false
	}
	quota := auth.Quota
	if state := existingModelState(auth, model); state != nil && len(state.Quota.Signals) > 0 {
		quota = state.Quota
	}
	if quota.ObservedAt.IsZero() || len(quota.Signals) == 0 {
		return quotaSample{}, false
	}

	var sample quotaSample
	var okUsage, okReset bool
	window := 24 * time.Hour
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "codex":
		sample.utilisation, okUsage = quotaSignalNumber(quota.Signals["X-Codex-Primary-Used-Percent"])
		sample.utilisation /= 100
		sample.resetAt, okReset = quotaSignalReset(quota.Signals["X-Codex-Primary-Reset-At"])
		if !okReset {
			if after, ok := quotaSignalDuration(quota.Signals["X-Codex-Primary-Reset-After-Seconds"], time.Second); ok {
				sample.resetAt, okReset = quota.ObservedAt.Add(after), true
			}
		}
		if duration, ok := quotaSignalDuration(quota.Signals["X-Codex-Primary-Window-Minutes"], time.Minute); ok && duration > 0 {
			window = duration
		}
	case "claude":
		window = 5 * time.Hour
		sample.utilisation, okUsage = quotaSignalNumber(quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"])
		sample.resetAt, okReset = quotaSignalReset(quota.Signals["Anthropic-Ratelimit-Unified-5h-Reset"])
		if !okReset {
			sample.resetAt, okReset = quotaSignalReset(quota.Signals["Anthropic-Ratelimit-Unified-Reset"])
		}
		status7d := strings.EqualFold(strings.TrimSpace(quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"]), "rejected")
		sample.atLimit = quotaClaudeSharedAtLimit(quota.Signals)
		if status7d {
			window = 7 * 24 * time.Hour
			sample.resetAt, okReset = quotaSignalReset(quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"])
			if !okReset {
				sample.resetAt, okReset = quotaSignalReset(quota.Signals["Anthropic-Ratelimit-Unified-Reset"])
			}
		}
		if sample.atLimit && !okUsage {
			sample.utilisation, okUsage = 1, true
		}
	default:
		return quotaSample{}, false
	}
	if !okUsage || !okReset || quota.ObservedAt.Add(window).Before(now) {
		return quotaSample{}, false
	}
	sample.utilisation = min(sample.utilisation, 1)
	sample.atLimit = sample.atLimit || sample.utilisation >= 1
	if !sample.resetAt.After(now) {
		return quotaSample{}, false
	}
	return sample, true
}

func quotaClaudeSharedAtLimit(signals map[string]string) bool {
	status := func(name string) string { return strings.ToLower(strings.TrimSpace(signals[name])) }
	shared5h := status("Anthropic-Ratelimit-Unified-5h-Status")
	shared7d := status("Anthropic-Ratelimit-Unified-7d-Status")
	if shared5h == "rejected" || shared7d == "rejected" {
		return true
	}
	if status("Anthropic-Ratelimit-Unified-Status") != "rejected" {
		return false
	}
	claimOverage := strings.Contains(status("Anthropic-Ratelimit-Unified-Representative-Claim"), "overage")
	overage := claimOverage || status("Anthropic-Ratelimit-Unified-7d_oi-Status") == "rejected" || status("Anthropic-Ratelimit-Unified-Overage-Status") == "rejected" || strings.TrimSpace(signals["Anthropic-Ratelimit-Unified-Overage-Disabled-Reason"]) != ""
	if !overage {
		return true
	}
	allowed := func(value string) bool { return value == "allowed" || value == "allowed_warning" }
	healthy := func(name string) bool {
		utilisation, ok := quotaSignalNumber(signals[name])
		return ok && utilisation < 1
	}
	if (allowed(shared5h) || shared5h == "" && healthy("Anthropic-Ratelimit-Unified-5h-Utilization")) && (allowed(shared7d) || shared7d == "" && healthy("Anthropic-Ratelimit-Unified-7d-Utilization")) {
		return false
	}
	if claimOverage && shared5h == "" && shared7d == "" {
		util5h, ok5h := quotaSignalNumber(signals["Anthropic-Ratelimit-Unified-5h-Utilization"])
		util7d, ok7d := quotaSignalNumber(signals["Anthropic-Ratelimit-Unified-7d-Utilization"])
		return (ok5h && util5h >= 1) || (ok7d && util7d >= 1)
	}
	return true
}

func quotaSignalNumber(raw string) (float64, bool) {
	value, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return value, errParse == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func quotaSignalDuration(raw string, unit time.Duration) (time.Duration, bool) {
	value, ok := quotaSignalNumber(raw)
	if !ok || value >= float64(math.MaxInt64)/float64(unit) {
		return 0, false
	}
	return time.Duration(value * float64(unit)), true
}

func quotaSignalReset(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if seconds, errParse := strconv.ParseFloat(raw, 64); errParse == nil && seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds < float64(math.MaxInt64) {
		whole := int64(seconds)
		return time.Unix(whole, int64((seconds-float64(whole))*float64(time.Second))), true
	}
	if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
		return parsed, true
	}
	parsed, errParse := http.ParseTime(raw)
	return parsed, errParse == nil
}
