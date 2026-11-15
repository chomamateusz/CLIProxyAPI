package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func fleetEndModelListed(model string) bool {
	for _, listed := range registry.GetGlobalRegistry().GetAvailableModels("openai") {
		if id, _ := listed["id"].(string); id == model {
			return true
		}
	}
	return false
}

func registerFleetEndModel(t *testing.T, m *Manager, model string, auths ...*Auth) {
	t.Helper()
	for _, auth := range auths {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		ids = append(ids, auth.ID)
	}
	registerSchedulerModels(t, auths[0].Provider, model, ids...)
}

func TestEndedCredentialProjectionOverridesQuotaReason(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withSecond bool
		wantListed bool
	}{
		{"only client", false, false},
		{"second live client", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			model := "fleet-end-projection-" + tc.name
			ended := fleetEndAuth("fleet-end-projection/ended/"+tc.name, "codex", now.Add(-time.Minute))
			ended.Quota = QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}
			ended.ModelStates = map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: now.Add(time.Hour), Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}}}
			auths := []*Auth{ended}
			if tc.withSecond {
				auths = append(auths, fleetEndAuth("fleet-end-projection/live/"+tc.name, "codex", time.Time{}))
			}
			m := NewManager(nil, nil, nil)
			registerFleetEndModel(t, m, model, auths...)

			projection := m.clientModelProjectionForAuth(ended, model, now)
			if !projection.Suspended || projection.SuspendReason != fleetCredentialEndedReason {
				t.Fatalf("projection = %+v; want suspended with %q", projection, fleetCredentialEndedReason)
			}
			current, _ := m.GetByID(ended.ID)
			m.projectQuotaRecovery(current, now)
			if got := fleetEndModelListed(model); got != tc.wantListed {
				t.Fatalf("model listed = %t; want %t", got, tc.wantListed)
			}
		})
	}
}

func TestQuotaSamplerScanRepublishesEndedProjection(t *testing.T) {
	now := time.Now()
	model := "fleet-end-sampler-scan"
	m := NewManager(nil, nil, nil)
	registerFleetEndModel(t, m, model, fleetEndAuth("fleet-end-sampler-scan/a", "claude", now.Add(-time.Minute)))
	if !fleetEndModelListed(model) {
		t.Fatal("model not listed after registration")
	}
	m.quotaSampler.scan(context.Background(), now)
	if fleetEndModelListed(model) {
		t.Fatal("scan kept listing the model of an ended credential")
	}

	for _, endsAt := range []any{nil, FormatFleetEndsAt(now.Add(time.Hour))} {
		current, _ := m.GetByID("fleet-end-sampler-scan/a")
		current.Metadata[MetadataFleetEndsAt] = FormatFleetEndsAt(now.Add(-time.Minute))
		if _, err := m.Update(context.Background(), current); err != nil {
			t.Fatal(err)
		}
		m.quotaSampler.scan(context.Background(), time.Now())
		if fleetEndModelListed(model) {
			t.Fatal("model listed while the credential has ended")
		}
		current, _ = m.GetByID("fleet-end-sampler-scan/a")
		if endsAt == nil {
			delete(current.Metadata, MetadataFleetEndsAt)
		} else {
			current.Metadata[MetadataFleetEndsAt] = endsAt
		}
		if _, err := m.Update(context.Background(), current); err != nil {
			t.Fatal(err)
		}
		m.quotaSampler.scan(context.Background(), time.Now())
		if !fleetEndModelListed(model) {
			t.Fatalf("model still hidden after fleet_ends_at became %v", endsAt)
		}
	}
}

func TestQuotaSamplerWakesAtFleetEnd(t *testing.T) {
	model := "fleet-end-sampler-wake"
	m := NewManager(nil, nil, nil)
	registerFleetEndModel(t, m, model, fleetEndAuth("fleet-end-sampler-wake/a", "claude", time.Now().Add(300*time.Millisecond)))
	s := m.quotaSampler
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx, make(chan time.Time)) }()
	t.Cleanup(func() { cancel(); <-done; s.workers.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for fleetEndModelListed(model) {
		if time.Now().After(deadline) {
			t.Fatal("sampler did not republish the projection at the credential's end")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
