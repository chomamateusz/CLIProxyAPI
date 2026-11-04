package auth

import (
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

var quotaLimitsNow = time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)

func ultraOpusSignals() map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":                "1794153600",
		"Anthropic-Ratelimit-Unified-7d-Status":               "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization":          "0.64",
		"Anthropic-Ratelimit-Unified-Representative-Claim":    "seven_day",
		"Anthropic-Ratelimit-Unified-Status":                  "allowed",
		"Anthropic-Ratelimit-Unified-Overage-Status":          "rejected",
		"Anthropic-Ratelimit-Unified-Reset":                   "1794153600",
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": "org_level_disabled",
	}
}

func ultraFableSignals() map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":                  "1794153600",
		"Anthropic-Ratelimit-Unified-7d-Status":                 "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization":            "0.64",
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":               "1794153600",
		"Anthropic-Ratelimit-Unified-7d_oi-Status":              "rejected",
		"Anthropic-Ratelimit-Unified-7d_oi-Surpassed-Threshold": "1.0",
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization":         "1.0",
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":       "0.5",
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason":   "org_level_disabled",
		"Anthropic-Ratelimit-Unified-Overage-Status":            "rejected",
		"Anthropic-Ratelimit-Unified-Representative-Claim":      "seven_day_overage_included",
		"Anthropic-Ratelimit-Unified-Reset":                     "1794153600",
		"Anthropic-Ratelimit-Unified-Status":                    "rejected",
		"Anthropic-Ratelimit-Unified-Upgrade-Paths":             "overage",
		"Retry-After": "417173",
	}
}

func codexProSignals() map[string]string {
	return map[string]string{
		"X-Codex-Active-Limit":                         "premium",
		"X-Codex-Credits-Balance":                      "14090.3601385000",
		"X-Codex-Credits-Has-Credits":                  "True",
		"X-Codex-Credits-Unlimited":                    "False",
		"X-Codex-Plan-Type":                            "pro",
		"X-Codex-Primary-Over-Secondary-Limit-Percent": "0",
		"X-Codex-Primary-Reset-After-Seconds":          "38819",
		"X-Codex-Primary-Reset-At":                     "1793775332",
		"X-Codex-Primary-Used-Percent":                 "100",
		"X-Codex-Primary-Window-Minutes":               "10080",
		"X-Codex-Secondary-Reset-After-Seconds":        "0",
		"X-Codex-Secondary-Used-Percent":               "0",
		"X-Codex-Secondary-Window-Minutes":             "0",
	}
}

func ultraClaudeAuth() *Auth {
	return &Auth{
		ID:       "claude-ultra",
		Provider: "claude",
		Quota:    QuotaState{ObservedAt: quotaLimitsNow.Add(-time.Hour), Signals: ultraOpusSignals()},
		ModelStates: map[string]*ModelState{
			"claude-fable-5-1": {Quota: QuotaState{ObservedAt: quotaLimitsNow.Add(-2 * time.Hour), Signals: ultraFableSignals()}},
		},
	}
}

func barByID(bars []QuotaBar, id string) (QuotaBar, bool) {
	for _, bar := range bars {
		if bar.ID == id {
			return bar, true
		}
	}
	return QuotaBar{}, false
}

func TestQuotaLimitsForAuthClaudeUltra(t *testing.T) {
	limits := QuotaLimitsForAuth(ultraClaudeAuth())
	weekly, ok := barByID(limits.Bars, "weekly_all")
	if !ok || weekly.Percent == nil || *weekly.Percent != 64 || weekly.Status != "allowed" || !weekly.ResetsAt.Equal(time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC)) || !weekly.SampledAt.Equal(quotaLimitsNow.Add(-time.Hour)) {
		t.Fatalf("weekly_all = %+v", weekly)
	}
	fable, ok := barByID(limits.Bars, "weekly_fable")
	if !ok || fable.Percent == nil || *fable.Percent != 100 || fable.Status != "rejected" || !fable.ResetsAt.Equal(time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC)) || !fable.SampledAt.Equal(quotaLimitsNow.Add(-2*time.Hour)) {
		t.Fatalf("weekly_fable = %+v", fable)
	}
	if _, ok := barByID(limits.Bars, "session_5h"); ok {
		t.Fatal("Ultra credential without 5h headers must not report session_5h")
	}
	if limits.Plan != "" || limits.Credits != nil {
		t.Fatalf("claude plan/credits = %q/%+v", limits.Plan, limits.Credits)
	}
}

