package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const quotaSampleInterval = 5 * time.Minute
const quotaSampleIdleAge = 15 * time.Minute
const quotaSampleTimeout = 30 * time.Second

type quotaSampleJob struct {
	epoch    uint64
	version  uint64
	inFlight bool
	rescan   bool
	next     time.Time
	failures int
}

type quotaSampler struct {
	manager         *Manager
	lifecycle       sync.Mutex
	mu              sync.Mutex
	jobs            map[string]*quotaSampleJob
	wake            chan struct{}
	cancel          context.CancelFunc
	done            chan struct{}
	workers         sync.WaitGroup
	running         bool
	zeroUsageLogged sync.Once
}

func newQuotaSampler(m *Manager) *quotaSampler {
	return &quotaSampler{manager: m, jobs: make(map[string]*quotaSampleJob), wake: make(chan struct{}, 1)}
}

func (m *Manager) StartQuotaSampler(parent context.Context) {
	m.quotaSampler.start(parent)
}

func (s *quotaSampler) start(parent context.Context) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	go func() {
		defer close(s.done)
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		defer s.workers.Wait()
		ticker := time.NewTicker(quotaSampleInterval)
		defer ticker.Stop()
		s.run(ctx, ticker.C)
	}()
}

func (s *quotaSampler) run(ctx context.Context, ticks <-chan time.Time) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	now := time.Now()
	for {
		s.scan(ctx, now)
		timer.Stop()
		var due <-chan time.Time
		if next := s.nextCooldownSampleAt(time.Now()); !next.IsZero() {
			timer.Reset(time.Until(next))
			due = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case now = <-ticks:
		case <-due:
			now = time.Now()
		case <-s.wake:
			now = time.Now()
		}
	}
}

func (s *quotaSampler) nextCooldownSampleAt(now time.Time) time.Time {
	auths := s.manager.List()
	s.mu.Lock()
	defer s.mu.Unlock()
	var next time.Time
	for _, a := range auths {
		job := s.jobs[a.ID]
		if !quotaSampleEligible(a) || !hasActiveQuotaCooldown(a, now) || job == nil || job.inFlight || job.next.IsZero() {
			continue
		}
		if next.IsZero() || job.next.Before(next) {
			next = job.next
		}
	}
	return next
}

func (s *quotaSampler) stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.cancel != nil {
		s.cancel()
		<-s.done
		s.cancel = nil
	}
}

func (m *Manager) quotaSamplerAuthChanged(a *Auth) {
	if m.quotaSampler == nil {
		return
	}
	m.quotaSampler.mu.Lock()
	if job := m.quotaSampler.jobs[a.ID]; job != nil {
		if job.inFlight {
			job.rescan = job.rescan || !quotaSampleEligible(a) || job.epoch != a.RegistrationEpoch || job.version != a.CredentialVersion
		} else if !quotaSampleEligible(a) {
			delete(m.quotaSampler.jobs, a.ID)
		}
	}
	m.quotaSampler.mu.Unlock()
	m.wakeQuotaSampler()
}

func (m *Manager) wakeQuotaSampler() {
	if m.quotaSampler == nil {
		return
	}
	select {
	case m.quotaSampler.wake <- struct{}{}:
	default:
	}
}

func quotaSampleEligible(a *Auth) bool {
	if a == nil || a.Disabled || a.Status == StatusDisabled || IsConfigAPIKeyAuth(a) || IsPluginVirtualAuth(a) || authAccessToken(a) == "" {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	return provider == "claude" || provider == "codex"
}

func (s *quotaSampler) pending(a *Auth) bool {
	if s == nil || !quotaSampleEligible(a) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[a.ID]
	if job != nil && job.inFlight {
		return true
	}
	return s.running && (job == nil || job.epoch != a.RegistrationEpoch || job.version != a.CredentialVersion || job.next.IsZero())
}

func hasActiveQuotaCooldown(a *Auth, now time.Time) bool {
	if activeQuotaCooldown(a.Quota, now) {
		return true
	}
	for _, state := range a.ModelStates {
		if state != nil && activeQuotaCooldown(state.Quota, now) {
			return true
		}
	}
	return false
}

func quotaSampleNeeded(a *Auth, now time.Time) bool {
	if hasActiveQuotaCooldown(a, now) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "claude") && a.sampledFable {
		fable, _ := useOrLoseClaudeWindow(quotaObservations(a), "7d_oi")
		if !fable.usable(now) {
			return true
		}
	}
	selector := &UseOrLoseSelector{}
	for _, model := range []string{"", "fable"} {
		sample := selector.sampleFor(a, model, now)
		if !sample.sampled || !sample.window.observed.Add(quotaSampleIdleAge).After(now) && !a.lastRequestAt.Add(quotaSampleIdleAge).After(now) {
			return true
		}
	}
	return false
}

func (s *quotaSampler) scan(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	auths := s.manager.List()
	s.mu.Lock()
	defer s.mu.Unlock()
	present := make(map[string]bool, len(auths))
	for _, a := range auths {
		present[a.ID] = true
		job := s.jobs[a.ID]
		if job != nil && job.inFlight {
			job.rescan = job.rescan || !quotaSampleEligible(a) || job.epoch != a.RegistrationEpoch || job.version != a.CredentialVersion
			continue
		}
		if !quotaSampleEligible(a) {
			delete(s.jobs, a.ID)
			continue
		}
		if job == nil || job.epoch != a.RegistrationEpoch || job.version != a.CredentialVersion {
			job = &quotaSampleJob{epoch: a.RegistrationEpoch, version: a.CredentialVersion}
			s.jobs[a.ID] = job
		}
		if job.next.After(now) || !quotaSampleNeeded(a, now) {
			continue
		}
		job.inFlight = true
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			headers, err := s.manager.fetchQuotaSample(ctx, a)
			s.finish(ctx, a, job, headers, err, now, time.Now())
		}()
	}
	for id, job := range s.jobs {
		if !present[id] && !job.inFlight {
			delete(s.jobs, id)
		}
	}
}

