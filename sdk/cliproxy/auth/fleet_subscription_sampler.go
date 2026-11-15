package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	log "github.com/sirupsen/logrus"
)

const (
	subscriptionSampleInterval       = 6 * time.Hour
	subscriptionSampleUrgentInterval = time.Hour
	subscriptionSampleUrgentWindow   = 48 * time.Hour
	subscriptionSampleStagger        = 10 * time.Second
	subscriptionSampleTimeout        = 30 * time.Second
	claudeProfileEndpoint            = "https://api.anthropic.com/api/oauth/profile"
	codexSubscriptionsEndpoint       = "https://chatgpt.com/backend-api/subscriptions"
)

type ClaudeProfileFetcher func(ctx context.Context, auth *Auth, accessToken string) ([]byte, error)

type subscriptionSampleJob struct {
	due      time.Time
	attempt  time.Time
	failed   bool
	inFlight bool
}

type subscriptionSampler struct {
	manager        *Manager
	lifecycle      sync.Mutex
	mu             sync.Mutex
	jobs           map[string]*subscriptionSampleJob
	wake           chan struct{}
	cancel         context.CancelFunc
	done           chan struct{}
	workers        sync.WaitGroup
	stagger        time.Duration
	profileFetcher atomic.Pointer[ClaudeProfileFetcher]
}

func newSubscriptionSampler(m *Manager) *subscriptionSampler {
	return &subscriptionSampler{manager: m, jobs: make(map[string]*subscriptionSampleJob), wake: make(chan struct{}, 1), stagger: subscriptionSampleStagger}
}

func (m *Manager) SetClaudeProfileFetcher(fetcher ClaudeProfileFetcher) {
	if m.subscriptionSampler == nil {
		return
	}
	if fetcher == nil {
		m.subscriptionSampler.profileFetcher.Store(nil)
		return
	}
	m.subscriptionSampler.profileFetcher.Store(&fetcher)
}

func (m *Manager) StartSubscriptionSampler(parent context.Context) {
	m.subscriptionSampler.start(parent)
}

func (s *subscriptionSampler) start(parent context.Context) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		defer s.workers.Wait()
		s.run(ctx)
	}()
}

func (s *subscriptionSampler) stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.cancel != nil {
		s.cancel()
		<-s.done
		s.cancel = nil
	}
}

func (s *subscriptionSampler) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriptionSampler) run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		next := s.scan(ctx, time.Now())
		timer.Stop()
		var due <-chan time.Time
		if !next.IsZero() {
			timer.Reset(time.Until(next))
			due = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case <-due:
		case <-s.wake:
		}
	}
}

func subscriptionSampleEligible(a *Auth) bool {
	if a == nil || a.Disabled || a.Status == StatusDisabled || IsConfigAPIKeyAuth(a) || IsPluginVirtualAuth(a) || authAccessToken(a) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(a.Provider)) {
	case "claude":
		return true
	case "codex":
		return authMetadataString(a, "account_id") != ""
	default:
		return false
	}
}

func subscriptionSampleIntervalFor(a *Auth, now time.Time) time.Duration {
	end := FleetCredentialEndFor(a, now)
	switch {
	case end.State == FleetEndStateOverdue:
		return subscriptionSampleUrgentInterval
	case end.State == FleetEndStateScheduled && end.EndsAt.Sub(now) <= subscriptionSampleUrgentWindow:
		return subscriptionSampleUrgentInterval
	default:
		return subscriptionSampleInterval
	}
}

func (s *subscriptionSampler) nextDue(a *Auth, job *subscriptionSampleJob, now time.Time) time.Time {
	if job.attempt.IsZero() {
		return job.due
	}
	interval := subscriptionSampleIntervalFor(a, now)
	if job.failed {
		interval = min(interval, subscriptionSampleUrgentInterval)
	}
	return job.attempt.Add(interval)
}

func (s *subscriptionSampler) scan(ctx context.Context, now time.Time) time.Time {
	if ctx.Err() != nil {
		return time.Time{}
	}
	auths := s.manager.List()
	s.mu.Lock()
	defer s.mu.Unlock()
	present := make(map[string]bool, len(auths))
	newJobs := 0
	var next time.Time
	for _, a := range auths {
		present[a.ID] = true
		job := s.jobs[a.ID]
		if !subscriptionSampleEligible(a) {
			if job != nil && !job.inFlight {
				delete(s.jobs, a.ID)
			}
			continue
		}
		if job == nil {
			job = &subscriptionSampleJob{due: now.Add(time.Duration(newJobs) * s.stagger)}
			newJobs++
			s.jobs[a.ID] = job
		}
		if job.inFlight {
			continue
		}
		due := s.nextDue(a, job, now)
		if due.After(now) {
			if next.IsZero() || due.Before(next) {
				next = due
			}
			continue
		}
		job.inFlight = true
		job.attempt = now
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			sample, err := s.manager.fetchSubscriptionSample(ctx, a)
			s.finish(ctx, a, job, sample, err)
		}()
	}
	for id, job := range s.jobs {
		if !present[id] && !job.inFlight {
			delete(s.jobs, id)
		}
	}
	return next
}

