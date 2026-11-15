package auth

import (
	"errors"
	"time"
)

var ErrNoBillingAnchor = errors.New("no_billing_anchor")
var ErrNoPeriodEnd = errors.New("next_period needs an end set by hand or by the billing anniversary")

func setFleetEnd(metadata map[string]any, endsAt time.Time, source string) {
	metadata[MetadataFleetEndsAt] = FormatFleetEndsAt(endsAt)
	metadata[MetadataFleetEndsAtSource] = source
}

func ClearFleetEnd(metadata map[string]any) {
	delete(metadata, MetadataFleetEndsAt)
	delete(metadata, MetadataFleetEndsAtSource)
	delete(metadata, MetadataFleetCancelledAt)
}

func SetManualFleetEnd(metadata map[string]any, endsAt time.Time) {
	setFleetEnd(metadata, endsAt, FleetEndSourceManual)
}

func CancelAtBillingAnniversary(metadata map[string]any, anchor, now time.Time) error {
	if anchor.IsZero() {
		return ErrNoBillingAnchor
	}
	setFleetEnd(metadata, NextBillingAnniversary(anchor, now), FleetEndSourceClaudeAnniversary)
	metadata[MetadataFleetCancelledAt] = FormatFleetEndsAt(now)
	return nil
}

func FleetEndNeedsBillingAnchor(metadata map[string]any) bool {
	_, ok := fleetMetadataTime(metadata, MetadataFleetEndsAt)
	return ok && fleetEndSource(metadata) == FleetEndSourceClaudeAnniversary
}

func MoveFleetEndToNextPeriod(metadata map[string]any, anchor time.Time) error {
	endsAt, ok := fleetMetadataTime(metadata, MetadataFleetEndsAt)
	if !ok {
		return ErrNoPeriodEnd
	}
	switch source := fleetEndSource(metadata); source {
	case FleetEndSourceManual:
		setFleetEnd(metadata, oneMonthLater(endsAt), source)
	case FleetEndSourceClaudeAnniversary:
		if anchor.IsZero() {
			return ErrNoBillingAnchor
		}
		setFleetEnd(metadata, NextBillingAnniversary(anchor, endsAt), source)
	default:
		return ErrNoPeriodEnd
	}
	return nil
}

func reconcileFleetEnd(metadata map[string]any, sample *subscriptionSample) (string, bool) {
	switch sample.paidPlan() {
	case paidPlanInactive:
		return endForInactivePlan(metadata, sample.SampledAt)
	case paidPlanActive:
		if planEndedEndSet(metadata) {
			ClearFleetEnd(metadata)
			if reason, changed := reconcileProviderEnd(metadata, sample); changed {
				return "paid plan active again; " + reason, true
			}
			return "paid plan active again", true
		}
	}
	return reconcileProviderEnd(metadata, sample)
}

func planEndedEndSet(metadata map[string]any) bool {
	_, ok := fleetMetadataTime(metadata, MetadataFleetEndsAt)
	return ok && fleetEndSource(metadata) == FleetEndSourcePlanEnded
}

func reconcileProviderEnd(metadata map[string]any, sample *subscriptionSample) (string, bool) {
	switch sample.Provider {
	case "codex":
		return reconcileCodexEnd(metadata, sample)
	case "claude":
		return reconcileClaudeAnniversary(metadata, sample.SubscriptionCreatedAt)
	}
	return "", false
}

func endForInactivePlan(metadata map[string]any, sampledAt time.Time) (string, bool) {
	if endsAt, ok := fleetMetadataTime(metadata, MetadataFleetEndsAt); ok && !endsAt.After(sampledAt) {
		if providerConfirmsFleetEnd(fleetEndSource(metadata)) {
			return "", false
		}
		metadata[MetadataFleetEndsAtSource] = FleetEndSourcePlanEnded
		return "no active paid plan", true
	}
	setFleetEnd(metadata, sampledAt, FleetEndSourcePlanEnded)
	return "no active paid plan", true
}

func reconcileCodexEnd(metadata map[string]any, sample *subscriptionSample) (string, bool) {
	currentEnd, hasEnd := fleetMetadataTime(metadata, MetadataFleetEndsAt)
	source := fleetEndSource(metadata)
	if hasEnd && source != FleetEndSourceCodexAPI {
		return "", false
	}
	if providerEnd, ok := sample.codexEnd(); ok {
		if hasEnd && currentEnd.Equal(providerEnd) {
			return "", false
		}
		setFleetEnd(metadata, providerEnd, FleetEndSourceCodexAPI)
		return "subscription does not renew", true
	}
	if hasEnd && sample.renews() {
		ClearFleetEnd(metadata)
		return "subscription renews", true
	}
	return "", false
}

func reconcileClaudeAnniversary(metadata map[string]any, anchor time.Time) (string, bool) {
	currentEnd, hasEnd := fleetMetadataTime(metadata, MetadataFleetEndsAt)
	if anchor.IsZero() || !hasEnd || fleetEndSource(metadata) != FleetEndSourceClaudeAnniversary {
		return "", false
	}
	if nearestBillingAnniversary(anchor, currentEnd).Equal(currentEnd) {
		return "", false
	}
	setFleetEnd(metadata, recomputedAnniversaryEnd(metadata, anchor, currentEnd), FleetEndSourceClaudeAnniversary)
	return "billing anniversary changed", true
}

func recomputedAnniversaryEnd(metadata map[string]any, anchor, currentEnd time.Time) time.Time {
	cancelledAt, ok := fleetMetadataTime(metadata, MetadataFleetCancelledAt)
	if !ok {
		if nearest := nearestBillingAnniversary(anchor, currentEnd); nearest.After(anchor) {
			return nearest
		}
		return NextBillingAnniversary(anchor, anchor)
	}
	floor := cancelledAt
	if anchor.After(floor) {
		floor = anchor
	}
	end := NextBillingAnniversary(anchor, floor)
	for range periodMovesSinceCancel(currentEnd, cancelledAt) {
		end = NextBillingAnniversary(anchor, end)
	}
	return end
}

func periodMovesSinceCancel(currentEnd, cancelledAt time.Time) int {
	moves := 0
	for billingAnniversary(currentEnd, -(moves + 1)).After(cancelledAt) {
		moves++
	}
	return moves
}
