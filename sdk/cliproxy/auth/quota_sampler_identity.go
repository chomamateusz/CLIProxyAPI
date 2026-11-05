package auth

import (
	"strings"
	"time"
)

func quotaAccountChanged(existing, incoming *Auth) bool {
	if !strings.EqualFold(strings.TrimSpace(existing.Provider), strings.TrimSpace(incoming.Provider)) {
		return true
	}
	for _, key := range []string{"account_uuid", "organization_uuid", "account_id", "organization_id", "email"} {
		before, after := authMetadataString(existing, key), authMetadataString(incoming, key)
		if before != "" && after != "" && !strings.EqualFold(before, after) {
			return true
		}
	}
	return false
}

func clearQuotaObservations(a *Auth) {
	a.Quota.ObservedAt = time.Time{}
	a.Quota.Signals = nil
	a.ModelStates = cloneModelStates(a.ModelStates)
	for _, state := range a.ModelStates {
		if state != nil {
			state.Quota.ObservedAt = time.Time{}
			state.Quota.Signals = nil
		}
	}
}
