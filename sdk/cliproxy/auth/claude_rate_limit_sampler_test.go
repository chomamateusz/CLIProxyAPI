package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func sampledRateLimitAuth(t *testing.T, m *Manager, id string, now time.Time, weekly float64, resetIn time.Duration) *Auth {
	t.Helper()
	a := samplerRegister(t, m, &Auth{ID: id, Provider: "claude", Status: StatusActive})
	headers, err := parseQuotaSample("claude", []byte(samplerPayload(now, weekly, resetIn)), now)
	if err != nil {
		t.Fatal(err)
	}
	if !m.applyQuotaSample(context.Background(), a, headers, now) {
		t.Fatal("sample not applied")
	}
	current, _ := m.GetByID(id)
	return current
}

func TestUseOrLoseDemotesSampledWeeklyLimitWithoutRateLimitEvidence(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	m := NewManager(nil, nil, nil)
	limited := sampledRateLimitAuth(t, m, "sampled-limit", now, 100, 2*24*time.Hour)
	room := sampledRateLimitAuth(t, m, "sampled-room", now, 50, 5*24*time.Hour)

	selector := &UseOrLoseSelector{}
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5-1"} {
		if sample := selector.sampleFor(limited, model, now); !sample.sampled || !sample.atLimit {
			t.Fatalf("%s sampled weekly 100%% not weekly-limited: %+v", model, sample)
		}
		picked, err := selector.pick(context.Background(), "claude", model, []*Auth{limited, room}, now)
		if err != nil || picked.ID != room.ID {
			t.Fatalf("%s picked %s, %v; want %s", model, authID(picked), err, room.ID)
		}
	}

	var weekly *QuotaBar
	for _, bar := range QuotaLimitsForAuth(limited).Bars {
		if bar.ID == "weekly_all" {
			weekly = &bar
		}
	}
	if weekly == nil || weekly.Percent == nil || *weekly.Percent != 100 || !weekly.ResetsAt.Equal(now.Add(2*24*time.Hour)) || weekly.Status != "rejected" {
		t.Fatalf("limits bar = %+v", weekly)
	}

	if block := claudeRateLimitBlockForAuth(limited, "claude-opus-5-5", "", now); block.quota || !block.until.IsZero() {
		t.Fatalf("sampled reading counted as rejection: %+v", block)
	}
}

func TestClaudeRateLimitBlockIgnoresSampledCopyOnTransientCooldown(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	m := NewManager(nil, nil, nil)
	a := sampledRateLimitAuth(t, m, "sampled-transient", now, 100, 24*time.Hour)
	if !a.Quota.ObservedAt.Equal(now) || a.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] != "rejected" {
		t.Fatalf("sample was not copied into the credential observation: %+v", a.Quota)
	}
	a.Unavailable = true
	a.NextRetryAfter = now.Add(time.Minute)
	a.LastError = &Error{HTTPStatus: http.StatusServiceUnavailable}
	if block := claudeRateLimitBlockForAuth(a, "claude-opus-5-5", "", now); block.quota || !block.until.Equal(now.Add(time.Minute)) {
		t.Fatalf("block = %+v", block)
	}

	response := http.Header{}
	response.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
	response.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10))
	a.Quota.ObserveResponseHeadersForProvider("claude", response, now.Add(time.Second))
	if block := claudeRateLimitBlockForAuth(a, "claude-opus-5-5", "", now); !block.quota || !block.until.Equal(now.Add(3*time.Hour)) || block.claim != "seven_day" {
		t.Fatalf("provider rejection after the sample ignored: %+v", block)
	}
}
