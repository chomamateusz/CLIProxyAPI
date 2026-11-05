package cliproxy

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestUseOrLoseServiceCapReloadPreservesExplicitAndLCPBindings(t *testing.T) {
	for _, lcp := range []bool{false, true} {
		name := "explicit"
		if lcp {
			name = "lcp"
		}
		t.Run(name, func(t *testing.T) {
			cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "use-or-lose", SessionAffinity: true}}
			service := &Service{coreManager: coreauth.NewManager(nil, nil, nil)}
			if !service.applyManagerConfig(context.Background(), configCommit{cfg: cfg}) {
				t.Fatal("initial config failed")
			}
			selector := service.coreManager.Selector().(*coreauth.SessionAffinitySelector)
			defer selector.Stop()
			now := time.Now()
			a := &coreauth.Auth{ID: "bound", Provider: "claude", Quota: coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": ".2",
				"Anthropic-Ratelimit-Unified-7d-Reset":       now.Add(24 * time.Hour).Format(time.RFC3339),
				"Anthropic-Ratelimit-Unified-5h-Utilization": ".85",
				"Anthropic-Ratelimit-Unified-5h-Reset":       now.Add(time.Hour).Format(time.RFC3339),
			}}}
			b := &coreauth.Auth{ID: "other", Provider: "claude", Quota: coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": ".2",
				"Anthropic-Ratelimit-Unified-7d-Reset":       now.Add(48 * time.Hour).Format(time.RFC3339),
			}}}
			opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"cap-reload"}`), Metadata: map[string]any{}}
			if lcp {
				opts.SourceFormat = sdktranslator.FormatOpenAI
				opts.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"preserve across hot reload"}]}`)
				opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = t.Name()
			}
			pick := func(want string) {
				t.Helper()
				got, err := service.coreManager.Selector().Pick(context.Background(), "claude", "claude-opus-5-5", opts, []*coreauth.Auth{a, b})
				if err != nil || got == nil || got.ID != want {
					t.Fatalf("pick = %v, %v; want %s", got, err, want)
				}
			}
			reload := func(cap float64) {
				t.Helper()
				next := *cfg
				next.Routing.UseOrLose.ShortWindowCapPct = cap
				if !service.applyManagerConfig(context.Background(), configCommit{cfg: &next}) {
					t.Fatal("hot reload failed")
				}
				if service.coreManager.Selector() != selector {
					t.Fatal("hot reload discarded session state")
				}
			}
			pick(a.ID)
			b.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"] = now.Add(time.Hour).Format(time.RFC3339)
			reload(90)
			pick(a.ID)
			reload(80)
			pick(b.ID)
			reload(95)
			pick(b.ID)
		})
	}
}
