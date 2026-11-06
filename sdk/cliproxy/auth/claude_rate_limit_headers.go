package auth

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type claudeRateLimitBlock struct {
	until time.Time
	claim string
	quota bool
}

func claudeRateLimitBlockForAuth(auth *Auth, model, upstreamClaim string, now time.Time) claudeRateLimitBlock {
	var block claudeRateLimitBlock
	for _, cooldown := range CooldownSnapshotForAuth(auth, now) {
		if cooldown.Scope == "credential" || cooldown.ModelKey == canonicalModelKey(model) {
			quota := auth.Quota
			if cooldown.Scope == "model" {
				if state := existingModelState(auth, cooldown.ModelKey); state != nil {
					quota = state.Quota
				}
			}
			if cooldown.Reason == "credential_quota" || claudeQuotaRejectedForModel(quota, model, now) {
				block.quota = true
			}
			if cooldown.RetryAt.After(block.until) {
				block.until = cooldown.RetryAt
			}
		}
	}
	var windowEnd time.Time
	hasSession := false
	for _, bar := range QuotaLimitsForAuth(auth).Bars {
		hasSession = hasSession || bar.ID == "session_5h"
		if bar.ID == "weekly_fable" && !strings.Contains(strings.ToLower(model), "fable") {
			continue
		}
		if bar.Status != "rejected" || !bar.ResetsAt.After(now) {
			continue
		}
		block.quota = true
		if bar.ResetsAt.After(block.until) {
			block.until = bar.ResetsAt
		}
		if bar.ResetsAt.After(windowEnd) {
			windowEnd = bar.ResetsAt
			switch bar.ID {
			case "session_5h":
				block.claim = "five_hour"
			case "weekly_all":
				block.claim = "seven_day"
			case "weekly_fable":
				block.claim = "seven_day_overage_included"
			}
		}
	}
	if block.claim == "" {
		switch {
		case upstreamClaim != "":
			block.claim = upstreamClaim
		case hasSession:
			block.claim = "five_hour"
		default:
			block.claim = "seven_day"
		}
	}
	return block
}

func claudeQuotaRejectedForModel(quota QuotaState, model string, now time.Time) bool {
	signals := quota.Signals
	claim := strings.ToLower(strings.TrimSpace(signals["Anthropic-Ratelimit-Unified-Representative-Claim"]))
	fable := strings.Contains(strings.ToLower(model), "fable")
	active := func(prefix string) bool {
		reset, ok := quotaSignalReset(signals[prefix+"Reset"])
		return !ok || reset.After(now)
	}
	for _, window := range []string{"5h-", "7d-", "7d_oi-", ""} {
		if !fable && (window == "7d_oi-" || window == "" && claim == "seven_day_overage_included") {
			continue
		}
		prefix := "Anthropic-Ratelimit-Unified-" + window
		if strings.EqualFold(strings.TrimSpace(signals[prefix+"Status"]), "rejected") && active(prefix) {
			return true
		}
	}
	return false
}

func (m *Manager) ClaudeRateLimitHeaders(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, err error, now time.Time) http.Header {
	if m == nil {
		return nil
	}
	providers = m.normalizeProviders(providers)
	if !slices.Contains(providers, "claude") {
		return nil
	}
	var refusal *ProviderRefusalError
	if errors.As(err, &refusal) && refusal.refusal.provider != "claude" {
		return nil
	}
	model = authSelectionModelFromOptions(opts, model)
	if !access.KeyPolicyFromContext(ctx).AllowsModel(model) {
		return nil
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinned := pinnedAuthIDFromMetadata(opts.Metadata)
	registryRef := registry.GetGlobalRegistry()
	var first claudeRateLimitBlock
	var firstID string
	m.mu.RLock()
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled || candidate.Status == StatusDisabled || !eligibility.allows(candidate) {
			continue
		}
		if pinned != "" && candidate.ID != pinned {
			continue
		}
		provider := executorKeyFromAuth(candidate)
		if !slices.Contains(providers, provider) || refusal != nil && provider != "claude" {
			continue
		}
		if _, ok := m.executorLocked(provider); !ok {
			continue
		}
		if !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		models, _, _, _ := m.unrotatedExecutionModelCandidatesWithAlias(candidate, model)
		if len(filterPolicyExecutionModels(ctx, requestedModelAliasFromOptions(opts, model), models)) == 0 {
			continue
		}
		checkModel := m.selectionModelForAuth(candidate, model)
		if blocked, _, next := isAuthBlockedForModel(candidate, checkModel, now); blocked && !next.After(now) {
			continue
		}
		if provider != "claude" {
			m.mu.RUnlock()
			return nil
		}
		upstreamClaim := ""
		if refusal != nil && refusal.refusal.authID == candidate.ID {
			upstreamClaim = refusal.refusal.headers.Get("Anthropic-Ratelimit-Unified-Representative-Claim")
		}
		block := claudeRateLimitBlockForAuth(candidate, checkModel, upstreamClaim, now)
		if block.until.IsZero() {
			m.mu.RUnlock()
			return nil
		}
		if firstID == "" || block.until.Before(first.until) || block.until.Equal(first.until) && candidate.ID < firstID {
			first, firstID = block, candidate.ID
		}
	}
	m.mu.RUnlock()
	if firstID == "" || !first.quota {
		return nil
	}
	reset := first.until.Unix()
	if first.until.Nanosecond() != 0 {
		reset++
	}
	resetAt := time.Unix(reset, 0).UTC()
	headers := make(http.Header)
	headers.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	headers.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(reset, 10))
	headers.Set("Anthropic-Ratelimit-Unified-Representative-Claim", first.claim)
	headers.Set("Retry-After", strconv.FormatInt(max(1, int64(math.Ceil(resetAt.Sub(now).Seconds()))), 10))
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	label := FleetLimitsLabel(FleetLimitsLabelKey(cfg), "claude", firstID)
	logEntryWithRequestID(ctx).Infof("rate-limit headers: model=%s reset=%s claim=%s from=%s", model, resetAt.Format(time.RFC3339), first.claim, label)
	return headers
}
