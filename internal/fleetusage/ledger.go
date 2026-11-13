package fleetusage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const (
	RetentionDays = 400
	AnonymousKey  = "anonymous"
	unnamedPrefix = "unnamed-"
	unknownValue  = "unknown"
)

type Counters struct {
	Requests            int64 `json:"requests"`
	FailedRequests      int64 `json:"failed_requests"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

func (c *Counters) add(other Counters) {
	c.Requests += other.Requests
	c.FailedRequests += other.FailedRequests
	c.InputTokens += other.InputTokens
	c.OutputTokens += other.OutputTokens
	c.ReasoningTokens += other.ReasoningTokens
	c.CacheReadTokens += other.CacheReadTokens
	c.CacheCreationTokens += other.CacheCreationTokens
	c.TotalTokens += other.TotalTokens
}

func (c Counters) negative() bool {
	return c.Requests < 0 || c.FailedRequests < 0 || c.InputTokens < 0 || c.OutputTokens < 0 ||
		c.ReasoningTokens < 0 || c.CacheReadTokens < 0 || c.CacheCreationTokens < 0 || c.TotalTokens < 0
}

type ModelUsage struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Counters
}

type KeyUsage struct {
	Name string
	Counters
	Models []ModelUsage
}

type bucketKey struct {
	hour     int64
	key      string
	provider string
	model    string
}

type Ledger struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	days  map[int64]map[bucketKey]*Counters
	dirty map[int64]struct{}

	flushMu sync.Mutex
}

func NewLedger(dir string, now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{
		dir:   dir,
		now:   now,
		days:  make(map[int64]map[bucketKey]*Counters),
		dirty: make(map[int64]struct{}),
	}
}

func KeyName(ctx context.Context, record coreusage.Record) string {
	if policy := access.KeyPolicyFromContext(ctx); policy != nil {
		if name := strings.TrimSpace(policy.Name); name != "" {
			return name
		}
	}
	if apiKey := strings.TrimSpace(record.APIKey); apiKey != "" {
		sum := sha256.Sum256([]byte(apiKey))
		return unnamedPrefix + hex.EncodeToString(sum[:4])
	}
	return AnonymousKey
}

func IsSyntheticKeyName(name string) bool {
	return name == AnonymousKey || strings.HasPrefix(name, unnamedPrefix)
}

func countersFor(record coreusage.Record) Counters {
	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	counters := Counters{
		InputTokens:         detail.InputTokens,
		OutputTokens:        detail.OutputTokens,
		ReasoningTokens:     detail.ReasoningTokens,
		CacheReadTokens:     detail.CacheReadTokens,
		CacheCreationTokens: detail.CacheCreationTokens,
		TotalTokens:         detail.TotalTokens,
	}
	if record.Failed {
		counters.FailedRequests = 1
	} else {
		counters.Requests = 1
	}
	return counters
}

func orUnknown(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return unknownValue
}

func (l *Ledger) Record(ctx context.Context, record coreusage.Record) {
	if l == nil || !coreusage.GenerateEnabled(record.Generate) {
		return
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = l.now()
	}
	hour := at.UTC().Truncate(time.Hour)
	if hour.Before(retentionCutoff(l.now())) {
		return
	}
	key := bucketKey{
		hour:     hour.Unix(),
		key:      KeyName(ctx, record),
		provider: orUnknown(record.Provider),
		model:    orUnknown(record.Model),
	}
	l.add(dayStart(hour).Unix(), key, countersFor(record), true)
}

func (l *Ledger) add(day int64, key bucketKey, delta Counters, markDirty bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	buckets := l.days[day]
	if buckets == nil {
		buckets = make(map[bucketKey]*Counters)
		l.days[day] = buckets
	}
	counters := buckets[key]
	if counters == nil {
		counters = &Counters{}
		buckets[key] = counters
	}
	counters.add(delta)
	if markDirty {
		l.dirty[day] = struct{}{}
	}
}

func (l *Ledger) RecordingSince() (time.Time, bool) {
	if l == nil {
		return time.Time{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	earliestDay, found := int64(0), false
	for day, buckets := range l.days {
		if len(buckets) > 0 && (!found || day < earliestDay) {
			earliestDay, found = day, true
		}
	}
	if !found {
		return time.Time{}, false
	}
	earliestHour := int64(0)
	first := true
	for key := range l.days[earliestDay] {
		if first || key.hour < earliestHour {
			earliestHour, first = key.hour, false
		}
	}
	return time.Unix(earliestHour, 0).UTC(), true
}

func (l *Ledger) Usage(from, to time.Time, keyFilter string) []KeyUsage {
	if l == nil {
		return []KeyUsage{}
	}
	fromUnix, toUnix := from.Unix(), to.Unix()
	firstDay := dayStart(from).Unix()
	type modelKey struct{ key, provider, model string }
	totals := make(map[modelKey]*Counters)
	l.mu.Lock()
	for day, buckets := range l.days {
		if day < firstDay || day >= toUnix {
			continue
		}
		for key, counters := range buckets {
			if key.hour < fromUnix || key.hour >= toUnix || (keyFilter != "" && key.key != keyFilter) {
				continue
			}
			mk := modelKey{key.key, key.provider, key.model}
			total := totals[mk]
			if total == nil {
				total = &Counters{}
				totals[mk] = total
			}
			total.add(*counters)
		}
	}
	l.mu.Unlock()

	byKey := make(map[string]*KeyUsage)
	for mk, counters := range totals {
		usage := byKey[mk.key]
		if usage == nil {
			usage = &KeyUsage{Name: mk.key}
			byKey[mk.key] = usage
		}
		usage.add(*counters)
		usage.Models = append(usage.Models, ModelUsage{Provider: mk.provider, Model: mk.model, Counters: *counters})
	}
	result := make([]KeyUsage, 0, len(byKey))
	for _, usage := range byKey {
		if usage.Counters == (Counters{}) {
			continue
		}
		sort.Slice(usage.Models, func(i, j int) bool {
			a, b := usage.Models[i], usage.Models[j]
			if a.TotalTokens != b.TotalTokens {
				return a.TotalTokens > b.TotalTokens
			}
			if a.Provider != b.Provider {
				return a.Provider < b.Provider
			}
			return a.Model < b.Model
		})
		result = append(result, *usage)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalTokens != result[j].TotalTokens {
			return result[i].TotalTokens > result[j].TotalTokens
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func dayStart(t time.Time) time.Time {
	year, month, day := t.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func retentionCutoff(now time.Time) time.Time {
	return dayStart(now).AddDate(0, 0, -RetentionDays)
}
