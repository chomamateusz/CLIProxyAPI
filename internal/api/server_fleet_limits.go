package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fleetLimitsResponse struct {
	Object       string                                `json:"object"`
	GeneratedAt  string                                `json:"generated_at"`
	ProxyVersion string                                `json:"proxy_version"`
	Client       fleetLimitsClient                     `json:"client"`
	Models       []fleetLimitsModel                    `json:"models"`
	Exclusions   []fleetLimitsExclusion                `json:"exclusions"`
	Credentials  []fleetLimitsCredential               `json:"credentials"`
	Summary      map[string]fleetLimitsProviderSummary `json:"summary"`
}

type fleetLimitsClient struct {
	Name *string `json:"name"`
}

type fleetLimitsModel struct {
	ID        string   `json:"id"`
	Providers []string `json:"providers"`
}

type fleetLimitsExclusion struct {
	Pattern     string  `json:"pattern"`
	Provider    *string `json:"provider"`
	Source      string  `json:"source"`
	Until       *string `json:"until"`
	UntilSource *string `json:"until_source"`
}

type fleetLimitsCredential struct {
	Label     string                `json:"label"`
	Provider  string                `json:"provider"`
	Plan      *string               `json:"plan"`
	State     string                `json:"state"`
	Bars      []fleetLimitsBar      `json:"bars"`
	Credits   *fleetLimitsCredits   `json:"credits,omitempty"`
	Cooldowns []fleetLimitsCooldown `json:"cooldowns"`
	usable    bool
	sampled   bool
}

type fleetLimitsBar struct {
	ID          string   `json:"id"`
	Percent     *float64 `json:"percent"`
	Status      *string  `json:"status"`
	ResetsAt    *string  `json:"resets_at"`
	SampledAt   *string  `json:"sampled_at"`
	ResetPassed bool     `json:"reset_passed"`
}

type fleetLimitsCredits struct {
	HasCredits *bool    `json:"has_credits"`
	Unlimited  *bool    `json:"unlimited"`
	Balance    *float64 `json:"balance"`
}

type fleetLimitsCooldown struct {
	Scope      string  `json:"scope"`
	Model      *string `json:"model"`
	Reason     string  `json:"reason"`
	Until      string  `json:"until"`
	HTTPStatus *int    `json:"http_status"`
}

type fleetLimitsProviderSummary struct {
	UsableCredentials int                              `json:"usable_credentials"`
	Bars              map[string]fleetLimitsSummaryBar `json:"bars"`
}

type fleetLimitsSummaryBar struct {
	Percent  float64 `json:"percent"`
	ResetsAt *string `json:"resets_at"`
}

var fleetLimitsNow = time.Now

// Without a management secret the label key is random per process, so labels
// stay opaque but change on restart.
var fleetLimitsProcessLabelKey = func() []byte {
	key := make([]byte, sha256.Size)
	_, _ = rand.Read(key)
	return key
}()

func (s *Server) fleetLimits(c *gin.Context) {
	// AuthMiddleware lets every request through when no client keys are
	// configured; this route must still never answer anonymously.
	principal, _ := c.Get("userApiKey")
	if key, _ := principal.(string); strings.TrimSpace(key) == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": access.NewNoCredentialsError().Message})
		return
	}
	now := fleetLimitsNow().UTC()
	cfg := s.getConfig()
	var policy *access.KeyPolicy
	if value, exists := c.Get(access.KeyPolicyGinKey); exists {
		policy, _ = value.(*access.KeyPolicy)
	}
	var auths []*coreauth.Auth
	if s.handlers != nil && s.handlers.AuthManager != nil {
		auths = s.handlers.AuthManager.List()
	}
	response := fleetLimitsResponse{
		Object:       "fleet.limits",
		GeneratedAt:  now.Format(time.RFC3339),
		ProxyVersion: buildinfo.Version,
		Models:       fleetLimitsModels(policy),
		Exclusions:   make([]fleetLimitsExclusion, 0),
		Credentials:  make([]fleetLimitsCredential, 0),
		Summary:      make(map[string]fleetLimitsProviderSummary),
	}
	if policy != nil {
		response.Client.Name = optionalString(strings.TrimSpace(policy.Name))
	}

	labelKey := fleetLimitsLabelKey(cfg)
	visibleProviders := make(map[string]bool)
	for _, auth := range auths {
		if auth == nil || policy != nil && !policy.AllowsCredential(auth.FileName, auth.ID, auth.Label) {
			continue
		}
		credential := fleetLimitsCredentialFor(auth, policy, labelKey, now)
		response.Credentials = append(response.Credentials, credential)
		visibleProviders[credential.Provider] = true
	}
	sort.SliceStable(response.Credentials, func(i, j int) bool {
		if response.Credentials[i].Provider != response.Credentials[j].Provider {
			return response.Credentials[i].Provider < response.Credentials[j].Provider
		}
		return response.Credentials[i].Label < response.Credentials[j].Label
	})
	for _, credential := range response.Credentials {
		addFleetLimitsSummary(response.Summary, credential)
	}

	if cfg != nil {
		providers := make([]string, 0, len(cfg.OAuthExcludedModels))
		for provider := range cfg.OAuthExcludedModels {
			if visibleProviders[provider] {
				providers = append(providers, provider)
			}
		}
		sort.Strings(providers)
		for _, provider := range providers {
			for _, pattern := range cfg.OAuthExcludedModels[provider] {
				until, source := coreauth.OAuthExclusionUntil(cfg, auths, provider, pattern, now)
				response.Exclusions = append(response.Exclusions, fleetLimitsExclusion{
					Pattern:     pattern,
					Provider:    optionalString(provider),
					Source:      "oauth-excluded-models",
					Until:       optionalTime(until),
					UntilSource: optionalString(source),
				})
			}
		}
	}
	if policy != nil {
		for _, pattern := range policy.ExcludedModels {
			if pattern = strings.TrimSpace(pattern); pattern != "" {
				response.Exclusions = append(response.Exclusions, fleetLimitsExclusion{Pattern: pattern, Source: "key-policy"})
			}
		}
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, response)
}

