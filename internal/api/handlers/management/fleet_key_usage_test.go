package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/fleetusage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func startTestLedger(t *testing.T) *fleetusage.Ledger {
	t.Helper()
	ledger := fleetusage.Start(context.Background(), t.TempDir())
	t.Cleanup(fleetusage.Stop)
	return ledger
}

func recordUsage(ledger *fleetusage.Ledger, ctx context.Context, provider, model string, at time.Time, total int64, failed bool) {
	ledger.Record(ctx, coreusage.Record{
		APIKey:      "raw-fixture-key",
		Provider:    provider,
		Model:       model,
		RequestedAt: at,
		Failed:      failed,
		Detail:      coreusage.Detail{InputTokens: total, TotalTokens: total},
	})
}

func policyContext(name string) context.Context {
	return access.WithKeyPolicy(context.Background(), &access.KeyPolicy{Name: name})
}

func clientKeyUsageHandler() *Handler {
	cfg := &config.Config{}
	cfg.APIKeyPolicies = []access.KeyPolicy{
		{Key: "k1", Name: "laptop", CreatedAt: "2026-10-01T08:00:00Z"},
		{Key: "k2", Name: "server"},
	}
	return &Handler{cfg: cfg}
}

func getClientKeyUsage(t *testing.T, h *Handler, query url.Values) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	router := gin.New()
	router.GET("/v8/management/usage/client-keys", h.GetClientKeyUsage)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v8/management/usage/client-keys?"+query.Encode(), nil))
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing Cache-Control: no-store")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %s", rec.Body.String())
	}
	return rec, body
}

