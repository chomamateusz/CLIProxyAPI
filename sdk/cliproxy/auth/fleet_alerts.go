package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

type fleetAlertPayload struct {
	Event         string `json:"event"`
	Provider      string `json:"provider"`
	Account       string `json:"account"`
	AuthFile      string `json:"auth_file"`
	Reason        string `json:"reason"`
	StatusMessage string `json:"status_message"`
	Since         string `json:"since"`
	RetryAt       string `json:"retry_at"`
	ProxyVersion  string `json:"proxy_version"`
}

type fleetPendingAlert struct {
	payload fleetAlertPayload
	retryAt time.Time
}

type fleetCredentialAlertState struct {
	since           time.Time
	autoDisabled    bool
	disabledQueued  bool
	unhealthyQueued bool
	last            fleetAlertPayload
	pending         []fleetPendingAlert
}

type fleetAlertMonitor struct {
	url      string
	stuck    time.Duration
	interval time.Duration
	client   *http.Client
	list     func() []*Auth
	now      func() time.Time
	states   map[string]*fleetCredentialAlertState
	wake     chan struct{}
	mu       sync.Mutex
	incoming []CredentialHealthEvent
}

func newFleetAlertMonitor(cfg config.FleetAlertsConfig, list func() []*Auth) (*fleetAlertMonitor, error) {
	stuck, interval, err := cfg.Durations()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.WebhookURL) == "" {
		return nil, nil
	}
	return &fleetAlertMonitor{
		url: cfg.WebhookURL, stuck: stuck, interval: interval,
		client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		list: list, now: time.Now, states: make(map[string]*fleetCredentialAlertState), wake: make(chan struct{}, 1),
	}, nil
}

func (m *Manager) StartFleetAlerts(ctx context.Context, cfg config.FleetAlertsConfig) (func(), error) {
	monitor, err := newFleetAlertMonitor(cfg, m.List)
	if err != nil {
		return nil, err
	}
	if monitor == nil {
		return func() {}, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.SetCredentialHealthHook(monitor.recordAutoDisable)
	go func() {
		defer close(done)
		monitor.run(ctx)
	}()
	return func() {
		cancel()
		m.SetCredentialHealthHook(nil)
		<-done
	}, nil
}

func (m *fleetAlertMonitor) recordAutoDisable(_ context.Context, event CredentialHealthEvent) {
	m.mu.Lock()
	m.incoming = append(m.incoming, event)
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *fleetAlertMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	m.step(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.step(ctx, true)
		case <-m.wake:
			m.step(ctx, false)
		}
	}
}

func (m *fleetAlertMonitor) step(ctx context.Context, scan bool) {
	now := m.now()
	m.mu.Lock()
	events := m.incoming
	m.incoming = nil
	m.mu.Unlock()
	for _, event := range events {
		state := m.state(event.AuthID)
		if state.since.IsZero() {
			state.since = event.Timestamp
			if state.since.IsZero() {
				state.since = now
			}
		}
		state.autoDisabled = true
		state.last = fleetAlertPayload{
			Provider: event.Provider, Account: event.Account, AuthFile: event.AuthFile,
			Reason: "oauth_refused", StatusMessage: oauthOrganizationDisabledMessage,
			Since: state.since.UTC().Format(time.RFC3339), ProxyVersion: buildinfo.Version,
		}
		m.queueDisabled(state)
	}
	if scan {
		present := make(map[string]bool)
		for _, auth := range m.list() {
			if auth == nil {
				continue
			}
			present[auth.ID] = true
			state := m.state(auth.ID)
			disabled := auth.Disabled || auth.Status == StatusDisabled
			autoDisabled := disabled && (state.autoDisabled || auth.StatusMessage == oauthOrganizationDisabledMessage)
			if disabled && !autoDisabled {
				delete(m.states, auth.ID)
				continue
			}
			unhealthy, reason, retry := fleetCredentialHealth(auth, now)
			if autoDisabled {
				unhealthy, reason = true, "oauth_refused"
			}
			if !unhealthy {
				if state.disabledQueued || state.unhealthyQueued {
					payload := state.last
					payload.Event, payload.Reason = "credential_recovered", "recovered"
					payload.StatusMessage, payload.RetryAt = "Credential is available again", ""
					state.pending = append(state.pending, fleetPendingAlert{payload: payload})
				}
				state.since = time.Time{}
				state.autoDisabled, state.disabledQueued, state.unhealthyQueued = false, false, false
				continue
			}
			if state.since.IsZero() {
				state.since = now
			}
			state.last = fleetAlertPayload{
				Provider: auth.Provider, Account: fleetAlertAccount(auth), AuthFile: fleetAlertAuthFile(auth),
				Reason: reason, StatusMessage: fleetAlertStatus(reason),
				Since: state.since.UTC().Format(time.RFC3339), ProxyVersion: buildinfo.Version,
			}
			if !retry.IsZero() {
				state.last.RetryAt = retry.UTC().Format(time.RFC3339)
			}
			state.autoDisabled = autoDisabled
			if autoDisabled {
				m.queueDisabled(state)
			}
			if !state.unhealthyQueued && now.Sub(state.since) >= m.stuck {
				payload := state.last
				payload.Event = "credential_unhealthy"
				state.pending = append(state.pending, fleetPendingAlert{payload: payload})
				state.unhealthyQueued = true
			}
		}
		for id, state := range m.states {
			if !present[id] && len(state.pending) == 0 {
				delete(m.states, id)
			}
		}
	}
	for id, state := range m.states {
		for len(state.pending) > 0 && !state.pending[0].retryAt.After(now) && ctx.Err() == nil {
			if !m.post(ctx, state.pending[0].payload) {
				state.pending[0].retryAt = now.Add(m.interval)
				break
			}
			state.pending = state.pending[1:]
		}
		if state.since.IsZero() && len(state.pending) == 0 {
			delete(m.states, id)
		}
	}
}

