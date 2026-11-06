package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	ProxyNoticeHeader           = "X-Proxy-Notice"
	ProviderRefusalNotice       = "proxy: all credentials refused; last provider response follows"
	maxProviderRefusalBodyBytes = 64 << 10
	refusedCredentialRetryGuard = time.Minute
)

type providerRefusal struct {
	authID          string
	provider        string
	model           string
	routeModel      string
	status          int
	body            []byte
	headers         http.Header
	credentialScope bool
	at              time.Time
}

func (r *providerRefusal) matches(model, routeModel string) bool {
	return r.credentialScope || r.model == model || routeModel != "" && r.routeModel == routeModel
}

type authRefusals struct {
	byModel map[string]*providerRefusal
	probes  map[string]time.Time
}

type providerRefusals struct {
	mu     sync.Mutex
	byAuth map[string]*authRefusals
}

func (s *providerRefusals) entryLocked(authID string) *authRefusals {
	if s.byAuth == nil {
		s.byAuth = make(map[string]*authRefusals)
	}
	entry := s.byAuth[authID]
	if entry == nil {
		entry = &authRefusals{byModel: make(map[string]*providerRefusal), probes: make(map[string]time.Time)}
		s.byAuth[authID] = entry
	}
	return entry
}

func (s *providerRefusals) record(authID string, refusal *providerRefusal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entryLocked(authID)
	entry.byModel[refusal.model] = refusal
	if refusal.credentialScope {
		clear(entry.probes)
	} else {
		delete(entry.probes, refusal.model)
	}
}

func (s *providerRefusals) forget(authID, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.byAuth[authID]
	if entry == nil {
		return
	}
	delete(entry.probes, model)
	for key, refusal := range entry.byModel {
		if refusal.credentialScope {
			delete(entry.byModel, key)
			clear(entry.probes)
		} else if key == model {
			delete(entry.byModel, key)
		}
	}
	if len(entry.byModel) == 0 && len(entry.probes) == 0 {
		delete(s.byAuth, authID)
	}
}

func (s *providerRefusals) drop(authID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byAuth, authID)
}

func (s *providerRefusals) latest(authID, model, routeModel string) *providerRefusal {
	s.mu.Lock()
	defer s.mu.Unlock()
	var newest *providerRefusal
	if entry := s.byAuth[authID]; entry != nil {
		for _, refusal := range entry.byModel {
			if refusal.matches(model, routeModel) && (newest == nil || refusal.at.After(newest.at)) {
				newest = refusal
			}
		}
	}
	return newest
}

// Without a claim every request arriving during the probe's round trip would
// hit the exhausted credential as well.
func (s *providerRefusals) claimProbe(authID, model string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entryLocked(authID)
	if claimed, ok := entry.probes[model]; ok && now.Sub(claimed) < refusedCredentialRetryGuard {
		return false
	}
	entry.probes[model] = now
	return true
}

func providerRefusalModelKey(result Result) string {
	if model := canonicalModelKey(result.Model); model != "" {
		return model
	}
	return canonicalModelKey(result.RouteModel)
}

func providerRefusalFromResult(ctx context.Context, result Result) *providerRefusal {
	if result.Success || result.Error == nil || shouldSkipCredentialCooldown(result.Error) {
		return nil
	}
	status := statusCodeFromResult(result.Error)
	if status != http.StatusTooManyRequests && (!result.CredentialScope || status < http.StatusBadRequest) {
		return nil
	}
	body := []byte(result.Error.Message)
	if len(body) > maxProviderRefusalBodyBytes {
		body = body[:maxProviderRefusalBodyBytes]
	}
	return &providerRefusal{
		authID:          result.AuthID,
		provider:        result.Provider,
		model:           providerRefusalModelKey(result),
		routeModel:      canonicalModelKey(result.RouteModel),
		status:          status,
		body:            body,
		headers:         providerRefusalHeaders(internallogging.GetResponseHeaders(ctx)),
		credentialScope: result.CredentialScope,
		at:              time.Now(),
	}
}

