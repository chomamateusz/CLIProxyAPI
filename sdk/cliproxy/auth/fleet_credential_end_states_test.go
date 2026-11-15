package auth

import (
	"errors"
	"testing"
	"time"
)

func endTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UTC()
}

func TestNextBillingAnniversary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ anchor, after, want string }{
		{"2026-10-07T21:09:56Z", "2026-11-15T12:00:00Z", "2026-12-07T21:09:56Z"},
		{"2026-10-07T21:09:56Z", "2026-11-07T21:09:55Z", "2026-11-07T21:09:56Z"},
		{"2026-10-07T21:09:56Z", "2026-11-07T21:09:56Z", "2026-12-07T21:09:56Z"},
		{"2026-10-07T21:09:56Z", "2026-10-01T00:00:00Z", "2026-10-07T21:09:56Z"},
		{"2026-01-31T08:00:00Z", "2027-02-01T00:00:00Z", "2027-02-28T08:00:00Z"},
		{"2026-01-31T08:00:00Z", "2028-02-01T00:00:00Z", "2028-02-29T08:00:00Z"},
		{"2026-01-31T08:00:00Z", "2027-02-28T08:00:00Z", "2027-03-31T08:00:00Z"},
		{"2026-01-30T08:00:00Z", "2027-04-01T00:00:00Z", "2027-04-30T08:00:00Z"},
		{"2025-12-15T23:30:00Z", "2026-12-31T00:00:00Z", "2027-01-15T23:30:00Z"},
		{"2026-10-07T23:30:00+02:00", "2026-11-15T00:00:00Z", "2026-12-07T21:30:00Z"},
	} {
		if got := NextBillingAnniversary(endTime(t, tc.anchor), endTime(t, tc.after)); !got.Equal(endTime(t, tc.want)) {
			t.Errorf("NextBillingAnniversary(%s, %s) = %s; want %s", tc.anchor, tc.after, FormatFleetEndsAt(got), tc.want)
		}
	}
}

func TestOneMonthLaterClampsToMonthEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ at, want string }{
		{"2026-12-12T23:00:00Z", "2027-01-12T23:00:00Z"},
		{"2027-01-31T10:00:00Z", "2027-02-28T10:00:00Z"},
		{"2027-02-28T10:00:00Z", "2027-03-28T10:00:00Z"},
	} {
		if got := oneMonthLater(endTime(t, tc.at)); !got.Equal(endTime(t, tc.want)) {
			t.Errorf("oneMonthLater(%s) = %s; want %s", tc.at, FormatFleetEndsAt(got), tc.want)
		}
	}
}

func claudeProfileBody(status, createdAt, orgType string) string {
	return `{"account":{"uuid":"u","email":"secret@example.com","has_claude_max":false,"has_claude_pro":false},"organization":{"uuid":"o","subscription_status":"` + status + `","subscription_created_at":"` + createdAt + `","organization_type":"` + orgType + `","rate_limit_tier":"default_claude_max_20x","billing_type":"stripe_subscription"}}`
}

func mustSample(t *testing.T, provider, body string, at time.Time) *subscriptionSample {
	t.Helper()
	sample, err := parseSubscriptionSample(provider, []byte(body), at)
	if err != nil {
		t.Fatal(err)
	}
	return sample
}

