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

type providerRefusals struct {
	mu     sync.Mutex
	byAuth map[string]map[string]*providerRefusal
}

func (s *providerRefusals) record(authID string, refusal *providerRefusal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAuth == nil {
		s.byAuth = make(map[string]map[string]*providerRefusal)
	}
	models := s.byAuth[authID]
	if models == nil {
		models = make(map[string]*providerRefusal)
		s.byAuth[authID] = models
	}
	models[refusal.model] = refusal
}

func (s *providerRefusals) forget(authID, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	models := s.byAuth[authID]
	for key, refusal := range models {
		if key == model || refusal.credentialScope {
			delete(models, key)
		}
	}
	if len(models) == 0 {
		delete(s.byAuth, authID)
	}
}

func (s *providerRefusals) latest(authID, model, routeModel string) *providerRefusal {
	s.mu.Lock()
	defer s.mu.Unlock()
	var newest *providerRefusal
	for _, refusal := range s.byAuth[authID] {
		if refusal.matches(model, routeModel) && (newest == nil || refusal.at.After(newest.at)) {
			newest = refusal
		}
	}
	return newest
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
	soonestID, soonestModel, newest := m.refusedCooldownCandidates(ctx, providers, routeModel, opts, tried, now)
	if soonestID == "" {
		return nil, nil, "", pickErr
	}
	if started := m.refusals.latest(soonestID, soonestModel, canonicalModelKey(routeModel)); started != nil && now.Sub(started.at) < refusedCredentialRetryGuard {
		if newest == nil || newest.at.Before(started.at) {
			newest = started
		}
		return nil, nil, "", newProviderRefusalError(ctx, newest, nil)
	}
	m.mu.Lock()
	current := m.auths[soonestID]
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

func (m *Manager) refusedCooldownCandidates(ctx context.Context, providers []string, routeModel string, opts cliproxyexecutor.Options, tried map[string]struct{}, now time.Time) (soonestID, soonestModel string, newest *providerRefusal) {
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerSet[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	routeKey := canonicalModelKey(routeModel)
	registryRef := registry.GetGlobalRegistry()
	var soonestNext time.Time
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
			return "", "", nil
		}
		if reason != blockReasonCooldown || !next.After(now) {
			continue
		}
		modelKey := canonicalModelKey(checkModel)
		if refusal := m.refusals.latest(candidate.ID, modelKey, routeKey); refusal != nil && (newest == nil || refusal.at.After(newest.at)) {
			newest = refusal
		}
		if soonestID == "" || next.Before(soonestNext) || next.Equal(soonestNext) && candidate.ID < soonestID {
			soonestID, soonestModel, soonestNext = candidate.ID, modelKey, next
		}
	}
	return soonestID, soonestModel, newest
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
