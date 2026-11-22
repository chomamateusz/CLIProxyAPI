package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/tidwall/gjson"
)

const (
	claudeResetGrantStatusEndpoint = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1"
	resetGrantSampleMaxAge         = 24 * time.Hour
	resetGrantWindowWeekly         = "seven_day"
	resetGrantWindowFable          = "seven_day_overage_included"
	resetGrantWeek                 = 7 * 24 * time.Hour
	resetGrantMaxWeeks             = 52
)

var (
	resetGrantIDPattern    = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	resetGrantKnownWindows = []string{"five_hour", resetGrantWindowWeekly, resetGrantWindowFable}
	errResetGrantPayload   = errors.New("invalid reset grant payload")
	errResetGrantNoBlock   = errors.New("no reset grant block")
)

type resetGrant struct {
	resetsLeft int
	startsAt   time.Time
	endsAt     time.Time
	clears     []string
	paused     bool
}

type resetGrantSample struct {
	SampledAt time.Time
	grants    []resetGrant
}

type resetGrantUse struct {
	count     int
	expiresAt time.Time
}

type FleetResetGrants struct {
	Weekly    int     `json:"weekly"`
	Total     int     `json:"total"`
	ExpiresAt *string `json:"expires_at"`
	SampledAt string  `json:"sampled_at"`
}

func (g resetGrant) usable(now time.Time) bool {
	return g.resetsLeft > 0 && !g.paused && !g.startsAt.After(now) && (g.endsAt.IsZero() || g.endsAt.After(now))
}

func (g resetGrant) clearsWindow(window string) bool {
	for _, cleared := range g.clears {
		if cleared == window {
			return true
		}
	}
	return false
}

func (s *resetGrantSample) fresh(now time.Time) bool {
	return s != nil && now.Sub(s.SampledAt) <= resetGrantSampleMaxAge
}

func (s *resetGrantSample) use(window string, now time.Time) (resetGrantUse, bool) {
	if !s.fresh(now) {
		return resetGrantUse{}, false
	}
	var use resetGrantUse
	for _, grant := range s.grants {
		if !grant.usable(now) || window != "" && !grant.clearsWindow(window) {
			continue
		}
		use.count += grant.resetsLeft
		if !grant.endsAt.IsZero() && (use.expiresAt.IsZero() || grant.endsAt.Before(use.expiresAt)) {
			use.expiresAt = grant.endsAt
		}
	}
	return use, true
}

func FleetResetGrantsFor(auth *Auth, now time.Time) *FleetResetGrants {
	if auth == nil {
		return nil
	}
	weekly, ok := auth.resetGrants.use(resetGrantWindowWeekly, now)
	if !ok {
		return nil
	}
	total, _ := auth.resetGrants.use("", now)
	view := &FleetResetGrants{Weekly: weekly.count, Total: total.count, SampledAt: FormatFleetEndsAt(auth.resetGrants.SampledAt)}
	if !weekly.expiresAt.IsZero() {
		expiresAt := FormatFleetEndsAt(weekly.expiresAt)
		view.ExpiresAt = &expiresAt
	}
	return view
}

func parseResetGrantSample(body []byte, sampledAt time.Time) (*resetGrantSample, error) {
	if !gjson.ValidBytes(body) {
		return nil, errResetGrantPayload
	}
	block := gjson.GetBytes(body, "cedar_ember")
	if !block.Exists() || block.Type == gjson.Null {
		return nil, errResetGrantNoBlock
	}
	if !block.IsObject() || !isJSONBool(block.Get("eligible")) {
		return nil, errResetGrantPayload
	}
	for _, key := range []string{"weekly_resets_at", "cooldown_until"} {
		if _, ok := resetGrantTime(block.Get(key)); !ok {
			return nil, errResetGrantPayload
		}
	}
	if !optionalJSONBool(block.Get("at_limit")) || !optionalJSONString(block.Get("ineligible_reason")) {
		return nil, errResetGrantPayload
	}
	rawGrants := block.Get("grants")
	if rawGrants.Exists() && rawGrants.Type != gjson.Null && !rawGrants.IsArray() {
		return nil, errResetGrantPayload
	}
	sample := &resetGrantSample{SampledAt: sampledAt.UTC()}
	seen := make(map[string]bool)
	for _, raw := range rawGrants.Array() {
		grant, id, ok := parseResetGrant(raw)
		if !ok || seen[id] {
			return nil, errResetGrantPayload
		}
		seen[id] = true
		sample.grants = append(sample.grants, grant)
	}
	return sample, nil
}

