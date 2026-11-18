package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/fleetusage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const defaultRecentRequestsLimit = 200

type recentRequestsReport struct {
	Object      string              `json:"object"`
	GeneratedAt string              `json:"generated_at"`
	Capacity    int                 `json:"capacity"`
	LatestSeq   uint64              `json:"latest_seq"`
	Requests    []recentRequestView `json:"requests"`
}

type recentRequestView struct {
	Seq             uint64 `json:"seq"`
	RequestedAt     string `json:"requested_at"`
	Key             string `json:"key"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ResponseModel   string `json:"response_model,omitempty"`
	AuthIndex       string `json:"auth_index"`
	Account         string `json:"account"`
	Stream          bool   `json:"stream"`
	LatencyMS       int64  `json:"latency_ms"`
	TTFTMS          int64  `json:"ttft_ms,omitempty"`
	Failed          bool   `json:"failed"`
	StatusCode      int    `json:"status_code,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	fleetusage.Counters
}

func (h *Handler) GetRecentRequests(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ring := fleetusage.Recent()
	query, errQuery := parseRecentQuery(c, ring.Capacity())
	if errQuery != nil {
		clientKeyPolicyError(c, http.StatusBadRequest, errQuery.code, errQuery.Error())
		return
	}
	entries, latestSeq := ring.Snapshot(query)
	labelKey := h.fleetLimitsLabelKey()
	views := make([]recentRequestView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, recentRequestViewFor(entry, labelKey))
	}
	c.JSON(http.StatusOK, recentRequestsReport{
		Object:      "fleet.recent_requests",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Capacity:    ring.Capacity(),
		LatestSeq:   latestSeq,
		Requests:    views,
	})
}

func recentRequestViewFor(entry fleetusage.RecentRequest, labelKey []byte) recentRequestView {
	view := recentRequestView{
		Seq:             entry.Seq,
		RequestedAt:     entry.RequestedAt.UTC().Format(time.RFC3339Nano),
		Key:             entry.Key,
		Provider:        entry.Provider,
		Model:           entry.Model,
		ResponseModel:   entry.ResponseModel,
		AuthIndex:       entry.AuthIndex,
		Stream:          entry.Stream,
		LatencyMS:       entry.Latency.Milliseconds(),
		TTFTMS:          entry.TTFT.Milliseconds(),
		Failed:          entry.Failed,
		StatusCode:      entry.StatusCode,
		ServiceTier:     entry.ServiceTier,
		ReasoningEffort: entry.ReasoningEffort,
		Counters:        entry.Counters,
	}
	if entry.AuthID != "" {
		view.Account = coreauth.FleetLimitsLabel(labelKey, strings.ToLower(entry.Provider), entry.AuthID)
	}
	return view
}

func (h *Handler) fleetLimitsLabelKey() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return coreauth.FleetLimitsLabelKey(h.cfg)
}

type recentQueryError struct {
	code string
	error
}

func parseRecentQuery(c *gin.Context, capacity int) (fleetusage.RecentQuery, *recentQueryError) {
	query := fleetusage.RecentQuery{Limit: defaultRecentRequestsLimit, Key: strings.TrimSpace(c.Query("key"))}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		limit, errLimit := strconv.Atoi(raw)
		if errLimit != nil || limit < 1 || limit > capacity {
			return query, &recentQueryError{"invalid_limit", errors.New("limit must be an integer from 1 to " + strconv.Itoa(capacity))}
		}
		query.Limit = limit
	}
	if raw := strings.TrimSpace(c.Query("after_seq")); raw != "" {
		afterSeq, errAfter := strconv.ParseUint(raw, 10, 64)
		if errAfter != nil {
			return query, &recentQueryError{"invalid_after_seq", errors.New("after_seq must be a non-negative integer")}
		}
		query.AfterSeq = afterSeq
	}
	return query, nil
}
