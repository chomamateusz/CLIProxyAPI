package auth

import (
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
)

func activeQuotaCooldown(quota QuotaState, now time.Time) bool {
	return quota.Exceeded && (quota.Reason == "credential_quota" || quota.Reason == "quota") &&
		(quota.NextRecoverAt.IsZero() || quota.NextRecoverAt.After(now))
}

func quotaRecoveryAllowed(window useOrLoseWindow, refusedAt time.Time) bool {
	return window.observed.After(refusedAt) && window.hasUsage && window.utilisation < 1 && !window.rejected
}

func quotaWindowsRecovered(provider string, sample QuotaState, observations []QuotaState, fable bool, refusedAt time.Time) bool {
	sampled := []QuotaState{sample}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		if fable {
			window, _ := useOrLoseClaudeWindow(sampled, "7d_oi")
			return quotaRecoveryAllowed(window, refusedAt)
		}
		weekly, _ := useOrLoseClaudeWindow(sampled, "7d")
		if !quotaRecoveryAllowed(weekly, refusedAt) {
			return false
		}
		_, reported := useOrLoseClaudeWindow(observations, "5h")
		short, sampledShort := useOrLoseClaudeWindow(sampled, "5h")
		return !reported && !sampledShort || quotaRecoveryAllowed(short, refusedAt)
	case "codex":
		reported := false
		for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
			_, known := freshestQuotaObservation(observations, prefix+"Used-Percent", prefix+"Window-Minutes", prefix+"Reset-At")
			quota, ok := freshestQuotaObservation(sampled, prefix+"Used-Percent", prefix+"Window-Minutes", prefix+"Reset-At")
			if !known && !ok {
				continue
			}
			reported = true
			usage, hasUsage := quotaSignalNumber(quota.Signals[prefix+"Used-Percent"])
			if !quotaRecoveryAllowed(useOrLoseWindow{observed: quota.ObservedAt, utilisation: usage / 100, hasUsage: hasUsage}, refusedAt) {
				return false
			}
		}
		return reported
	}
	return false
}

func propagatedFableQuotaNeedsRecovery(sample QuotaState, observations []QuotaState, refusedAt time.Time) bool {
	if window, reported := useOrLoseClaudeWindow([]QuotaState{sample}, "7d_oi"); reported {
		return !quotaRecoveryAllowed(window, refusedAt)
	}
	for _, observation := range observations {
		if window, reported := useOrLoseClaudeWindow([]QuotaState{observation}, "7d_oi"); reported &&
			(!window.hasUsage || window.utilisation >= 1 || window.rejected) {
			return true
		}
	}
	return false
}

func quotaRecoveryErrorAllowed(err *Error) bool {
	return err == nil || statusCodeFromResult(err) == 429
}

func (m *Manager) quotaRefusedAt(authID, model string, updatedAt time.Time) time.Time {
	if refusal := m.refusals.latest(authID, model, model); refusal != nil && refusal.at.After(updatedAt) {
		return refusal.at
	}
	return updatedAt
}

func (m *Manager) liftObservedQuotaCooldownsLocked(a *Auth, observations []QuotaState, now time.Time) bool {
	if a == nil || a.Disabled || a.Status == StatusDisabled || hasUnauthorizedAuthFailure(a) {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	if provider != "claude" && provider != "codex" {
		return false
	}
	credentialLift := a.Quota.Reason == "credential_quota" && activeQuotaCooldown(a.Quota, now) &&
		quotaWindowsRecovered(provider, a.sampledQuota, observations, false, m.quotaRefusedAt(a.ID, "", a.UpdatedAt))
	models := make([]string, 0)
	for model, state := range a.ModelStates {
		if state == nil {
			continue
		}
		independent := state
		if state.credentialQuotaShadow != nil {
			independent = state.credentialQuotaShadow
		} else if state.Quota.Reason == "credential_quota" {
			independent = nil
		}
		if independent == nil || independent.Status == StatusDisabled || independent.Quota.Reason != "quota" ||
			!activeQuotaCooldown(independent.Quota, now) || !quotaRecoveryErrorAllowed(independent.LastError) {
			continue
		}
		fable := strings.Contains(strings.ToLower(model), "fable")
		if provider == "claude" && !fable {
			continue
		}
		if quotaWindowsRecovered(provider, a.sampledQuota, observations, fable, m.quotaRefusedAt(a.ID, model, independent.UpdatedAt)) {
			models = append(models, model)
		}
	}
	if !credentialLift && len(models) == 0 {
		return false
	}
	if credentialLift {
		applyCooldownFields(&a.Quota, QuotaState{})
		a.Unavailable = false
		a.NextRetryAfter = time.Time{}
		a.cooldownRecordedSeq = 0
		a.LastError = nil
		a.StatusMessage = ""
		for model, state := range a.ModelStates {
			if state == nil || !state.credentialQuotaPropagated && state.Quota.Reason != "credential_quota" {
				continue
			}
			if shadow := state.credentialQuotaShadow; shadow != nil && isModelStateActiveCooldown(shadow, now) {
				*state = *shadow.cloneShallow()
			} else if provider == "claude" && strings.Contains(strings.ToLower(model), "fable") && state.Quota.Reason == "quota" &&
				propagatedFableQuotaNeedsRecovery(a.sampledQuota, observations, m.quotaRefusedAt(a.ID, model, state.UpdatedAt)) {
				state.credentialQuotaPropagated = false
			} else {
				resetModelState(state, now)
			}
		}
		m.refusals.forget(a.ID, "", true)
		log.Infof("quota cooldown lifted: auth=%s scope=credential model= reason=sample_allowed", a.ID)
	}
	for _, model := range models {
		state := a.ModelStates[model]
		if a.Quota.Reason == "credential_quota" && activeQuotaCooldown(a.Quota, now) &&
			(state.credentialQuotaPropagated || state.Quota.Reason == "credential_quota") {
			state.credentialQuotaPropagated = true
			state.credentialQuotaShadow = nil
			state.NextRetryAfter = a.Quota.NextRecoverAt
			applyCooldownFields(&state.Quota, QuotaState{
				Exceeded:      true,
				Reason:        "credential_quota",
				NextRecoverAt: a.Quota.NextRecoverAt,
				BackoffLevel:  a.Quota.BackoffLevel,
			})
		} else {
			resetModelState(state, now)
		}
		m.refusals.forget(a.ID, model, false)
		log.Infof("quota cooldown lifted: auth=%s scope=model model=%s reason=sample_allowed", a.ID, model)
	}
	if a.Quota.Reason == "quota" {
		applyCooldownFields(&a.Quota, QuotaState{})
	}
	updateAggregatedAvailability(a, now)
	if !hasModelError(a, now) {
		a.LastError = nil
		a.StatusMessage = ""
		a.Status = StatusActive
	}
	a.UpdatedAt = now
	return true
}

func (m *Manager) projectQuotaRecovery(snapshot *Auth, now time.Time) {
	models, epoch := registry.GetGlobalRegistry().GetModelsAndEpochForClient(snapshot.ID)
	projections := make([]registry.ClientModelProjection, 0, len(models))
	for _, model := range models {
		if model != nil && strings.TrimSpace(model.ID) != "" {
			projections = append(projections, m.clientModelProjectionForAuth(snapshot, model.ID, now))
		}
	}
	registry.GetGlobalRegistry().ApplyClientModelProjections(snapshot.ID, epoch, snapshot.Generation, projections)
}
