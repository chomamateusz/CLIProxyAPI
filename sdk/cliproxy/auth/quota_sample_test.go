package auth

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestQuotaSampleClaudeWindowClassification(t *testing.T) {
	now := time.Unix(1787279282, 0)
	for _, tc := range []struct {
		name    string
		signals map[string]string
		limit   bool
		reset   time.Time
	}{
		{"overage only", map[string]string{"Anthropic-Ratelimit-Unified-Status": "rejected", "Anthropic-Ratelimit-Unified-Overage-Status": "rejected", "Anthropic-Ratelimit-Unified-5h-Status": "allowed", "Anthropic-Ratelimit-Unified-7d-Status": "allowed"}, false, now.Add(time.Hour)},
		{"seven day", map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected", "Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10)}, true, now.Add(24 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signals := map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.3", "Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}
			for key, value := range tc.signals {
				signals[key] = value
			}
			sample, ok := quotaSampleFor(&Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: signals}}, "", now)
			if !ok || sample.atLimit != tc.limit || !sample.resetAt.Equal(tc.reset) {
				t.Fatalf("sample = %+v, ok = %v", sample, ok)
			}
		})
	}
}

func TestQuotaSignalResetFormats(t *testing.T) {
	want := time.Unix(1787279282, 500000000)
	for _, raw := range []string{strconv.FormatFloat(float64(want.Unix())+0.5, 'f', 1, 64), want.Format(time.RFC3339Nano), want.UTC().Truncate(time.Second).Format(http.TimeFormat)} {
		got, ok := quotaSignalReset(raw)
		if !ok || !got.Equal(want) && !got.Equal(want.Truncate(time.Second)) {
			t.Fatalf("quotaSignalReset(%q) = %v, %v", raw, got, ok)
		}
	}
}

func TestQuotaSampleFor(t *testing.T) {
	t.Parallel()
	now := time.Unix(1787279282, 0)
	for _, tc := range []struct {
		name        string
		provider    string
		age         time.Duration
		signals     map[string]string
		utilisation float64
		resetIn     time.Duration
		atLimit     bool
		ok          bool
	}{
		{
			name: "codex relative reset", provider: "codex", age: time.Minute,
			signals:     map[string]string{"X-Codex-Primary-Used-Percent": "51", "X-Codex-Primary-Reset-After-Seconds": "180"},
			utilisation: 0.51, resetIn: 2 * time.Minute, ok: true,
		},
		{
			name: "codex absolute reset", provider: "codex",
			signals:     map[string]string{"X-Codex-Primary-Used-Percent": "25.5", "X-Codex-Primary-Reset-At": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
			utilisation: 0.255, resetIn: time.Hour, ok: true,
		},
		{
			name: "codex absolute reset preferred", provider: "codex",
			signals:     map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-At": strconv.FormatInt(now.Add(time.Hour).Unix(), 10), "X-Codex-Primary-Reset-After-Seconds": "180"},
			utilisation: 0.2, resetIn: time.Hour, ok: true,
		},
		{
			name: "codex primary only", provider: "codex",
			signals:     map[string]string{"X-Codex-Active-Limit": "bengalfox", "X-Codex-Bengalfox-Primary-Used-Percent": "100", "X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-After-Seconds": "60"},
			utilisation: 0.2, resetIn: time.Minute, ok: true,
		},
		{
			name: "codex exhausted", provider: "codex",
			signals:     map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "60"},
			utilisation: 1, resetIn: time.Minute, atLimit: true, ok: true,
		},
		{
			name: "claude fraction", provider: "claude",
			signals:     map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.53", "Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10), "Anthropic-Ratelimit-Unified-5h-Status": "allowed_warning"},
			utilisation: 0.53, resetIn: time.Hour, ok: true,
		},
		{
			name: "claude rejected", provider: "claude",
			signals:     map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.9", "Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10), "Anthropic-Ratelimit-Unified-5h-Status": "rejected"},
			utilisation: 0.9, resetIn: time.Hour, atLimit: true, ok: true,
		},
		{
			name: "claude unified rejection and reset fallback", provider: "claude",
			signals:     map[string]string{"Anthropic-Ratelimit-Unified-Status": "rejected", "Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
			utilisation: 1, resetIn: time.Hour, atLimit: true, ok: true,
		},
		{
			name: "claude exhausted", provider: "claude",
			signals:     map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0", "Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
			utilisation: 1, resetIn: time.Hour, atLimit: true, ok: true,
		},
		{name: "missing signals", provider: "codex"},
		{
			name: "missing reset", provider: "codex",
			signals: map[string]string{"X-Codex-Primary-Used-Percent": "20"},
		},
		{
			name: "codex reset window", provider: "codex", age: 2 * time.Minute,
			signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "60"},
		},
		{
			name: "claude reset window", provider: "claude", age: 2 * time.Minute,
			signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected", "Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)},
		},
		{
			name: "reset exactly now", provider: "codex",
			signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "0"},
		},
		{
			name: "old unknown window", provider: "codex", age: 24*time.Hour + time.Second,
			signals: map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-After-Seconds": "60"},
		},
		{
			name: "old known window", provider: "codex", age: time.Hour + time.Second,
			signals: map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-After-Seconds": "7200", "X-Codex-Primary-Window-Minutes": "60"},
		},
		{
			name: "long codex window", provider: "codex", age: 48 * time.Hour,
			signals:     map[string]string{"X-Codex-Primary-Used-Percent": "51", "X-Codex-Primary-Reset-After-Seconds": "176400", "X-Codex-Primary-Window-Minutes": "10080"},
			utilisation: 0.51, resetIn: time.Hour, ok: true,
		},
		{
			name: "old claude window", provider: "claude", age: 5*time.Hour + time.Second,
			signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.53", "Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
		},
		{
			name: "unsupported provider", provider: "gemini",
			signals: map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-After-Seconds": "60"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Provider: tc.provider, Quota: QuotaState{ObservedAt: now.Add(-tc.age), Signals: tc.signals}}
			got, ok := quotaSampleFor(auth, "", now)
			if ok != tc.ok {
				t.Fatalf("sampled = %v, want %v", ok, tc.ok)
			}
			if ok && (got.utilisation != tc.utilisation || !got.resetAt.Equal(now.Add(tc.resetIn)) || got.atLimit != tc.atLimit) {
				t.Fatalf("sample = %+v, want utilisation %v, reset %v, atLimit %v", got, tc.utilisation, now.Add(tc.resetIn), tc.atLimit)
			}
		})
	}
}