func (s *subscriptionSampler) finish(ctx context.Context, a *Auth, job *subscriptionSampleJob, sample *subscriptionSample, err error) {
	defer s.notify()
	if err == nil && ctx.Err() == nil {
		s.manager.applySubscriptionSample(ctx, a, sample)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job.inFlight = false
	job.failed = err != nil
	if err != nil && ctx.Err() == nil {
		log.Warnf("subscription sample failed: auth=%s reason=%s", a.ID, err)
	}
}

func (m *Manager) subscriptionSamplerAuthChanged() {
	if m.subscriptionSampler != nil {
		m.subscriptionSampler.notify()
	}
}

func (m *Manager) LatestSubscriptionBillingAnchor(ctx context.Context, id string) time.Time {
	current, ok := m.GetByID(id)
	if !ok {
		return time.Time{}
	}
	if current.subscription != nil && !current.subscription.SubscriptionCreatedAt.IsZero() {
		return current.subscription.SubscriptionCreatedAt
	}
	if !subscriptionSampleEligible(current) || !strings.EqualFold(strings.TrimSpace(current.Provider), "claude") {
		return time.Time{}
	}
	sample, err := m.fetchSubscriptionSample(ctx, current)
	if err != nil {
		log.Warnf("subscription sample failed: auth=%s reason=%s", current.ID, err)
		return time.Time{}
	}
	m.applySubscriptionSample(ctx, current, sample)
	return sample.SubscriptionCreatedAt
}

func (m *Manager) fetchSubscriptionSample(ctx context.Context, a *Auth) (*subscriptionSample, error) {
	ctx, cancel := context.WithTimeout(ctx, subscriptionSampleTimeout)
	defer cancel()
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	var body []byte
	var err error
	if fetcher := m.claudeProfileFetcher(); provider == "claude" && fetcher != nil {
		body, err = fetcher(ctx, a, authAccessToken(a))
	} else {
		body, err = m.fetchSubscriptionBody(ctx, provider, a)
	}
	if err != nil {
		return nil, err
	}
	return parseSubscriptionSample(provider, body, time.Now())
}

func (m *Manager) claudeProfileFetcher() ClaudeProfileFetcher {
	if m.subscriptionSampler == nil {
		return nil
	}
	if fetcher := m.subscriptionSampler.profileFetcher.Load(); fetcher != nil {
		return *fetcher
	}
	return nil
}

func (m *Manager) fetchSubscriptionBody(ctx context.Context, provider string, a *Auth) ([]byte, error) {
	endpoint := claudeProfileEndpoint
	accountID := authMetadataString(a, "account_id")
	if provider == "codex" {
		endpoint = codexSubscriptionsEndpoint + "?account_id=" + url.QueryEscape(accountID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("request construction")
	}
	req.Header.Set("Authorization", "Bearer "+authAccessToken(a))
	req.Header.Set("Content-Type", "application/json")
	if provider == "claude" {
		req.Header.Set("User-Agent", constant.ClaudeUserAgent)
		req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	} else {
		req.Header.Set("User-Agent", constant.CodexUserAgent)
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
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
	return body, nil
}

func newerSubscriptionSample(existing, incoming *subscriptionSample) *subscriptionSample {
	if incoming == nil || existing != nil && existing.SampledAt.After(incoming.SampledAt) {
		return existing
	}
	return incoming
}

func (m *Manager) applySubscriptionSample(ctx context.Context, base *Auth, sample *subscriptionSample) {
	release, err := m.lockAuthMutationContext(ctx, base.ID)
	if err != nil {
		return
	}
	defer release()
	now := time.Now()
	m.mu.Lock()
	a := m.auths[base.ID]
	if a == nil || a.RegistrationEpoch != base.RegistrationEpoch || quotaAccountChanged(base, a) || newerSubscriptionSample(a.subscription, sample) != sample {
		m.mu.Unlock()
		return
	}
	before := FleetCredentialEndFor(a, now)
	a.subscription = sample
	edited := a.Clone()
	reason, changed := reconcileFleetEnd(edited.Metadata, sample)
	if !changed {
		a.Generation++
		snapshot := a.Clone()
		m.mu.Unlock()
		release()
		if m.scheduler != nil {
			m.scheduler.upsertAuth(snapshot)
		}
		if after := FleetCredentialEndFor(snapshot, now); after.State != before.State {
			logFleetEndTransition(snapshot, before, after, "provider sample", sample)
			m.wakeQuotaSampler()
		}
		return
	}
	m.mu.Unlock()
	edited.UpdatedAt = now
	committed, didCommit, persistCooldown, errUpdate := m.updateInternalLocked(ctx, nil, edited, updateModeReplace)
	release()
	if errUpdate != nil || !didCommit || committed == nil {
		log.Warnf("failed to store provider credential end: auth=%s error=%v", base.ID, errUpdate)
		return
	}
	m.finishAuthUpdate(ctx, committed, persistCooldown)
	logFleetEndTransition(committed, before, FleetCredentialEndFor(committed, now), reason, sample)
}

func logFleetEndTransition(a *Auth, before, after FleetCredentialEnd, reason string, sample *subscriptionSample) {
	fields := log.Fields{
		"auth":        a.ID,
		"reason":      reason,
		"state_from":  string(before.State),
		"state_to":    string(after.State),
		"source_from": before.Source,
		"source_to":   after.Source,
		"observed":    sample.logFields(),
	}
	if !before.EndsAt.IsZero() {
		fields["ends_at_from"] = FormatFleetEndsAt(before.EndsAt)
	}
	if !after.EndsAt.IsZero() {
		fields["ends_at_to"] = FormatFleetEndsAt(after.EndsAt)
	}
	log.WithFields(fields).Info("credential end changed by provider sample")
}