func TestParseSubscriptionSample(t *testing.T) {
	t.Parallel()
	at := endTime(t, "2026-11-15T12:00:00Z")
	claude := mustSample(t, "claude", claudeProfileBody("active", "2026-10-07T21:09:56.274004Z", "claude_max"), at)
	if !claude.SubscriptionCreatedAt.Equal(endTime(t, "2026-10-07T21:09:56Z")) || claude.OrganizationType != "claude_max" || claude.RateLimitTier != "default_claude_max_20x" || claude.paidPlan() != paidPlanActive {
		t.Fatalf("claude sample = %+v", claude)
	}
	codex := mustSample(t, "codex", `{"plan_type":"pro","will_renew":false,"active_until":"2026-12-01T10:00:00Z","is_active":true,"is_delinquent":false,"grace_period_end_timestamp":1765000000,"cancellation_outcome":"cancelled","entitlement":{"cancels_at":null,"expires_at":"2026-12-02T10:00:00Z","renews_at":null,"has_active_subscription":true},"last_active_subscription":{"will_renew":true}}`, at)
	if codex.WillRenew == nil || *codex.WillRenew || !codex.ActiveUntil.Equal(endTime(t, "2026-12-01T10:00:00Z")) || !codex.ExpiresAt.Equal(endTime(t, "2026-12-02T10:00:00Z")) ||
		!codex.CancelsAt.IsZero() || !codex.GracePeriodEnd.Equal(time.Unix(1765000000, 0).UTC()) || codex.paidPlan() != paidPlanActive || codex.PlanType != "pro" {
		t.Fatalf("codex sample = %+v", codex)
	}
	if end, ok := codex.codexEnd(); !ok || !end.Equal(codex.ActiveUntil) {
		t.Fatalf("codex end = %s, %t; want active_until", end, ok)
	}
	nested := mustSample(t, "codex", `{"last_active_subscription":{"will_renew":false},"entitlement":{"expires_at":1764583200000}}`, at)
	if end, ok := nested.codexEnd(); !ok || !end.Equal(time.Unix(1764583200, 0).UTC()) {
		t.Fatalf("nested will_renew end = %s, %t; want expires_at in milliseconds", end, ok)
	}
	for _, body := range []string{``, `[]`, `not json`} {
		if _, err := parseSubscriptionSample("codex", []byte(body), at); err == nil {
			t.Fatalf("payload %q accepted", body)
		}
	}
	for _, field := range claude.logFields() {
		if field == "secret@example.com" || field == "u" {
			t.Fatal("log fields carry account identity")
		}
	}
}

func TestPaidPlanActivity(t *testing.T) {
	t.Parallel()
	at := time.Now()
	for _, tc := range []struct {
		name, provider, body string
		want                 paidPlanActivity
	}{
		{"claude max", "claude", claudeProfileBody("active", "", "claude_max"), paidPlanActive},
		{"claude pro", "claude", claudeProfileBody("active", "", "claude_pro"), paidPlanActive},
		{"claude team", "claude", claudeProfileBody("active", "", "claude_team_standard"), paidPlanActive},
		{"claude flag only", "claude", `{"account":{"has_claude_max":true},"organization":{"subscription_status":"active","organization_type":"other"}}`, paidPlanActive},
		{"claude cancelled", "claude", claudeProfileBody("canceled", "", "claude_max"), paidPlanInactive},
		{"claude free org", "claude", claudeProfileBody("active", "", "claude_free"), paidPlanInactive},
		{"claude no status", "claude", `{"organization":{"organization_type":"claude_max"}}`, paidPlanUnknown},
		{"claude null status free org", "claude", `{"organization":{"subscription_status":null,"organization_type":"claude_free"},"account":{"has_claude_max":false,"has_claude_pro":false}}`, paidPlanInactive},
		{"claude no status both flags false", "claude", `{"account":{"has_claude_max":false,"has_claude_pro":false}}`, paidPlanInactive},
		{"claude no status one flag false", "claude", `{"account":{"has_claude_max":false}}`, paidPlanUnknown},
		{"claude empty", "claude", `{}`, paidPlanUnknown},
		{"claude no tier", "claude", `{"organization":{"subscription_status":"active"}}`, paidPlanUnknown},
		{"codex active", "codex", `{"is_active":true,"entitlement":{"has_active_subscription":true}}`, paidPlanActive},
		{"codex inactive", "codex", `{"is_active":false,"entitlement":{"has_active_subscription":true}}`, paidPlanInactive},
		{"codex no entitlement", "codex", `{"is_active":true,"entitlement":{"has_active_subscription":false}}`, paidPlanInactive},
		{"codex missing entitlement", "codex", `{"is_active":true}`, paidPlanUnknown},
		{"codex empty", "codex", `{}`, paidPlanUnknown},
	} {
		if got := mustSample(t, tc.provider, tc.body, at).paidPlan(); got != tc.want {
			t.Errorf("%s: paidPlan = %d; want %d", tc.name, got, tc.want)
		}
	}
}

func endMetadata(endsAt time.Time, source string) map[string]any {
	metadata := map[string]any{MetadataFleetEndsAt: FormatFleetEndsAt(endsAt)}
	if source != "" {
		metadata[MetadataFleetEndsAtSource] = source
	}
	return metadata
}

