package fleetusage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

var fixedNow = time.Date(2026, 11, 13, 15, 30, 0, 0, time.UTC)

func fixedClock() time.Time { return fixedNow }

func namedContext(name string) context.Context {
	return access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Name: name})
}

func TestKeyName(t *testing.T) {
	const raw = "sk-fleet-raw-fixture"
	sum := sha256.Sum256([]byte(raw))
	unnamed := "unnamed-" + hex.EncodeToString(sum[:])[:8]
	for _, tc := range []struct {
		name string
		ctx  context.Context
		key  string
		want string
	}{
		{"named policy", namedContext("laptop"), raw, "laptop"},
		{"named policy without principal", namedContext("laptop"), "", "laptop"},
		{"policy without name", access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Models: []string{"x"}}), raw, unnamed},
		{"no policy", context.Background(), raw, unnamed},
		{"no key", context.Background(), "", AnonymousKey},
		{"nil context", nil, "", AnonymousKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := KeyName(tc.ctx, coreusage.Record{APIKey: tc.key})
			if got != tc.want {
				t.Fatalf("KeyName = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, raw) {
				t.Fatal("raw key leaked into the key name")
			}
		})
	}
	if !IsSyntheticKeyName(AnonymousKey) || !IsSyntheticKeyName(unnamed) || IsSyntheticKeyName("laptop") {
		t.Fatal("synthetic key name detection is wrong")
	}
}

