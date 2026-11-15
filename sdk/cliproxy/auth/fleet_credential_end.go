package auth

import (
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

const (
	MetadataFleetEndsAt       = "fleet_ends_at"
	MetadataFleetEndsAtSource = "fleet_ends_at_source"
	MetadataFleetCancelledAt  = "fleet_cancelled_at"
)

const (
	FleetEndSourceManual            = "manual"
	FleetEndSourceClaudeAnniversary = "claude-anniversary"
	FleetEndSourceCodexAPI          = "codex-api"
	FleetEndSourcePlanEnded         = "plan-ended"
)

type FleetEndState string

const (
	FleetEndStateNone      FleetEndState = "none"
	FleetEndStateScheduled FleetEndState = "scheduled"
	FleetEndStateOverdue   FleetEndState = "overdue"
	FleetEndStateEnded     FleetEndState = "ended"
)

const (
	FleetEndWarningProviderRenews = "provider_renews"
	FleetEndWarningGracePeriod    = "grace_period"
)

const fleetCredentialEndedReason = registry.SuspendReasonCredentialEnded

type FleetEndWarning struct {
	Code  string  `json:"code"`
	Until *string `json:"until,omitempty"`
}

type FleetCredentialEnd struct {
	State         FleetEndState
	EndsAt        time.Time
	Source        string
	CancelledAt   time.Time
	Warnings      []FleetEndWarning
	BillingAnchor time.Time
	CheckedAt     time.Time
}

func ParseFleetEndsAt(raw string) (time.Time, error) {
	endsAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp or null", MetadataFleetEndsAt)
	}
	return endsAt.UTC().Truncate(time.Second), nil
}

func FormatFleetEndsAt(endsAt time.Time) string {
	return endsAt.UTC().Format(time.RFC3339)
}

func fleetMetadataTime(metadata map[string]any, key string) (time.Time, bool) {
	raw, _ := metadata[key].(string)
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	parsed, err := ParseFleetEndsAt(raw)
	return parsed, err == nil
}

func FleetCredentialEndsAt(auth *Auth) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	return fleetMetadataTime(auth.Metadata, MetadataFleetEndsAt)
}

func fleetEndSource(metadata map[string]any) string {
	switch source, _ := metadata[MetadataFleetEndsAtSource].(string); source {
	case FleetEndSourceClaudeAnniversary, FleetEndSourceCodexAPI, FleetEndSourcePlanEnded:
		return source
	default:
		return FleetEndSourceManual
	}
}

func providerConfirmsFleetEnd(source string) bool {
	return source == FleetEndSourceCodexAPI || source == FleetEndSourcePlanEnded
}

func fleetEndState(metadata map[string]any, sample *subscriptionSample, now time.Time) (FleetEndState, time.Time) {
	endsAt, ok := fleetMetadataTime(metadata, MetadataFleetEndsAt)
	switch {
	case !ok:
		return FleetEndStateNone, time.Time{}
	case now.Before(endsAt):
		return FleetEndStateScheduled, endsAt
	case providerConfirmsFleetEnd(fleetEndSource(metadata)):
		return FleetEndStateEnded, endsAt
	case sample != nil && !sample.SampledAt.Before(endsAt) && sample.paidPlan() == paidPlanInactive:
		return FleetEndStateEnded, endsAt
	default:
		return FleetEndStateOverdue, endsAt
	}
}

func FleetCredentialEndFor(auth *Auth, now time.Time) FleetCredentialEnd {
	if auth == nil {
		return FleetCredentialEnd{State: FleetEndStateNone}
	}
	return fleetCredentialEnd(auth.Metadata, auth.subscription, now)
}

func FleetCredentialEndFromMetadata(metadata map[string]any, now time.Time) FleetCredentialEnd {
	return fleetCredentialEnd(metadata, nil, now)
}

func fleetCredentialEnd(metadata map[string]any, sample *subscriptionSample, now time.Time) FleetCredentialEnd {
	end := FleetCredentialEnd{}
	end.State, end.EndsAt = fleetEndState(metadata, sample, now)
	if end.State != FleetEndStateNone {
		end.Source = fleetEndSource(metadata)
		end.CancelledAt, _ = fleetMetadataTime(metadata, MetadataFleetCancelledAt)
	}
	if sample == nil {
		return end
	}
	end.CheckedAt = sample.SampledAt
	end.BillingAnchor = sample.SubscriptionCreatedAt
	if end.Source == FleetEndSourceManual && sample.renews() {
		end.Warnings = append(end.Warnings, FleetEndWarning{Code: FleetEndWarningProviderRenews})
	}
	if !sample.GracePeriodEnd.IsZero() {
		until := FormatFleetEndsAt(sample.GracePeriodEnd)
		end.Warnings = append(end.Warnings, FleetEndWarning{Code: FleetEndWarningGracePeriod, Until: &until})
	}
	return end
}

func FleetCredentialEnded(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	state, _ := fleetEndState(auth.Metadata, auth.subscription, now)
	return state == FleetEndStateEnded
}

func nextFleetCredentialEnd(auths []*Auth, now time.Time) time.Time {
	var next time.Time
	for _, a := range auths {
		endsAt, ok := FleetCredentialEndsAt(a)
		if ok && endsAt.After(now) && (next.IsZero() || endsAt.Before(next)) {
			next = endsAt
		}
	}
	return next
}

func (s *UseOrLoseSelector) drainsEndingCredentials() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.DrainEndingCredentials
}

func (s *UseOrLoseSelector) drainEndingCredential(auth *Auth, sample useOrLoseSample) useOrLoseSample {
	if !s.drainsEndingCredentials() {
		return sample
	}
	endsAt, ok := FleetCredentialEndsAt(auth)
	if ok && (sample.window.reset.IsZero() || endsAt.Before(sample.window.reset)) {
		sample.window.reset = endsAt
	}
	return sample
}