func TestFleetEndStates(t *testing.T) {
	t.Parallel()
	end := endTime(t, "2026-12-07T21:09:56Z")
	active := mustSample(t, "claude", claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max"), end.Add(time.Hour))
	inactive := mustSample(t, "claude", claudeProfileBody("canceled", "2026-10-07T21:09:56Z", "claude_max"), end.Add(time.Hour))
	inactiveBeforeEnd := mustSample(t, "claude", claudeProfileBody("canceled", "2026-10-07T21:09:56Z", "claude_max"), end.Add(-time.Hour))
	unknown := mustSample(t, "claude", `{}`, end.Add(time.Hour))
	after := end.Add(2 * time.Hour)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		sample   *subscriptionSample
		now      time.Time
		want     FleetEndState
	}{
		{"no end", map[string]any{}, active, after, FleetEndStateNone},
		{"future manual", endMetadata(end, ""), nil, end.Add(-time.Second), FleetEndStateScheduled},
		{"future codex", endMetadata(end, FleetEndSourceCodexAPI), nil, end.Add(-time.Second), FleetEndStateScheduled},
		{"passed manual without sample", endMetadata(end, FleetEndSourceManual), nil, end, FleetEndStateOverdue},
		{"passed anniversary with active post-end sample", endMetadata(end, FleetEndSourceClaudeAnniversary), active, after, FleetEndStateOverdue},
		{"passed manual with inactive pre-end sample", endMetadata(end, FleetEndSourceManual), inactiveBeforeEnd, after, FleetEndStateOverdue},
		{"passed manual with unknown post-end sample", endMetadata(end, FleetEndSourceManual), unknown, after, FleetEndStateOverdue},
		{"passed anniversary with inactive post-end sample", endMetadata(end, FleetEndSourceClaudeAnniversary), inactive, after, FleetEndStateEnded},
		{"passed codex", endMetadata(end, FleetEndSourceCodexAPI), nil, end, FleetEndStateEnded},
		{"passed plan ended", endMetadata(end, FleetEndSourcePlanEnded), active, end, FleetEndStateEnded},
		{"unknown source counts as manual", endMetadata(end, "guess"), nil, after, FleetEndStateOverdue},
	} {
		got := fleetCredentialEnd(tc.metadata, tc.sample, tc.now)
		if got.State != tc.want {
			t.Errorf("%s: state = %s; want %s", tc.name, got.State, tc.want)
		}
		auth := &Auth{Metadata: tc.metadata, subscription: tc.sample}
		if FleetCredentialEnded(auth, tc.now) != (tc.want == FleetEndStateEnded) {
			t.Errorf("%s: FleetCredentialEnded disagrees with state %s", tc.name, tc.want)
		}
	}
	listed := fleetCredentialEnd(endMetadata(end, FleetEndSourceClaudeAnniversary), active, after)
	if listed.Source != FleetEndSourceClaudeAnniversary || !listed.BillingAnchor.Equal(endTime(t, "2026-10-07T21:09:56Z")) || !listed.CheckedAt.Equal(active.SampledAt) {
		t.Fatalf("end = %+v", listed)
	}
}

func TestFleetEndWarnings(t *testing.T) {
	t.Parallel()
	now := endTime(t, "2026-11-15T12:00:00Z")
	renews := mustSample(t, "codex", `{"will_renew":true,"is_active":true,"entitlement":{"cancels_at":null,"has_active_subscription":true}}`, now)
	grace := mustSample(t, "codex", `{"will_renew":true,"is_active":true,"grace_period_end_timestamp":"2026-11-20T00:00:00Z","entitlement":{"has_active_subscription":true}}`, now)
	end := now.Add(48 * time.Hour)
	manual := fleetCredentialEnd(endMetadata(end, FleetEndSourceManual), renews, now)
	if len(manual.Warnings) != 1 || manual.Warnings[0].Code != FleetEndWarningProviderRenews || manual.Warnings[0].Until != nil {
		t.Fatalf("manual end warnings = %+v; want provider_renews", manual.Warnings)
	}
	if got := fleetCredentialEnd(endMetadata(end, FleetEndSourceCodexAPI), renews, now).Warnings; len(got) != 0 {
		t.Fatalf("codex end warnings = %+v; want none", got)
	}
	if got := fleetCredentialEnd(map[string]any{}, renews, now).Warnings; len(got) != 0 {
		t.Fatalf("no end warnings = %+v; want none", got)
	}
	graced := fleetCredentialEnd(map[string]any{}, grace, now).Warnings
	if len(graced) != 1 || graced[0].Code != FleetEndWarningGracePeriod || graced[0].Until == nil || *graced[0].Until != "2026-11-20T00:00:00Z" {
		t.Fatalf("grace warnings = %+v", graced)
	}
}

