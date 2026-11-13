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
}

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
	recordingSince, recording := ledger.RecordingSince()
	window, errWindow := parseUsageWindow(c.Query("from"), c.Query("to"), now, recordingSince, recording)
	if errWindow != nil {
		clientKeyPolicyError(c, http.StatusBadRequest, "invalid_window", errWindow.Error())
		return
	}
	createdAt := h.clientKeyCreationTimes()
	usage := ledger.Usage(window.from, window.to, strings.TrimSpace(c.Query("key")))
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
		Granularity: "hour",
		Keys:        keys,
	}
	if recording {
		since := recordingSince.Format(time.RFC3339)
		report.RecordingSince = &since
	}
	c.JSON(http.StatusOK, report)
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