func TestQuotaLimitsForAuthPicksFreshestObservation(t *testing.T) {
	auth := ultraClaudeAuth()
	newer := ultraFableSignals()
	newer["Anthropic-Ratelimit-Unified-7d-Utilization"] = "0.71"
	auth.ModelStates["claude-fable-5-1"].Quota = QuotaState{ObservedAt: quotaLimitsNow, Signals: newer}
	weekly, _ := barByID(QuotaLimitsForAuth(auth).Bars, "weekly_all")
	if weekly.Percent == nil || *weekly.Percent != 71 || !weekly.SampledAt.Equal(quotaLimitsNow) {
		t.Fatalf("weekly_all = %+v", weekly)
	}
}

func TestQuotaLimitsForAuthClaudeMaxSession(t *testing.T) {
	signals := map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.123",
		"Anthropic-Ratelimit-Unified-5h-Status":      "allowed_warning",
		"Anthropic-Ratelimit-Unified-5h-Reset":       "1794081600",
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.3",
	}
	limits := QuotaLimitsForAuth(&Auth{Provider: "claude", Quota: QuotaState{ObservedAt: quotaLimitsNow, Signals: signals}})
	session, ok := barByID(limits.Bars, "session_5h")
	if !ok || session.Percent == nil || *session.Percent != 12.3 || session.Status != "allowed_warning" || session.ResetsAt.IsZero() {
		t.Fatalf("session_5h = %+v", session)
	}
	weekly, _ := barByID(limits.Bars, "weekly_all")
	if weekly.Status != "" || !weekly.ResetsAt.IsZero() {
		t.Fatalf("weekly_all without status/reset = %+v", weekly)
	}
}

func TestQuotaLimitsForAuthCodexPro(t *testing.T) {
	limits := QuotaLimitsForAuth(&Auth{Provider: "codex", Quota: QuotaState{ObservedAt: quotaLimitsNow, Signals: codexProSignals()}})
	if limits.Plan != "pro" {
		t.Fatalf("plan = %q", limits.Plan)
	}
	if len(limits.Bars) != 1 {
		t.Fatalf("bars = %+v, want only the primary window", limits.Bars)
	}
	weekly := limits.Bars[0]
	if weekly.ID != "weekly_all" || weekly.Percent == nil || *weekly.Percent != 100 || weekly.Status != "" || !weekly.ResetsAt.Equal(time.Unix(1793775332, 0)) {
		t.Fatalf("weekly_all = %+v", weekly)
	}
	credits := limits.Credits
	if credits == nil || credits.HasCredits == nil || !*credits.HasCredits || credits.Unlimited == nil || *credits.Unlimited || credits.Balance == nil || *credits.Balance != 14090.36 {
		t.Fatalf("credits = %+v", credits)
	}
}

func TestQuotaLimitsForAuthCodexWindows(t *testing.T) {
	signals := map[string]string{
		"X-Codex-Primary-Used-Percent":          "41.26",
		"X-Codex-Primary-Window-Minutes":        "300",
		"X-Codex-Primary-Reset-After-Seconds":   "600",
		"X-Codex-Secondary-Used-Percent":        "7",
		"X-Codex-Secondary-Window-Minutes":      "1440",
		"X-Codex-Secondary-Reset-After-Seconds": "60",
	}
	limits := QuotaLimitsForAuth(&Auth{Provider: "codex", Quota: QuotaState{ObservedAt: quotaLimitsNow, Signals: signals}})
	session, ok := barByID(limits.Bars, "session_5h")
	if !ok || *session.Percent != 41.3 || !session.ResetsAt.Equal(quotaLimitsNow.Add(10*time.Minute)) {
		t.Fatalf("session_5h = %+v", session)
	}
	if daily, ok := barByID(limits.Bars, "window_1440m"); !ok || *daily.Percent != 7 {
		t.Fatalf("window_1440m = %+v", daily)
	}
	if limits.Plan != "" || limits.Credits == nil || limits.Credits.HasCredits != nil || limits.Credits.Balance != nil {
		t.Fatalf("plan/credits = %q/%+v", limits.Plan, limits.Credits)
	}
}