func (m *fleetAlertMonitor) state(id string) *fleetCredentialAlertState {
	if m.states[id] == nil {
		m.states[id] = &fleetCredentialAlertState{}
	}
	return m.states[id]
}

func (m *fleetAlertMonitor) queueDisabled(state *fleetCredentialAlertState) {
	if !state.disabledQueued {
		payload := state.last
		payload.Event = "credential_disabled"
		state.pending = append(state.pending, fleetPendingAlert{payload: payload})
		state.disabledQueued = true
	}
}

func (m *fleetAlertMonitor) post(ctx context.Context, payload fleetAlertPayload) bool {
	body, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		log.Warn("fleet alert webhook request could not be created")
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		log.WithField("event", payload.Event).Warn("fleet alert webhook POST failed; retrying on the next tick")
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.WithFields(log.Fields{"event": payload.Event, "status": resp.StatusCode}).Warn("fleet alert webhook rejected POST; retrying on the next tick")
		return false
	}
	return true
}

func fleetCredentialHealth(auth *Auth, now time.Time) (bool, string, time.Time) {
	if hasUnauthorizedAuthFailure(auth) {
		return true, "unauthorized", time.Time{}
	}
	for _, view := range CooldownSnapshotForAuth(auth, now) {
		if view.Scope == "credential" {
			return true, view.Reason, view.RetryAt
		}
	}
	if len(auth.ModelStates) == 0 && (auth.Status == StatusError || auth.Unavailable) && auth.NextRetryAfter.IsZero() && auth.Quota.NextRecoverAt.IsZero() {
		reason := cooldownErrorReason(auth.LastError)
		if reason == "unknown" {
			reason = cooldownStatusReason(auth.StatusMessage)
		}
		return true, reason, time.Time{}
	}
	return false, "", time.Time{}
}

func fleetAlertAccount(auth *Auth) string {
	if email, _ := auth.Metadata["email"].(string); strings.TrimSpace(email) != "" {
		return email
	}
	if strings.TrimSpace(auth.Label) != "" {
		return auth.Label
	}
	return fleetAlertAuthFile(auth)
}

func fleetAlertAuthFile(auth *Auth) string {
	if auth.FileName != "" {
		return filepath.Base(auth.FileName)
	}
	return filepath.Base(auth.ID)
}

func fleetAlertStatus(reason string) string {
	switch reason {
	case "oauth_refused":
		return oauthOrganizationDisabledMessage
	case "unauthorized":
		return "Credential authorization was rejected"
	case "payment_required":
		return "Credential requires payment or permission"
	case "credential_quota", "quota":
		return "Credential quota is exhausted"
	case "invalid_grant":
		return "Credential refresh authorization was rejected"
	default:
		return fmt.Sprintf("Credential is unavailable (%s)", reason)
	}
}