func providerRefusalHeaders(src http.Header) http.Header {
	dst := make(http.Header)
	for key, values := range src {
		lower := strings.ToLower(key)
		if lower == "content-type" || lower == "retry-after" || strings.HasPrefix(lower, "x-codex-") ||
			strings.HasPrefix(lower, "anthropic-ratelimit-") || strings.HasPrefix(lower, "x-ratelimit-") {
			dst[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
		}
	}
	return dst
}

func (m *Manager) observeProviderResult(ctx context.Context, result Result) {
	if m == nil {
		return
	}
	refusal := providerRefusalFromResult(ctx, result)
	if tracker := refusalTrackerFrom(ctx); tracker != nil && !result.Success {
		tracker.observeFailure(refusal)
	}
	if refusal != nil {
		m.refusals.record(result.AuthID, refusal)
	} else if result.Success {
		m.refusals.forget(result.AuthID, providerRefusalModelKey(result))
	}
}

type refusalTrackerKey struct{}

type refusalTracker struct {
	mu      sync.Mutex
	refused bool
	last    *providerRefusal
}

func withRefusalTracker(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, refusalTrackerKey{}, &refusalTracker{})
}

func refusalTrackerFrom(ctx context.Context) *refusalTracker {
	if ctx == nil {
		return nil
	}
	tracker, _ := ctx.Value(refusalTrackerKey{}).(*refusalTracker)
	return tracker
}

func (t *refusalTracker) observeFailure(refusal *providerRefusal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last = refusal
	t.refused = t.refused || refusal != nil
}

func (t *refusalTracker) snapshot() (refused bool, last *providerRefusal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refused, t.last
}

// The proxy's note travels in ProxyNoticeHeader so the body stays byte-identical
// to what the provider sent.
type ProviderRefusalError struct {
	refusal *providerRefusal
	cause   error
}

func newProviderRefusalError(ctx context.Context, refusal *providerRefusal, cause error) *ProviderRefusalError {
	logEntryWithRequestID(ctx).Warnf("%s (provider=%s, model=%s, status=%d)", ProviderRefusalNotice, refusal.provider, refusal.model, refusal.status)
	return &ProviderRefusalError{refusal: refusal, cause: cause}
}

func (e *ProviderRefusalError) Error() string {
	if len(e.refusal.body) > 0 {
		return string(e.refusal.body)
	}
	return fmt.Sprintf("status %d", e.refusal.status)
}

func (e *ProviderRefusalError) Unwrap() error { return e.cause }

func (e *ProviderRefusalError) StatusCode() int { return e.refusal.status }

func (e *ProviderRefusalError) DirectResponse() bool { return true }

func (e *ProviderRefusalError) IsRequestStop() bool { return true }

func (e *ProviderRefusalError) ResponseBody() []byte {
	return append([]byte(nil), e.refusal.body...)
}

func (e *ProviderRefusalError) ResponseHeaders() http.Header {
	headers := e.refusal.headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set(ProxyNoticeHeader, ProviderRefusalNotice)
	return headers
}

func providerRefusalPassThrough(ctx context.Context, err error) error {
	tracker := refusalTrackerFrom(ctx)
	if err == nil || tracker == nil {
		return err
	}
	_, last := tracker.snapshot()
	var passed *ProviderRefusalError
	if last == nil || errors.As(err, &passed) {
		return err
	}
	return newProviderRefusalError(ctx, last, err)
}

type refusedCandidate struct {
	authID  string
	model   string
	next    time.Time
	started time.Time
	refusal *providerRefusal
}

// Only a credential whose last answer was a refusal is probed single-flight:
// after a success there is no refusal to hand to concurrent requests, and the
// credential has just shown that it serves.
func (m *Manager) mayProbe(candidate *refusedCandidate, now time.Time) bool {
	if now.Sub(candidate.started) < refusedCredentialRetryGuard {
		return false
	}
	return candidate.refusal == nil || m.refusals.claimProbe(candidate.authID, candidate.model, now)
}

