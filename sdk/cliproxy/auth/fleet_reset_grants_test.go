package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func resetGrantBody(grants ...string) string {
	return `{"five_hour":{"utilization":100},"cedar_ember":{"eligible":true,"at_limit":true,"grants":[` + strings.Join(grants, ",") + `],"weekly_resets_at":null,"cooldown_until":null}}`
}

func resetGrantJSON(id string, left int, clears string, extra string) string {
	grant := fmt.Sprintf(`{"id":%q,"label":"Made-up grant","resets_total":%d,"resets_left":%d,"clears":%s,"usable_now":true,"use_requires_limit":true`, id, max(left, 1), left, clears)
	if extra != "" {
		grant += "," + extra
	}
	return grant + "}"
}

func at(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func TestParseResetGrantSampleCountsUsableGrants(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	body := resetGrantBody(
		resetGrantJSON("weekly_late", 1, `["five_hour","seven_day"]`, `"ends_at":"`+at(now.Add(10*24*time.Hour))+`"`),
		resetGrantJSON("weekly_soon", 1, `["seven_day"]`, `"ends_at":"`+at(now.Add(3*24*time.Hour))+`","usable_now":false`),
		resetGrantJSON("fable_only", 2, `["seven_day_overage_included"]`, `"starts_at":"`+at(now.Add(-time.Hour))+`"`),
		resetGrantJSON("unknown_window", 1, `["mystery_window"]`, ""),
		resetGrantJSON("paused", 1, `["seven_day"]`, `"paused":true`),
		resetGrantJSON("not_started", 1, `["seven_day"]`, `"starts_at":"`+at(now.Add(time.Hour))+`"`),
		resetGrantJSON("ended", 1, `["seven_day"]`, `"ends_at":"`+at(now)+`"`),
		resetGrantJSON("used_up", 0, `["seven_day"]`, ""),
	)
	sample, err := parseResetGrantSample([]byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	weekly, _ := sample.use(resetGrantWindowWeekly, now)
	if weekly.count != 2 || !weekly.expiresAt.Equal(now.Add(3*24*time.Hour)) {
		t.Fatalf("weekly = %+v; want 2 expiring with the sooner grant", weekly)
	}
	fable, _ := sample.use(resetGrantWindowFable, now)
	if fable.count != 2 || !fable.expiresAt.IsZero() {
		t.Fatalf("fable = %+v; want 2 resets left without an end", fable)
	}
	total, _ := sample.use("", now)
	if total.count != 5 {
		t.Fatalf("total = %d; want 5 usable resets", total.count)
	}
	sample.SampledAt = now.Add(4 * 24 * time.Hour)
	later, _ := sample.use(resetGrantWindowWeekly, sample.SampledAt)
	if later.count != 2 || !later.expiresAt.Equal(now.Add(10*24*time.Hour)) {
		t.Fatalf("four days later = %+v; want the later grant and the one that has started since", later)
	}
}

func TestParseResetGrantSampleFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	good := resetGrantJSON("good", 1, `["seven_day"]`, "")
	for name, body := range map[string]string{
		"not json":          `{`,
		"block not object":  `{"cedar_ember":[]}`,
		"eligible missing":  `{"cedar_ember":{"grants":[]}}`,
		"eligible not bool": `{"cedar_ember":{"eligible":1}}`,
		"grants not array":  `{"cedar_ember":{"eligible":true,"grants":{}}}`,
		"bad cooldown":      `{"cedar_ember":{"eligible":true,"cooldown_until":"soon"}}`,
		"bad at_limit":      `{"cedar_ember":{"eligible":true,"at_limit":"yes"}}`,
		"duplicate id":      resetGrantBody(good, good),
		"bad id":            resetGrantBody(resetGrantJSON("Bad ID", 1, `["seven_day"]`, "")),
		"left above total":  resetGrantBody(`{"id":"x","resets_total":1,"resets_left":2}`),
		"negative left":     resetGrantBody(`{"id":"x","resets_total":1,"resets_left":-1}`),
		"fractional left":   resetGrantBody(`{"id":"x","resets_total":2,"resets_left":1.5}`),
		"bad starts_at":     resetGrantBody(resetGrantJSON("x", 1, `["seven_day"]`, `"starts_at":"later"`)),
		"numeric ends_at":   resetGrantBody(resetGrantJSON("x", 1, `["seven_day"]`, `"ends_at":1794153600`)),
		"clears not array":  resetGrantBody(resetGrantJSON("x", 1, `"seven_day"`, "")),
		"paused not bool":   resetGrantBody(resetGrantJSON("x", 1, `["seven_day"]`, `"paused":"no"`)),
	} {
		if _, err := parseResetGrantSample([]byte(body), now); !errors.Is(err, errResetGrantPayload) {
			t.Errorf("%s: err = %v; want a payload error", name, err)
		}
	}
	for _, body := range []string{`{}`, `{"cedar_ember":null}`} {
		if _, err := parseResetGrantSample([]byte(body), now); !errors.Is(err, errResetGrantNoBlock) {
			t.Errorf("%s: err = %v; want no block", body, err)
		}
	}
	sample, err := parseResetGrantSample([]byte(`{"cedar_ember":{"eligible":false,"ineligible_reason":"tier"}}`), now)
	if err != nil || len(sample.grants) != 0 {
		t.Fatalf("ineligible block without grants = %+v, %v", sample, err)
	}
}

func TestFleetResetGrantsViewAbsentUntilFresh(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "a", Provider: "claude"}
	if FleetResetGrantsFor(auth, now) != nil || FleetResetGrantsFor(nil, now) != nil {
		t.Fatal("view present before a sample")
	}
	auth.resetGrants = &resetGrantSample{SampledAt: now.Add(-23 * time.Hour), grants: []resetGrant{
		{resetsLeft: 1, clears: []string{resetGrantWindowFable}},
	}}
	view := FleetResetGrantsFor(auth, now)
	raw, _ := json.Marshal(view)
	if string(raw) != `{"weekly":0,"total":1,"expires_at":null,"sampled_at":"2026-11-21T13:00:00Z"}` {
		t.Fatalf("view = %s", raw)
	}
	if FleetResetGrantsFor(auth, now.Add(2*time.Hour)) != nil {
		t.Fatal("view present for a sample older than 24 hours")
	}
	auth.resetGrants.grants = append(auth.resetGrants.grants, resetGrant{resetsLeft: 1, clears: []string{resetGrantWindowWeekly}, endsAt: now.Add(48 * time.Hour)})
	if view := FleetResetGrantsFor(auth, now); view.Weekly != 1 || view.Total != 2 || view.ExpiresAt == nil || *view.ExpiresAt != "2026-11-24T12:00:00Z" {
		t.Fatalf("view with a weekly grant = %+v", view)
	}
}

