package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/fleetusage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const (
	recentClientKeyFixture   = "sk-recent-client-fixture"
	recentFailureBodyFixture = "upstream failure body fixture"
	recentAccountFixture     = "codex-owner@example.com.json"
)

type recentRequestsBody struct {
	Object      string           `json:"object"`
	GeneratedAt string           `json:"generated_at"`
	Capacity    int              `json:"capacity"`
	LatestSeq   uint64           `json:"latest_seq"`
	Requests    []map[string]any `json:"requests"`
}

func recentRequestsHandler() *Handler {
	cfg := &config.Config{}
	cfg.RemoteManagement.SecretKey = "management-secret-fixture"
	return &Handler{cfg: cfg}
}

func getRecentRequests(t *testing.T, h *Handler, query url.Values) (*httptest.ResponseRecorder, recentRequestsBody) {
	t.Helper()
	router := gin.New()
	router.GET("/v8/management/usage/recent-requests", h.GetRecentRequests)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v8/management/usage/recent-requests?"+query.Encode(), nil))
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing Cache-Control: no-store")
	}
	var body recentRequestsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %s", rec.Body.String())
	}
	return rec, body
}

func currentRecentSeq() uint64 {
	_, latest := fleetusage.Recent().Snapshot(fleetusage.RecentQuery{Limit: 1})
	return latest
}

