package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const oauthOrganizationRefusal = "oauth authentication is currently not allowed for this organization"
const oauthOrganizationDisabledMessage = "disabled: upstream refused OAuth for this organization"

type CredentialHealthEvent struct {
	Timestamp        time.Time
	AuthID           string
	AuthIndex        string
	Provider         string
	Account          string
	AuthFile         string
	StatusMessage    string
	HTTPStatus       int
	PersistenceError error
}

type credentialHealthHookHolder struct {
	hook func(context.Context, CredentialHealthEvent)
}

func (m *Manager) SetCredentialHealthHook(hook func(context.Context, CredentialHealthEvent)) {
	if hook == nil {
		m.credentialHealthHook.Store(nil)
		return
	}
	m.credentialHealthHook.Store(&credentialHealthHookHolder{hook: hook})
}

func isOAuthOrganizationRefusal(auth *Auth, err *Error) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") || err == nil {
		return false
	}
	return (err.HTTPStatus == http.StatusUnauthorized || err.HTTPStatus == http.StatusForbidden) &&
		strings.Contains(strings.ToLower(err.Message), oauthOrganizationRefusal)
}

func disableOAuthOrganizationAuth(auth *Auth, now time.Time) {
	auth.Disabled = true
	auth.Status = StatusDisabled
	clearCooldownStateForAuth(auth, now)
	auth.StatusMessage = oauthOrganizationDisabledMessage
	auth.NextRefreshAfter = time.Time{}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = true
}

func (m *Manager) publishCredentialAutoDisabled(ctx context.Context, auth *Auth, status int, persistErr error) {
	fields := log.Fields{"auth_id": auth.ID, "provider": auth.Provider}
	if persistErr != nil {
		log.WithFields(fields).WithError(persistErr).Error("failed to persist auto-disabled credential")
	} else {
		log.WithFields(fields).Warn(oauthOrganizationDisabledMessage)
	}
	if holder := m.credentialHealthHook.Load(); holder != nil {
		holder.hook(ctx, CredentialHealthEvent{
			Timestamp:        auth.UpdatedAt,
			AuthID:           auth.ID,
			AuthIndex:        auth.Index,
			Provider:         auth.Provider,
			Account:          fleetAlertAccount(auth),
			AuthFile:         fleetAlertAuthFile(auth),
			StatusMessage:    auth.StatusMessage,
			HTTPStatus:       status,
			PersistenceError: persistErr,
		})
	}
}
