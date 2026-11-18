package management

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/fleetusage"
)

type clientKeyUsageReport struct {
	Object         string               `json:"object"`
	GeneratedAt    string               `json:"generated_at"`
	From           string               `json:"from"`
	To             string               `json:"to"`
	RecordingSince *string              `json:"recording_since"`
	Granularity    string               `json:"granularity"`
	Keys           []clientKeyUsageView `json:"keys"`
	Series         *[]usageSeriesView   `json:"series,omitempty"`
}

type usageSeriesView struct {
	Start    string `json:"start"`
	Key      string `json:"key"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	fleetusage.Counters
}

const maxHourSeriesWindow = 744 * time.Hour

type clientKeyUsageView struct {
	Name      string  `json:"name"`
	CreatedAt *string `json:"created_at"`
	Revoked   bool    `json:"revoked"`
	fleetusage.Counters
	Models []fleetusage.ModelUsage `json:"models"`
}

type usageWindow struct {
	from, to time.Time
}

func (h *Handler) GetClientKeyUsage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	now := time.Now().UTC()
	ledger := fleetusage.Active()
	granularity, withSeries, errSeries := parseSeriesGranularity(c.Query("series"))
	if errSeries != nil {
		clientKeyPolicyError(c, http.StatusBadRequest, "invalid_series", errSeries.Error())
		return
	}
	recordingSince, recording := ledger.RecordingSince()
	window, errWindow := parseUsageWindow(c.Query("from"), c.Query("to"), now, recordingSince, recording)
	if errWindow != nil {
		clientKeyPolicyError(c, http.StatusBadRequest, "invalid_window", errWindow.Error())
		return
	}
	if withSeries && granularity == fleetusage.GranularityHour && window.to.Sub(window.from) > maxHourSeriesWindow {
		clientKeyPolicyError(c, http.StatusBadRequest, "invalid_window", "series=hour allows at most 744 hours; use series=day")
		return
	}
	keyFilter := strings.TrimSpace(c.Query("key"))
	createdAt := h.clientKeyCreationTimes()
	usage := ledger.Usage(window.from, window.to, keyFilter)
	keys := make([]clientKeyUsageView, 0, len(usage))
	for _, entry := range usage {
		created, current := createdAt[entry.Name]
		view := clientKeyUsageView{
			Name:     entry.Name,
			Revoked:  !current && !fleetusage.IsSyntheticKeyName(entry.Name),
			Counters: entry.Counters,
			Models:   entry.Models,
		}
		if created != "" {
			view.CreatedAt = &created
		}
		keys = append(keys, view)
	}
	report := clientKeyUsageReport{
		Object:      "fleet.client_key_usage",
		GeneratedAt: now.Format(time.RFC3339),
		From:        window.from.Format(time.RFC3339),
		To:          window.to.Format(time.RFC3339),
		Granularity: string(granularity),
		Keys:        keys,
	}
	if withSeries {
		series := seriesViews(ledger.Series(window.from, window.to, keyFilter, granularity))
		report.Series = &series
	}
	if recording {
		since := recordingSince.Format(time.RFC3339)
		report.RecordingSince = &since
	}
	c.JSON(http.StatusOK, report)
}

func parseSeriesGranularity(raw string) (granularity fleetusage.Granularity, withSeries bool, err error) {
	switch fleetusage.Granularity(raw) {
	case "":
		return fleetusage.GranularityHour, false, nil
	case fleetusage.GranularityHour, fleetusage.GranularityDay:
		return fleetusage.Granularity(raw), true, nil
	}
	return "", false, errors.New("series must be hour or day")
}

func seriesViews(buckets []fleetusage.SeriesBucket) []usageSeriesView {
	views := make([]usageSeriesView, 0, len(buckets))
	for _, bucket := range buckets {
		views = append(views, usageSeriesView{
			Start:    bucket.Start.Format(time.RFC3339),
			Key:      bucket.Key,
			Provider: bucket.Provider,
			Model:    bucket.Model,
			Counters: bucket.Counters,
		})
	}
	return views
}

func (h *Handler) clientKeyCreationTimes() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	createdAt := make(map[string]string)
	if h.cfg == nil {
		return createdAt
	}
	for _, policy := range h.cfg.APIKeyPolicies {
		if name := strings.TrimSpace(policy.Name); name != "" {
			createdAt[name] = strings.TrimSpace(policy.CreatedAt)
		}
	}
	return createdAt
}

func parseUsageWindow(rawFrom, rawTo string, now, earliest time.Time, recorded bool) (usageWindow, error) {
	to := now
	if rawTo = strings.TrimSpace(rawTo); rawTo != "" {
		parsed, errParse := time.Parse(time.RFC3339, rawTo)
		if errParse != nil {
			return usageWindow{}, errors.New("to must be an RFC3339 timestamp")
		}
		to = parsed.UTC()
	}
	from := to
	if rawFrom = strings.TrimSpace(rawFrom); rawFrom != "" {
		parsed, errParse := time.Parse(time.RFC3339, rawFrom)
		if errParse != nil {
			return usageWindow{}, errors.New("from must be an RFC3339 timestamp")
		}
		from = parsed.UTC()
		if from.After(to) {
			return usageWindow{}, errors.New("from must not be after to")
		}
	} else if recorded {
		from = earliest
		if from.After(to) {
			return usageWindow{}, errors.New("to precedes the earliest recorded bucket")
		}
	}
	return usageWindow{from: from.Truncate(time.Hour), to: ceilHour(to)}, nil
}

func ceilHour(t time.Time) time.Time {
	floor := t.Truncate(time.Hour)
	if floor.Equal(t) {
		return floor
	}
	return floor.Add(time.Hour)
}
