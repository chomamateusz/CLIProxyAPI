package auth

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

type QuotaBar struct {
	ID        string
	Percent   *float64
	Status    string
	ResetsAt  time.Time
	SampledAt time.Time
}

type QuotaCredits struct {
	HasCredits *bool
	Unlimited  *bool
	Balance    *float64
}

type QuotaLimits struct {
	Plan    string
	Bars    []QuotaBar
	Credits *QuotaCredits
}

type OAuthModelExclusion struct {
	Provider    string
	Pattern     string
	Until       time.Time
	UntilSource string
}

const (
	ExclusionUntilConfig     = "config"
	ExclusionUntilQuotaReset = "quota_reset"
	ExclusionUntilCooldown   = "cooldown"
)

func QuotaLimitsForAuth(auth *Auth) QuotaLimits {
	if auth == nil {
		return QuotaLimits{}
	}
	observations := quotaObservations(auth)
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return QuotaLimits{Bars: claudeQuotaBars(observations)}
	case "codex":
		return codexQuotaLimits(observations)
	default:
		return QuotaLimits{}
	}
}

func quotaObservations(auth *Auth) []QuotaState {
	observations := make([]QuotaState, 0, 2+len(auth.ModelStates))
	if !auth.sampledQuota.ObservedAt.IsZero() && len(auth.sampledQuota.Signals) > 0 {
		observations = append(observations, auth.sampledQuota)
	}
	if !auth.Quota.ObservedAt.IsZero() && len(auth.Quota.Signals) > 0 {
		observations = append(observations, auth.Quota)
	}
	models := make([]string, 0, len(auth.ModelStates))
	for model := range auth.ModelStates {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		state := auth.ModelStates[model]
		if state != nil && !state.Quota.ObservedAt.IsZero() && len(state.Quota.Signals) > 0 {
			observations = append(observations, state.Quota)
		}
	}
	return observations
}

// Each window is read from the freshest observation carrying it because the
// 7d_oi headers only arrive with Fable responses.
func freshestQuotaObservation(observations []QuotaState, names ...string) (QuotaState, bool) {
	var best QuotaState
	found := false
	for _, quota := range observations {
		carries := false
		for _, name := range names {
			if strings.TrimSpace(quota.Signals[name]) != "" {
				carries = true
				break
			}
		}
		if carries && (!found || quota.ObservedAt.After(best.ObservedAt)) {
			best, found = quota, true
		}
	}
	return best, found
}

func claudeQuotaBars(observations []QuotaState) []QuotaBar {
	bars := make([]QuotaBar, 0, 3)
	for _, window := range []struct{ id, prefix string }{
		{"weekly_all", "Anthropic-Ratelimit-Unified-7d-"},
		{"weekly_fable", "Anthropic-Ratelimit-Unified-7d_oi-"},
		{"session_5h", "Anthropic-Ratelimit-Unified-5h-"},
	} {
		utilisation, status, reset := window.prefix+"Utilization", window.prefix+"Status", window.prefix+"Reset"
		quota, ok := freshestQuotaObservation(observations, utilisation, status, reset)
		if !ok {
			continue
		}
		bar := QuotaBar{ID: window.id, Status: quotaBarStatus(quota.Signals[status]), SampledAt: quota.ObservedAt}
		if value, okValue := quotaSignalNumber(quota.Signals[utilisation]); okValue {
			bar.Percent = roundedQuotaPercent(value * 100)
		}
		if resetAt, okReset := quotaSignalReset(quota.Signals[reset]); okReset {
			bar.ResetsAt = resetAt
		}
		bars = append(bars, bar)
	}
	return bars
}

