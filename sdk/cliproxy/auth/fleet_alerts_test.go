package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type fleetAlertRecorder struct {
	mu       sync.Mutex
	status   int
	payloads []fleetAlertPayload
	bodies   []string
}

func (r *fleetAlertRecorder) serveHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	var payload fleetAlertPayload
	if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" || json.Unmarshal(body, &payload) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.payloads = append(r.payloads, payload)
	r.bodies = append(r.bodies, string(body))
	w.WriteHeader(r.status)
}

func (r *fleetAlertRecorder) events() []fleetAlertPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fleetAlertPayload(nil), r.payloads...)
}

func fleetAlertFixture(t *testing.T, auths ...*Auth) (*fleetAlertMonitor, *fleetAlertRecorder, *time.Time) {
	t.Helper()
	recorder := &fleetAlertRecorder{status: http.StatusNoContent}
	server := httptest.NewServer(http.HandlerFunc(recorder.serveHTTP))
	t.Cleanup(server.Close)
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	monitor, err := newFleetAlertMonitor(config.FleetAlertsConfig{WebhookURL: server.URL}, func() []*Auth { return auths })
	if err != nil {
		t.Fatal(err)
	}
	monitor.now = func() time.Time { return now }
	return monitor, recorder, &now
}

func TestFleetAlertsThresholdNoRepeatRecovery(t *testing.T) {
	auth := &Auth{ID: "quota", Provider: "codex", FileName: "/private/auths/codex-user@example.com.json", Label: "work", Status: StatusError, Unavailable: true}
	monitor, recorder, now := fleetAlertFixture(t, auth)
	auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(4 * time.Hour)}
	since := now.Format(time.RFC3339)
	monitor.step(context.Background(), true)
	*now = now.Add(time.Hour - time.Second)
	monitor.step(context.Background(), true)
	if got := len(recorder.events()); got != 0 {
		t.Fatalf("early alerts = %d", got)
	}
	*now = now.Add(time.Second)
	monitor.step(context.Background(), true)
	*now = now.Add(time.Minute)
	monitor.step(context.Background(), true)
	events := recorder.events()
	if len(events) != 1 || events[0].Event != "credential_unhealthy" || events[0].Since != since || events[0].Reason != "credential_quota" {
		t.Fatalf("threshold events = %+v", events)
	}
	if events[0].Provider != "codex" || events[0].Account != "work" || events[0].AuthFile != "codex-user@example.com.json" || events[0].RetryAt != auth.Quota.NextRecoverAt.Format(time.RFC3339) || events[0].ProxyVersion != buildinfo.Version {
		t.Fatalf("payload = %+v", events[0])
	}
	auth.Quota = QuotaState{}
	auth.Unavailable, auth.Status = false, StatusActive
	monitor.step(context.Background(), true)
	monitor.step(context.Background(), true)
	events = recorder.events()
	if len(events) != 2 || events[1].Event != "credential_recovered" || events[1].Since != since || events[1].RetryAt != "" {
		t.Fatalf("recovery events = %+v", events)
	}
}

func TestFleetAlertsModelCooldownAloneDoesNotAlert(t *testing.T) {
	auth := &Auth{ID: "model", Provider: "claude", Status: StatusError, Unavailable: true, StatusMessage: "quota exhausted"}
	monitor, recorder, now := fleetAlertFixture(t, auth)
	next := now.Add(7 * 24 * time.Hour)
	auth.NextRetryAfter = next
	auth.Quota = QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next}
	auth.ModelStates = map[string]*ModelState{"claude-fable-5-1": {Status: StatusError, Unavailable: true, NextRetryAfter: next, Quota: auth.Quota}}
	monitor.step(context.Background(), true)
	*now = now.Add(2 * time.Hour)
	monitor.step(context.Background(), true)
	if got := recorder.events(); len(got) != 0 {
		t.Fatalf("model-only alerts = %+v", got)
	}
	auth.Quota.Reason = "credential_quota"
	monitor.step(context.Background(), true)
	*now = now.Add(time.Hour)
	monitor.step(context.Background(), true)
	if got := recorder.events(); len(got) != 1 || got[0].Reason != "credential_quota" {
		t.Fatalf("credential-wide alerts = %+v", got)
	}
}

func TestFleetAlertsAutoDisableImmediateAndRecovery(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auto", Provider: "claude", FileName: "claude-person@example.com.json", Metadata: map[string]any{"email": "person@example.com", "access_token": "never-send-this-token"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	monitor, recorder, now := fleetAlertFixture(t)
	monitor.list = manager.List
	manager.SetCredentialHealthHook(monitor.recordAutoDisable)
	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Error: &Error{HTTPStatus: 403, Message: oauthOrganizationErrorBody}})
	monitor.step(context.Background(), false)
	events := recorder.events()
	if len(events) != 1 || events[0].Event != "credential_disabled" || events[0].Reason != "oauth_refused" || events[0].Account != "person@example.com" || events[0].AuthFile != auth.FileName {
		t.Fatalf("auto-disable alerts = %+v", events)
	}
	*now, _ = time.Parse(time.RFC3339, events[0].Since)
	monitor.step(context.Background(), true)
	*now = now.Add(time.Hour + time.Second)
	monitor.step(context.Background(), true)
	monitor.step(context.Background(), true)
	events = recorder.events()
	if len(events) != 2 || events[1].Event != "credential_unhealthy" {
		t.Fatalf("disabled threshold alerts = %+v", events)
	}
	updated, _ := manager.GetByID(auth.ID)
	updated.Disabled, updated.Status, updated.StatusMessage = false, StatusActive, ""
	if _, err := manager.Update(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	monitor.step(context.Background(), true)
	monitor.step(context.Background(), true)
	if got := recorder.events(); len(got) != 3 || got[2].Event != "credential_recovered" {
		t.Fatalf("re-enabled alerts = %+v", got)
	}
}