func TestReconcileFleetEnd(t *testing.T) {
	t.Parallel()
	now := endTime(t, "2026-11-15T12:00:00Z")
	later := now.Add(10 * 24 * time.Hour)
	earlier := now.Add(-24 * time.Hour)
	cancelsAt := "2026-12-01T00:00:00Z"
	for _, tc := range []struct {
		name        string
		metadata    map[string]any
		provider    string
		body        string
		wantChanged bool
		wantEnd     string
		wantSource  string
	}{
		{"inactive claude without end", map[string]any{}, "claude", claudeProfileBody("canceled", "", "claude_max"), true, FormatFleetEndsAt(now), FleetEndSourcePlanEnded},
		{"inactive claude replaces a later manual end", endMetadata(later, FleetEndSourceManual), "claude", claudeProfileBody("canceled", "", "claude_max"), true, FormatFleetEndsAt(now), FleetEndSourcePlanEnded},
		{"inactive claude keeps an earlier end", endMetadata(earlier, FleetEndSourceManual), "claude", claudeProfileBody("canceled", "", "claude_max"), true, FormatFleetEndsAt(earlier), FleetEndSourcePlanEnded},
		{"inactive claude keeps an earlier anniversary end", endMetadata(earlier, FleetEndSourceClaudeAnniversary), "claude", claudeProfileBody("canceled", "", "claude_max"), true, FormatFleetEndsAt(earlier), FleetEndSourcePlanEnded},
		{"inactive codex leaves a past codex end", endMetadata(earlier, FleetEndSourceCodexAPI), "codex", `{"is_active":false,"entitlement":{"has_active_subscription":false}}`, false, FormatFleetEndsAt(earlier), FleetEndSourceCodexAPI},
		{"inactive claude leaves a past plan-ended end", endMetadata(earlier, FleetEndSourcePlanEnded), "claude", claudeProfileBody("canceled", "", "claude_max"), false, FormatFleetEndsAt(earlier), FleetEndSourcePlanEnded},
		{"unknown claude changes nothing", map[string]any{}, "claude", `{}`, false, "", ""},
		{"inactive codex", map[string]any{}, "codex", `{"is_active":false,"entitlement":{"has_active_subscription":false}}`, true, FormatFleetEndsAt(now), FleetEndSourcePlanEnded},
		{"codex cancels_at", map[string]any{}, "codex", `{"will_renew":false,"active_until":"2026-12-05T00:00:00Z","is_active":true,"entitlement":{"cancels_at":"` + cancelsAt + `","has_active_subscription":true}}`, true, cancelsAt, FleetEndSourceCodexAPI},
		{"codex will not renew", map[string]any{}, "codex", `{"will_renew":false,"active_until":"2026-12-05T00:00:00Z","is_active":true,"entitlement":{"expires_at":"2026-12-06T00:00:00Z","has_active_subscription":true}}`, true, "2026-12-05T00:00:00Z", FleetEndSourceCodexAPI},
		{"codex will not renew without active_until", map[string]any{}, "codex", `{"will_renew":false,"is_active":true,"entitlement":{"expires_at":"2026-12-06T00:00:00Z","has_active_subscription":true}}`, true, "2026-12-06T00:00:00Z", FleetEndSourceCodexAPI},
		{"codex moves its own end", endMetadata(later, FleetEndSourceCodexAPI), "codex", `{"is_active":true,"entitlement":{"cancels_at":"` + cancelsAt + `","has_active_subscription":true}}`, true, cancelsAt, FleetEndSourceCodexAPI},
		{"codex same end is no change", endMetadata(endTime(t, cancelsAt), FleetEndSourceCodexAPI), "codex", `{"is_active":true,"entitlement":{"cancels_at":"` + cancelsAt + `","has_active_subscription":true}}`, false, cancelsAt, FleetEndSourceCodexAPI},
		{"manual end wins over codex", endMetadata(later, FleetEndSourceManual), "codex", `{"is_active":true,"entitlement":{"cancels_at":"` + cancelsAt + `","has_active_subscription":true}}`, false, FormatFleetEndsAt(later), FleetEndSourceManual},
		{"codex renews clears its end", endMetadata(later, FleetEndSourceCodexAPI), "codex", `{"will_renew":true,"is_active":true,"entitlement":{"cancels_at":null,"has_active_subscription":true}}`, true, "", ""},
		{"codex renews keeps a manual end", endMetadata(later, FleetEndSourceManual), "codex", `{"will_renew":true,"is_active":true,"entitlement":{"cancels_at":null,"has_active_subscription":true}}`, false, FormatFleetEndsAt(later), FleetEndSourceManual},
		{"codex renew unknown keeps its end", endMetadata(later, FleetEndSourceCodexAPI), "codex", `{"is_active":true,"entitlement":{"has_active_subscription":true}}`, false, FormatFleetEndsAt(later), FleetEndSourceCodexAPI},
		{"claude anchor moved", endMetadata(endTime(t, "2026-12-07T21:09:56Z"), FleetEndSourceClaudeAnniversary), "claude", claudeProfileBody("active", "2026-10-10T08:00:00Z", "claude_max"), true, "2026-12-10T08:00:00Z", FleetEndSourceClaudeAnniversary},
		{"claude anchor unchanged", endMetadata(endTime(t, "2026-12-07T21:09:56Z"), FleetEndSourceClaudeAnniversary), "claude", claudeProfileBody("active", "2026-10-07T21:09:56Z", "claude_max"), false, "2026-12-07T21:09:56Z", FleetEndSourceClaudeAnniversary},
		{"claude anchor ignores manual end", endMetadata(later, FleetEndSourceManual), "claude", claudeProfileBody("active", "2026-10-10T08:00:00Z", "claude_max"), false, FormatFleetEndsAt(later), FleetEndSourceManual},
		{"claude anchor moved before the cancel", endMetadata(endTime(t, "2026-11-07T21:09:56Z"), FleetEndSourceClaudeAnniversary), "claude", claudeProfileBody("active", "2026-10-24T08:00:00Z", "claude_max"), true, "2026-11-24T08:00:00Z", FleetEndSourceClaudeAnniversary},
		{"claude resubscribed after the cancel", endMetadata(endTime(t, "2026-11-07T21:09:56Z"), FleetEndSourceClaudeAnniversary), "claude", claudeProfileBody("active", "2026-11-05T08:00:00Z", "claude_max"), true, "2026-12-05T08:00:00Z", FleetEndSourceClaudeAnniversary},
	} {
		metadata := cloneMetadataMap(tc.metadata)
		metadata[MetadataFleetCancelledAt] = "2026-11-01T00:00:00Z"
		_, changed := reconcileFleetEnd(metadata, mustSample(t, tc.provider, tc.body, now))
		gotEnd, _ := metadata[MetadataFleetEndsAt].(string)
		gotSource, _ := metadata[MetadataFleetEndsAtSource].(string)
		if changed != tc.wantChanged || gotEnd != tc.wantEnd || gotSource != tc.wantSource && !(tc.wantSource == FleetEndSourceManual && gotSource == "") {
			t.Errorf("%s: changed %t end %q source %q; want %t %q %q", tc.name, changed, gotEnd, gotSource, tc.wantChanged, tc.wantEnd, tc.wantSource)
		}
		if _, kept := metadata[MetadataFleetCancelledAt]; kept == (tc.wantChanged && tc.wantEnd == "") {
			t.Errorf("%s: cancel marker kept = %t", tc.name, kept)
		}
	}
}