func TestClientKeyUsageShapeAndOrdering(t *testing.T) {
	ledger := startTestLedger(t)
	base := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	recordUsage(ledger, policyContext("laptop"), "codex", "gpt-6", base, 100, false)
	recordUsage(ledger, policyContext("laptop"), "claude", "opus", base.Add(time.Hour), 40, false)
	recordUsage(ledger, policyContext("laptop"), "claude", "opus", base.Add(time.Hour), 0, true)
	recordUsage(ledger, policyContext("server"), "codex", "gpt-6", base, 140, false)
	recordUsage(ledger, policyContext("old-worker"), "codex", "gpt-6", base, 5, false)
	recordUsage(ledger, context.Background(), "codex", "gpt-6", base, 1, false)
	ledger.Record(context.Background(), coreusage.Record{Provider: "codex", Model: "gpt-6", RequestedAt: base, Detail: coreusage.Detail{TotalTokens: 1}})

	rec, body := getClientKeyUsage(t, clientKeyUsageHandler(), url.Values{"from": {base.Format(time.RFC3339)}, "to": {base.Add(2 * time.Hour).Format(time.RFC3339)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if body["object"] != "fleet.client_key_usage" || body["granularity"] != "hour" || body["recording_since"] != base.Format(time.RFC3339) {
		t.Fatalf("unexpected envelope: %s", rec.Body.String())
	}
	if _, errParse := time.Parse(time.RFC3339, body["generated_at"].(string)); errParse != nil {
		t.Fatal("generated_at is not RFC3339")
	}
	var report struct {
		Keys []struct {
			Name      string  `json:"name"`
			CreatedAt *string `json:"created_at"`
			Revoked   bool    `json:"revoked"`
			fleetusage.Counters
			Models []fleetusage.ModelUsage `json:"models"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(report.Keys))
	for _, key := range report.Keys {
		names = append(names, key.Name)
	}
	if len(names) != 5 || names[0] != "laptop" || names[1] != "server" || names[2] != "old-worker" || names[3] != "anonymous" {
		t.Fatalf("keys must sort by total_tokens desc then name: %v", names)
	}
	laptop, server, oldWorker, anonymous, unnamed := report.Keys[0], report.Keys[1], report.Keys[2], report.Keys[3], report.Keys[4]
	if laptop.CreatedAt == nil || *laptop.CreatedAt != "2026-10-01T08:00:00Z" || laptop.Revoked {
		t.Fatalf("laptop metadata: %+v", laptop)
	}
	if laptop.Requests != 2 || laptop.FailedRequests != 1 || laptop.TotalTokens != 140 || laptop.InputTokens != 140 {
		t.Fatalf("laptop counters: %+v", laptop.Counters)
	}
	if len(laptop.Models) != 2 || laptop.Models[0].Model != "gpt-6" || laptop.Models[1].Provider != "claude" || laptop.Models[1].FailedRequests != 1 {
		t.Fatalf("laptop models: %+v", laptop.Models)
	}
	if server.CreatedAt != nil || server.Revoked {
		t.Fatalf("server metadata: %+v", server)
	}
	if !oldWorker.Revoked || oldWorker.CreatedAt != nil {
		t.Fatalf("a name without a current policy must be revoked: %+v", oldWorker)
	}
	if anonymous.Revoked || unnamed.Revoked || len(unnamed.Name) != len("unnamed-")+8 {
		t.Fatalf("synthetic rows: %+v %+v", anonymous, unnamed)
	}
	if strings.Contains(rec.Body.String(), "raw-fixture-key") {
		t.Fatal("raw client key leaked into the response")
	}
}

func TestClientKeyUsageWindowAndKeyFilter(t *testing.T) {
	ledger := startTestLedger(t)
	base := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	recordUsage(ledger, policyContext("laptop"), "codex", "gpt-6", base, 1, false)
	recordUsage(ledger, policyContext("laptop"), "codex", "gpt-6", base.Add(time.Hour), 10, false)
	recordUsage(ledger, policyContext("server"), "codex", "gpt-6", base.Add(2*time.Hour), 100, false)
	recordUsage(ledger, policyContext("laptop"), "codex", "gpt-6", time.Now().UTC(), 1000, false)
	h := clientKeyUsageHandler()

	total := func(body map[string]any) float64 {
		var sum float64
		for _, key := range body["keys"].([]any) {
			sum += key.(map[string]any)["total_tokens"].(float64)
		}
		return sum
	}
	rec, body := getClientKeyUsage(t, h, url.Values{
		"from": {base.Add(time.Hour + 15*time.Minute).Format(time.RFC3339)},
		"to":   {base.Add(time.Hour + 45*time.Minute).Format(time.RFC3339)},
	})
	if rec.Code != http.StatusOK || body["from"] != base.Add(time.Hour).Format(time.RFC3339) || body["to"] != base.Add(2*time.Hour).Format(time.RFC3339) || total(body) != 10 {
		t.Fatalf("from must floor and to must ceil to the hour: %s", rec.Body.String())
	}
	_, body = getClientKeyUsage(t, h, url.Values{"to": {base.Add(2 * time.Hour).Format(time.RFC3339)}})
	if body["from"] != base.Format(time.RFC3339) || total(body) != 11 {
		t.Fatalf("from defaults to the earliest stored bucket: %v", body)
	}
	_, body = getClientKeyUsage(t, h, url.Values{})
	if total(body) != 1111 {
		t.Fatalf("the default window must reach now and include unflushed data: %v", body)
	}
	_, body = getClientKeyUsage(t, h, url.Values{"key": {"server"}})
	if keys := body["keys"].([]any); len(keys) != 1 || keys[0].(map[string]any)["name"] != "server" {
		t.Fatalf("key filter: %v", body)
	}
	_, body = getClientKeyUsage(t, h, url.Values{"key": {"nobody"}})
	if keys, ok := body["keys"].([]any); !ok || len(keys) != 0 {
		t.Fatalf("keys must be an empty array: %v", body)
	}
	rec, body = getClientKeyUsage(t, h, url.Values{
		"from": {base.Add(-3 * time.Hour).Format(time.RFC3339)},
		"to":   {base.Add(-time.Hour).Format(time.RFC3339)},
	})
	if keys, ok := body["keys"].([]any); rec.Code != http.StatusOK || !ok || len(keys) != 0 {
		t.Fatalf("a window before the data must be empty: %s", rec.Body.String())
	}
	rec, body = getClientKeyUsage(t, h, url.Values{"to": {base.Add(-time.Hour).Format(time.RFC3339)}})
	if rec.Code != http.StatusBadRequest || body["error"] != "invalid_window" || body["message"] != "to precedes the earliest recorded bucket" {
		t.Fatalf("a to before the earliest bucket with from omitted must be rejected: %d %s", rec.Code, rec.Body.String())
	}

	for _, query := range []url.Values{
		{"from": {"yesterday"}},
		{"to": {"2026-13-01T00:00:00Z"}},
		{"from": {"2026-11-02T00:00:00Z"}, "to": {"2026-11-01T00:00:00Z"}},
	} {
		rec, body := getClientKeyUsage(t, h, query)
		if rec.Code != http.StatusBadRequest || body["error"] != "invalid_window" || body["message"] == "" {
			t.Fatalf("%v: status %d body %s", query, rec.Code, rec.Body.String())
		}
	}
}

func TestClientKeyUsageWithoutLedger(t *testing.T) {
	window, err := parseUsageWindow("", "", time.Date(2026, 11, 13, 10, 20, 0, 0, time.UTC), time.Time{}, false)
	if err != nil || !window.from.Equal(time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)) || !window.to.Equal(time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("empty ledger window = %+v %v", window, err)
	}
	exact := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if window, err = parseUsageWindow(exact.Format(time.RFC3339), exact.Format(time.RFC3339), exact, time.Time{}, false); err != nil || !window.from.Equal(exact) || !window.to.Equal(exact) {
		t.Fatalf("aligned bounds must stay put: %+v %v", window, err)
	}
}

func TestClientKeyUsageConcurrentWithRecordingAndFlush(t *testing.T) {
	ledger := startTestLedger(t)
	h := clientKeyUsageHandler()
	now := time.Now().UTC()
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				recordUsage(ledger, policyContext("laptop"), "codex", "gpt-6", now, 1, false)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				ledger.Flush()
			}
		}()
		go func() {
			defer wg.Done()
			router := gin.New()
			router.GET("/usage", h.GetClientKeyUsage)
			for i := 0; i < 20; i++ {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage", nil))
				if rec.Code != http.StatusOK {
					t.Errorf("status %d", rec.Code)
				}
			}
		}()
	}
	wg.Wait()
	_, body := getClientKeyUsage(t, h, url.Values{"key": {"laptop"}})
	if keys := body["keys"].([]any); len(keys) != 1 || keys[0].(map[string]any)["requests"].(float64) != 800 {
		t.Fatalf("concurrent totals: %v", body)
	}
}

func TestIssuerAllowsOnlyClientKeyUsageReads(t *testing.T) {
	issuer := managementPrincipal{Kind: "issuer", Name: "fleet"}
	management := managementPrincipal{Kind: "management", Name: "management"}
	router := gin.New()
	routes := []string{"/v8/management/usage/client-keys", "/v8/management/usage/recent-requests", "/v8/management/issued-keys", "/v8/management/issued-keys/:name", "/v8/management/config", "/v8/management/observability/usage/api-keys"}
	for _, path := range routes {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			router.Handle(method, path, func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"issuer": issuer.allows(c), "management": management.allows(c)})
			})
		}
	}
	allowedForIssuer := map[string]bool{
		"GET /v8/management/usage/client-keys":     true,
		"GET /v8/management/usage/recent-requests": true,
		"GET /v8/management/issued-keys":           true,
		"POST /v8/management/issued-keys":          true,
		"DELETE /v8/management/issued-keys/:name":  true,
	}
	for _, path := range routes {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(method, concretePath(path), nil))
			var got map[string]bool
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s %s: %s", method, path, rec.Body.String())
			}
			if got["issuer"] != allowedForIssuer[method+" "+path] || !got["management"] {
				t.Fatalf("%s %s: %v", method, path, got)
			}
		}
	}
}

func concretePath(path string) string {
	if path == "/v8/management/issued-keys/:name" {
		return "/v8/management/issued-keys/worker"
	}
	return path
}

func TestClientKeyUsageSeries(t *testing.T) {
	ledger := startTestLedger(t)
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3)
	recordUsage(ledger, policyContext("server"), "codex", "gpt-6", day.Add(22*time.Hour), 1, false)
	recordUsage(ledger, policyContext("laptop"), "codex", "gpt-6", day.Add(22*time.Hour), 2, false)
	recordUsage(ledger, policyContext("laptop"), "codex", "gpt-6", day.Add(22*time.Hour+30*time.Minute), 0, true)
	recordUsage(ledger, policyContext("laptop"), "claude", "opus", day.Add(23*time.Hour), 4, false)
	recordUsage(ledger, policyContext("laptop"), "codex", "gpt-6", day.Add(25*time.Hour), 8, false)
	h := clientKeyUsageHandler()
	window := url.Values{"from": {day.Format(time.RFC3339)}, "to": {day.Add(48 * time.Hour).Format(time.RFC3339)}}
	withQuery := func(extra map[string]string) url.Values {
		query := url.Values{}
		for name, values := range window {
			query[name] = values
		}
		for name, value := range extra {
			query.Set(name, value)
		}
		return query
	}
	type seriesRow struct {
		Start    string `json:"start"`
		Key      string `json:"key"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		fleetusage.Counters
	}
	decodeSeries := func(rec *httptest.ResponseRecorder) []seriesRow {
		var report struct {
			Granularity string       `json:"granularity"`
			Series      *[]seriesRow `json:"series"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil || report.Series == nil {
			t.Fatalf("series missing: %s", rec.Body.String())
		}
		return *report.Series
	}

	rec, body := getClientKeyUsage(t, h, window)
	if _, present := body["series"]; rec.Code != http.StatusOK || present || body["granularity"] != "hour" {
		t.Fatalf("without series the report must keep its shape: %s", rec.Body.String())
	}

	rec, body = getClientKeyUsage(t, h, withQuery(map[string]string{"series": "hour"}))
	if rec.Code != http.StatusOK || body["granularity"] != "hour" || len(body["keys"].([]any)) != 2 {
		t.Fatalf("hour series: %s", rec.Body.String())
	}
	hourly := decodeSeries(rec)
	wantHourly := []struct {
		start                   time.Time
		key, provider           string
		requests, failed, total int64
	}{
		{day.Add(22 * time.Hour), "laptop", "codex", 1, 1, 2},
		{day.Add(22 * time.Hour), "server", "codex", 1, 0, 1},
		{day.Add(23 * time.Hour), "laptop", "claude", 1, 0, 4},
		{day.Add(25 * time.Hour), "laptop", "codex", 1, 0, 8},
	}
	if len(hourly) != len(wantHourly) {
		t.Fatalf("hour series rows: %+v", hourly)
	}
	for i, want := range wantHourly {
		got := hourly[i]
		if got.Start != want.start.Format(time.RFC3339) || got.Key != want.key || got.Provider != want.provider || got.Requests != want.requests || got.FailedRequests != want.failed || got.TotalTokens != want.total {
			t.Fatalf("hour row %d = %+v, want %+v", i, got, want)
		}
	}

	rec, body = getClientKeyUsage(t, h, withQuery(map[string]string{"series": "day", "key": "laptop"}))
	if rec.Code != http.StatusOK || body["granularity"] != "day" {
		t.Fatalf("day series: %s", rec.Body.String())
	}
	daily := decodeSeries(rec)
	if len(daily) != 3 ||
		daily[0].Start != day.Format(time.RFC3339) || daily[0].Provider != "claude" || daily[0].TotalTokens != 4 ||
		daily[1].Start != day.Format(time.RFC3339) || daily[1].Provider != "codex" || daily[1].Requests != 1 || daily[1].FailedRequests != 1 || daily[1].TotalTokens != 2 ||
		daily[2].Start != day.AddDate(0, 0, 1).Format(time.RFC3339) || daily[2].TotalTokens != 8 {
		t.Fatalf("day series rows: %+v", daily)
	}
	for _, row := range daily {
		if row.Key != "laptop" {
			t.Fatalf("key filter must apply to the series: %+v", daily)
		}
	}

	rec, _ = getClientKeyUsage(t, h, withQuery(map[string]string{"series": "day", "key": "nobody"}))
	if rows := decodeSeries(rec); len(rows) != 0 || !strings.Contains(rec.Body.String(), `"series":[]`) {
		t.Fatalf("an empty series must be an empty array: %s", rec.Body.String())
	}

	longWindow := url.Values{"from": {day.AddDate(0, 0, -40).Format(time.RFC3339)}, "to": {day.Add(48 * time.Hour).Format(time.RFC3339)}}
	longWindow.Set("series", "hour")
	rec, body = getClientKeyUsage(t, h, longWindow)
	if rec.Code != http.StatusBadRequest || body["error"] != "invalid_window" {
		t.Fatalf("series=hour beyond 744 hours must be rejected: %d %s", rec.Code, rec.Body.String())
	}
	longWindow.Set("series", "day")
	if rec, _ = getClientKeyUsage(t, h, longWindow); rec.Code != http.StatusOK || len(decodeSeries(rec)) != 4 {
		t.Fatalf("series=day accepts any window: %d %s", rec.Code, rec.Body.String())
	}
	longWindow.Del("series")
	if rec, _ = getClientKeyUsage(t, h, longWindow); rec.Code != http.StatusOK {
		t.Fatalf("the window limit applies only to series=hour: %d %s", rec.Code, rec.Body.String())
	}
	limitWindow := url.Values{"from": {day.Format(time.RFC3339)}, "to": {day.Add(744 * time.Hour).Format(time.RFC3339)}, "series": {"hour"}}
	if rec, _ = getClientKeyUsage(t, h, limitWindow); rec.Code != http.StatusOK {
		t.Fatalf("exactly 744 hours is allowed: %d %s", rec.Code, rec.Body.String())
	}

	for _, value := range []string{"week", "HOUR", "1h"} {
		rec, body = getClientKeyUsage(t, h, withQuery(map[string]string{"series": value}))
		if rec.Code != http.StatusBadRequest || body["error"] != "invalid_series" || body["message"] == "" {
			t.Fatalf("series=%s: %d %s", value, rec.Code, rec.Body.String())
		}
	}
}