func fleetLimitsModels(policy *access.KeyPolicy) []fleetLimitsModel {
	modelRegistry := registry.GetGlobalRegistry()
	models := make([]fleetLimitsModel, 0)
	for _, model := range modelRegistry.GetAvailableModels("openai") {
		id, _ := model["id"].(string)
		if id == "" || !policy.AllowsModel(id) {
			continue
		}
		providers := append([]string{}, modelRegistry.GetModelProviders(id)...)
		sort.Strings(providers)
		models = append(models, fleetLimitsModel{ID: id, Providers: providers})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func fleetLimitsCredentialFor(auth *coreauth.Auth, policy *access.KeyPolicy, labelKey []byte, now time.Time) fleetLimitsCredential {
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	credential := fleetLimitsCredential{
		Label:     fleetLimitsLabel(labelKey, provider, auth.ID),
		Provider:  provider,
		State:     "active",
		Bars:      make([]fleetLimitsBar, 0),
		Cooldowns: make([]fleetLimitsCooldown, 0),
	}
	credentialCooldown := false
	for _, view := range coreauth.CooldownSnapshotForAuth(auth, now) {
		if view.Scope == "model" && !policy.AllowsModel(view.ModelKey) {
			continue
		}
		cooldown := fleetLimitsCooldown{Scope: view.Scope, Model: optionalString(view.ModelKey), Reason: view.Reason, Until: view.RetryAt.UTC().Format(time.RFC3339)}
		if view.HTTPStatus > 0 {
			status := view.HTTPStatus
			cooldown.HTTPStatus = &status
		}
		credentialCooldown = credentialCooldown || view.Scope == "credential"
		credential.Cooldowns = append(credential.Cooldowns, cooldown)
	}
	disabled := auth.Disabled || auth.Status == coreauth.StatusDisabled
	switch {
	case disabled:
		credential.State = "disabled"
	case credentialCooldown:
		credential.State = "cooldown"
	case auth.Status == coreauth.StatusError:
		credential.State = "error"
	}

	limits := coreauth.QuotaLimitsForAuth(auth)
	credential.Plan = optionalString(limits.Plan)
	credential.sampled = len(limits.Bars) > 0
	for _, bar := range limits.Bars {
		credential.Bars = append(credential.Bars, fleetLimitsBar{
			ID:          bar.ID,
			Percent:     bar.Percent,
			Status:      optionalString(bar.Status),
			ResetsAt:    optionalTime(bar.ResetsAt),
			SampledAt:   optionalTime(bar.SampledAt),
			ResetPassed: !bar.ResetsAt.IsZero() && bar.ResetsAt.Before(now),
		})
	}
	if provider == "codex" {
		credential.Credits = &fleetLimitsCredits{}
		if limits.Credits != nil {
			credential.Credits = &fleetLimitsCredits{HasCredits: limits.Credits.HasCredits, Unlimited: limits.Credits.Unlimited, Balance: limits.Credits.Balance}
		}
	}
	credential.usable = !disabled && !credentialCooldown
	return credential
}

func addFleetLimitsSummary(summary map[string]fleetLimitsProviderSummary, credential fleetLimitsCredential) {
	providerSummary, exists := summary[credential.Provider]
	if !exists {
		providerSummary = fleetLimitsProviderSummary{Bars: make(map[string]fleetLimitsSummaryBar)}
	}
	if credential.usable {
		providerSummary.UsableCredentials++
		lowerTo := func(id string, percent float64, resetsAt *string) {
			if current, seen := providerSummary.Bars[id]; !seen || percent < current.Percent {
				providerSummary.Bars[id] = fleetLimitsSummaryBar{Percent: percent, ResetsAt: resetsAt}
			}
		}
		hasSession := false
		for _, bar := range credential.Bars {
			hasSession = hasSession || bar.ID == "session_5h"
			if bar.Percent == nil {
				continue
			}
			percent := *bar.Percent
			if bar.ResetPassed {
				percent = 0
			}
			lowerTo(bar.ID, percent, bar.ResetsAt)
		}
		// A sampled plan without a 5h window (Ultra) has no session cap at all,
		// unlike a missing weekly sample, which only means "not seen yet".
		if credential.sampled && !hasSession {
			lowerTo("session_5h", 0, nil)
		}
	}
	summary[credential.Provider] = providerSummary
}

func fleetLimitsLabelKey(cfg *config.Config) []byte {
	if cfg != nil {
		if secret := strings.TrimSpace(cfg.RemoteManagement.SecretKey); secret != "" {
			sum := sha256.Sum256([]byte("fleet-limits-label\x00" + secret))
			return sum[:]
		}
	}
	return fleetLimitsProcessLabelKey
}

func fleetLimitsLabel(key []byte, provider, authID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(provider + "\x00" + authID))
	if provider == "" {
		provider = "unknown"
	}
	return provider + "-" + hex.EncodeToString(mac.Sum(nil))[:8]
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}