func TestReconcileClaudeAnniversaryFromCancel(t *testing.T) {
	t.Parallel()
	now := endTime(t, "2026-11-15T12:00:00Z")
	for _, tc := range []struct{ name, end, cancelledAt, anchor, want string }{
		{"later cancel", "2026-11-07T21:09:56Z", "2026-11-05T00:00:00Z", "2026-10-27T08:00:00Z", "2026-11-27T08:00:00Z"},
		{"two moves kept", "2027-01-07T21:09:56Z", "2026-11-01T00:00:00Z", "2026-10-24T08:00:00Z", "2027-01-24T08:00:00Z"},
		{"no cancel marker", "2026-12-07T21:09:56Z", "", "2026-10-10T08:00:00Z", "2026-12-10T08:00:00Z"},
		{"no cancel marker never at the anchor", "2026-11-07T21:09:56Z", "", "2026-11-05T08:00:00Z", "2026-12-05T08:00:00Z"},
	} {
		metadata := endMetadata(endTime(t, tc.end), FleetEndSourceClaudeAnniversary)
		if tc.cancelledAt != "" {
			metadata[MetadataFleetCancelledAt] = tc.cancelledAt
		}
		_, changed := reconcileFleetEnd(metadata, mustSample(t, "claude", claudeProfileBody("active", tc.anchor, "claude_max"), now))
		if !changed || metadata[MetadataFleetEndsAt] != tc.want {
			t.Errorf("%s: changed %t end %v; want %s", tc.name, changed, metadata[MetadataFleetEndsAt], tc.want)
		}
	}
}

