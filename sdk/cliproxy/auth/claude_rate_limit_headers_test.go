package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

var rateLimitNow = time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)

func rateLimitAuth(id string, cooldown time.Duration) *Auth {
	auth := &Auth{ID: id, Provider: "claude", Status: StatusActive}
	if cooldown != 0 {
		auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: rateLimitNow.Add(cooldown)}
	}
	return auth
}

func rateLimitWindow(auth *Auth, prefix, status string, after time.Duration) {
	auth.Quota.ObservedAt = rateLimitNow
	if auth.Quota.Signals == nil {
		auth.Quota.Signals = make(map[string]string)
	}
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-"+prefix+"-Status"] = status
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-"+prefix+"-Reset"] = strconv.FormatInt(rateLimitNow.Add(after).Unix(), 10)
}

func TestClaudeRateLimitBlock(t *testing.T) {
	const sonnet = "claude-sonnet-5-5"
	for _, test := range []struct {
		name     string
		model    string
		cooldown time.Duration
		upstream string
		setup    func(*Auth)
		want     time.Duration
		claim    string
	}{
		{name: "no data", model: sonnet, claim: "seven_day"},
		{name: "cooldown only ultra", model: sonnet, cooldown: time.Hour, want: time.Hour, claim: "seven_day"},
		{name: "cooldown keeps upstream claim", model: sonnet, cooldown: time.Hour, upstream: "five_hour", want: time.Hour, claim: "five_hour"},
		{name: "cooldown with session plan", model: sonnet, cooldown: time.Hour, setup: func(a *Auth) { rateLimitWindow(a, "5h", "allowed", 3*time.Hour) }, want: time.Hour, claim: "five_hour"},
		{name: "session rejection", model: sonnet, setup: func(a *Auth) { rateLimitWindow(a, "5h", "rejected", time.Hour) }, want: time.Hour, claim: "five_hour"},
		{name: "window outlasts cooldown", model: sonnet, cooldown: time.Hour, upstream: "five_hour", setup: func(a *Auth) { rateLimitWindow(a, "7d", "rejected", 4*time.Hour) }, want: 4 * time.Hour, claim: "seven_day"},
		{name: "cooldown outlasts window", model: sonnet, cooldown: 4 * time.Hour, setup: func(a *Auth) { rateLimitWindow(a, "5h", "rejected", time.Hour) }, want: 4 * time.Hour, claim: "five_hour"},
		{name: "latest rejected window", model: sonnet, setup: func(a *Auth) {
			rateLimitWindow(a, "5h", "rejected", time.Hour)
			rateLimitWindow(a, "7d", "rejected", 5*time.Hour)
		}, want: 5 * time.Hour, claim: "seven_day"},
		{name: "fable window ignored on sonnet", model: sonnet, setup: func(a *Auth) { rateLimitWindow(a, "7d_oi", "rejected", 5*time.Hour) }, claim: "seven_day"},
		{name: "fable window applies", model: "claude-fable-5-1(high)", setup: func(a *Auth) { rateLimitWindow(a, "7d_oi", "rejected", 5*time.Hour) }, want: 5 * time.Hour, claim: "seven_day_overage_included"},
		{name: "past timestamps", model: sonnet, cooldown: -time.Hour, setup: func(a *Auth) { rateLimitWindow(a, "7d", "rejected", -time.Minute) }, claim: "seven_day"},
		{name: "reset now", model: sonnet, setup: func(a *Auth) { rateLimitWindow(a, "7d", "rejected", 0) }, claim: "seven_day"},
		{name: "other model cooldown ignored", model: sonnet, setup: func(a *Auth) {
			a.ModelStates = map[string]*ModelState{"claude-fable-5-1": {Unavailable: true, NextRetryAfter: rateLimitNow.Add(5 * time.Hour)}}
		}, claim: "seven_day"},
		{name: "matching model cooldown", model: sonnet + "(high)", setup: func(a *Auth) {
			a.ModelStates = map[string]*ModelState{sonnet: {Unavailable: true, NextRetryAfter: rateLimitNow.Add(time.Hour)}}
		}, want: time.Hour, claim: "seven_day"},
		{name: "model outlasts credential cooldown", model: sonnet, cooldown: time.Hour, setup: func(a *Auth) {
			a.ModelStates = map[string]*ModelState{sonnet: {Unavailable: true, NextRetryAfter: rateLimitNow.Add(5 * time.Hour)}}
		}, want: 5 * time.Hour, claim: "seven_day"},
		{name: "credential outlasts model cooldown", model: sonnet, cooldown: 5 * time.Hour, setup: func(a *Auth) {
			a.ModelStates = map[string]*ModelState{sonnet: {Unavailable: true, NextRetryAfter: rateLimitNow.Add(time.Hour)}}
		}, want: 5 * time.Hour, claim: "seven_day"},
		{name: "freshest window clears stale rejection", model: sonnet, setup: func(a *Auth) {
			rateLimitWindow(a, "7d", "rejected", time.Hour)
			a.ModelStates = map[string]*ModelState{sonnet: {Quota: QuotaState{ObservedAt: rateLimitNow.Add(time.Second), Signals: map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "allowed"}}}}
		}, claim: "seven_day"},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := rateLimitAuth("block-test", test.cooldown)
			if test.setup != nil {
				test.setup(auth)
			}
			got := claudeRateLimitBlockForAuth(auth, test.model, test.upstream, rateLimitNow)
			var want time.Time
			if test.want != 0 {
				want = rateLimitNow.Add(test.want)
			}
			if !got.until.Equal(want) || got.claim != test.claim {
				t.Fatalf("block=%+v, want until=%s claim=%s", got, want, test.claim)
			}
		})
	}
}

