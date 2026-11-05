package auth

import (
	"context"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const useOrLoseProbeInterval = time.Minute

type UseOrLoseSelector struct {
	mu         sync.Mutex
	probes     map[useOrLoseProbeKey]useOrLoseProbe
	lastPruned time.Time
}

type useOrLoseProbe struct {
	after     time.Time
	seen      time.Time
	attempted time.Time
}

type useOrLoseProbeKey struct {
	provider string
	id       string
	fable    bool
}

type useOrLoseWindow struct {
	reset       time.Time
	utilisation float64
	observed    time.Time
	hasUsage    bool
	rejected    bool
}

func (w useOrLoseWindow) usable(now time.Time) bool {
	return w.hasUsage && w.reset.After(now) && w.observed.Add(7*24*time.Hour).After(now)
}

type useOrLoseSample struct {
	window  useOrLoseWindow
	sampled bool
	capped  bool
	atLimit bool
}

func (s *UseOrLoseSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return s.pick(ctx, provider, model, auths, time.Now())
}

func (s *UseOrLoseSelector) pick(ctx context.Context, provider, model string, auths []*Auth, now time.Time) (*Auth, error) {
	available, err := getSelectorAvailableAuthsAcrossPriorities(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	samples := make(map[*Auth]useOrLoseSample, len(available))
	for _, auth := range available {
		samples[auth] = useOrLoseSampleFor(auth, model, now)
	}
	ordered := make([]*Auth, 0, len(available))
	for _, auth := range auths {
		if _, ok := samples[auth]; ok {
			ordered = append(ordered, auth)
		}
	}
	ordered = preferUseOrLoseRoom(ordered, samples)
	ordered = highestPriorityAuths(ordered)
	ordered = preferCodexWebsocketAuths(ctx, provider, ordered)
	if len(ordered) == 0 {
		return nil, newAuthUnavailableError(time.Time{}, now)
	}
	var earliest time.Time
	for _, auth := range ordered {
		sample := samples[auth]
		if sample.sampled && (earliest.IsZero() || sample.window.reset.Before(earliest)) {
			earliest = sample.window.reset
		}
	}
	selected := ordered[0]
	found := false
	for _, auth := range ordered {
		sample := samples[auth]
		// Anchor the tie group to the earliest reset; pairwise one-hour comparisons are not transitive.
		if !sample.sampled || sample.window.reset.After(earliest.Add(time.Hour)) {
			continue
		}
		best := samples[selected].window
		if !found || sample.window.utilisation < best.utilisation || sample.window.utilisation == best.utilisation && sample.window.reset.Before(best.reset) {
			selected, found = auth, true
		}
	}
	return s.probeUnsampled(model, ordered, samples, selected, now), nil
}

func useOrLoseSampleFor(auth *Auth, model string, now time.Time) useOrLoseSample {
	observations := quotaObservations(auth)
	var weekly, short useOrLoseWindow
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		weekly, _ = useOrLoseClaudeWindow(observations, "7d")
		if strings.Contains(strings.ToLower(model), "fable") {
			if fable, _ := useOrLoseClaudeWindow(observations, "7d_oi"); fable.usable(now) {
				weekly = fable
			}
		}
		short, _ = useOrLoseClaudeWindow(observations, "5h")
	case "codex":
		for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
			quota, ok := freshestQuotaObservation(observations, prefix+"Used-Percent")
			if !ok {
				continue
			}
			minutes, _ := quotaSignalNumber(quota.Signals[prefix+"Window-Minutes"])
			window := useOrLoseWindow{observed: quota.ObservedAt}
			window.utilisation, window.hasUsage = quotaSignalNumber(quota.Signals[prefix+"Used-Percent"])
			window.utilisation /= 100
			window.reset, _ = quotaSignalReset(quota.Signals[prefix+"Reset-At"])
			if window.reset.IsZero() {
				if after, ok := quotaSignalDuration(quota.Signals[prefix+"Reset-After-Seconds"], time.Second); ok {
					window.reset = quota.ObservedAt.Add(after)
				}
			}
			switch minutes {
			case 7 * 24 * 60:
				weekly = window
			case 5 * 60:
				short = window
			}
		}
	}
	shortActive := short.reset.After(now) || short.reset.IsZero() && short.observed.Add(5*time.Hour).After(now)
	return useOrLoseSample{
		window:  weekly,
		sampled: weekly.usable(now),
		capped:  short.hasUsage && short.utilisation >= 0.95 && shortActive,
		atLimit: weekly.reset.After(now) && (weekly.hasUsage && weekly.utilisation >= 1 || weekly.rejected),
	}
}

func useOrLoseClaudeWindow(observations []QuotaState, name string) (useOrLoseWindow, bool) {
	prefix := "Anthropic-Ratelimit-Unified-" + name + "-"
	quota, ok := freshestQuotaObservation(observations, prefix+"Utilization", prefix+"Reset", prefix+"Status")
	window := useOrLoseWindow{observed: quota.ObservedAt}
	window.utilisation, window.hasUsage = quotaSignalNumber(quota.Signals[prefix+"Utilization"])
	window.reset, _ = quotaSignalReset(quota.Signals[prefix+"Reset"])
	window.rejected = strings.EqualFold(strings.TrimSpace(quota.Signals[prefix+"Status"]), "rejected")
	return window, ok
}

func preferUseOrLoseRoom(auths []*Auth, samples map[*Auth]useOrLoseSample) []*Auth {
	room := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if sample := samples[auth]; !sample.capped && !sample.atLimit {
			room = append(room, auth)
		}
	}
	if len(room) != 0 {
		return room
	}
	return auths
}

func (s *UseOrLoseSelector) probeUnsampled(model string, auths []*Auth, samples map[*Auth]useOrLoseSample, selected *Auth, now time.Time) *Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probes == nil {
		s.probes = make(map[useOrLoseProbeKey]useOrLoseProbe)
	}
	keyFor := func(auth *Auth) useOrLoseProbeKey {
		return useOrLoseProbeKey{provider: strings.ToLower(strings.TrimSpace(auth.Provider)), id: auth.ID, fable: strings.Contains(strings.ToLower(model), "fable")}
	}
	var probe *Auth
	var oldest useOrLoseProbe
	for _, auth := range auths {
		key := keyFor(auth)
		if key.provider != "claude" && key.provider != "codex" {
			continue
		}
		if samples[auth].sampled {
			delete(s.probes, key)
			continue
		}
		entry, seen := s.probes[key]
		if !seen {
			entry.after = now.Add(useOrLoseProbeInterval)
		}
		entry.seen = now
		s.probes[key] = entry
		if !entry.after.After(now) && (probe == nil || entry.after.Before(oldest.after) || entry.after.Equal(oldest.after) && entry.attempted.Before(oldest.attempted)) {
			probe, oldest = auth, entry
		}
	}
	if probe != nil {
		selected = probe
	}
	if key := keyFor(selected); !samples[selected].sampled && (key.provider == "claude" || key.provider == "codex") {
		s.probes[key] = useOrLoseProbe{after: now.Add(useOrLoseProbeInterval), seen: now, attempted: now}
	}
	if now.Sub(s.lastPruned) >= useOrLoseProbeInterval {
		for key, entry := range s.probes {
			if entry.seen.Before(now.Add(-10 * time.Minute)) {
				delete(s.probes, key)
			}
		}
		s.lastPruned = now
	}
	return selected
}