func TestCancelAndNextPeriod(t *testing.T) {
	t.Parallel()
	now := endTime(t, "2026-11-15T12:00:00Z")
	anchor := endTime(t, "2026-10-07T21:09:56Z")
	metadata := map[string]any{}
	if err := CancelAtBillingAnniversary(metadata, time.Time{}, now); !errors.Is(err, ErrNoBillingAnchor) || len(metadata) != 0 {
		t.Fatalf("cancel without anchor = %v, %v", err, metadata)
	}
	if err := CancelAtBillingAnniversary(metadata, anchor, now); err != nil {
		t.Fatal(err)
	}
	if metadata[MetadataFleetEndsAt] != "2026-12-07T21:09:56Z" || metadata[MetadataFleetEndsAtSource] != FleetEndSourceClaudeAnniversary || metadata[MetadataFleetCancelledAt] != "2026-11-15T12:00:00Z" {
		t.Fatalf("cancelled metadata = %v", metadata)
	}
	if !FleetEndNeedsBillingAnchor(metadata) {
		t.Fatal("anniversary end does not ask for its anchor")
	}
	if err := MoveFleetEndToNextPeriod(metadata, time.Time{}); !errors.Is(err, ErrNoBillingAnchor) {
		t.Fatalf("next period without anchor = %v", err)
	}
	if err := MoveFleetEndToNextPeriod(metadata, anchor); err != nil || metadata[MetadataFleetEndsAt] != "2027-01-07T21:09:56Z" {
		t.Fatalf("next period = %v, %v", err, metadata)
	}

	manual := endMetadata(endTime(t, "2027-01-31T10:00:00Z"), FleetEndSourceManual)
	if FleetEndNeedsBillingAnchor(manual) {
		t.Fatal("manual end asks for a billing anchor")
	}
	if err := MoveFleetEndToNextPeriod(manual, time.Time{}); err != nil || manual[MetadataFleetEndsAt] != "2027-02-28T10:00:00Z" || manual[MetadataFleetEndsAtSource] != FleetEndSourceManual {
		t.Fatalf("manual next period = %v, %v", err, manual)
	}
	for _, metadata := range []map[string]any{{}, endMetadata(now, FleetEndSourceCodexAPI), endMetadata(now, FleetEndSourcePlanEnded)} {
		if err := MoveFleetEndToNextPeriod(metadata, anchor); !errors.Is(err, ErrNoPeriodEnd) {
			t.Fatalf("next period on %v = %v; want ErrNoPeriodEnd", metadata, err)
		}
	}
	ClearFleetEnd(metadata)
	if len(metadata) != 0 {
		t.Fatalf("cleared metadata = %v", metadata)
	}
}

func TestSubscriptionSampleInterval(t *testing.T) {
	t.Parallel()
	now := endTime(t, "2026-11-15T12:00:00Z")
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     time.Duration
	}{
		{"no end", map[string]any{}, subscriptionSampleInterval},
		{"end in three days", endMetadata(now.Add(72*time.Hour), FleetEndSourceManual), subscriptionSampleInterval},
		{"end within 48 hours", endMetadata(now.Add(47*time.Hour), FleetEndSourceManual), subscriptionSampleUrgentInterval},
		{"overdue", endMetadata(now.Add(-time.Hour), FleetEndSourceManual), subscriptionSampleUrgentInterval},
		{"ended", endMetadata(now.Add(-time.Hour), FleetEndSourceCodexAPI), subscriptionSampleInterval},
	} {
		if got := subscriptionSampleIntervalFor(&Auth{Metadata: tc.metadata}, now); got != tc.want {
			t.Errorf("%s: interval = %s; want %s", tc.name, got, tc.want)
		}
	}
}