func TestClaudeRateLimitHeadersEligibility(t *testing.T) {
	const model = "claude-sonnet-5-5"
	for _, test := range []struct {
		name     string
		setup    func(*Manager, *Auth, *Auth)
		policy   *access.KeyPolicy
		pinned   string
		provider string
		want     int64
	}{
		{name: "minimum across credentials", want: 3600},
		{name: "disabled flag ignored", setup: func(_ *Manager, a, _ *Auth) { a.Disabled = true }, want: 7200},
		{name: "disabled status ignored", setup: func(_ *Manager, a, _ *Auth) { a.Status = StatusDisabled }, want: 7200},
		{name: "key policy excludes credential", policy: &access.KeyPolicy{ExcludedCredentials: []string{"early"}}, want: 7200},
		{name: "key policy permits one credential", policy: &access.KeyPolicy{Credentials: []string{"late"}}, want: 7200},
		{name: "key policy excludes model", policy: &access.KeyPolicy{ExcludedModels: []string{"claude-*"}}},
		{name: "pinned credential", pinned: "late", want: 7200},
		{name: "unknown pinned credential", pinned: "missing"},
		{name: "non claude provider", provider: "codex"},
		{name: "other credential provider ignored", setup: func(_ *Manager, a, _ *Auth) { a.Provider = "codex" }, want: 7200},
		{name: "oauth excluded model absent from registration", setup: func(_ *Manager, a, _ *Auth) {
			registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-haiku-5-5"}})
		}, want: 7200},
		{name: "no block data", setup: func(_ *Manager, a, b *Auth) { a.Quota = QuotaState{}; b.Quota = QuotaState{} }},
		{name: "one usable credential", setup: func(_ *Manager, a, _ *Auth) { a.Quota = QuotaState{} }},
		{name: "one expired cooldown", setup: func(_ *Manager, a, _ *Auth) { a.Quota.NextRecoverAt = rateLimitNow.Add(-time.Second) }},
		{name: "rejected window extends earliest credential", setup: func(_ *Manager, a, _ *Auth) { rateLimitWindow(a, "7d", "rejected", 3*time.Hour) }, want: 7200},
		{name: "missing executor", setup: func(m *Manager, _, _ *Auth) { delete(m.executors, "claude") }},
		{name: "subsecond reset rounds up", setup: func(_ *Manager, a, _ *Auth) { a.Quota.NextRecoverAt = rateLimitNow.Add(time.Millisecond) }, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(nil, &NearestResetSelector{}, nil)
			m.RegisterExecutor(&refusalTestExecutor{provider: "claude"})
			a, b := rateLimitAuth("early", time.Hour), rateLimitAuth("late", 2*time.Hour)
			m.auths[a.ID], m.auths[b.ID] = a, b
			for _, candidate := range []*Auth{a, b} {
				registry.GetGlobalRegistry().RegisterClient(candidate.ID, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
			}
			if test.setup != nil {
				test.setup(m, a, b)
			}
			provider := test.provider
			if provider == "" {
				provider = "claude"
			}
			ctx := access.WithKeyPolicy(context.Background(), test.policy)
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: test.pinned}}
			got := m.ClaudeRateLimitHeaders(ctx, []string{provider}, model, opts, nil, rateLimitNow)
			if test.want == 0 {
				if len(got) != 0 {
					t.Fatalf("unexpected headers: %v", got)
				}
			} else {
				if len(got) != 4 || got.Get("Anthropic-Ratelimit-Unified-Status") != "rejected" || got.Get("Anthropic-Ratelimit-Unified-Representative-Claim") != "seven_day" || got.Get("Anthropic-Ratelimit-Unified-Reset") != strconv.FormatInt(rateLimitNow.Unix()+test.want, 10) || got.Get("Retry-After") != strconv.FormatInt(test.want, 10) {
					t.Fatalf("headers=%v, want retry=%d", got, test.want)
				}
			}
		})
	}
}

