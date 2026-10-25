package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCredentialFilterSelection(t *testing.T) {
	for _, path := range []string{"scheduler", "legacy", "mixed", "mixed legacy"} {
		t.Run(path, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.RegisterExecutor(schedulerTestExecutor{provider: "gemini"})
			manager.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
			for _, candidate := range []*Auth{{ID: "blocked", Provider: "gemini"}, {ID: "allowed", Provider: "gemini"}, {ID: "other", Provider: "claude"}} {
				if _, err := manager.Register(context.Background(), candidate); err != nil {
					t.Fatal(err)
				}
			}
			pick := func(ctx context.Context, opts cliproxyexecutor.Options) (*Auth, error) {
				switch path {
				case "scheduler":
					auth, _, err := manager.pickNext(ctx, "gemini", "", opts, nil)
					return auth, err
				case "legacy":
					auth, _, err := manager.pickNextLegacy(ctx, "gemini", "", opts, nil)
					return auth, err
				case "mixed":
					auth, _, _, err := manager.pickNextMixed(ctx, []string{"gemini", "claude"}, "", opts, nil)
					return auth, err
				default:
					auth, _, _, err := manager.pickNextMixedLegacy(ctx, []string{"gemini", "claude"}, "", opts, nil)
					return auth, err
				}
			}
			for _, tc := range []struct {
				name, allowed, pinned string
				want                  bool
			}{
				{"one allowed", "allowed", "", true},
				{"all filtered", "missing", "", false},
				{"pin cannot bypass", "allowed", "blocked", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := WithCredentialFilter(context.Background(), func(a *Auth) bool { return a.ID == tc.allowed })
					opts := cliproxyexecutor.Options{}
					if tc.pinned != "" {
						opts.Metadata = map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: tc.pinned}
					}
					got, err := pick(ctx, opts)
					if tc.want {
						if err != nil || got == nil || got.ID != tc.allowed {
							t.Fatalf("selection failed: %v", err)
						}
					} else {
						var authErr *Error
						if got != nil || !errors.As(err, &authErr) || (authErr.Code != "auth_not_found" && authErr.Code != "auth_unavailable") {
							t.Fatalf("expected no eligible auth, got error %v", err)
						}
					}
				})
			}
		})
	}
}

func TestCredentialFilterSessionAffinity(t *testing.T) {
	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	defer selector.Stop()
	auths := []*Auth{{ID: "bound", Provider: "gemini"}, {ID: "allowed", Provider: "gemini"}}
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"policy-session"}`)}
	ctx := context.Background()
	first, err := selector.Pick(ctx, "gemini", "", opts, auths[:1])
	if err != nil || first == nil || first.ID != "bound" {
		t.Fatalf("initial selection: %v", err)
	}
	ctx = WithCredentialFilter(ctx, func(a *Auth) bool { return a.ID != "bound" })
	got, err := selector.Pick(ctx, "gemini", "", opts, auths)
	if err != nil || got == nil || got.ID != "allowed" {
		t.Fatalf("filtered sticky selection: %v", err)
	}
	ctx = WithCredentialFilter(ctx, func(a *Auth) bool { return a.ID == "bound" })
	if got, err = selector.Pick(ctx, "gemini", "", opts, auths); got != nil || err == nil {
		t.Fatal("composed filters must deny all candidates")
	}
}

func TestCredentialFilterHomeSelection(t *testing.T) {
	dispatcher := &fixtureHomeDispatcher{payload: []byte(`{"concurrency":{"accounted":true,"credential_id":"cred-1","model":"gpt"},"auth":{"id":"cred-1","provider":"codex"}}`)}
	manager := newHomeSelectionTestManager(t, dispatcher)
	ctx := WithCredentialFilter(context.Background(), func(*Auth) bool { return false })
	selection, err := manager.pickHomeDispatchSelection(ctx, "gpt", cliproxyexecutor.Options{})
	var authErr *Error
	if selection != nil || !errors.As(err, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatal("Home selection bypassed credential filter")
	}
	if got := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now()); len(got.Executions) != 0 {
		t.Fatal("filtered Home dispatch leaked execution scope")
	}
}