func TestRecordAggregatesAndNormalises(t *testing.T) {
	ledger := NewLedger("", fixedClock)
	hour := fixedNow.Add(-2 * time.Hour)
	ctx := namedContext("laptop")
	details := []coreusage.Detail{
		{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, CacheReadTokens: 40, CacheCreationTokens: 10},
		{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
	}
	var want Counters
	for _, detail := range details {
		record := coreusage.Record{Provider: "claude", ExecutorType: "ClaudeExecutor", Model: "claude-opus", RequestedAt: hour, Detail: detail}
		ledger.Record(ctx, record)
		normalised := coreusage.EnsureTokenBreakdownForProvider(detail, "claude", "ClaudeExecutor")
		want.add(Counters{
			Requests:            1,
			InputTokens:         normalised.InputTokens,
			OutputTokens:        normalised.OutputTokens,
			ReasoningTokens:     normalised.ReasoningTokens,
			CacheReadTokens:     normalised.CacheReadTokens,
			CacheCreationTokens: normalised.CacheCreationTokens,
			TotalTokens:         normalised.TotalTokens,
		})
	}
	ledger.Record(ctx, coreusage.Record{Provider: "claude", ExecutorType: "ClaudeExecutor", Model: "claude-opus", RequestedAt: hour, Failed: true})
	want.FailedRequests = 1
	ledger.Record(ctx, coreusage.Record{Provider: "claude", Model: "claude-opus", RequestedAt: hour, Generate: coreusage.GenerateFlag(false), Detail: coreusage.Detail{InputTokens: 999}})
	ledger.Record(ctx, coreusage.Record{Provider: " ", Model: "", Detail: coreusage.Detail{TotalTokens: 1}})

	if want.TotalTokens == 0 {
		t.Fatal("normalisation should derive a total for the first record")
	}
	usage := ledger.Usage(fixedNow.Add(-24*time.Hour), fixedNow.Add(time.Hour), "")
	if len(usage) != 1 || usage[0].Name != "laptop" || len(usage[0].Models) != 2 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	opus := usage[0].Models[0]
	if opus.Provider != "claude" || opus.Model != "claude-opus" || opus.Counters != want {
		t.Fatalf("claude-opus counters = %+v, want %+v", opus, want)
	}
	unknown := usage[0].Models[1]
	if unknown.Provider != "unknown" || unknown.Model != "unknown" || unknown.Requests != 1 {
		t.Fatalf("records without provider or model should land in unknown: %+v", unknown)
	}
	ledger.mu.Lock()
	buckets := len(ledger.days[dayStart(hour).Unix()])
	ledger.mu.Unlock()
	if buckets != 2 {
		t.Fatalf("buckets = %d, want one per hour and model", buckets)
	}
}

func TestUsageWindowFilterAndOrdering(t *testing.T) {
	ledger := NewLedger("", fixedClock)
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	add := func(key, provider, model string, at time.Time, total int64) {
		ledger.Record(namedContext(key), coreusage.Record{Provider: provider, Model: model, RequestedAt: at, Detail: coreusage.Detail{TotalTokens: total}})
	}
	add("beta", "codex", "gpt-6", base.Add(1*time.Hour), 50)
	add("alpha", "codex", "gpt-6", base.Add(1*time.Hour+30*time.Minute), 50)
	add("alpha", "claude", "opus", base.Add(2*time.Hour), 10)
	add("alpha", "claude", "haiku", base.Add(2*time.Hour), 10)
	add("gamma", "codex", "gpt-6", base.Add(30*time.Hour), 500)

	usage := ledger.Usage(base, base.Add(24*time.Hour), "")
	if len(usage) != 2 || usage[0].Name != "alpha" || usage[1].Name != "beta" {
		t.Fatalf("keys must sort by total desc then name: %+v", usage)
	}
	if usage[0].TotalTokens != 70 || usage[0].Requests != 3 {
		t.Fatalf("alpha totals = %+v", usage[0].Counters)
	}
	models := usage[0].Models
	if models[0].Model != "gpt-6" || models[1].Model != "haiku" || models[2].Model != "opus" {
		t.Fatalf("models must sort by total desc then provider and model: %+v", models)
	}
	if got := ledger.Usage(base.Add(2*time.Hour), base.Add(3*time.Hour), ""); len(got) != 1 || got[0].TotalTokens != 20 {
		t.Fatalf("window must include its start hour and exclude its end hour: %+v", got)
	}
	if got := ledger.Usage(base, base.Add(48*time.Hour), "gamma"); len(got) != 1 || got[0].Name != "gamma" {
		t.Fatalf("key filter: %+v", got)
	}
	if got := ledger.Usage(base, base.Add(48*time.Hour), "missing"); len(got) != 0 {
		t.Fatalf("unknown key filter must be empty: %+v", got)
	}
	since, ok := ledger.RecordingSince()
	if !ok || !since.Equal(base.Add(time.Hour)) {
		t.Fatalf("recording since = %v %v", since, ok)
	}
	var nilLedger *Ledger
	if _, ok := nilLedger.RecordingSince(); ok || len(nilLedger.Usage(base, base, "")) != 0 {
		t.Fatal("nil ledger must report nothing")
	}
}

func TestConcurrentRecordFlushAndQuery(t *testing.T) {
	var clock atomic.Int64
	clock.Store(fixedNow.Unix())
	ledger := NewLedger(t.TempDir(), func() time.Time { return time.Unix(clock.Load(), 0).UTC() })
	const writers, perWriter = 8, 250
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ledger.Flush()
			}
		}
	}()
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ledger.Usage(fixedNow.Add(-48*time.Hour), fixedNow.Add(time.Hour), "")
				ledger.RecordingSince()
			}
		}
	}()
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			ctx := namedContext([]string{"alpha", "beta"}[writer%2])
			for i := 0; i < perWriter; i++ {
				at := fixedNow.Add(-time.Duration(i%30) * time.Hour)
				ledger.Record(ctx, coreusage.Record{Provider: "codex", Model: "gpt-6", RequestedAt: at, Detail: coreusage.Detail{TotalTokens: 2}})
			}
		}(writer)
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	ledger.Flush()

	reloaded := NewLedger(ledger.dir, ledger.now)
	reloaded.Load()
	for _, l := range []*Ledger{ledger, reloaded} {
		var requests, tokens int64
		for _, key := range l.Usage(fixedNow.Add(-48*time.Hour), fixedNow.Add(time.Hour), "") {
			requests += key.Requests
			tokens += key.TotalTokens
		}
		if requests != writers*perWriter || tokens != 2*writers*perWriter {
			t.Fatalf("requests=%d tokens=%d, want %d and %d", requests, tokens, writers*perWriter, 2*writers*perWriter)
		}
	}
}