func parseResetGrant(raw gjson.Result) (resetGrant, string, bool) {
	if !raw.IsObject() {
		return resetGrant{}, "", false
	}
	id := raw.Get("id")
	total, totalOK := resetGrantCount(raw.Get("resets_total"))
	left, leftOK := resetGrantCount(raw.Get("resets_left"))
	if id.Type != gjson.String || !resetGrantIDPattern.MatchString(id.String()) || !totalOK || !leftOK || left > total {
		return resetGrant{}, "", false
	}
	startsAt, startsOK := resetGrantTime(raw.Get("starts_at"))
	endsAt, endsOK := resetGrantTime(raw.Get("ends_at"))
	clears, clearsOK := resetGrantWindows(raw.Get("clears"))
	if !startsOK || !endsOK || !clearsOK {
		return resetGrant{}, "", false
	}
	for _, key := range []string{"paused", "usable_now", "use_requires_limit", "gives_back_weekly"} {
		if !optionalJSONBool(raw.Get(key)) {
			return resetGrant{}, "", false
		}
	}
	return resetGrant{resetsLeft: left, startsAt: startsAt, endsAt: endsAt, clears: clears, paused: raw.Get("paused").Type == gjson.True}, id.String(), true
}

func resetGrantCount(value gjson.Result) (int, bool) {
	if value.Type != gjson.Number {
		return 0, false
	}
	number := value.Float()
	if number < 0 || number != float64(int64(number)) || number > 1<<31 {
		return 0, false
	}
	return int(number), true
}

func resetGrantTime(value gjson.Result) (time.Time, bool) {
	if !value.Exists() || value.Type == gjson.Null {
		return time.Time{}, true
	}
	if value.Type != gjson.String {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(value.String())
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func resetGrantWindows(value gjson.Result) ([]string, bool) {
	if !value.Exists() || value.Type == gjson.Null {
		return nil, true
	}
	if !value.IsArray() {
		return nil, false
	}
	listed := make(map[string]bool)
	for _, item := range value.Array() {
		if item.Type == gjson.String {
			listed[item.String()] = true
		}
	}
	var windows []string
	for _, window := range resetGrantKnownWindows {
		if listed[window] {
			windows = append(windows, window)
		}
	}
	return windows, true
}

func isJSONBool(value gjson.Result) bool {
	return value.Type == gjson.True || value.Type == gjson.False
}

func optionalJSONBool(value gjson.Result) bool {
	return !value.Exists() || value.Type == gjson.Null || isJSONBool(value)
}

func optionalJSONString(value gjson.Result) bool {
	return !value.Exists() || value.Type == gjson.Null || value.Type == gjson.String
}

func resetGrantSampleEligible(a *Auth, now time.Time) bool {
	return subscriptionSampleEligible(a) && strings.EqualFold(strings.TrimSpace(a.Provider), "claude") && a.AuthKind() == AuthKindOAuth && !FleetCredentialEnded(a, now)
}

func (m *Manager) fetchResetGrantSample(ctx context.Context, a *Auth) (*resetGrantSample, error) {
	ctx, cancel := context.WithTimeout(ctx, subscriptionSampleTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeResetGrantStatusEndpoint, nil)
	if err != nil {
		return nil, errors.New("request construction")
	}
	req.Header.Set("Authorization", "Bearer "+authAccessToken(a))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", constant.ClaudeUserAgent)
	req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	client, closeIdle, err := m.samplerHTTPClient(a)
	if err != nil {
		return nil, err
	}
	defer closeIdle()
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("transport failure")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, errors.New("invalid response body")
	}
	return parseResetGrantSample(body, time.Now())
}

func newerResetGrantSample(existing, incoming *resetGrantSample) *resetGrantSample {
	if incoming == nil || existing != nil && existing.SampledAt.After(incoming.SampledAt) {
		return existing
	}
	return incoming
}

func (m *Manager) applyResetGrantSample(ctx context.Context, base *Auth, sample *resetGrantSample) {
	release, err := m.lockAuthMutationContext(ctx, base.ID)
	if err != nil {
		return
	}
	defer release()
	m.mu.Lock()
	a := m.auths[base.ID]
	if a == nil || a.RegistrationEpoch != base.RegistrationEpoch || quotaAccountChanged(base, a) || newerResetGrantSample(a.resetGrants, sample) != sample {
		m.mu.Unlock()
		return
	}
	a.resetGrants = sample
	a.Generation++
	snapshot := a.Clone()
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
}

func (s *UseOrLoseSelector) drainsEndingResets() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.DrainEndingResets
}

func endingResetBinding(auth *Auth, endsAt time.Time, fable bool, now time.Time) time.Time {
	window := resetGrantWindowWeekly
	if fable {
		window = resetGrantWindowFable
	}
	use, ok := auth.resetGrants.use(window, now)
	if !ok || use.count == 0 {
		return endsAt
	}
	binding := endsAt.Add(-time.Duration(min(use.count, resetGrantMaxWeeks)) * resetGrantWeek)
	if !use.expiresAt.IsZero() && use.expiresAt.Before(binding) {
		binding = use.expiresAt
	}
	return binding
}
