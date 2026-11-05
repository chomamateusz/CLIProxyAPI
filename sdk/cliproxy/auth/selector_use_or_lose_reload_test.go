package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestUseOrLoseTransientRetryLCPAndNetwork(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, path := range []string{"claude", "mixed"} {
		for _, status := range []int{0, 503} {
			for _, lcp := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/status=%d/lcp=%t", path, status, lcp), func(t *testing.T) {
					f := newFleetManager(t, []string{"claude"}, fleetOther, true, "")
					if lcp {
						f.opts = cliproxyexecutor.Options{
							SourceFormat:    sdktranslator.FormatOpenAI,
							OriginalRequest: []byte(`{"messages":[{"role":"user","content":"preserve this conversation"}]}`),
							Metadata:        map[string]any{cliproxyexecutor.CallerScopeMetadataKey: t.Name()},
						}
					}
					bound, other := f.auths[0], f.auths[1]
					fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "initial binding")
					f.manager.MarkResult(context.Background(), Result{AuthID: bound.ID, Provider: "claude", Model: f.model, Options: f.opts, Error: &Error{HTTPStatus: status, Message: "transient upstream failure"}})
					fleetRequirePick(t, f, path, f.opts, map[string]struct{}{bound.ID: {}}, other.ID, "retry uses another credential")
					f.manager.MarkResult(context.Background(), Result{AuthID: other.ID, Provider: "claude", Model: f.model, Options: f.opts, Success: true})
					f.expireCooldown(bound.ID)
					fleetRequirePick(t, f, path, f.opts, nil, bound.ID, "retry success preserves the original binding")
				})
			}
		}
	}
}

func TestUseOrLoseConcurrentCapReloadPreservesSelectorState(t *testing.T) {
	f := newFleetManager(t, []string{"claude"}, fleetOther, true, SessionAffinityRebindOnQuota)
	bound, other := f.auths[0], f.auths[1]
	fleetRequirePick(t, f, "claude", f.opts, nil, bound.ID, "initial binding")
	changed := other.Clone()
	useOrLoseTestWindow(changed, "7d", time.Now().Add(time.Hour), .1)
	f.update(t, changed)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 20 {
				opts := cliproxyexecutor.Options{OriginalRequest: f.opts.OriginalRequest, Metadata: map[string]any{}}
				got, err := f.pick("claude", opts, nil)
				if err != nil || got == nil || got.ID != bound.ID {
					t.Errorf("concurrent reload pick = %s, %v; want %s", fleetPickedID(got), err, bound.ID)
					return
				}
			}
		})
	}
	for iteration := range 20 {
		replacement := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &UseOrLoseSelector{ShortWindowCapPct: float64(80 + iteration%10)}, RebindOn: SessionAffinityRebindOnQuota})
		f.manager.SetSelector(replacement)
		if f.manager.Selector() != f.affinity {
			t.Error("cap reload replaced the live affinity selector")
		}
		select {
		case <-replacement.cache.stopCh:
		default:
			t.Error("unused replacement cache cleanup loop was not stopped")
		}
	}
	wg.Wait()
	if got := f.selector.effectiveShortWindowCapPct(); got != 89 {
		t.Fatalf("reloaded cap = %g; want 89", got)
	}
	changed = bound.Clone()
	useOrLoseTestWindow(changed, "5h", time.Now().Add(time.Hour), .90)
	f.update(t, changed)
	fleetRequirePick(t, f, "claude", f.opts, nil, other.ID, "new threshold applies to the retained selector")
}

func TestUseOrLoseCapReloadDoesNotSwallowAffinitySettings(t *testing.T) {
	for _, setting := range []string{"ttl", "subagents", "rebind", "disable"} {
		t.Run(setting, func(t *testing.T) {
			current := NewSessionAffinitySelector(&UseOrLoseSelector{})
			defer current.Stop()
			manager := NewManager(nil, current, nil)
			cfg := SessionAffinityConfig{Fallback: &UseOrLoseSelector{ShortWindowCapPct: 80}}
			switch setting {
			case "ttl":
				cfg.TTL = 2 * time.Hour
			case "subagents":
				value := false
				cfg.SubagentAffinity = &value
			case "rebind":
				cfg.RebindOn = SessionAffinityRebindOnQuota
			}
			var replacement Selector
			if setting == "disable" {
				replacement = cfg.Fallback
			} else {
				affinity := NewSessionAffinitySelectorWithConfig(cfg)
				defer affinity.Stop()
				replacement = affinity
			}
			manager.SetSelector(replacement)
			if manager.Selector() != replacement {
				t.Fatal("affinity settings change must install the new selector")
			}
			select {
			case <-current.cache.stopCh:
			default:
				t.Fatal("replaced selector was not stopped")
			}
		})
	}
}