func TestRecentRequestsShapeAndPrivacy(t *testing.T) {
	baseline := currentRecentSeq()
	ring := fleetusage.Recent()
	at := time.Date(2026, 11, 18, 9, 30, 15, 123456789, time.FixedZone("CET", 3600))
	ring.Record(policyContext("recent-laptop"), coreusage.Record{
		APIKey:               recentClientKeyFixture,
		AccessTokenSHA256:    "access-token-hash-fixture",
		BaseURL:              "https://upstream.example.invalid",
		SessionID:            "session-fixture",
		Provider:             "codex",
		Model:                "gpt-6",
		ResponseModel:        "gpt-6-2026-11",
		AuthID:               recentAccountFixture,
		AuthIndex:            "7",
		Stream:               true,
		RequestedAt:          at,
		Latency:              2500 * time.Millisecond,
		TTFT:                 400 * time.Millisecond,
		Failed:               true,
		Fail:                 coreusage.Failure{StatusCode: 429, Body: recentFailureBodyFixture},
		EffectiveServiceTier: "priority",
		ReasoningEffort:      "high",
		ResponseHeaders:      http.Header{"X-Secret-Header": {"header-fixture"}},
		Detail:               coreusage.Detail{InputTokens: 11, OutputTokens: 4, TotalTokens: 15},
	})
	ring.Record(policyContext("recent-laptop"), coreusage.Record{Provider: "claude", Model: "opus", ResponseModel: "opus", RequestedAt: at.Add(time.Second)})

	h := recentRequestsHandler()
	rec, body := getRecentRequests(t, h, url.Values{"after_seq": {strconv.FormatUint(baseline, 10)}})
	if rec.Code != http.StatusOK || body.Object != "fleet.recent_requests" || body.Capacity != fleetusage.RecentCapacity || body.LatestSeq != baseline+2 || len(body.Requests) != 2 {
		t.Fatalf("envelope: %s", rec.Body.String())
	}
	if _, errParse := time.Parse(time.RFC3339, body.GeneratedAt); errParse != nil {
		t.Fatal("generated_at is not RFC3339")
	}
	plain, failed := body.Requests[0], body.Requests[1]
	if plain["seq"].(float64) != float64(baseline+2) || failed["seq"].(float64) != float64(baseline+1) {
		t.Fatalf("requests must be newest first: %s", rec.Body.String())
	}
	for _, field := range []string{"response_model", "ttft_ms", "status_code", "service_tier", "reasoning_effort"} {
		if _, present := plain[field]; present {
			t.Fatalf("%s must be omitted when empty: %v", field, plain)
		}
	}
	if plain["account"] != "" || plain["auth_index"] != "" || plain["failed"] != false || plain["requests"].(float64) != 1 {
		t.Fatalf("plain entry: %v", plain)
	}
	wantAccount := coreauth.FleetLimitsLabel(coreauth.FleetLimitsLabelKey(h.cfg), "codex", recentAccountFixture)
	want := map[string]any{
		"requested_at":          at.UTC().Format(time.RFC3339Nano),
		"key":                   "recent-laptop",
		"provider":              "codex",
		"model":                 "gpt-6",
		"response_model":        "gpt-6-2026-11",
		"auth_index":            "7",
		"account":               wantAccount,
		"stream":                true,
		"latency_ms":            float64(2500),
		"ttft_ms":               float64(400),
		"failed":                true,
		"status_code":           float64(429),
		"service_tier":          "priority",
		"reasoning_effort":      "high",
		"requests":              float64(0),
		"failed_requests":       float64(1),
		"input_tokens":          float64(11),
		"output_tokens":         float64(4),
		"reasoning_tokens":      float64(0),
		"cache_read_tokens":     float64(0),
		"cache_creation_tokens": float64(0),
		"total_tokens":          float64(15),
	}
	for field, value := range want {
		if failed[field] != value {
			t.Fatalf("%s = %v, want %v", field, failed[field], value)
		}
	}
	if len(failed) != len(want)+1 {
		t.Fatalf("unexpected fields in an entry: %v", failed)
	}
	for _, forbidden := range []string{recentClientKeyFixture, recentFailureBodyFixture, recentAccountFixture, "example.com", "access-token-hash-fixture", "upstream.example.invalid", "session-fixture", "header-fixture", "X-Secret-Header"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestRecentRequestsQueryParameters(t *testing.T) {
	baseline := currentRecentSeq()
	for i := 0; i < 5; i++ {
		name := "recent-query-a"
		if i%2 == 1 {
			name = "recent-query-b"
		}
		fleetusage.Recent().Record(policyContext(name), coreusage.Record{Provider: "codex", Model: "gpt-6"})
	}
	h := recentRequestsHandler()
	seqs := func(body recentRequestsBody) []uint64 {
		out := make([]uint64, 0, len(body.Requests))
		for _, request := range body.Requests {
			out = append(out, uint64(request["seq"].(float64)))
		}
		return out
	}
	after := strconv.FormatUint(baseline, 10)

	_, body := getRecentRequests(t, h, url.Values{"after_seq": {after}, "key": {"recent-query-a"}})
	if got := seqs(body); len(got) != 3 || got[0] != baseline+5 || got[1] != baseline+3 || got[2] != baseline+1 {
		t.Fatalf("key filter: %v", got)
	}
	_, body = getRecentRequests(t, h, url.Values{"after_seq": {strconv.FormatUint(baseline+3, 10)}})
	if got := seqs(body); len(got) != 2 || got[0] != baseline+5 || got[1] != baseline+4 || body.LatestSeq != baseline+5 {
		t.Fatalf("after_seq: %v", got)
	}
	_, body = getRecentRequests(t, h, url.Values{"after_seq": {after}, "limit": {"2"}})
	if got := seqs(body); len(got) != 2 || got[0] != baseline+5 {
		t.Fatalf("limit: %v", got)
	}
	_, body = getRecentRequests(t, h, url.Values{"after_seq": {strconv.FormatUint(baseline+5, 10)}})
	if body.Requests == nil || len(body.Requests) != 0 {
		t.Fatalf("requests must be an empty array: %+v", body)
	}
	if rec, _ := getRecentRequests(t, h, url.Values{"limit": {strconv.Itoa(fleetusage.RecentCapacity)}}); rec.Code != http.StatusOK {
		t.Fatalf("limit at capacity: %d", rec.Code)
	}
	if _, body = getRecentRequests(t, h, url.Values{}); len(body.Requests) > 200 {
		t.Fatalf("default limit is 200: %d", len(body.Requests))
	}

	for _, tc := range []struct {
		query url.Values
		code  string
	}{
		{url.Values{"limit": {"0"}}, "invalid_limit"},
		{url.Values{"limit": {"-1"}}, "invalid_limit"},
		{url.Values{"limit": {strconv.Itoa(fleetusage.RecentCapacity + 1)}}, "invalid_limit"},
		{url.Values{"limit": {"many"}}, "invalid_limit"},
		{url.Values{"after_seq": {"-1"}}, "invalid_after_seq"},
		{url.Values{"after_seq": {"later"}}, "invalid_after_seq"},
	} {
		router := gin.New()
		router.GET("/recent", h.GetRecentRequests)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/recent?"+tc.query.Encode(), nil))
		var errBody map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
		if rec.Code != http.StatusBadRequest || errBody["error"] != tc.code || errBody["message"] == "" {
			t.Fatalf("%v: %d %s", tc.query, rec.Code, rec.Body.String())
		}
	}
}

func TestRecentRequestsConcurrentWithRecording(t *testing.T) {
	baseline := currentRecentSeq()
	h := recentRequestsHandler()
	router := gin.New()
	router.GET("/recent", h.GetRecentRequests)
	const writes = 1000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			fleetusage.Recent().Record(policyContext("recent-concurrent"), coreusage.Record{Provider: "codex", Model: "gpt-6", AuthID: "auth-" + strconv.Itoa(i)})
		}
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/recent?key=recent-concurrent&limit=50", nil))
				if rec.Code != http.StatusOK {
					t.Errorf("status %d", rec.Code)
					return
				}
			}
		}()
	}
	wg.Wait()
	_, body := getRecentRequests(t, h, url.Values{"key": {"recent-concurrent"}, "after_seq": {strconv.FormatUint(baseline, 10)}, "limit": {strconv.Itoa(fleetusage.RecentCapacity)}})
	if len(body.Requests) != writes || body.LatestSeq < baseline+writes {
		t.Fatalf("concurrent writes: %d entries, latest %d", len(body.Requests), body.LatestSeq)
	}
}
