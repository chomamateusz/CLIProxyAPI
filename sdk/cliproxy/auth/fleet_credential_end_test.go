package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestParseFleetEndsAtNormalisesToUTCSeconds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"2026-12-12T23:00:00Z", "2026-12-12T23:00:00Z"},
		{" 2026-12-13T00:00:00+01:00 ", "2026-12-12T23:00:00Z"},
		{"2026-12-12T23:00:00.987Z", "2026-12-12T23:00:00Z"},
	} {
		got, err := ParseFleetEndsAt(tc.in)
		if err != nil || FormatFleetEndsAt(got) != tc.want {
			t.Fatalf("ParseFleetEndsAt(%q) = %s, %v; want %s", tc.in, FormatFleetEndsAt(got), err, tc.want)
		}
	}
	for _, in := range []string{"", "2026-12-12", "12/12/2026 23:00", "tomorrow"} {
		if _, err := ParseFleetEndsAt(in); err == nil {
			t.Fatalf("ParseFleetEndsAt(%q) accepted an invalid value", in)
		}
	}
}

func TestFleetCredentialEndedAtBoundary(t *testing.T) {
	t.Parallel()
	end := time.Date(2026, 12, 12, 23, 0, 0, 0, time.UTC)
	auth := &Auth{Metadata: map[string]any{MetadataFleetEndsAt: FormatFleetEndsAt(end), MetadataFleetEndsAtSource: FleetEndSourcePlanEnded}}
	if FleetCredentialEnded(auth, end.Add(-time.Second)) {
		t.Fatal("ended one second before the end")
	}
	if !FleetCredentialEnded(auth, end) || !FleetCredentialEnded(auth, end.Add(time.Hour)) {
		t.Fatal("not ended at or after the end")
	}
	for _, metadata := range []map[string]any{nil, {}, {MetadataFleetEndsAt: nil}, {MetadataFleetEndsAt: ""}, {MetadataFleetEndsAt: "garbage"}, {MetadataFleetEndsAt: 123}, {MetadataFleetEndsAt: FormatFleetEndsAt(end)}} {
		if FleetCredentialEnded(&Auth{Metadata: metadata}, end.Add(time.Hour)) {
			t.Fatalf("metadata %#v counted as ended", metadata)
		}
	}
}

func fleetEndAuth(id, provider string, endsAt time.Time) *Auth {
	auth := &Auth{ID: id, Provider: provider, Metadata: map[string]any{}}
	if !endsAt.IsZero() {
		auth.Metadata[MetadataFleetEndsAt] = FormatFleetEndsAt(endsAt)
		auth.Metadata[MetadataFleetEndsAtSource] = FleetEndSourceCodexAPI
	}
	return auth
}