func TestResetGrantSamplerSkipsIneligibleAndKeepsLastGood(t *testing.T) {
	now := time.Now()
	upstream := newSubscriptionUpstream()
	grants := resetGrantBody(resetGrantJSON("weekly", 1, `["seven_day"]`, ""))
	profile := claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max")
	for _, id := range []string{"claude-ok", "claude-disabled", "claude-ended", "claude-key"} {
		upstream.set("tok-"+id, profile, http.StatusOK)
		upstream.setGrants("tok-"+id, grants, http.StatusOK)
	}
	upstream.set("tok-claude-ended", claudeProfileBody("canceled", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	upstream.set("tok-codex", `{"is_active":true}`, http.StatusOK)
	upstream.setGrants("tok-codex", grants, http.StatusOK)
	disabled := subscriptionAuth("claude-disabled", "claude", nil)
	disabled.Disabled = true
	keyed := subscriptionAuth("claude-key", "claude", nil)
	keyed.Attributes = map[string]string{"api_key": "sk-example"}
	m := NewManager(nil, nil, nil)
	registerAuths(t, m,
		subscriptionAuth("claude-ok", "claude", nil),
		disabled,
		subscriptionAuth("claude-ended", "claude", endMetadata(now.Add(-time.Hour), FleetEndSourcePlanEnded)),
		keyed,
		subscriptionAuth("codex", "codex", nil),
	)
	m.subscriptionSampler.stagger = time.Millisecond
	startSubscriptionSampler(t, m, upstream)
	waitFor(t, "the eligible credential's grants sampled", func() bool {
		current, _ := m.GetByID("claude-ok")
		return FleetResetGrantsFor(current, time.Now()) != nil && upstream.callCount("tok-claude-ended") > 0 && upstream.callCount("tok-codex") > 0 && upstream.callCount("tok-claude-key") > 0
	})
	for _, token := range []string{"tok-claude-disabled", "tok-claude-ended", "tok-claude-key", "tok-codex"} {
		if calls := upstream.grantCallCount(token); calls != 0 {
			t.Errorf("%s: %d grant requests; want none", token, calls)
		}
	}
	current, _ := m.GetByID("claude-ok")
	good := current.resetGrants
	if view := FleetResetGrantsFor(current, time.Now()); view.Weekly != 1 || view.Total != 1 {
		t.Fatalf("sampled view = %+v", view)
	}

	for _, tc := range []struct {
		body   string
		status int
	}{
		{grants, http.StatusInternalServerError},
		{`{"cedar_ember":{"eligible":"maybe"}}`, http.StatusOK},
		{`{}`, http.StatusOK},
	} {
		upstream.setGrants("tok-claude-ok", tc.body, tc.status)
		if err := m.sampleResetGrants(context.Background(), "claude-ok"); err == nil {
			t.Fatalf("%d %s: read reported no error", tc.status, tc.body)
		}
		if current, _ = m.GetByID("claude-ok"); current.resetGrants != good {
			t.Fatalf("%d %s: failed read replaced the last good sample", tc.status, tc.body)
		}
	}

	current.Metadata["access_token"] = "tok-rotated"
	if _, err := m.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if current, _ = m.GetByID("claude-ok"); current.resetGrants != good {
		t.Fatal("token rotation dropped the grant sample")
	}
	current.Metadata["email"] = "other@example.com"
	if _, err := m.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	current.Metadata["email"] = "changed@example.com"
	if _, err := m.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if current, _ = m.GetByID("claude-ok"); current.resetGrants != nil {
		t.Fatal("account change kept the previous account's grant sample")
	}
}

func TestResetGrantPathsOnlyRead(t *testing.T) {
	model := "fleet-reset-grants-read-only"
	upstream := newSubscriptionUpstream()
	upstream.set("tok-ending", claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max"), http.StatusOK)
	upstream.setGrants("tok-ending", resetGrantBody(resetGrantJSON("ready", 2, `["five_hour","seven_day","seven_day_overage_included"]`, `"gives_back_weekly":true,"percent_used":{"seven_day":100}`)), http.StatusOK)
	now := time.Now()
	ending := subscriptionAuth("ending", "claude", endMetadata(now.Add(36*time.Hour), FleetEndSourceManual))
	ending.Quota = QuotaState{ObservedAt: now, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":       now.Add(24 * time.Hour).Format(time.RFC3339),
		"Anthropic-Ratelimit-Unified-7d-Utilization": "1",
		"Anthropic-Ratelimit-Unified-7d-Status":      "rejected",
	}}
	m := NewManager(nil, &UseOrLoseSelector{ShortWindowCapPct: 95, DrainEndingCredentials: true, DrainEndingResets: true}, nil)
	m.RegisterExecutor(schedulerTestExecutor{provider: "claude"})
	registerFleetEndModel(t, m, model, ending)
	startSubscriptionSampler(t, m, upstream)
	waitFor(t, "the ending credential's grants sampled", func() bool {
		current, _ := m.GetByID("ending")
		return FleetResetGrantsFor(current, time.Now()) != nil
	})
	for range 3 {
		if _, _, err := m.pickNext(context.Background(), "claude", model, cliproxyexecutor.Options{Metadata: map[string]any{}}, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.sampleResetGrants(context.Background(), "ending"); err != nil {
			t.Fatal(err)
		}
		m.subscriptionSampler.notify()
	}
	for _, request := range upstream.seenRequests() {
		if request != http.MethodGet+" "+claudeProfileEndpoint && request != http.MethodGet+" "+claudeResetGrantStatusEndpoint {
			t.Fatalf("request %q; the proxy may only read the profile and the grant status", request)
		}
	}
}

func useOrLoseGrantAuth(id string, now time.Time, resetIn time.Duration, used float64, endIn time.Duration, grants ...resetGrant) *Auth {
	auth := useOrLoseTestAuth(id, now, resetIn, used)
	if endIn != 0 {
		auth.Metadata = map[string]any{MetadataFleetEndsAt: FormatFleetEndsAt(now.Add(endIn)), MetadataFleetEndsAtSource: FleetEndSourceManual}
	}
	if grants != nil {
		auth.resetGrants = &resetGrantSample{SampledAt: now.Add(-time.Hour), grants: grants}
	}
	return auth
}

func weeklyGrants(n int) []resetGrant {
	grants := []resetGrant{}
	for range n {
		grants = append(grants, resetGrant{resetsLeft: 1, clears: []string{resetGrantWindowWeekly}})
	}
	return grants
}

func TestUseOrLoseDrainsEndingResetGrants(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name    string
		endIn   time.Duration
		grants  []resetGrant
		prepare func(steady, ending *Auth, selector *UseOrLoseSelector)
		model   string
		want    string
	}{
		{name: "no grants ranks by the earlier weekly reset", endIn: 16 * day, grants: weeklyGrants(0), want: "steady"},
		{name: "one grant on a far end still ranks by the weekly reset", endIn: 16 * day, grants: weeklyGrants(1), want: "steady"},
		{name: "two grants pull the binding two weeks before the end", endIn: 16 * day, grants: weeklyGrants(2), want: "ending"},
		{name: "one grant pulls a near end one week earlier", endIn: 9 * day, grants: weeklyGrants(1), want: "ending"},
		{name: "a grant with two resets left counts twice", endIn: 16 * day, grants: []resetGrant{{resetsLeft: 2, clears: []string{resetGrantWindowWeekly}}}, want: "ending"},
		{name: "an early grant end binds", endIn: 60 * day, grants: []resetGrant{{resetsLeft: 1, clears: []string{resetGrantWindowWeekly}, endsAt: now.Add(day)}}, want: "ending"},
		{name: "grants that do not clear the weekly window change nothing", endIn: 16 * day, grants: []resetGrant{{resetsLeft: 2, clears: []string{"five_hour"}}}, want: "steady"},
		{name: "a past binding outranks a reset inside the tie hour", endIn: 7*day - 10*time.Minute, grants: weeklyGrants(1), prepare: func(steady, _ *Auth, _ *UseOrLoseSelector) {
			useOrLoseTestWindow(steady, "7d", now.Add(20*time.Minute), .1)
		}, want: "ending"},
		{name: "setting off keeps the one-hour tie group after a past end", endIn: -10 * time.Minute, grants: weeklyGrants(0), prepare: func(steady, _ *Auth, selector *UseOrLoseSelector) {
			useOrLoseTestWindow(steady, "7d", now.Add(20*time.Minute), .1)
			selector.DrainEndingResets = false
		}, want: "steady"},
		{name: "an implausible reset count still binds early", endIn: 16 * day, grants: []resetGrant{{resetsLeft: 1 << 31, clears: []string{resetGrantWindowWeekly}}}, want: "ending"},
		{name: "a stale sample counts as no grants", endIn: 16 * day, grants: weeklyGrants(2), prepare: func(_, ending *Auth, _ *UseOrLoseSelector) {
			ending.resetGrants.SampledAt = now.Add(-25 * time.Hour)
		}, want: "steady"},
		{name: "a five-hour cap still yields to room", endIn: 16 * day, grants: weeklyGrants(2), prepare: func(_, ending *Auth, _ *UseOrLoseSelector) {
			useOrLoseTestWindow(ending, "5h", now.Add(time.Hour), .99)
		}, want: "steady"},
		{name: "a weekly limit still yields to room", endIn: 16 * day, grants: weeklyGrants(2), prepare: func(_, ending *Auth, _ *UseOrLoseSelector) {
			useOrLoseTestWindow(ending, "7d", now.Add(6*day), 1)
		}, want: "steady"},
		{name: "setting off ranks by the end only", endIn: 16 * day, grants: weeklyGrants(2), prepare: func(_, _ *Auth, selector *UseOrLoseSelector) {
			selector.DrainEndingResets = false
		}, want: "steady"},
		{name: "drain-ending-credentials off disables the grant rule", endIn: 9 * day, grants: weeklyGrants(1), prepare: func(_, _ *Auth, selector *UseOrLoseSelector) {
			selector.DrainEndingCredentials = false
		}, want: "steady"},
		{name: "fable requests ignore grants that only clear the all-models week", endIn: 16 * day, grants: weeklyGrants(2), model: "claude-fable-5-2", prepare: fableWindows(now), want: "steady"},
		{name: "fable requests count grants that clear the fable week", endIn: 16 * day, grants: []resetGrant{{resetsLeft: 2, clears: []string{resetGrantWindowFable}}}, model: "claude-fable-5-2", prepare: fableWindows(now), want: "ending"},
		{name: "fable grants do not move other models", endIn: 16 * day, grants: []resetGrant{{resetsLeft: 2, clears: []string{resetGrantWindowFable}}}, prepare: fableWindows(now), want: "steady"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steady := useOrLoseGrantAuth("steady", now, 3*day, .5, 0)
			ending := useOrLoseGrantAuth("ending", now, 6*day, .6, tc.endIn, tc.grants...)
			selector := &UseOrLoseSelector{ShortWindowCapPct: 95, DrainEndingCredentials: true, DrainEndingResets: true}
			if tc.prepare != nil {
				tc.prepare(steady, ending, selector)
			}
			model := tc.model
			if model == "" {
				model = "claude-opus-5-5"
			}
			got, err := selector.pick(context.Background(), "claude", model, []*Auth{steady, ending}, now)
			if err != nil || got.ID != tc.want {
				t.Fatalf("picked %v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func fableWindows(now time.Time) func(steady, ending *Auth, _ *UseOrLoseSelector) {
	return func(steady, ending *Auth, _ *UseOrLoseSelector) {
		useOrLoseTestWindow(steady, "7d_oi", now.Add(3*24*time.Hour), .5)
		useOrLoseTestWindow(ending, "7d_oi", now.Add(6*24*time.Hour), .6)
	}
}

func TestUseOrLosePastBindingsTieOnUtilisation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	early := useOrLoseGrantAuth("early", now, 6*day, .7, 10*day, weeklyGrants(2)...)
	late := useOrLoseGrantAuth("late", now, 6*day, .2, 6*day, weeklyGrants(1)...)
	selector := &UseOrLoseSelector{ShortWindowCapPct: 95, DrainEndingCredentials: true, DrainEndingResets: true}
	got, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", []*Auth{early, late}, now)
	if err != nil || got.ID != "late" {
		t.Fatalf("picked %v, %v; want the less used of two overdue bindings", got, err)
	}
}

func TestUpdateUseOrLoseSelectorCarriesResetSwitch(t *testing.T) {
	t.Parallel()
	current := &UseOrLoseSelector{ShortWindowCapPct: 95, DrainEndingCredentials: true, DrainEndingResets: true}
	if !updateUseOrLoseSelector(current, &UseOrLoseSelector{ShortWindowCapPct: 95, DrainEndingCredentials: true, DrainEndingResets: false}) {
		t.Fatal("use-or-lose selector was replaced instead of updated")
	}
	if current.drainsEndingResets() || !current.drainsEndingCredentials() {
		t.Fatalf("updated selector = %+v; want drain without the grant rule", current)
	}
}
