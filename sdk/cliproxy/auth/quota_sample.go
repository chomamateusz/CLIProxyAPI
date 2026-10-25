package auth

import (
	"math"
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
		for _, name := range []string{"Anthropic-Ratelimit-Unified-5h-Status", "Anthropic-Ratelimit-Unified-Status"} {
			if strings.EqualFold(strings.TrimSpace(quota.Signals[name]), "rejected") {
				sample.atLimit = true
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
		sample.utilisation = 0
		sample.resetAt = now
		sample.atLimit = false
	}
	return sample, true
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
	seconds, errParse := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if errParse != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}