func TestManagerNeverPicksEndedCredential(t *testing.T) {
	selectors := map[string]func() Selector{
		"round-robin": func() Selector { return &RoundRobinSelector{} },
		"fill-first":  func() Selector { return &FillFirstSelector{} },
		"use-or-lose": func() Selector { return &UseOrLoseSelector{DrainEndingCredentials: true} },
		"use-or-lose-affinity": func() Selector {
			return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &UseOrLoseSelector{DrainEndingCredentials: true}})
		},
	}
	for name, newSelector := range selectors {
		for _, path := range []string{"single", "mixed"} {
			t.Run(name+"/"+path, func(t *testing.T) {
				now := time.Now()
				model := "fleet-end-model-" + name + "-" + path
				ended := fmt.Sprintf("%s/%s/a-ended", name, path)
				future := fmt.Sprintf("%s/%s/b-future", name, path)
				registerSchedulerModels(t, "claude", model, ended, future)
				selector := newSelector()
				if stoppable, ok := selector.(StoppableSelector); ok {
					t.Cleanup(stoppable.Stop)
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
				for _, auth := range []*Auth{fleetEndAuth(ended, "claude", now.Add(-time.Minute)), fleetEndAuth(future, "claude", now.Add(time.Hour))} {
					if _, err := manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
				}
				pick := func() (*Auth, error) {
					opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"fleet-end"}`), Metadata: map[string]any{}}
					if path == "mixed" {
						picked, _, _, err := manager.pickNextMixed(context.Background(), []string{"claude"}, model, opts, nil)
						return picked, err
					}
					picked, _, err := manager.pickNext(context.Background(), "claude", model, opts, nil)
					return picked, err
				}
				for range 4 {
					picked, err := pick()
					if err != nil || picked == nil || picked.ID != future {
						t.Fatalf("picked %v, %v; want %s", picked, err, future)
					}
				}
				current, _ := manager.GetByID(future)
				current.Metadata[MetadataFleetEndsAt] = FormatFleetEndsAt(now.Add(-time.Second))
				if _, err := manager.Update(context.Background(), current); err != nil {
					t.Fatal(err)
				}
				if picked, err := pick(); err == nil {
					t.Fatalf("picked %s although every credential has ended", picked.ID)
				}
				if manager.HasProviderAuth("claude") || len(manager.AvailableProviders()) != 0 {
					t.Fatal("ended credentials still count as provider auth")
				}
			})
		}
	}
}

func TestQuotaSamplerSkipsEndedCredential(t *testing.T) {
	t.Parallel()
	now := time.Now()
	auth := fleetEndAuth("sampled", "claude", now.Add(time.Hour))
	auth.Metadata["access_token"] = "token"
	if !quotaSampleEligible(auth) {
		t.Fatal("credential before its end is not sampled")
	}
	auth.Metadata[MetadataFleetEndsAt] = FormatFleetEndsAt(now.Add(-time.Second))
	if quotaSampleEligible(auth) {
		t.Fatal("ended credential is still sampled")
	}
}

func TestUseOrLoseDrainsEndingCredential(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		drain bool
		endIn time.Duration
		want  string
	}{
		{"ending credential drains first", true, 24 * time.Hour, "ending"},
		{"switch off ranks by reset only", false, 24 * time.Hour, "steady"},
		{"end after the reset changes nothing", true, 6 * 24 * time.Hour, "steady"},
		{"end inside the steady tie hour prefers lower use", true, 3*24*time.Hour + 30*time.Minute, "steady"},
		{"overdue end keeps drain priority", true, -time.Hour, "ending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steady := useOrLoseTestAuth("steady", now, 3*24*time.Hour, .5)
			ending := useOrLoseTestAuth("ending", now, 5*24*time.Hour, .6)
			ending.Metadata = map[string]any{MetadataFleetEndsAt: FormatFleetEndsAt(now.Add(tc.endIn)), MetadataFleetEndsAtSource: FleetEndSourceManual}
			selector := &UseOrLoseSelector{ShortWindowCapPct: 95, DrainEndingCredentials: tc.drain}
			got, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", []*Auth{steady, ending}, now)
			if err != nil || got.ID != tc.want {
				t.Fatalf("picked %v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestUpdateUseOrLoseSelectorCarriesDrainSwitch(t *testing.T) {
	t.Parallel()
	current := &UseOrLoseSelector{ShortWindowCapPct: 95, DrainEndingCredentials: true}
	if !updateUseOrLoseSelector(current, &UseOrLoseSelector{ShortWindowCapPct: 80, DrainEndingCredentials: false}) {
		t.Fatal("use-or-lose selector was replaced instead of updated")
	}
	if current.drainsEndingCredentials() || current.effectiveShortWindowCapPct() != 80 {
		t.Fatalf("updated selector = %+v; want cap 80 without drain", current)
	}
}

func TestMergeRefreshedAuthKeepsFleetEndsAt(t *testing.T) {
	t.Parallel()
	base := &Auth{ID: "c", Provider: "claude", Metadata: map[string]any{"access_token": "old", MetadataFleetEndsAt: "2026-12-12T23:00:00Z", MetadataFleetEndsAtSource: FleetEndSourceClaudeAnniversary, MetadataFleetCancelledAt: "2026-11-15T10:00:00Z"}}
	updated := base.Clone()
	updated.Metadata["access_token"] = "new"
	merged := MergeRefreshedAuth(base, base.Clone(), updated)
	if merged.Metadata[MetadataFleetEndsAt] != "2026-12-12T23:00:00Z" || merged.Metadata[MetadataFleetEndsAtSource] != FleetEndSourceClaudeAnniversary || merged.Metadata[MetadataFleetCancelledAt] != "2026-11-15T10:00:00Z" || merged.Metadata["access_token"] != "new" {
		t.Fatalf("refresh merge metadata = %#v", merged.Metadata)
	}
	editedDuringRefresh := base.Clone()
	editedDuringRefresh.Metadata[MetadataFleetEndsAt] = "2027-01-01T00:00:00Z"
	merged = MergeRefreshedAuth(base, editedDuringRefresh, updated)
	if merged.Metadata[MetadataFleetEndsAt] != "2027-01-01T00:00:00Z" {
		t.Fatalf("refresh overwrote an end date set during the refresh: %#v", merged.Metadata)
	}
	clearedDuringRefresh := base.Clone()
	delete(clearedDuringRefresh.Metadata, MetadataFleetEndsAt)
	merged = MergeRefreshedAuth(base, clearedDuringRefresh, updated)
	if _, ok := merged.Metadata[MetadataFleetEndsAt]; ok {
		t.Fatalf("refresh restored an end date cleared during the refresh: %#v", merged.Metadata)
	}
}