func TestQuotaLimitsForAuthOtherProviders(t *testing.T) {
	limits := QuotaLimitsForAuth(&Auth{Provider: "gemini", Quota: QuotaState{ObservedAt: quotaLimitsNow, Signals: codexProSignals()}})
	if len(limits.Bars) != 0 || limits.Credits != nil || limits.Plan != "" {
		t.Fatalf("limits = %+v", limits)
	}
}

func TestQuotaLimitsDoesNotChangeRoutingSample(t *testing.T) {
	auth := ultraClaudeAuth()
	before, okBefore := quotaSampleFor(auth, "claude-fable-5-1", quotaLimitsNow)
	_ = QuotaLimitsForAuth(auth)
	after, okAfter := quotaSampleFor(auth, "claude-fable-5-1", quotaLimitsNow)
	if before != after || okBefore != okAfter {
		t.Fatalf("routing sample changed: %+v/%v -> %+v/%v", before, okBefore, after, okAfter)
	}
}

func TestOAuthExclusionUntilSources(t *testing.T) {
	fableReset := time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC)
	cooldownEnd := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	ultra := ultraClaudeAuth()
	codex := &Auth{ID: "codex-pro", Provider: "codex", ModelStates: map[string]*ModelState{
		"gpt-6-sol": {Unavailable: true, NextRetryAfter: cooldownEnd, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: cooldownEnd}},
	}}
	cfg := &internalconfig.Config{
		OAuthExcludedModels: map[string][]string{"claude": {"claude-fable-5-1", "claude-fable-*"}, "codex": {"gpt-6-luna", "gpt-6-sol", "gpt-5-codex-mini"}},
		OAuthExcludedModelsUntil: map[string]map[string]string{
			"codex":  {"gpt-6-luna": "2026-11-10T00:00:00Z", "gpt-5-codex-mini": "2026-11-01T00:00:00Z"},
			"claude": {"claude-fable-5-1": "not-a-time"},
		},
	}
	auths := []*Auth{ultra, codex}
	for _, tc := range []struct {
		provider, pattern string
		until             time.Time
		source            string
	}{
		{"codex", "gpt-6-luna", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), ExclusionUntilConfig},
		{"claude", "claude-fable-5-1", fableReset, ExclusionUntilQuotaReset},
		{"claude", "claude-fable-*", fableReset, ExclusionUntilQuotaReset},
		{"codex", "gpt-6-sol", cooldownEnd, ExclusionUntilCooldown},
		{"codex", "gpt-5-codex-mini", time.Time{}, ""},
		{"claude", "claude-opus-*", time.Time{}, ""},
	} {
		until, source := OAuthExclusionUntil(cfg, auths, tc.provider, tc.pattern, quotaLimitsNow)
		if !until.Equal(tc.until) || source != tc.source {
			t.Errorf("%s/%s until = %v (%q), want %v (%q)", tc.provider, tc.pattern, until, source, tc.until, tc.source)
		}
	}
	if until, source := OAuthExclusionUntil(cfg, auths, "claude", "claude-fable-*", fableReset.Add(time.Minute)); !until.IsZero() || source != "" {
		t.Fatalf("passed reset still reported: %v (%q)", until, source)
	}
}

