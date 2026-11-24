package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"

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
	filenameDispatcher := &fixtureHomeDispatcher{payload: []byte(`{"concurrency":{"accounted":true,"credential_id":"work.json","model":"gpt"},"auth":{"id":"work.json","provider":"codex","label":"unrelated"}}`)}
	filenameManager := newHomeSelectionTestManager(t, filenameDispatcher)
	policyCtx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{ExcludedCredentials: []string{"work"}})
	selection, err = filenameManager.pickHomeDispatchSelection(policyCtx, "gpt", cliproxyexecutor.Options{})
	if selection != nil || !errors.As(err, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatal("Home filename stem bypassed credential policy")
	}
}

type policyRetryHomeDispatcher struct {
	fixtureHomeDispatcher
	excluded [][]string
}

func (d *policyRetryHomeDispatcher) RPopAuthWithRetryRoundConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, _ int, excluded []string, _ string) ([]byte, error) {
	d.excluded = append(d.excluded, append([]string(nil), excluded...))
	id := "blocked"
	if len(excluded) > 0 {
		id = "allowed"
	}
	return []byte(`{"concurrency":{"accounted":true,"credential_id":"` + id + `","model":"gpt"},"auth_index":"` + id + `","auth":{"id":"` + id + `","provider":"codex"}}`), nil
}

func TestHomePolicyRepopExcludesRejectedCredential(t *testing.T) {
	dispatcher := &policyRetryHomeDispatcher{}
	manager := newHomeSelectionTestManager(t, dispatcher)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	ctx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Credentials: []string{"allowed"}})
	selection, err := manager.pickHomeDispatchSelection(ctx, "gpt", cliproxyexecutor.Options{})
	if err != nil || selection == nil || selection.CloneAuth().ID != "allowed" {
		t.Fatalf("Home selection = %v, %v", selection, err)
	}
	selection.End("test_complete")
	if len(dispatcher.excluded) != 2 || len(dispatcher.excluded[1]) != 1 || dispatcher.excluded[1][0] != "blocked" {
		t.Fatalf("Home exclusions = %v", dispatcher.excluded)
	}
}

type manyPolicyRetryHomeDispatcher struct {
	fixtureHomeDispatcher
	seen int
}

func (d *manyPolicyRetryHomeDispatcher) RPopAuthWithRetryRoundConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, _ int, excluded []string, _ string) ([]byte, error) {
	d.seen++
	id := "blocked-" + strconv.Itoa(len(excluded))
	if len(excluded) == 17 {
		id = "allowed"
	}
	return []byte(`{"concurrency":{"accounted":true,"credential_id":"` + id + `","model":"gpt"},"auth_index":"` + id + `","auth":{"id":"` + id + `","provider":"codex"}}`), nil
}

func TestHomePolicyRepopBeyondSixteenCredentials(t *testing.T) {
	dispatcher := &manyPolicyRetryHomeDispatcher{}
	manager := newHomeSelectionTestManager(t, dispatcher)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	ctx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Credentials: []string{"allowed"}})
	selection, err := manager.pickHomeDispatchSelection(ctx, "gpt", cliproxyexecutor.Options{})
	if err != nil || selection == nil || selection.CloneAuth().ID != "allowed" || dispatcher.seen != 18 {
		t.Fatalf("Home selection = %v, %v; dispatches = %d", selection, err, dispatcher.seen)
	}
	selection.End("test_complete")
}

func TestPolicyFiltersResolvedExecutionModels(t *testing.T) {
	ctx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Models: []string{"gpt-*"}, ExcludedModels: []string{"gpt-5"}})
	models := filterPolicyExecutionModels(ctx, "alias", []string{"gpt-5(high)", "gpt-6", "claude-opus"})
	if len(models) != 1 || models[0] != "gpt-6" {
		t.Fatalf("resolved model filtering = %v", models)
	}
	ctx = access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Models: []string{"claude-fable-5-dd-4.5-tpg"}, ExcludedModels: []string{"claude-opus*"}})
	models = filterPolicyExecutionModels(ctx, "claude-fable-5-dd-4.5-tpg", []string{"gpt-5.4", "claude-opus-4-6"})
	if len(models) != 1 || models[0] != "gpt-5.4" {
		t.Fatalf("exact alias filtering = %v", models)
	}
}

func TestPolicyAllowsExactClientAliasThroughConductor(t *testing.T) {
	const alias, target, id = "claude-fable-5-dd-4.5-tpg", "gpt-5.4", "alias-policy-auth"
	executor := &forceMappingExecutor{id: "codex"}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: target}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Models: []string{alias}})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: alias}}
	if _, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: target}, opts); err != nil {
		t.Fatalf("Execute() rejected exact client alias: %v", err)
	}
	if models := executor.ExecuteModels(); len(models) != 1 || models[0] != target {
		t.Fatalf("executed models = %v", models)
	}
	t.Run("home", func(t *testing.T) {
		const homeAlias, homeID = "team-gpt", "home-alias-policy-auth"
		manager := newHomeSelectionTestManager(t, &fixtureHomeDispatcher{payload: []byte(`{"auth":{"id":"home-alias-policy-auth","provider":"codex"}}`)})
		manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
		executor := &forceMappingExecutor{id: "codex"}
		manager.RegisterExecutor(executor)
		registry.GetGlobalRegistry().RegisterClient(homeID, "codex", []*registry.ModelInfo{{ID: target}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(homeID) })
		ctx := access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Models: []string{homeAlias}})
		opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: homeAlias}}
		if _, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: target}, opts); err != nil {
			t.Fatalf("Execute() rejected exact client alias: %v", err)
		}
		if models := executor.ExecuteModels(); len(models) != 1 || models[0] != target {
			t.Fatalf("executed models = %v", models)
		}
	})
}