func TestClaudeRateLimitHeadersRefusalClaimAndQuota(t *testing.T) {
	const model = "claude-sonnet-5-5"
	for _, test := range []struct {
		name      string
		refusedID string
		provider  string
		setup     func(*Auth)
		claim     string
	}{
		{name: "same credential claim", refusedID: "early", claim: "seven_day_overage_included"},
		{name: "other credential ultra fallback", refusedID: "late", claim: "seven_day"},
		{name: "other credential session fallback", refusedID: "late", setup: func(a *Auth) { rateLimitWindow(a, "5h", "allowed", 3*time.Hour) }, claim: "five_hour"},
		{name: "non claude refusal", refusedID: "late", provider: "antigravity"},
		{name: "transient first to free", setup: func(a *Auth) {
			a.Quota = QuotaState{}
			a.Unavailable = true
			a.NextRetryAfter = rateLimitNow.Add(time.Minute)
			a.LastError = &Error{HTTPStatus: http.StatusTooManyRequests}
		}},
		{name: "transient plus rejected window", setup: func(a *Auth) {
			a.Quota = QuotaState{}
			a.Unavailable = true
			a.NextRetryAfter = rateLimitNow.Add(time.Minute)
			rateLimitWindow(a, "5h", "rejected", time.Hour)
		}, claim: "five_hour"},
		{name: "model quota reason without rejection", setup: func(a *Auth) {
			a.Quota = QuotaState{}
			a.ModelStates = map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: rateLimitNow.Add(time.Hour), Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: rateLimitNow.Add(time.Hour)}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.RegisterExecutor(&refusalTestExecutor{provider: "claude"})
			a, b := rateLimitAuth("early", time.Hour), rateLimitAuth("late", 2*time.Hour)
			if test.setup != nil {
				test.setup(a)
			}
			for _, candidate := range []*Auth{a, b} {
				m.auths[candidate.ID] = candidate
				registry.GetGlobalRegistry().RegisterClient(candidate.ID, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
			}
			provider := test.provider
			if provider == "" {
				provider = "claude"
			}
			refusal := providerRefusalFromResult(context.Background(), Result{AuthID: test.refusedID, Provider: provider, Model: model, Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
			refusal.headers = http.Header{"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day_overage_included"}}
			got := m.ClaudeRateLimitHeaders(context.Background(), []string{"claude", "antigravity"}, model, cliproxyexecutor.Options{}, &ProviderRefusalError{refusal: refusal}, rateLimitNow)
			if refusal.headers.Get("Anthropic-Ratelimit-Unified-Representative-Claim") != "seven_day_overage_included" {
				t.Fatal("mutated stored upstream claim")
			}
			if test.claim == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected headers: %v", got)
				}
			} else if got.Get("Anthropic-Ratelimit-Unified-Representative-Claim") != test.claim {
				t.Fatalf("headers=%v, want claim=%s", got, test.claim)
			}
		})
	}
}

