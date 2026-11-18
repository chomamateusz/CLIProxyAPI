package fleetusage

import (
	"context"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const RecentCapacity = 2000

type RecentRequest struct {
	Seq             uint64
	RequestedAt     time.Time
	Key             string
	Provider        string
	Model           string
	ResponseModel   string
	AuthID          string
	AuthIndex       string
	Stream          bool
	Latency         time.Duration
	TTFT            time.Duration
	Failed          bool
	StatusCode      int
	ServiceTier     string
	ReasoningEffort string
	Counters
}

type RecentRing struct {
	now func() time.Time

	mu      sync.Mutex
	entries []RecentRequest
	next    int
	size    int
	lastSeq uint64
}

func NewRecentRing(capacity int, now func() time.Time) *RecentRing {
	if capacity < 1 {
		capacity = 1
	}
	if now == nil {
		now = time.Now
	}
	return &RecentRing{now: now, entries: make([]RecentRequest, capacity)}
}

var recent = NewRecentRing(RecentCapacity, nil)

func Recent() *RecentRing { return recent }

func (r *RecentRing) Capacity() int { return len(r.entries) }

func (r *RecentRing) Record(ctx context.Context, record coreusage.Record) {
	if r == nil || !coreusage.GenerateEnabled(record.Generate) {
		return
	}
	entry := r.entryFor(ctx, record)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSeq++
	entry.Seq = r.lastSeq
	r.entries[r.next] = entry
	r.next = (r.next + 1) % len(r.entries)
	if r.size < len(r.entries) {
		r.size++
	}
}

func (r *RecentRing) entryFor(ctx context.Context, record coreusage.Record) RecentRequest {
	at := record.RequestedAt
	if at.IsZero() {
		at = r.now()
	}
	model := orUnknown(record.Model)
	responseModel := strings.TrimSpace(record.ResponseModel)
	if responseModel == model {
		responseModel = ""
	}
	entry := RecentRequest{
		RequestedAt:     at.UTC(),
		Key:             KeyName(ctx, record),
		Provider:        orUnknown(record.Provider),
		Model:           model,
		ResponseModel:   responseModel,
		AuthID:          strings.TrimSpace(record.AuthID),
		AuthIndex:       strings.TrimSpace(record.AuthIndex),
		Stream:          record.Stream,
		Latency:         record.Latency,
		TTFT:            record.TTFT,
		Failed:          record.Failed,
		ServiceTier:     servedTier(record),
		ReasoningEffort: strings.TrimSpace(record.ReasoningEffort),
		Counters:        countersFor(record),
	}
	if record.Failed && record.Fail.StatusCode > 0 {
		entry.StatusCode = record.Fail.StatusCode
	}
	return entry
}

func servedTier(record coreusage.Record) string {
	if tier := strings.TrimSpace(record.EffectiveServiceTier); tier != "" {
		return tier
	}
	return strings.TrimSpace(record.ResponseServiceTier)
}

type RecentQuery struct {
	Limit    int
	Key      string
	AfterSeq uint64
}

func (r *RecentRing) Snapshot(query RecentQuery) (requests []RecentRequest, latestSeq uint64) {
	requests = make([]RecentRequest, 0)
	if r == nil {
		return requests, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for offset := 1; offset <= r.size && len(requests) < query.Limit; offset++ {
		entry := r.entries[(r.next-offset+len(r.entries))%len(r.entries)]
		if entry.Seq <= query.AfterSeq {
			break
		}
		if query.Key == "" || entry.Key == query.Key {
			requests = append(requests, entry)
		}
	}
	return requests, r.lastSeq
}