func TestFindOAuthModelExclusion(t *testing.T) {
	cfg := &internalconfig.Config{OAuthExcludedModels: map[string][]string{"claude": {"claude-fable-5-1", "claude-fable-*"}}}
	exclusion, ok := FindOAuthModelExclusion(cfg, []*Auth{ultraClaudeAuth()}, "Claude-Fable-5-1", quotaLimitsNow)
	if !ok || exclusion.Provider != "claude" || exclusion.Pattern != "claude-fable-5-1" || exclusion.UntilSource != ExclusionUntilQuotaReset {
		t.Fatalf("exclusion = %+v, %v", exclusion, ok)
	}
	exclusion, ok = FindOAuthModelExclusion(cfg, nil, "claude-fable-6", quotaLimitsNow)
	if !ok || exclusion.Pattern != "claude-fable-*" || !exclusion.Until.IsZero() {
		t.Fatalf("exclusion without until = %+v, %v", exclusion, ok)
	}
	if _, ok = FindOAuthModelExclusion(cfg, nil, "claude-opus-5-5", quotaLimitsNow); ok {
		t.Fatal("unexcluded model matched")
	}
}

func TestOAuthExclusionUntilUsesLatestFutureRejectedReset(t *testing.T) {
	sharedReset := time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC)
	signals := ultraFableSignals()
	signals["Anthropic-Ratelimit-Unified-7d_oi-Reset"] = "2026-11-01T00:00:00Z"
	signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
	signals["Anthropic-Ratelimit-Unified-5h-Status"] = "rejected"
	signals["Anthropic-Ratelimit-Unified-5h-Reset"] = "2026-11-04T01:00:00Z"
	auth := &Auth{ID: "claude-ultra", Provider: "claude", ModelStates: map[string]*ModelState{
		"claude-fable-5-1": {Quota: QuotaState{ObservedAt: quotaLimitsNow.Add(-time.Hour), Signals: signals}},
	}}
	until, source := OAuthExclusionUntil(nil, []*Auth{auth}, "claude", "claude-fable-*", quotaLimitsNow)
	if !until.Equal(sharedReset) || source != ExclusionUntilQuotaReset {
		t.Fatalf("until = %v (%q), want %v (quota_reset)", until, source, sharedReset)
	}
}

func TestOAuthExclusionUntilCodexFullWindow(t *testing.T) {
	cooldownEnd := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	observedAt := quotaLimitsNow.Add(-time.Hour)
	signals := map[string]string{
		"X-Codex-Primary-Used-Percent":          "100",
		"X-Codex-Primary-Window-Minutes":        "10080",
		"X-Codex-Primary-Reset-After-Seconds":   "234000",
		"X-Codex-Secondary-Used-Percent":        "35",
		"X-Codex-Secondary-Window-Minutes":      "300",
		"X-Codex-Secondary-Reset-After-Seconds": "600",
	}
	codex := &Auth{ID: "codex-pro", Provider: "codex", ModelStates: map[string]*ModelState{
		"gpt-6-sol": {Unavailable: true, NextRetryAfter: cooldownEnd, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: cooldownEnd, ObservedAt: observedAt, Signals: signals}},
	}}
	want := observedAt.Add(234000 * time.Second)
	until, source := OAuthExclusionUntil(nil, []*Auth{codex}, "codex", "gpt-6-sol", quotaLimitsNow)
	if !until.Equal(want) || source != ExclusionUntilQuotaReset {
		t.Fatalf("until = %v (%q), want %v (quota_reset)", until, source, want)
	}

	signals["X-Codex-Primary-Used-Percent"] = "99.5"
	until, source = OAuthExclusionUntil(nil, []*Auth{codex}, "codex", "gpt-6-sol", quotaLimitsNow)
	if !until.Equal(cooldownEnd) || source != ExclusionUntilCooldown {
		t.Fatalf("window below 100 %%: until = %v (%q), want cooldown %v", until, source, cooldownEnd)
	}

	signals["X-Codex-Limit-Reached"] = "true"
	until, source = OAuthExclusionUntil(nil, []*Auth{codex}, "codex", "gpt-6-sol", quotaLimitsNow)
	if !until.Equal(want) || source != ExclusionUntilQuotaReset {
		t.Fatalf("limit reached: until = %v (%q), want %v (quota_reset)", until, source, want)
	}
}