func (m *Manager) pickRefusedCredential(ctx context.Context, providers []string, routeModel string, opts cliproxyexecutor.Options, tried map[string]struct{}, pickErr error) (*Auth, ProviderExecutor, string, error) {
	if m == nil || !isAuthUnavailableError(pickErr) || !isQuotaSelector(m.Selector()) {
		return nil, nil, "", pickErr
	}
	if tracker := refusalTrackerFrom(ctx); tracker != nil {
		if refused, _ := tracker.snapshot(); refused {
			return nil, nil, "", pickErr
		}
	}
	now := time.Now()
	soonest, newest := m.refusedCooldownCandidates(ctx, providers, routeModel, opts, tried, now)
	if soonest == nil {
		return nil, nil, "", pickErr
	}
	if !m.mayProbe(soonest, now) {
		if newest == nil {
			return nil, nil, "", pickErr
		}
		return nil, nil, "", newProviderRefusalError(ctx, newest, nil)
	}
	m.mu.Lock()
	current := m.auths[soonest.authID]
	if current == nil {
		m.mu.Unlock()
		return nil, nil, "", pickErr
	}
	current.EnsureIndex()
	selected := current.Clone()
	m.mu.Unlock()
	releaseCooldownForAttempt(selected)
	providerKey := executorKeyFromAuth(selected)
	executor, ok := m.Executor(providerKey)
	if !ok {
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	logEntryWithRequestID(ctx).Infof("all credentials for model %s are cooling down after refusals; trying %s, whose cooldown ends first", routeModel, selected.ID)
	return selected, executor, providerKey, nil
}

func (m *Manager) refusedCooldownCandidates(ctx context.Context, providers []string, routeModel string, opts cliproxyexecutor.Options, tried map[string]struct{}, now time.Time) (soonest *refusedCandidate, newest *providerRefusal) {
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerSet[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	routeKey := canonicalModelKey(routeModel)
	registryRef := registry.GetGlobalRegistry()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled || !eligibility.allows(candidate) {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		providerKey := executorKeyFromAuth(candidate)
		if _, ok := providerSet[canonicalSchedulingProvider(providerKey)]; !ok {
			continue
		}
		if _, ok := m.executorLocked(providerKey); !ok {
			continue
		}
		if routeKey != "" && !m.authSupportsRouteModel(registryRef, candidate, routeModel) {
			continue
		}
		checkModel := m.selectionModelForAuth(candidate, routeModel)
		blocked, reason, next := isAuthBlockedForModel(candidate, checkModel, now)
		if !blocked {
			return nil, nil
		}
		if reason != blockReasonCooldown || !next.After(now) {
			continue
		}
		started, refused := refusalCooldownStart(candidate, checkModel, now)
		if !refused {
			continue
		}
		modelKey := canonicalModelKey(checkModel)
		refusal := m.refusals.latest(candidate.ID, modelKey, routeKey)
		if refusal != nil && (newest == nil || refusal.at.After(newest.at)) {
			newest = refusal
		}
		if soonest == nil || next.Before(soonest.next) || next.Equal(soonest.next) && candidate.ID < soonest.authID {
			soonest = &refusedCandidate{authID: candidate.ID, model: modelKey, next: next, started: started, refusal: refusal}
		}
	}
	return soonest, newest
}

func isRefusalQuota(quota QuotaState, nextRetryAfter, now time.Time) bool {
	return quota.Exceeded && (quota.Reason == "quota" || quota.Reason == "credential_quota") &&
		quota.NextRecoverAt.After(now) && !nextRetryAfter.After(quota.NextRecoverAt)
}

// The start comes from the persisted cooldown state rather than the in-memory
// refusals so the guard also holds for cooldowns restored after a restart.
func refusalCooldownStart(auth *Auth, model string, now time.Time) (time.Time, bool) {
	credentialWide := auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now)
	modelKey := canonicalModelKey(model)
	var started time.Time
	matched, refusalState := false, false
	for stateModel, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		forModel := modelKey != "" && canonicalModelKey(stateModel) == modelKey
		matched = matched || forModel
		refusal := isRefusalQuota(state.Quota, state.NextRetryAfter, now)
		if forModel && !refusal {
			if blocked, _, _ := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now); blocked {
				return time.Time{}, false
			}
		}
		if refusal && (forModel || credentialWide) {
			refusalState = true
			if state.UpdatedAt.After(started) {
				started = state.UpdatedAt
			}
		}
	}
	switch {
	case refusalState:
		return started, true
	case credentialWide:
		return auth.UpdatedAt, true
	case matched:
		return time.Time{}, false
	default:
		return auth.UpdatedAt, isRefusalQuota(auth.Quota, auth.NextRetryAfter, now)
	}
}

func releaseCooldownForAttempt(auth *Auth) {
	auth.Unavailable = false
	auth.NextRetryAfter = time.Time{}
	applyCooldownFields(&auth.Quota, QuotaState{})
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		state.Unavailable = false
		state.NextRetryAfter = time.Time{}
		applyCooldownFields(&state.Quota, QuotaState{})
	}
}
