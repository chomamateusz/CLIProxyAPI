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
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const quotaSampleInterval = 5 * time.Minute
const quotaSampleIdleAge = 15 * time.Minute

type quotaSampleJob struct {
	epoch    uint64
	version  uint64
	inFlight bool
	rescan   bool
	next     time.Time
	failures int
}

type quotaSampler struct {
	manager   *Manager
	lifecycle sync.Mutex
	mu        sync.Mutex
	jobs      map[string]*quotaSampleJob
	wake      chan struct{}
	cancel    context.CancelFunc
	done      chan struct{}
	workers   sync.WaitGroup
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
	go func() {
		defer close(s.done)
		defer s.workers.Wait()
		ticker := time.NewTicker(quotaSampleInterval)
		defer ticker.Stop()
		s.run(ctx, ticker.C)
	}()
}

func (s *quotaSampler) run(ctx context.Context, ticks <-chan time.Time) {
	s.scan(ctx, time.Now())
	for {
		var now time.Time
		select {
		case <-ctx.Done():
			return
		case now = <-ticks:
		case <-s.wake:
			now = time.Now()
		}
		s.scan(ctx, now)
	}
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

func quotaSampleNeeded(a *Auth, now time.Time) bool {
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
		s.manager.wakeQuotaSampler()
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
	changed := a.sampledQuota.ObserveResponseHeadersForProvider(a.Provider, headers, started)
	if changed && !a.Quota.ObservedAt.After(started) {
		a.Quota.ObserveResponseHeadersForProvider(a.Provider, headers, started)
	}
	var snapshot *Auth
	if changed {
		a.Generation++
		snapshot = a.Clone()
	}
	m.mu.Unlock()
	if snapshot != nil && m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	return changed
}

func (m *Manager) fetchQuotaSample(ctx context.Context, a *Auth) (http.Header, error) {
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
		req.Header.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
		req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	} else {
		req.Header.Set("User-Agent", "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)")
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
	return parseQuotaSample(provider, body, time.Now())
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
