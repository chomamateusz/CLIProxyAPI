package fleetusage

import (
	"context"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func recentSeqs(requests []RecentRequest) []uint64 {
	seqs := make([]uint64, 0, len(requests))
	for _, request := range requests {
		seqs = append(seqs, request.Seq)
	}
	return seqs
}

func equalSeqs(got []uint64, want ...uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestRecentRingOrderWrapAndFilters(t *testing.T) {
	ring := NewRecentRing(4, fixedClock)
	requests, latest := ring.Snapshot(RecentQuery{Limit: 10})
	if len(requests) != 0 || latest != 0 || requests == nil {
		t.Fatalf("empty ring = %v %d", requests, latest)
	}
	for i := 0; i < 6; i++ {
		name := "laptop"
		if i%2 == 1 {
			name = "server"
		}
		ring.Record(namedContext(name), coreusage.Record{Provider: "codex", Model: "gpt-6", RequestedAt: fixedNow.Add(time.Duration(i) * time.Second)})
	}
	requests, latest = ring.Snapshot(RecentQuery{Limit: 10})
	if latest != 6 || !equalSeqs(recentSeqs(requests), 6, 5, 4, 3) {
		t.Fatalf("wrapped ring must hold the newest entries newest first: %v latest %d", recentSeqs(requests), latest)
	}
	if !requests[0].RequestedAt.Equal(fixedNow.Add(5 * time.Second)) {
		t.Fatalf("newest entry time = %v", requests[0].RequestedAt)
	}
	if requests, _ = ring.Snapshot(RecentQuery{Limit: 2}); !equalSeqs(recentSeqs(requests), 6, 5) {
		t.Fatalf("limit: %v", recentSeqs(requests))
	}
	if requests, _ = ring.Snapshot(RecentQuery{Limit: 10, AfterSeq: 4}); !equalSeqs(recentSeqs(requests), 6, 5) {
		t.Fatalf("after_seq: %v", recentSeqs(requests))
	}
	if requests, _ = ring.Snapshot(RecentQuery{Limit: 10, AfterSeq: 6}); len(requests) != 0 {
		t.Fatalf("after_seq at latest must be empty: %v", recentSeqs(requests))
	}
	if requests, _ = ring.Snapshot(RecentQuery{Limit: 10, Key: "server"}); !equalSeqs(recentSeqs(requests), 6, 4) {
		t.Fatalf("key filter: %v", recentSeqs(requests))
	}
	if requests, _ = ring.Snapshot(RecentQuery{Limit: 1, Key: "laptop"}); !equalSeqs(recentSeqs(requests), 5) {
		t.Fatalf("limit applies after the key filter: %v", recentSeqs(requests))
	}
}

func TestRecentRingEntryFields(t *testing.T) {
	ring := NewRecentRing(8, fixedClock)
	ring.Record(namedContext("laptop"), coreusage.Record{Generate: coreusage.GenerateFlag(false), Model: "skipped"})
	ring.Record(namedContext("laptop"), coreusage.Record{
		APIKey:               "sk-raw-fixture",
		Provider:             "codex",
		Model:                "gpt-6",
		ResponseModel:        "gpt-6-2026",
		AuthID:               "codex-account.json",
		AuthIndex:            "3",
		Stream:               true,
		Latency:              1500 * time.Millisecond,
		TTFT:                 300 * time.Millisecond,
		Failed:               true,
		Fail:                 coreusage.Failure{StatusCode: 429, Body: "quota body"},
		EffectiveServiceTier: "priority",
		ResponseServiceTier:  "default",
		ReasoningEffort:      "high",
		Detail:               coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	})
	ring.Record(context.Background(), coreusage.Record{Model: "gpt-6", ResponseModel: "gpt-6", ResponseServiceTier: "flex", Fail: coreusage.Failure{StatusCode: 500}})
	requests, latest := ring.Snapshot(RecentQuery{Limit: 10})
	if latest != 2 || len(requests) != 2 {
		t.Fatalf("a non-generating record must be skipped: %v latest %d", recentSeqs(requests), latest)
	}
	plain, full := requests[0], requests[1]
	if plain.Key != AnonymousKey || plain.Provider != unknownValue || plain.ResponseModel != "" || plain.ServiceTier != "flex" || plain.StatusCode != 0 || plain.Requests != 1 || !plain.RequestedAt.Equal(fixedNow) {
		t.Fatalf("plain entry: %+v", plain)
	}
	want := RecentRequest{
		Seq: 1, RequestedAt: fixedNow, Key: "laptop", Provider: "codex", Model: "gpt-6", ResponseModel: "gpt-6-2026",
		AuthID: "codex-account.json", AuthIndex: "3", Stream: true, Latency: 1500 * time.Millisecond, TTFT: 300 * time.Millisecond,
		Failed: true, StatusCode: 429, ServiceTier: "priority", ReasoningEffort: "high",
		Counters: countersFor(coreusage.Record{Failed: true, Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}),
	}
	if full != want {
		t.Fatalf("full entry:\n got %+v\nwant %+v", full, want)
	}
}

func TestUsagePluginFeedsLedgerAndRecentRing(t *testing.T) {
	ledger := NewLedger("", fixedClock)
	active.Store(ledger)
	t.Cleanup(func() { active.Store(nil) })
	_, before := Recent().Snapshot(RecentQuery{Limit: 1})
	usagePlugin{}.HandleUsage(namedContext("plugin-path"), coreusage.Record{Provider: "codex", Model: "gpt-6", RequestedAt: fixedNow, Detail: coreusage.Detail{TotalTokens: 7}})
	usagePlugin{}.HandleUsage(namedContext("plugin-path"), coreusage.Record{Provider: "codex", Model: "gpt-6", RequestedAt: fixedNow, Generate: coreusage.GenerateFlag(false)})
	requests, latest := Recent().Snapshot(RecentQuery{Limit: RecentCapacity, AfterSeq: before, Key: "plugin-path"})
	if latest != before+1 || len(requests) != 1 || requests[0].TotalTokens != 7 {
		t.Fatalf("recent ring: %+v latest %d before %d", requests, latest, before)
	}
	usage := ledger.Usage(fixedNow.Truncate(time.Hour), fixedNow.Truncate(time.Hour).Add(time.Hour), "plugin-path")
	if len(usage) != 1 || usage[0].Requests != 1 || usage[0].TotalTokens != 7 {
		t.Fatalf("ledger: %+v", usage)
	}
}

func TestRecentRingConcurrentWriterAndReaders(t *testing.T) {
	ring := NewRecentRing(64, nil)
	const writes = 5000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			ring.Record(namedContext("laptop"), coreusage.Record{Provider: "codex", Model: "gpt-6"})
		}
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				requests, latest := ring.Snapshot(RecentQuery{Limit: 64, AfterSeq: uint64(i)})
				for j := 1; j < len(requests); j++ {
					if requests[j].Seq >= requests[j-1].Seq {
						t.Errorf("snapshot not strictly newest first: %v", recentSeqs(requests))
						return
					}
				}
				if len(requests) > 0 && requests[0].Seq != latest {
					t.Errorf("first entry %d is not latest %d", requests[0].Seq, latest)
					return
				}
			}
		}()
	}
	wg.Wait()
	requests, latest := ring.Snapshot(RecentQuery{Limit: 64})
	if latest != writes || len(requests) != 64 || requests[63].Seq != writes-63 {
		t.Fatalf("final ring: latest %d len %d", latest, len(requests))
	}
}
