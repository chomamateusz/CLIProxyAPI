package auth

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type paidPlanActivity int

const (
	paidPlanUnknown paidPlanActivity = iota
	paidPlanActive
	paidPlanInactive
)

type subscriptionSample struct {
	Provider   string
	SampledAt  time.Time
	HTTPStatus int

	claudeStatusKnown     bool
	SubscriptionStatus    string
	SubscriptionCreatedAt time.Time
	OrganizationType      string
	RateLimitTier         string
	BillingType           string
	HasClaudeMax          *bool
	HasClaudePro          *bool

	WillRenew             *bool
	CancelsAt             time.Time
	ExpiresAt             time.Time
	RenewsAt              time.Time
	ActiveUntil           time.Time
	IsActive              *bool
	HasActiveSubscription *bool
	IsDelinquent          *bool
	GracePeriodEnd        time.Time
	CancellationOutcome   string
	PlanType              string
}

func parseSubscriptionSample(provider string, body []byte, sampledAt time.Time) (*subscriptionSample, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, errors.New("invalid subscription payload")
	}
	payload := gjson.ParseBytes(body)
	sample := &subscriptionSample{Provider: provider, SampledAt: sampledAt.UTC(), HTTPStatus: 200}
	switch provider {
	case "claude":
		organization := payload.Get("organization")
		sample.claudeStatusKnown = organization.Get("subscription_status").Type == gjson.String
		sample.SubscriptionStatus = organization.Get("subscription_status").String()
		sample.SubscriptionCreatedAt = subscriptionTime(organization.Get("subscription_created_at"))
		sample.OrganizationType = organization.Get("organization_type").String()
		sample.RateLimitTier = organization.Get("rate_limit_tier").String()
		sample.BillingType = organization.Get("billing_type").String()
		sample.HasClaudeMax = subscriptionBool(payload.Get("account.has_claude_max"))
		sample.HasClaudePro = subscriptionBool(payload.Get("account.has_claude_pro"))
	case "codex":
		sample.WillRenew = subscriptionBool(payload.Get("will_renew"))
		if sample.WillRenew == nil {
			sample.WillRenew = subscriptionBool(payload.Get("last_active_subscription.will_renew"))
		}
		sample.CancelsAt = subscriptionTime(payload.Get("entitlement.cancels_at"))
		sample.ExpiresAt = subscriptionTime(payload.Get("entitlement.expires_at"))
		sample.RenewsAt = subscriptionTime(payload.Get("entitlement.renews_at"))
		sample.ActiveUntil = subscriptionTime(payload.Get("active_until"))
		sample.IsActive = subscriptionBool(payload.Get("is_active"))
		sample.HasActiveSubscription = subscriptionBool(payload.Get("entitlement.has_active_subscription"))
		sample.IsDelinquent = subscriptionBool(payload.Get("is_delinquent"))
		sample.GracePeriodEnd = subscriptionTime(payload.Get("grace_period_end_timestamp"))
		sample.CancellationOutcome = payload.Get("cancellation_outcome").String()
		sample.PlanType = payload.Get("plan_type").String()
	default:
		return nil, fmt.Errorf("provider %s has no subscription endpoint", provider)
	}
	return sample, nil
}

func subscriptionBool(value gjson.Result) *bool {
	if value.Type != gjson.True && value.Type != gjson.False {
		return nil
	}
	flag := value.Bool()
	return &flag
}

func subscriptionTime(value gjson.Result) time.Time {
	switch value.Type {
	case gjson.Number:
		return subscriptionUnixTime(value.Float())
	case gjson.String:
		raw := strings.TrimSpace(value.String())
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed.UTC().Truncate(time.Second)
			}
		}
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
			return subscriptionUnixTime(seconds)
		}
	}
	return time.Time{}
}

func subscriptionUnixTime(value float64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	if value > 1e12 {
		value /= 1000
	}
	return time.Unix(int64(value), 0).UTC()
}

