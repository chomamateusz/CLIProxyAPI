package registry

import "testing"

func suspendForTest(t *testing.T, r *ModelRegistry, clientID, modelID, reason string, quotaExceeded bool) {
	t.Helper()
	_, epoch := r.GetModelsAndEpochForClient(clientID)
	if !r.ApplyClientModelProjections(clientID, epoch, 1, []ClientModelProjection{{ModelID: modelID, Suspended: true, SuspendReason: reason, QuotaExceeded: quotaExceeded}}) {
		t.Fatalf("failed to suspend %s", clientID)
	}
}

func TestEndedCredentialCountsAsAbsentClient(t *testing.T) {
	for _, tc := range []struct {
		name         string
		liveReason   string
		liveQuota    bool
		wantListed   bool
		withLiveOnly bool
	}{
		{"live credential quota-cooled", "quota", true, true, false},
		{"live credential credential-wide quota", "credential_quota", false, true, false},
		{"live credential healthy", "", false, true, false},
		{"live credential manually suspended", "manual", false, false, false},
		{"ended credential alone", "", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestModelRegistry()
			model := &ModelInfo{ID: "fleet-ended-model", OwnedBy: "openai", Type: "openai"}
			r.RegisterClient("ended", "codex", []*ModelInfo{model})
			if !tc.withLiveOnly {
				r.RegisterClient("live", "codex", []*ModelInfo{model})
			}
			suspendForTest(t, r, "ended", model.ID, SuspendReasonCredentialEnded, true)
			if tc.liveReason != "" {
				suspendForTest(t, r, "live", model.ID, tc.liveReason, tc.liveQuota)
			}
			if got := len(r.GetAvailableModels("openai")) == 1; got != tc.wantListed {
				t.Errorf("available models listed = %t; want %t", got, tc.wantListed)
			}
			if got := len(r.GetAvailableModelsByProvider("codex")) == 1; got != tc.wantListed {
				t.Errorf("provider models listed = %t; want %t", got, tc.wantListed)
			}
			if got := len(r.GetAvailableModelInfos()) == 1; got != tc.wantListed {
				t.Errorf("model infos listed = %t; want %t", got, tc.wantListed)
			}
		})
	}
}