func TestFleetAlertsManualDisabledAndOff(t *testing.T) {
	auth := &Auth{ID: "manual", Disabled: true, Status: StatusDisabled, StatusMessage: "disabled via management API"}
	monitor, recorder, now := fleetAlertFixture(t, auth)
	monitor.step(context.Background(), true)
	*now = now.Add(2 * time.Hour)
	monitor.step(context.Background(), true)
	if got := recorder.events(); len(got) != 0 {
		t.Fatalf("manual disable alerts = %+v", got)
	}
	off, err := newFleetAlertMonitor(config.FleetAlertsConfig{StuckAfter: "invalid"}, func() []*Auth { t.Fatal("off feature listed credentials"); return nil })
	if err != nil || off != nil {
		t.Fatalf("off feature = %v, %v", off, err)
	}
	manager := NewManager(nil, nil, nil)
	stop, err := manager.StartFleetAlerts(context.Background(), config.FleetAlertsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if manager.credentialHealthHook.Load() != nil {
		t.Fatal("off feature installed a hook")
	}
}

func TestFleetAlertsPayloadHasNoSecrets(t *testing.T) {
	secret := "fixture-secret-access-refresh-cookie-key"
	auth := &Auth{ID: "safe", Provider: "claude", Label: "safe-label", FileName: "/private/auths/safe.json", Status: StatusError, Unavailable: true,
		StatusMessage: strings.Repeat(secret, 20), LastError: &Error{HTTPStatus: 402, Message: secret, Code: secret},
		Metadata: map[string]any{"access_token": secret, "refresh_token": secret, "cookie": secret}, Attributes: map[string]string{"api_key": secret},
	}
	monitor, recorder, now := fleetAlertFixture(t, auth)
	monitor.step(context.Background(), true)
	*now = now.Add(time.Hour)
	monitor.step(context.Background(), true)
	events := recorder.events()
	if len(events) != 1 || events[0].Reason != "payment_required" || len([]rune(events[0].StatusMessage)) > 300 {
		t.Fatalf("safe events = %+v", events)
	}
	recorder.mu.Lock()
	body := recorder.bodies[0]
	recorder.mu.Unlock()
	for _, forbidden := range []string{secret, "access_token", "refresh_token", "cookie", "api_key", "/private/auths"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("payload contains forbidden value %q", forbidden)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil || len(fields) != 9 {
		t.Fatalf("contract fields = %v, error = %v", fields, err)
	}
}

func TestFleetAlertsRetryNextTickPreservesTransitionOrder(t *testing.T) {
	auth := &Auth{ID: "retry", Provider: "codex", Status: StatusError, Unavailable: true, StatusMessage: "unauthorized"}
	monitor, recorder, now := fleetAlertFixture(t, auth)
	recorder.status = http.StatusServiceUnavailable
	monitor.step(context.Background(), true)
	*now = now.Add(time.Hour)
	monitor.step(context.Background(), true)
	monitor.step(context.Background(), false)
	if got := recorder.events(); len(got) != 1 {
		t.Fatalf("attempts before next tick = %+v", got)
	}
	auth.Unavailable, auth.Status = false, StatusActive
	recorder.mu.Lock()
	recorder.status = http.StatusNoContent
	recorder.mu.Unlock()
	*now = now.Add(time.Minute)
	monitor.step(context.Background(), true)
	monitor.step(context.Background(), true)
	got := recorder.events()
	if len(got) != 3 || got[0].Event != "credential_unhealthy" || got[1] != got[0] || got[2].Event != "credential_recovered" {
		t.Fatalf("retry and recovery = %+v", got)
	}
}

func TestFleetAlertsHealthyIntervalResetsThreshold(t *testing.T) {
	auth := &Auth{ID: "intermittent", Status: StatusError, Unavailable: true, StatusMessage: "unauthorized"}
	monitor, recorder, now := fleetAlertFixture(t, auth)
	monitor.step(context.Background(), true)
	*now = now.Add(59 * time.Minute)
	auth.Unavailable, auth.Status = false, StatusActive
	monitor.step(context.Background(), true)
	auth.Unavailable, auth.Status = true, StatusError
	monitor.step(context.Background(), true)
	*now = now.Add(59 * time.Minute)
	monitor.step(context.Background(), true)
	if got := recorder.events(); len(got) != 0 {
		t.Fatalf("non-continuous unhealthy alerts = %+v", got)
	}
	*now = now.Add(time.Minute)
	monitor.step(context.Background(), true)
	if got := recorder.events(); len(got) != 1 {
		t.Fatalf("continuous unhealthy alerts = %+v", got)
	}
}

func TestFleetAlertsBackgroundDeliveryDoesNotBlockAutoDisable(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-req.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)
	manager := NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &Auth{ID: "background", Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	stop, err := manager.StartFleetAlerts(context.Background(), config.FleetAlertsConfig{WebhookURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	finished := make(chan struct{})
	go func() {
		manager.MarkResult(context.Background(), Result{AuthID: "background", Error: &Error{HTTPStatus: 403, Message: oauthOrganizationErrorBody}})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("auto-disable waited for webhook delivery")
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("auto-disable did not trigger immediate background delivery")
	}
}