func (s *quotaSampler) finish(ctx context.Context, a *Auth, job *quotaSampleJob, headers http.Header, err error, started, now time.Time) {
	defer s.manager.wakeQuotaSampler()
	if err == nil && ctx.Err() == nil {
		if s.manager.applyQuotaSample(ctx, a, headers, started) {
			logQuotaSample(a, headers)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job.inFlight = false
	if ctx.Err() != nil {
		job.next = time.Time{}
		return
	}
	if job.rescan {
		delete(s.jobs, a.ID)
		return
	}
	job.next = now.Add(quotaSampleInterval)
	if err == nil {
		job.failures = 0
		return
	}
	job.failures++
	backoff := min(quotaSampleInterval*time.Duration(1<<min(job.failures-1, 4)), time.Hour)
	job.next = now.Add(backoff)
	if job.failures == 1 {
		log.Warnf("quota sample failed: auth=%s reason=%s retry-in=%s", a.ID, err, backoff)
	}
}

func (m *Manager) applyQuotaSample(ctx context.Context, base *Auth, headers http.Header, started time.Time) bool {
	release, err := m.lockAuthMutationContext(ctx, base.ID)
	if err != nil {
		return false
	}
	defer release()
	m.mu.Lock()
	a := m.auths[base.ID]
	if !quotaSampleEligible(a) || a.RegistrationEpoch != base.RegistrationEpoch || a.CredentialVersion != base.CredentialVersion || a.sampledQuota.ObservedAt.After(started) {
		m.mu.Unlock()
		return false
	}
	observations := quotaObservations(a)
	now := time.Now()
	var cooldownRecordsBefore []CooldownStateRecord
	if m.cooldownStore != nil {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(a, now)
	}
	changed := a.sampledQuota.ObserveResponseHeadersForProvider(a.Provider, headers, started)
	if changed && headers.Get("Anthropic-Ratelimit-Unified-7d_oi-Utilization") != "" {
		a.sampledFable = true
	}
	if changed && !a.Quota.ObservedAt.After(started) {
		a.Quota.ObserveResponseHeadersForProvider(a.Provider, headers, started)
	}
	lifted := changed && m.liftObservedQuotaCooldownsLocked(a, append(quotaObservations(a), observations...), now)
	var snapshot *Auth
	cooldownStateChanged := false
	if changed {
		a.Generation++
		if lifted {
			if errPersist := m.persistLocked(ctx, a); errPersist != nil {
				log.Warnf("failed to persist quota recovery: auth=%s error=%v", a.ID, errPersist)
			}
			if m.cooldownStore != nil {
				cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, m.cooldownStateRecordsForAuthLocked(a, now))
			}
		}
		snapshot = a.Clone()
	}
	m.mu.Unlock()
	release()
	if snapshot != nil && m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	if lifted {
		m.projectQuotaRecovery(snapshot, now)
	}
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return changed
}

func (m *Manager) fetchQuotaSample(ctx context.Context, a *Auth) (http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, quotaSampleTimeout)
	defer cancel()
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	endpoint := "https://api.anthropic.com/api/oauth/usage"
	if provider == "codex" {
		endpoint = "https://chatgpt.com/backend-api/wham/usage"
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
		if id := authMetadataString(a, "account_id"); id != "" {
			req.Header.Set("Chatgpt-Account-Id", id)
		}
	}
	transportAuth := a.Clone()
	if transportAuth.ProxyURL == "" {
		if cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config); cfg != nil {
			transportAuth.ProxyURL = cfg.ProxyURL
		}
	}
	rt := m.roundTripperFor(transportAuth)
	if rt == nil {
		proxy := transportAuth.ProxyURL
		if proxy == "" {
			proxy = "direct"
		}
		transport, _, errTransport := proxyutil.BuildHTTPTransport(proxy)
		if errTransport != nil {
			return nil, errors.New("proxy configuration")
		}
		rt = transport
		if transport != nil {
			defer transport.CloseIdleConnections()
		}
	}
	client := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
	headers, err := parseQuotaSample(provider, body, time.Now())
	if err == nil && provider == "claude" && log.IsLevelEnabled(log.DebugLevel) {
		if shape := zeroUsageShape(body); len(shape) != 0 {
			m.quotaSampler.zeroUsageLogged.Do(func() {
				log.WithField("windows", shape).Debug("quota sample zero-usage shape")
			})
		}
	}
	return headers, err
}

func logQuotaSample(a *Auth, headers http.Header) {
	percent := func(name string) string {
		value, err := strconv.ParseFloat(headers.Get("Anthropic-Ratelimit-Unified-"+name+"-Utilization"), 64)
		if err != nil {
			return "unknown"
		}
		return strconv.FormatFloat(value*100, 'f', 2, 64)
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "claude") {
		log.Infof("quota sampled: auth=%s 7d=%s 7d_oi=%s 5h=%s", a.ID, percent("7d"), percent("7d_oi"), percent("5h"))
	} else {
		log.Infof("quota sampled: auth=%s primary=%s secondary=%s", a.ID, headers.Get("X-Codex-Primary-Used-Percent"), headers.Get("X-Codex-Secondary-Used-Percent"))
	}
}
