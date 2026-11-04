package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const oauthOrganizationErrorBody = `{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."}}`

func TestOAuthOrganizationRefusalMatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		status   int
		message  string
		want     bool
	}{
		{"exact forbidden", "claude", 403, oauthOrganizationErrorBody, true},
		{"unauthorized uppercase", "CLAUDE", 401, strings.ToUpper(oauthOrganizationErrorBody), true},
		{"different forbidden", "claude", 403, `{"error":{"message":"Permission denied"}}`, false},
		{"near match", "claude", 403, "OAuth authentication is not allowed for this organization", false},
		{"payment", "claude", 402, oauthOrganizationErrorBody, false},
		{"server failure", "claude", 500, oauthOrganizationErrorBody, false},
		{"different provider", "codex", 403, oauthOrganizationErrorBody, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOAuthOrganizationRefusal(&Auth{Provider: tc.provider}, &Error{HTTPStatus: tc.status, Message: tc.message}); got != tc.want {
				t.Fatalf("match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOAuthOrganizationOtherErrorsKeepCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	for _, status := range []int{401, 402, 403} {
		for _, model := range []string{"", "claude-health-test"} {
			t.Run(http.StatusText(status)+"/"+model, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				id := "health-ordinary-error"
				if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "claude", Status: StatusActive}); err != nil {
					t.Fatal(err)
				}
				manager.SetCredentialHealthHook(func(context.Context, CredentialHealthEvent) { t.Error("unexpected auto-disable event") })
				before := time.Now()
				manager.MarkResult(context.Background(), Result{AuthID: id, Model: model, Error: &Error{HTTPStatus: status, Message: `{"error":{"message":"Permission denied"}}`}})
				after := time.Now()
				auth, _ := manager.GetByID(id)
				if auth.Disabled || auth.Status == StatusDisabled {
					t.Fatal("ordinary error disabled credential")
				}
				deadline := auth.NextRetryAfter
				if model != "" {
					deadline = auth.ModelStates[model].NextRetryAfter
				}
				if deadline.Before(before.Add(30*time.Minute)) || deadline.After(after.Add(30*time.Minute)) {
					t.Fatalf("cooldown deadline = %v, want 30 minutes", deadline)
				}
				if model == "" && status != 401 && auth.StatusMessage != "payment_required" {
					t.Fatalf("status message = %q, want payment_required", auth.StatusMessage)
				}
			})
		}
	}
}

func TestOAuthOrganizationAutoDisableIgnoresLaterInflightResults(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	id := "health-inflight"
	if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "claude", Status: StatusActive, Metadata: map[string]any{"refresh_token": "test-refresh"}}); err != nil {
		t.Fatal(err)
	}
	events := 0
	manager.SetCredentialHealthHook(func(context.Context, CredentialHealthEvent) { events++ })
	result := Result{AuthID: id, Model: "claude-health-test", Error: &Error{HTTPStatus: 401, Message: oauthOrganizationErrorBody}}
	manager.MarkResult(context.Background(), result)
	for _, later := range []Result{
		result,
		{AuthID: id, Model: result.Model, Success: true},
		{AuthID: id, Success: true},
		{AuthID: id, Model: result.Model, Error: &Error{HTTPStatus: 403, Message: "forbidden"}},
		{AuthID: id, Error: &Error{HTTPStatus: 401, Message: "unauthorized"}},
	} {
		manager.MarkResult(context.Background(), later)
		auth, _ := manager.GetByID(id)
		if !auth.Disabled || auth.Status != StatusDisabled || auth.StatusMessage != oauthOrganizationDisabledMessage || !auth.NextRefreshAfter.IsZero() || auth.RejectedAccessToken != "" {
			t.Fatalf("in-flight result changed disabled state: %+v", auth)
		}
	}
	if events != 1 {
		t.Fatalf("health events = %d, want 1", events)
	}
}

func TestOAuthOrganizationAutoDisableReportsPersistenceFailure(t *testing.T) {
	manager := NewManager(&persistFailureStore{}, nil, nil)
	id := "health-persist-failure"
	if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "claude", Metadata: map[string]any{"type": "claude"}}); err != nil {
		t.Fatal(err)
	}
	var event *CredentialHealthEvent
	manager.SetCredentialHealthHook(func(_ context.Context, got CredentialHealthEvent) { event = &got })
	manager.MarkResult(context.Background(), Result{AuthID: id, Error: &Error{HTTPStatus: 403, Message: oauthOrganizationErrorBody}})
	auth, _ := manager.GetByID(id)
	if !auth.Disabled || event == nil || event.PersistenceError == nil {
		t.Fatalf("persistence failure was silent or auth remained available: event=%+v auth=%+v", event, auth)
	}
}

func TestOAuthOrganizationStaleResultDoesNotDisableReplacement(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	original, err := manager.Register(context.Background(), &Auth{ID: "health-replaced", Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(context.Background(), &Auth{ID: original.ID, Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	manager.SetCredentialHealthHook(func(context.Context, CredentialHealthEvent) { t.Error("unexpected stale auto-disable event") })
	manager.MarkResult(context.Background(), Result{AuthID: original.ID, RegistrationEpoch: original.RegistrationEpoch, Error: &Error{HTTPStatus: 403, Message: oauthOrganizationErrorBody}})
	auth, _ := manager.GetByID(original.ID)
	if auth.Disabled {
		t.Fatal("stale result disabled replacement")
	}
}

func TestOAuthOrganizationAutoDisableRetriesEligibleCredential(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager(nil, quotaAttemptIsolationSelector{}, nil)
			model := "claude-health-failover-" + mode
			firstID, secondID := "health-"+mode+"-a", "health-"+mode+"-b"
			var attempts []string
			attempt := func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				attempts = append(attempts, auth.ID)
				if auth.ID == firstID {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: 403, Message: oauthOrganizationErrorBody}
				}
				return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
			}
			manager.RegisterExecutor(&issue6865Executor{
				id: "claude", executeFn: attempt, countFn: attempt,
				streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if _, err := attempt(ctx, auth, req, opts); err != nil {
						return nil, err
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok\n\n")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
			})
			for _, id := range []string{firstID, secondID} {
				if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "claude", Status: StatusActive}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			req := cliproxyexecutor.Request{Model: model}
			var err error
			switch mode {
			case "execute":
				_, err = manager.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
			case "count":
				_, err = manager.ExecuteCount(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
			case "stream":
				var stream *cliproxyexecutor.StreamResult
				stream, err = manager.ExecuteStream(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{Stream: true})
				if err == nil {
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				}
			}
			if err != nil || len(attempts) != 2 || attempts[0] != firstID || attempts[1] != secondID {
				t.Fatalf("eligible credential failover failed: attempts=%v err=%v", attempts, err)
			}
			auth, _ := manager.GetByID(firstID)
			if !auth.Disabled {
				t.Fatal("failed credential was not disabled")
			}
		})
	}
}
