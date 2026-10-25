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

func pickQuotaAuth(ctx context.Context, provider, model string, auths []*Auth, mostUsed bool) (*Auth, error) {
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	samples := make(map[*Auth]quotaSample, len(available))
	eligible := make(map[*Auth]bool, len(available))
	var earliest time.Time
	for _, auth := range available {
		sample, ok := quotaSampleFor(auth, model, now)
		if ok && sample.atLimit {
			if earliest.IsZero() || sample.resetAt.Before(earliest) {
				earliest = sample.resetAt
			}
			continue
		}
		eligible[auth] = true
		if ok {
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
		return nil, newAuthUnavailableError(earliest, now)
	}
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
		if mostUsed && left.utilisation != right.utilisation {
			return left.utilisation > right.utilisation
		}
		if !left.resetAt.Equal(right.resetAt) {
			return left.resetAt.Before(right.resetAt)
		}
		return left.utilisation > right.utilisation
	})
	return ordered[0], nil
}
