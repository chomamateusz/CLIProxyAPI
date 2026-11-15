package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type subscriptionUpstream struct {
	mu     sync.Mutex
	bodies map[string]string
	status map[string]int
	calls  map[string]int
	bad    atomic.Int32
}

func newSubscriptionUpstream() *subscriptionUpstream {
	return &subscriptionUpstream{bodies: map[string]string{}, status: map[string]int{}, calls: map[string]int{}}
}

func (u *subscriptionUpstream) set(token, body string, status int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.bodies[token] = body
	u.status[token] = status
}

func (u *subscriptionUpstream) callCount(token string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[token]
}

func (u *subscriptionUpstream) transport() samplerTransport {
	return func(r *http.Request) (*http.Response, error) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claudeShape := r.URL.String() == claudeProfileEndpoint && r.Header.Get("Anthropic-Beta") == "oauth-2025-04-20" && r.Header.Get("User-Agent") == constant.ClaudeUserAgent
		codexShape := r.URL.Path == "/backend-api/subscriptions" && r.URL.Host == "chatgpt.com" && r.URL.Query().Get("account_id") != "" &&
			r.Header.Get("Chatgpt-Account-Id") == r.URL.Query().Get("account_id") && r.Header.Get("User-Agent") == constant.CodexUserAgent
		if r.Method != http.MethodGet || !claudeShape && !codexShape {
			u.bad.Add(1)
			return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		u.mu.Lock()
		u.calls[token]++
		body, status := u.bodies[token], u.status[token]
		u.mu.Unlock()
		response := samplerResponse(body)
		response.StatusCode = status
		return response, nil
	}
}

func subscriptionAuth(id, provider string, metadata map[string]any) *Auth {
	values := map[string]any{"access_token": "tok-" + id}
	if provider == "codex" {
		values["account_id"] = "acct-" + id
	}
	for key, value := range metadata {
		values[key] = value
	}
	return &Auth{ID: id, Provider: provider, Metadata: values}
}

