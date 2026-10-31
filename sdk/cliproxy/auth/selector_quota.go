package auth

import (
	"context"
	"sort"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type NearestResetSelector struct{}

type MostUsedSelector struct{}

func (s *NearestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return pickQuotaAuth(ctx, provider, model, auths, false)
}

func (s *MostUsedSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return pickQuotaAuth(ctx, provider, model, auths, true)
}

func isQuotaSelector(selector Selector) bool {
	if affinity, ok := selector.(*SessionAffinitySelector); ok && affinity != nil {
		selector = affinity.fallback
	}
	switch selector.(type) {
	case *NearestResetSelector, *MostUsedSelector:
		return true
	default:
		return false
	}
}

// Plan headers can read 100 % while the provider still serves the account from
// credits, so a reading only orders credentials and never excludes one.
func pickQuotaAuth(ctx context.Context, provider, model string, auths []*Auth, mostUsed bool) (*Auth, error) {
	now := time.Now()
	available, err := getSelectorAvailableAuthsAcrossPriorities(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	samples := make(map[*Auth]quotaSample, len(available))
	eligible := make(map[*Auth]bool, len(available))
	for _, auth := range available {
		eligible[auth] = true
		if sample, ok := quotaSampleFor(auth, model, now); ok {
			samples[auth] = sample
		}
	}
	ordered := make([]*Auth, 0, len(eligible))
	for _, auth := range auths {
		if eligible[auth] {
			ordered = append(ordered, auth)
		}
	}
	if len(ordered) == 0 {
		return nil, newAuthUnavailableError(time.Time{}, now)
	}
	ordered = preferQuotaCapacity(ordered, model, now)
	ordered = highestPriorityAuths(ordered)
	ordered = preferCodexWebsocketAuths(ctx, provider, ordered)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, leftSampled := samples[ordered[i]]
		right, rightSampled := samples[ordered[j]]
		if leftSampled != rightSampled {
			return leftSampled
		}
		if !leftSampled {
			return false
		}
		if mostUsed && !left.atLimit && left.utilisation != right.utilisation {
			return left.utilisation > right.utilisation
		}
		if !left.resetAt.Equal(right.resetAt) {
			return left.resetAt.Before(right.resetAt)
		}
		return left.utilisation > right.utilisation
	})
	return ordered[0], nil
}

func preferQuotaCapacity(auths []*Auth, model string, now time.Time) []*Auth {
	withCapacity := make([]*Auth, 0, len(auths))
	for _, candidate := range auths {
		if sample, ok := quotaSampleFor(candidate, model, now); !ok || !sample.atLimit {
			withCapacity = append(withCapacity, candidate)
		}
	}
	if len(withCapacity) == 0 {
		return auths
	}
	return withCapacity
}