func TestClaudeRateLimitBlockRequiresQuotaEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		model   string
		signals map[string]string
		want    bool
	}{
		{name: "generic quota reason is not evidence"},
		{name: "allowed windows", signals: map[string]string{"Status": "allowed", "5h-Status": "allowed", "7d-Status": "allowed"}},
		{name: "unified rejection", signals: map[string]string{"Status": "rejected"}, want: true},
		{name: "session rejection without reset", signals: map[string]string{"5h-Status": "rejected"}, want: true},
		{name: "weekly rejection without reset", signals: map[string]string{"7d-Status": "rejected"}, want: true},
		{name: "session claim alone is not rejection", signals: map[string]string{"Representative-Claim": "five_hour"}},
		{name: "session claim with allowed status", signals: map[string]string{"Status": "allowed", "Representative-Claim": "five_hour"}},
		{name: "session claim with warning status", signals: map[string]string{"Status": "allowed_warning", "Representative-Claim": "five_hour"}},
		{name: "session claim with rejected status", signals: map[string]string{"Status": "rejected", "Representative-Claim": "five_hour"}, want: true},
		{name: "weekly claim alone is not rejection", signals: map[string]string{"Representative-Claim": "seven_day"}},
		{name: "weekly claim with allowed status", signals: map[string]string{"Status": "allowed", "Representative-Claim": "seven_day"}},
		{name: "weekly claim with warning status", signals: map[string]string{"Status": "allowed_warning", "Representative-Claim": "seven_day"}},
		{name: "weekly claim with rejected status", signals: map[string]string{"Status": "rejected", "Representative-Claim": "seven_day"}, want: true},
		{name: "fable rejected window", model: "claude-fable-5-1", signals: map[string]string{"7d_oi-Status": "rejected"}, want: true},
		{name: "fable claim alone is not rejection", model: "claude-fable-5-1", signals: map[string]string{"Representative-Claim": "seven_day_overage_included"}},
		{name: "fable claim with allowed status", model: "claude-fable-5-1", signals: map[string]string{"Status": "allowed", "Representative-Claim": "seven_day_overage_included"}},
		{name: "fable claim with warning status", model: "claude-fable-5-1", signals: map[string]string{"Status": "allowed_warning", "Representative-Claim": "seven_day_overage_included"}},
		{name: "fable claim with rejected status", model: "claude-fable-5-1", signals: map[string]string{"Status": "rejected", "Representative-Claim": "seven_day_overage_included"}, want: true},
		{name: "fable window ignored for sonnet", signals: map[string]string{"7d_oi-Status": "rejected"}},
		{name: "fable claim ignored for sonnet", signals: map[string]string{"Status": "rejected", "Representative-Claim": "seven_day_overage_included"}},
		{name: "unknown representative claim", signals: map[string]string{"Representative-Claim": "unknown"}},
		{name: "expired rejection", signals: map[string]string{"Status": "rejected", "Reset": strconv.FormatInt(rateLimitNow.Add(-time.Minute).Unix(), 10)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := test.model
			if model == "" {
				model = "claude-sonnet-5-5"
			}
			auth := rateLimitAuth("quota-evidence", 0)
			quota := QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: rateLimitNow.Add(time.Hour), ObservedAt: rateLimitNow, Signals: make(map[string]string)}
			for key, value := range test.signals {
				quota.Signals["Anthropic-Ratelimit-Unified-"+key] = value
			}
			auth.ModelStates = map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: quota.NextRecoverAt, Quota: quota}}
			got := claudeRateLimitBlockForAuth(auth, model, "", rateLimitNow)
			if got.quota != test.want || !got.until.Equal(quota.NextRecoverAt) {
				t.Fatalf("block=%+v, want quota=%t", got, test.want)
			}
		})
	}
}