func startSubscriptionSampler(t *testing.T, m *Manager, upstream *subscriptionUpstream) {
	t.Helper()
	m.SetRoundTripperProvider(upstream.transport())
	ctx, cancel := context.WithCancel(context.Background())
	m.StartSubscriptionSampler(ctx)
	t.Cleanup(func() {
		cancel()
		m.subscriptionSampler.stop()
		if bad := upstream.bad.Load(); bad != 0 {
			t.Errorf("%d upstream requests had an unexpected shape", bad)
		}
	})
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func registerAuths(t *testing.T, m *Manager, auths ...*Auth) {
	t.Helper()
	for _, auth := range auths {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubscriptionSamplerAppliesProviderEnds(t *testing.T) {
	upstream := newSubscriptionUpstream()
	upstream.set("tok-claude-active", claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	upstream.set("tok-claude-gone", claudeProfileBody("canceled", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	upstream.set("tok-codex-cancel", `{"will_renew":false,"is_active":true,"active_until":"2099-01-01T00:00:00Z","entitlement":{"cancels_at":"2099-01-02T00:00:00Z","has_active_subscription":true}}`, http.StatusOK)
	upstream.set("tok-codex-down", `{"is_active":false}`, http.StatusServiceUnavailable)
	m := NewManager(nil, nil, nil)
	registerAuths(t, m,
		subscriptionAuth("claude-active", "claude", nil),
		subscriptionAuth("claude-gone", "claude", nil),
		subscriptionAuth("codex-cancel", "codex", nil),
		subscriptionAuth("codex-down", "codex", nil),
		subscriptionAuth("claude-no-token", "claude", map[string]any{"access_token": ""}),
	)
	m.subscriptionSampler.stagger = time.Millisecond
	startSubscriptionSampler(t, m, upstream)

	waitFor(t, "every eligible credential sampled", func() bool {
		for _, token := range []string{"tok-claude-active", "tok-claude-gone", "tok-codex-cancel", "tok-codex-down"} {
			if upstream.callCount(token) == 0 {
				return false
			}
		}
		gone, _ := m.GetByID("claude-gone")
		cancel, _ := m.GetByID("codex-cancel")
		return FleetCredentialEnded(gone, time.Now()) && cancel.Metadata[MetadataFleetEndsAt] != nil
	})

	active, _ := m.GetByID("claude-active")
	if end := FleetCredentialEndFor(active, time.Now()); end.State != FleetEndStateNone || !end.BillingAnchor.Equal(endTime(t, "2026-10-07T21:09:56Z")) || end.CheckedAt.IsZero() {
		t.Fatalf("active claude end = %+v", end)
	}
	gone, _ := m.GetByID("claude-gone")
	if gone.Metadata[MetadataFleetEndsAtSource] != FleetEndSourcePlanEnded {
		t.Fatalf("ended claude metadata = %v", gone.Metadata)
	}
	cancel, _ := m.GetByID("codex-cancel")
	if cancel.Metadata[MetadataFleetEndsAt] != "2099-01-02T00:00:00Z" || cancel.Metadata[MetadataFleetEndsAtSource] != FleetEndSourceCodexAPI {
		t.Fatalf("codex cancel metadata = %v", cancel.Metadata)
	}
	down, _ := m.GetByID("codex-down")
	if down.subscription != nil || down.Metadata[MetadataFleetEndsAt] != nil {
		t.Fatalf("failed sample changed the credential: %v / %+v", down.Metadata, down.subscription)
	}
}

func TestSubscriptionSamplerStaggersStartup(t *testing.T) {
	upstream := newSubscriptionUpstream()
	m := NewManager(nil, nil, nil)
	for _, id := range []string{"a", "b", "c"} {
		upstream.set("tok-"+id, claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
		registerAuths(t, m, subscriptionAuth(id, "claude", nil))
	}
	startSubscriptionSampler(t, m, upstream)
	waitFor(t, "the first credential sampled", func() bool {
		return upstream.callCount("tok-a")+upstream.callCount("tok-b")+upstream.callCount("tok-c") >= 1
	})
	time.Sleep(200 * time.Millisecond)
	if total := upstream.callCount("tok-a") + upstream.callCount("tok-b") + upstream.callCount("tok-c"); total != 1 {
		t.Fatalf("startup sampled %d credentials at once; want one, the rest staggered", total)
	}
}

func TestSubscriptionSamplerSchedulesNextSample(t *testing.T) {
	t.Parallel()
	now := endTime(t, "2026-11-15T12:00:00Z")
	s := newSubscriptionSampler(nil)
	auth := &Auth{Metadata: endMetadata(now.Add(24*time.Hour), FleetEndSourceManual)}
	if got := s.nextDue(auth, &subscriptionSampleJob{due: now.Add(time.Minute)}, now); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("first sample due %s; want the stagger slot", got)
	}
	if got := s.nextDue(auth, &subscriptionSampleJob{attempt: now}, now); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("near end due %s; want hourly", got)
	}
	if got := s.nextDue(&Auth{}, &subscriptionSampleJob{attempt: now}, now); !got.Equal(now.Add(6 * time.Hour)) {
		t.Fatalf("no end due %s; want six hours", got)
	}
	if got := s.nextDue(&Auth{}, &subscriptionSampleJob{attempt: now, failed: true}, now); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("failed sample retry due %s; want one hour", got)
	}
}

func TestLatestSubscriptionBillingAnchorFetchesOnDemand(t *testing.T) {
	upstream := newSubscriptionUpstream()
	upstream.set("tok-claude", claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	upstream.set("tok-no-anchor", `{"organization":{"subscription_status":"active","organization_type":"claude_max"}}`, http.StatusOK)
	m := NewManager(nil, nil, nil)
	m.SetRoundTripperProvider(upstream.transport())
	registerAuths(t, m, subscriptionAuth("claude", "claude", nil), subscriptionAuth("no-anchor", "claude", nil), subscriptionAuth("codex", "codex", nil))
	ctx := context.Background()
	if got := m.LatestSubscriptionBillingAnchor(ctx, "claude"); !got.Equal(endTime(t, "2026-10-07T21:09:56Z")) {
		t.Fatalf("anchor = %s", got)
	}
	if m.LatestSubscriptionBillingAnchor(ctx, "claude"); upstream.callCount("tok-claude") != 1 {
		t.Fatalf("anchor fetched %d times; want the stored sample reused", upstream.callCount("tok-claude"))
	}
	if got := m.LatestSubscriptionBillingAnchor(ctx, "no-anchor"); !got.IsZero() {
		t.Fatalf("anchor without subscription_created_at = %s", got)
	}
	if got := m.LatestSubscriptionBillingAnchor(ctx, "codex"); !got.IsZero() || upstream.callCount("tok-codex") != 0 {
		t.Fatal("codex credential fetched for a billing anchor")
	}
	fetched := 0
	m.SetClaudeProfileFetcher(func(_ context.Context, auth *Auth, token string) ([]byte, error) {
		fetched++
		if token != "tok-fetcher" {
			t.Errorf("fetcher token = %q", token)
		}
		return []byte(claudeProfileBody("active", "2026-09-01T00:00:00Z", "claude_pro")), nil
	})
	registerAuths(t, m, subscriptionAuth("fetcher", "claude", nil))
	if got := m.LatestSubscriptionBillingAnchor(ctx, "fetcher"); fetched != 1 || !got.Equal(endTime(t, "2026-09-01T00:00:00Z")) {
		t.Fatalf("injected fetcher anchor = %s after %d calls", got, fetched)
	}
}

func TestSubscriptionSampleRoutesOverdueAndDropsEnded(t *testing.T) {
	now := time.Now()
	model := "fleet-end-overdue-routing"
	upstream := newSubscriptionUpstream()
	upstream.set("tok-overdue", claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
	m.SetRoundTripperProvider(upstream.transport())
	overdue := subscriptionAuth("overdue", "claude", endMetadata(now.Add(-time.Hour), FleetEndSourceClaudeAnniversary))
	registerFleetEndModel(t, m, model, overdue)
	pick := func() (*Auth, error) {
		picked, _, err := m.pickNext(context.Background(), "claude", model, cliproxyexecutor.Options{Metadata: map[string]any{}}, nil)
		return picked, err
	}
	if picked, err := pick(); err != nil || picked.ID != "overdue" {
		t.Fatalf("overdue credential without a sample: picked %v, %v", picked, err)
	}
	m.LatestSubscriptionBillingAnchor(context.Background(), "overdue")
	current, _ := m.GetByID("overdue")
	if state := FleetCredentialEndFor(current, time.Now()).State; state != FleetEndStateOverdue {
		t.Fatalf("state after an active post-end sample = %s; want overdue", state)
	}
	if picked, err := pick(); err != nil || picked.ID != "overdue" {
		t.Fatalf("overdue credential with an active sample: picked %v, %v", picked, err)
	}
	m.quotaSampler.scan(context.Background(), time.Now())
	if !fleetEndModelListed(model) {
		t.Fatal("overdue credential stopped advertising its model")
	}

	upstream.set("tok-overdue", claudeProfileBody("canceled", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	current, _ = m.GetByID("overdue")
	endBeforeInactiveSample := current.Metadata[MetadataFleetEndsAt]
	sample, err := m.fetchSubscriptionSample(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	m.applySubscriptionSample(context.Background(), current, sample)
	current, _ = m.GetByID("overdue")
	if state := FleetCredentialEndFor(current, time.Now()).State; state != FleetEndStateEnded || current.Metadata[MetadataFleetEndsAtSource] != FleetEndSourcePlanEnded || current.Metadata[MetadataFleetEndsAt] != endBeforeInactiveSample {
		t.Fatalf("after an inactive post-end sample: state %s metadata %v; want ended as plan-ended with the end kept", state, current.Metadata)
	}
	if picked, err := pick(); err == nil {
		t.Fatalf("ended credential picked: %s", picked.ID)
	}
	m.quotaSampler.scan(context.Background(), time.Now())
	if fleetEndModelListed(model) {
		t.Fatal("ended credential still advertises its model")
	}
}

func TestSubscriptionSampleSurvivesUpdatesUntilAccountChanges(t *testing.T) {
	upstream := newSubscriptionUpstream()
	upstream.set("tok-keep", claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	m := NewManager(nil, nil, nil)
	m.SetRoundTripperProvider(upstream.transport())
	registerAuths(t, m, subscriptionAuth("keep", "claude", map[string]any{"email": "a@example.com"}))
	m.LatestSubscriptionBillingAnchor(context.Background(), "keep")
	current, _ := m.GetByID("keep")
	current.Metadata["access_token"] = "tok-rotated"
	if _, err := m.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if current, _ = m.GetByID("keep"); current.subscription == nil {
		t.Fatal("token rotation dropped the subscription sample")
	}
	current.Metadata["email"] = "b@example.com"
	if _, err := m.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if current, _ = m.GetByID("keep"); current.subscription != nil {
		t.Fatal("account change kept the previous account's subscription sample")
	}
}

func TestEndedCredentialDoesNotHideQuotaCooledModel(t *testing.T) {
	now := time.Now()
	model := "fleet-end-quota-cooled"
	ended := fleetEndAuth("fleet-end-quota-cooled/ended", "codex", now.Add(-time.Minute))
	cooled := fleetEndAuth("fleet-end-quota-cooled/cooled", "codex", time.Time{})
	cooled.Quota = QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}
	cooled.ModelStates = map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: now.Add(time.Hour), Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}}}
	m := NewManager(nil, nil, nil)
	registerFleetEndModel(t, m, model, ended, cooled)
	for _, id := range []string{ended.ID, cooled.ID} {
		current, _ := m.GetByID(id)
		m.projectQuotaRecovery(current, now)
	}
	reg := registry.GetGlobalRegistry()
	if !reg.IsModelSuspendedForClient(ended.ID, model) || !reg.IsModelSuspendedForClient(cooled.ID, model) {
		t.Fatal("projection did not suspend both credentials")
	}
	if !fleetEndModelListed(model) {
		t.Fatal("ended credential hid a model whose live credential is only quota-cooled")
	}
	listedByProvider := false
	for _, info := range reg.GetAvailableModelsByProvider("codex") {
		listedByProvider = listedByProvider || info.ID == model
	}
	if !listedByProvider {
		t.Fatal("provider listing hid a model whose live credential is only quota-cooled")
	}
}

func TestInactiveSampleAfterPastEndSurvivesRestart(t *testing.T) {
	now := time.Now()
	model := "fleet-end-inactive-persisted"
	pastEnd := now.Add(-time.Hour).UTC().Truncate(time.Second)
	upstream := newSubscriptionUpstream()
	upstream.set("tok-lapsed", claudeProfileBody("canceled", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	m := NewManager(nil, nil, nil)
	m.SetRoundTripperProvider(upstream.transport())
	lapsed := subscriptionAuth("lapsed", "claude", endMetadata(pastEnd, FleetEndSourceManual))
	registerAuths(t, m, lapsed)
	current, _ := m.GetByID("lapsed")
	sample, err := m.fetchSubscriptionSample(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	m.applySubscriptionSample(context.Background(), current, sample)

	current, _ = m.GetByID("lapsed")
	persisted := cloneMetadataMap(current.Metadata)
	if end := FleetCredentialEndFromMetadata(persisted, time.Now()); end.State != FleetEndStateEnded || end.Source != FleetEndSourcePlanEnded || !end.EndsAt.Equal(pastEnd) {
		t.Fatalf("persisted end without the sample = %+v; want ended as plan-ended at %s", end, FormatFleetEndsAt(pastEnd))
	}

	restarted := NewManager(nil, &FillFirstSelector{}, nil)
	restarted.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
	registerFleetEndModel(t, restarted, model, &Auth{ID: "lapsed", Provider: "claude", Metadata: persisted})
	if picked, _, err := restarted.pickNext(context.Background(), "claude", model, cliproxyexecutor.Options{Metadata: map[string]any{}}, nil); err == nil {
		t.Fatalf("lapsed credential picked after a restart: %s", picked.ID)
	}
}