func (s *subscriptionSample) paidPlan() paidPlanActivity {
	switch s.Provider {
	case "claude":
		return s.claudePaidPlan()
	case "codex":
		return bothFlags(s.IsActive, s.HasActiveSubscription)
	default:
		return paidPlanUnknown
	}
}

func (s *subscriptionSample) claudePaidPlan() paidPlanActivity {
	tierUnknown := s.OrganizationType == "" && s.HasClaudeMax == nil && s.HasClaudePro == nil
	switch {
	case !s.claudeStatusKnown && s.claudeExplicitlyNotPaid():
		return paidPlanInactive
	case !s.claudeStatusKnown:
		return paidPlanUnknown
	case s.SubscriptionStatus != "active":
		return paidPlanInactive
	case s.claudePaidTier():
		return paidPlanActive
	case tierUnknown:
		return paidPlanUnknown
	default:
		return paidPlanInactive
	}
}

func (s *subscriptionSample) claudePaidTier() bool {
	return strings.HasPrefix(s.OrganizationType, "claude_max") || strings.HasPrefix(s.OrganizationType, "claude_pro") || strings.HasPrefix(s.OrganizationType, "claude_team") ||
		subscriptionFlag(s.HasClaudeMax) || subscriptionFlag(s.HasClaudePro)
}

func (s *subscriptionSample) claudeExplicitlyNotPaid() bool {
	if s.claudePaidTier() {
		return false
	}
	if s.OrganizationType != "" {
		return true
	}
	return s.HasClaudeMax != nil && s.HasClaudePro != nil
}

func subscriptionFlag(flag *bool) bool {
	return flag != nil && *flag
}

func bothFlags(first, second *bool) paidPlanActivity {
	switch {
	case first != nil && !*first, second != nil && !*second:
		return paidPlanInactive
	case first != nil && second != nil:
		return paidPlanActive
	default:
		return paidPlanUnknown
	}
}

func (s *subscriptionSample) renews() bool {
	return subscriptionFlag(s.WillRenew) && s.CancelsAt.IsZero()
}

func (s *subscriptionSample) codexEnd() (time.Time, bool) {
	if !s.CancelsAt.IsZero() {
		return s.CancelsAt, true
	}
	if s.WillRenew == nil || *s.WillRenew {
		return time.Time{}, false
	}
	if !s.ActiveUntil.IsZero() {
		return s.ActiveUntil, true
	}
	return s.ExpiresAt, !s.ExpiresAt.IsZero()
}

func (s *subscriptionSample) logFields() map[string]any {
	fields := map[string]any{"provider": s.Provider, "sampled_at": FormatFleetEndsAt(s.SampledAt), "http_status": s.HTTPStatus}
	addTime := func(key string, value time.Time) {
		if !value.IsZero() {
			fields[key] = FormatFleetEndsAt(value)
		}
	}
	addFlag := func(key string, value *bool) {
		if value != nil {
			fields[key] = *value
		}
	}
	if s.Provider == "claude" {
		fields["subscription_status"] = s.SubscriptionStatus
		fields["organization_type"] = s.OrganizationType
		fields["rate_limit_tier"] = s.RateLimitTier
		fields["billing_type"] = s.BillingType
		addTime("subscription_created_at", s.SubscriptionCreatedAt)
		addFlag("has_claude_max", s.HasClaudeMax)
		addFlag("has_claude_pro", s.HasClaudePro)
		return fields
	}
	fields["plan_type"] = s.PlanType
	fields["cancellation_outcome"] = s.CancellationOutcome
	addFlag("will_renew", s.WillRenew)
	addFlag("is_active", s.IsActive)
	addFlag("has_active_subscription", s.HasActiveSubscription)
	addFlag("is_delinquent", s.IsDelinquent)
	addTime("cancels_at", s.CancelsAt)
	addTime("expires_at", s.ExpiresAt)
	addTime("renews_at", s.RenewsAt)
	addTime("active_until", s.ActiveUntil)
	addTime("grace_period_end", s.GracePeriodEnd)
	return fields
}