func codexQuotaLimits(observations []QuotaState) QuotaLimits {
	limits := QuotaLimits{Bars: make([]QuotaBar, 0, 2)}
	if quota, ok := freshestQuotaObservation(observations, "X-Codex-Plan-Type"); ok {
		limits.Plan = strings.ToLower(strings.TrimSpace(quota.Signals["X-Codex-Plan-Type"]))
	}
	seen := make(map[string]bool, 2)
	for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
		quota, ok := freshestQuotaObservation(observations, prefix+"Used-Percent")
		if !ok {
			continue
		}
		minutes, okMinutes := quotaSignalNumber(quota.Signals[prefix+"Window-Minutes"])
		if !okMinutes || minutes < 1 || minutes > math.MaxInt32 {
			continue
		}
		id := codexQuotaBarID(int64(minutes))
		if seen[id] {
			continue
		}
		seen[id] = true
		bar := QuotaBar{ID: id, SampledAt: quota.ObservedAt}
		if value, okValue := quotaSignalNumber(quota.Signals[prefix+"Used-Percent"]); okValue {
			bar.Percent = roundedQuotaPercent(value)
		}
		if resetAt, okReset := quotaSignalReset(quota.Signals[prefix+"Reset-At"]); okReset {
			bar.ResetsAt = resetAt
		} else if after, okAfter := quotaSignalDuration(quota.Signals[prefix+"Reset-After-Seconds"], time.Second); okAfter {
			bar.ResetsAt = quota.ObservedAt.Add(after)
		}
		limits.Bars = append(limits.Bars, bar)
	}
	credits := &QuotaCredits{}
	if quota, ok := freshestQuotaObservation(observations, "X-Codex-Credits-Has-Credits", "X-Codex-Credits-Unlimited", "X-Codex-Credits-Balance"); ok {
		credits.HasCredits = quotaSignalBool(quota.Signals["X-Codex-Credits-Has-Credits"])
		credits.Unlimited = quotaSignalBool(quota.Signals["X-Codex-Credits-Unlimited"])
		if balance, okBalance := quotaSignalNumber(quota.Signals["X-Codex-Credits-Balance"]); okBalance {
			rounded := math.Round(balance*100) / 100
			credits.Balance = &rounded
		}
	}
	limits.Credits = credits
	return limits
}

func codexQuotaBarID(minutes int64) string {
	switch minutes {
	case 7 * 24 * 60:
		return "weekly_all"
	case 5 * 60:
		return "session_5h"
	default:
		return "window_" + strconv.FormatInt(minutes, 10) + "m"
	}
}

func quotaBarStatus(raw string) string {
	switch status := strings.ToLower(strings.TrimSpace(raw)); status {
	case "allowed", "allowed_warning", "rejected":
		return status
	default:
		return ""
	}
}

func quotaSignalBool(raw string) *bool {
	value, errParse := strconv.ParseBool(strings.TrimSpace(raw))
	if errParse != nil {
		return nil
	}
	return &value
}

func roundedQuotaPercent(value float64) *float64 {
	rounded := math.Round(value*10) / 10
	return &rounded
}

func OAuthExclusionUntil(cfg *internalconfig.Config, auths []*Auth, provider, pattern string, now time.Time) (time.Time, string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if cfg != nil {
		if raw := strings.TrimSpace(cfg.OAuthExcludedModelsUntil[provider][pattern]); raw != "" {
			if until, errParse := time.Parse(time.RFC3339, raw); errParse == nil && until.After(now) {
				return until.UTC(), ExclusionUntilConfig
			}
		}
	}
	var quotaUntil, quotaObservedAt, cooldownUntil time.Time
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), provider) {
			continue
		}
		for model, state := range auth.ModelStates {
			if state == nil || !access.WildcardMatch(pattern, canonicalModelKey(model)) {
				continue
			}
			reset, ok := quotaRejectedReset(state.Quota, now)
			if !ok {
				continue
			}
			observedAt := state.Quota.ObservedAt
			if quotaUntil.IsZero() || observedAt.After(quotaObservedAt) || observedAt.Equal(quotaObservedAt) && reset.After(quotaUntil) {
				quotaUntil, quotaObservedAt = reset, observedAt
			}
		}
		for _, view := range CooldownSnapshotForAuth(auth, now) {
			if view.Scope == "model" && access.WildcardMatch(pattern, view.ModelKey) && view.RetryAt.After(cooldownUntil) {
				cooldownUntil = view.RetryAt
			}
		}
	}
	if !quotaUntil.IsZero() {
		return quotaUntil.UTC(), ExclusionUntilQuotaReset
	}
	if !cooldownUntil.IsZero() {
		return cooldownUntil.UTC(), ExclusionUntilCooldown
	}
	return time.Time{}, ""
}

