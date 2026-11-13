package auth

import (
	"context"
	"net/http"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func isTransientProviderResult(result Result) bool {
	if canonicalSchedulingProvider(result.Provider) != "claude" || result.Success || result.Error == nil || isRequestScopedResultError(result.Error) || isInvalidGrantResultError(result.Error) {
		return false
	}
	status := statusCodeFromResult(result.Error)
	return status == 0 || status == http.StatusRequestTimeout || status >= 500 && status <= 599
}

func hasTransientCooldown(auth *Auth, model string, now time.Time) bool {
	if auth == nil || hasActiveQuotaRefusal(auth, model, now) {
		return false
	}
	transient := func(lastErr *Error, message string) bool {
		return isTransientProviderResult(Result{Provider: auth.Provider, Error: lastErr}) ||
			canonicalSchedulingProvider(auth.Provider) == "claude" && lastErr == nil && cooldownStatusReason(message) == "transient_error"
	}
	if len(auth.ModelStates) == 0 {
		return transient(auth.LastError, auth.StatusMessage)
	}
	modelKey := canonicalModelKey(model)
	found := false
	for stateModel, state := range auth.ModelStates {
		if state == nil || canonicalModelKey(stateModel) != modelKey {
			continue
		}
		if blocked, _, _ := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now); !blocked {
			continue
		}
		if !transient(state.LastError, state.StatusMessage) {
			return false
		}
		found = true
	}
	return found
}

func transientProviderResultError(ctx context.Context, result Result) error {
	status := statusCodeFromResult(result.Error)
	if status == 0 {
		now := time.Now()
		return newAuthUnavailableError(now.Add(transientErrorCooldown), now)
	}
	return &providerTransientError{
		status:  status,
		body:    []byte(result.Error.Message),
		headers: providerRefusalHeaders(internallogging.GetResponseHeaders(ctx)),
	}
}

type providerTransientError struct {
	status  int
	body    []byte
	headers http.Header
}

func (e *providerTransientError) Error() string { return string(e.body) }

func (e *providerTransientError) StatusCode() int { return e.status }

func (e *providerTransientError) DirectResponse() bool { return true }

func (e *providerTransientError) ResponseBody() []byte {
	return append([]byte(nil), e.body...)
}

func (e *providerTransientError) ResponseHeaders() http.Header { return e.headers.Clone() }
