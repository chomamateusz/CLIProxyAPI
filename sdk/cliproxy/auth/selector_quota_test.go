package auth

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestQuotaSelectorsPick(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	sampled := func(id, used string, resetIn time.Duration) *Auth {
		return &Auth{ID: id, Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent": used,
			"X-Codex-Primary-Reset-At":     strconv.FormatInt(now.Add(resetIn).Unix(), 10),
		}}}
	}
	for _, tc := range []struct {
		name         string
		auths        func() []*Auth
		websocket    bool
		nearestReset string
		mostUsed     string
		errorCode    string
	}{
		{
			name: "primary ordering",
			auths: func() []*Auth {
				return []*Auth{sampled("a", "90", 3*time.Hour), sampled("b", "20", time.Hour), sampled("c", "50", 2*time.Hour)}
			},
			nearestReset: "b", mostUsed: "a",
		},
		{
			name: "reset tie prefers utilisation",
			auths: func() []*Auth {
				return []*Auth{sampled("a", "20", time.Hour), sampled("b", "80", time.Hour)}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "utilisation tie prefers reset",
			auths: func() []*Auth {
				return []*Auth{sampled("a", "80", 2*time.Hour), sampled("b", "80", time.Hour)}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "equal samples preserve input order",
			auths: func() []*Auth {
				return []*Auth{sampled("z", "80", time.Hour), sampled("a", "80", time.Hour)}
			},
			nearestReset: "z", mostUsed: "z",
		},
		{
			name: "unsampled tail",
			auths: func() []*Auth {
				return []*Auth{{ID: "a", Provider: "codex"}, sampled("b", "0", time.Hour), {ID: "c", Provider: "codex"}}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "all unsampled preserve input order",
			auths: func() []*Auth {
				return []*Auth{{ID: "z", Provider: "codex"}, {ID: "a", Provider: "codex"}}
			},
			nearestReset: "z", mostUsed: "z",
		},
		{
			name: "unsupported provider",
			auths: func() []*Auth {
				return []*Auth{{ID: "z", Provider: "gemini"}, {ID: "a", Provider: "gemini"}}
			},
			nearestReset: "z", mostUsed: "z",
		},
		{
			name: "credential cooldown",
			auths: func() []*Auth {
				blocked := sampled("a", "90", time.Hour)
				blocked.Quota.Exceeded = true
				blocked.Quota.NextRecoverAt = now.Add(time.Hour)
				return []*Auth{blocked, sampled("b", "20", 2*time.Hour)}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "model cooldown",
			auths: func() []*Auth {
				blocked := sampled("a", "90", time.Hour)
				blocked.ModelStates = map[string]*ModelState{"model": {Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}}}
				return []*Auth{blocked, sampled("b", "20", 2*time.Hour)}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "at limit",
			auths: func() []*Auth {
				return []*Auth{sampled("a", "100", time.Hour), sampled("b", "20", 2*time.Hour)}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "claude rejected",
			auths: func() []*Auth {
				blocked := &Auth{ID: "a", Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
					"Anthropic-Ratelimit-Unified-5h-Utilization": "0.9",
					"Anthropic-Ratelimit-Unified-5h-Status":      "rejected",
					"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
				}}}
				return []*Auth{blocked, {ID: "b", Provider: "claude"}}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "disabled and nil",
			auths: func() []*Auth {
				blocked := sampled("a", "90", time.Hour)
				blocked.Disabled = true
				return []*Auth{nil, blocked, sampled("b", "20", 2*time.Hour)}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "priority before quota ordering",
			auths: func() []*Auth {
				preferred := sampled("b", "20", 2*time.Hour)
				preferred.Attributes = map[string]string{"priority": "10"}
				return []*Auth{sampled("a", "90", time.Hour), preferred}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "exhausted priority falls back",
			auths: func() []*Auth {
				blocked := sampled("a", "100", time.Hour)
				blocked.Attributes = map[string]string{"priority": "10"}
				return []*Auth{blocked, sampled("b", "20", 2*time.Hour)}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "elapsed reset is unsampled",
			auths: func() []*Auth {
				return []*Auth{sampled("a", "60", 10*time.Minute), sampled("b", "90", -time.Hour)}
			},
			nearestReset: "a", mostUsed: "a",
		},
		{
			name: "rfc reset is sampled",
			auths: func() []*Auth {
				first := sampled("a", "60", 2*time.Hour)
				second := sampled("b", "70", 3*time.Hour)
				second.Quota.Signals["X-Codex-Primary-Reset-At"] = now.Add(time.Hour).Format(time.RFC3339)
				return []*Auth{first, second}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "websocket preference", websocket: true,
			auths: func() []*Auth {
				preferred := sampled("b", "20", 2*time.Hour)
				preferred.Attributes = map[string]string{"websockets": "true"}
				return []*Auth{sampled("a", "90", time.Hour), preferred}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name: "exhausted websocket falls back", websocket: true,
			auths: func() []*Auth {
				blocked := sampled("a", "100", time.Hour)
				blocked.Attributes = map[string]string{"websockets": "true"}
				return []*Auth{blocked, sampled("b", "20", 2*time.Hour)}
			},
			nearestReset: "b", mostUsed: "b",
		},
		{
			name:      "all at limit",
			auths:     func() []*Auth { return []*Auth{sampled("a", "100", time.Hour)} },
			errorCode: "auth_unavailable",
		},
		{
			name:      "empty",
			auths:     func() []*Auth { return nil },
			errorCode: "auth_not_found",
		},
	} {
		for _, strategy := range []struct {
			name     string
			selector Selector
			want     string
		}{
			{name: "nearest-reset", selector: &NearestResetSelector{}, want: tc.nearestReset},
			{name: "most-used", selector: &MostUsedSelector{}, want: tc.mostUsed},
		} {
			t.Run(strategy.name+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				if tc.websocket {
					ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
				}
				auths := tc.auths()
				before := slices.Clone(auths)
				for i, auth := range before {
					if auth != nil {
						before[i] = auth.Clone()
					}
				}
				provider := "codex"
				if len(auths) > 0 && auths[0] != nil {
					provider = auths[0].Provider
				}
				got, errPick := strategy.selector.Pick(ctx, provider, "model", cliproxyexecutor.Options{}, auths)
				if !reflect.DeepEqual(auths, before) {
					t.Fatal("Pick mutated its input")
				}
				if tc.errorCode != "" {
					var authErr *Error
					if !errors.As(errPick, &authErr) || authErr.Code != tc.errorCode || got != nil {
						t.Fatalf("Pick() = %v, %v, want error %s", got, errPick, tc.errorCode)
					}
					return
				}
				if errPick != nil || got == nil || got.ID != strategy.want {
					t.Fatalf("Pick() = %v, %v, want %s", got, errPick, strategy.want)
				}
			})
		}
	}
}

func TestQuotaSessionAffinityRebindsAtLimitCredential(t *testing.T) {
	selector := NewSessionAffinitySelector(&NearestResetSelector{})
	defer selector.Stop()
	options := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"quota-bound-session"}`)}
	now := time.Now().Truncate(time.Second)
	bound := &Auth{ID: "bound", Provider: "codex"}
	other := &Auth{ID: "other", Provider: "codex"}
	first, err := selector.Pick(context.Background(), "codex", "model", options, []*Auth{bound, other})
	if err != nil || first == nil || first.ID != "bound" {
		t.Fatalf("initial affinity selection = %v, %v", first, err)
	}
	bound.Quota = QuotaState{ObservedAt: now, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-At": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}}
	selected, err := selector.Pick(context.Background(), "codex", "model", options, []*Auth{bound, other})
	if err != nil || selected == nil || selected.ID != "other" {
		t.Fatalf("affinity retained exhausted credential: %v, %v", selected, err)
	}
}

func TestQuotaSelectorsKeepPrevalidatedAliasAndModelSample(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, selector := range []Selector{&NearestResetSelector{}, &MostUsedSelector{}} {
		if !isBuiltInSelector(selector) || selectionArgForSelector(selector, "client-alias") != "client-alias" {
			t.Fatal("quota selector lost prevalidated alias handling")
		}
		candidate := &Auth{ID: "candidate", Provider: "codex", ModelStates: map[string]*ModelState{
			"client-alias": {Quota: QuotaState{ObservedAt: now, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-At": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}}},
		}}
		ctx := selectorContextForAvailableAuths(context.Background(), selector, "client-alias")
		selected, err := selector.Pick(ctx, "codex", selectionArgForSelector(selector, "client-alias"), cliproxyexecutor.Options{}, []*Auth{candidate})
		if selected != nil || err == nil {
			t.Fatalf("model-specific quota was ignored: %v, %v", selected, err)
		}
	}
}

func TestManagerPassesLowerPrioritiesToQuotaSelector(t *testing.T) {
	now := time.Now()
	high := &Auth{ID: "high", Provider: "codex", Attributes: map[string]string{"priority": "10"}}
	low := &Auth{ID: "low", Provider: "codex"}
	manager := NewManager(nil, &NearestResetSelector{}, nil)
	priority, candidates, err := manager.availableAuthsForSelector(&NearestResetSelector{}, []*Auth{high, low}, "codex", "model", now, false)
	if err != nil || len(priority) != 1 || priority[0].ID != "high" || len(candidates) != 2 {
		t.Fatalf("quota selector candidates: priority=%v all=%v err=%v", priority, candidates, err)
	}
}