func quotaRejectedReset(quota QuotaState, now time.Time) (time.Time, bool) {
	signals := quota.Signals
	var latest time.Time
	consider := func(reset time.Time, ok bool) {
		if ok && reset.After(now) && reset.After(latest) {
			latest = reset
		}
	}
	for _, window := range []struct{ status, reset string }{
		{"Anthropic-Ratelimit-Unified-7d_oi-Status", "Anthropic-Ratelimit-Unified-7d_oi-Reset"},
		{"Anthropic-Ratelimit-Unified-7d-Status", "Anthropic-Ratelimit-Unified-7d-Reset"},
		{"Anthropic-Ratelimit-Unified-5h-Status", "Anthropic-Ratelimit-Unified-5h-Reset"},
		{"Anthropic-Ratelimit-Unified-Status", "Anthropic-Ratelimit-Unified-Reset"},
	} {
		if strings.EqualFold(strings.TrimSpace(signals[window.status]), "rejected") {
			consider(quotaSignalReset(signals[window.reset]))
		}
	}
	limitReached := quotaSignalBool(signals["X-Codex-Limit-Reached"])
	codexResets := make([]time.Time, 0, 2)
	fullWindow := false
	for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
		reset, okReset := quotaSignalReset(signals[prefix+"Reset-At"])
		if !okReset {
			if after, okAfter := quotaSignalDuration(signals[prefix+"Reset-After-Seconds"], time.Second); okAfter && !quota.ObservedAt.IsZero() {
				reset, okReset = quota.ObservedAt.Add(after), true
			}
		}
		if !okReset {
			continue
		}
		if used, okUsed := quotaSignalNumber(signals[prefix+"Used-Percent"]); okUsed && used >= 100 {
			fullWindow = true
			consider(reset, true)
			continue
		}
		codexResets = append(codexResets, reset)
	}
	// An explicit limit-reached flag without a full window does not say which
	// window tripped, so every window has to reset before the model is usable.
	if limitReached != nil && *limitReached && !fullWindow {
		for _, reset := range codexResets {
			consider(reset, true)
		}
	}
	return latest, !latest.IsZero()
}

func FindOAuthModelExclusion(cfg *internalconfig.Config, auths []*Auth, model string, now time.Time) (OAuthModelExclusion, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if cfg == nil || model == "" || len(cfg.OAuthExcludedModels) == 0 {
		return OAuthModelExclusion{}, false
	}
	providers := make([]string, 0, len(cfg.OAuthExcludedModels))
	for provider := range cfg.OAuthExcludedModels {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	var first OAuthModelExclusion
	found := false
	for _, provider := range providers {
		for _, pattern := range cfg.OAuthExcludedModels[provider] {
			if !access.WildcardMatch(pattern, model) {
				continue
			}
			until, source := OAuthExclusionUntil(cfg, auths, provider, pattern, now)
			exclusion := OAuthModelExclusion{Provider: provider, Pattern: pattern, Until: until, UntilSource: source}
			if !until.IsZero() {
				return exclusion, true
			}
			if !found {
				first, found = exclusion, true
			}
		}
	}
	return first, found
}

func (m *Manager) FindOAuthModelExclusion(model string, now time.Time) (OAuthModelExclusion, bool) {
	if m == nil {
		return OAuthModelExclusion{}, false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || len(cfg.OAuthExcludedModels) == 0 {
		return OAuthModelExclusion{}, false
	}
	return FindOAuthModelExclusion(cfg, m.List(), model, now)
}