func TestQuotaSampleForInvalidSignals(t *testing.T) {
	t.Parallel()
	now := time.Unix(1787279282, 0)
	for _, header := range []string{"X-Codex-Primary-Used-Percent", "X-Codex-Primary-Reset-After-Seconds"} {
		for _, raw := range []string{"", "invalid", "NaN", "+Inf", "-Inf", "-1", "1e1000"} {
			t.Run(header+"/"+raw, func(t *testing.T) {
				auth := &Auth{Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
					"X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-After-Seconds": "60",
				}}}
				auth.Quota.Signals[header] = raw
				if _, ok := quotaSampleFor(auth, "", now); ok {
					t.Fatal("invalid signal was sampled")
				}
			})
		}
	}
	for _, auth := range []*Auth{nil, {Provider: "codex", Quota: QuotaState{Signals: map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-After-Seconds": "60"}}}} {
		if _, ok := quotaSampleFor(auth, "", now); ok {
			t.Fatal("missing observation was sampled")
		}
	}
}

func TestQuotaSampleForModelQuota(t *testing.T) {
	t.Parallel()
	now := time.Unix(1787279282, 0)
	for _, tc := range []struct {
		name  string
		model string
		state *ModelState
		want  float64
		ok    bool
	}{
		{name: "model preferred", model: "model", state: &ModelState{Quota: QuotaState{ObservedAt: now, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "80", "X-Codex-Primary-Reset-After-Seconds": "60"}}}, want: 0.8, ok: true},
		{name: "thinking suffix", model: "model(high)", state: &ModelState{Quota: QuotaState{ObservedAt: now, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "80", "X-Codex-Primary-Reset-After-Seconds": "60"}}}, want: 0.8, ok: true},
		{name: "missing model", model: "other", want: 0.2, ok: true},
		{name: "nil model state", model: "model", want: 0.2, ok: true},
		{name: "model without signals", model: "model", state: &ModelState{}, want: 0.2, ok: true},
		{name: "stale model does not revive credential sample", model: "model", state: &ModelState{Quota: QuotaState{ObservedAt: now.Add(-48 * time.Hour), Signals: map[string]string{"X-Codex-Primary-Used-Percent": "80", "X-Codex-Primary-Reset-After-Seconds": "60"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-After-Seconds": "60"}}, ModelStates: map[string]*ModelState{"model": tc.state}}
			got, ok := quotaSampleFor(auth, tc.model, now)
			if ok != tc.ok || (ok && got.utilisation != tc.want) {
				t.Fatalf("sample = %+v, %v, want utilisation %v, sampled %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
