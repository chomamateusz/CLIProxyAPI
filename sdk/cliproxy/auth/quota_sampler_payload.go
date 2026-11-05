package auth

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type claudeUsageWindow struct {
	Utilization      *float64 `json:"utilization"`
	ResetsAt         string   `json:"resets_at"`
	LimitDollars     *float64 `json:"limit_dollars"`
	UsedDollars      *float64 `json:"used_dollars"`
	RemainingDollars *float64 `json:"remaining_dollars"`
}

type claudeUsagePayload struct {
	FiveHour *claudeUsageWindow `json:"five_hour"`
	SevenDay *claudeUsageWindow `json:"seven_day"`
	Fable    *claudeUsageWindow `json:"iguana_necktie"`
	Limits   []struct {
		Kind     string   `json:"kind"`
		Percent  *float64 `json:"percent"`
		ResetsAt string   `json:"resets_at"`
		Active   bool     `json:"is_active"`
		Scope    struct {
			Model struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	} `json:"limits"`
}

func parseQuotaSample(provider string, body []byte, now time.Time) (http.Header, error) {
	if provider == "codex" {
		return parseCodexQuotaSample(body, now)
	}
	var payload claudeUsagePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("invalid usage payload")
	}
	headers := make(http.Header)
	add := func(name string, window *claudeUsageWindow) bool {
		if window == nil || window.Utilization == nil || !validQuotaPercentage(*window.Utilization) {
			return false
		}
		reset, err := time.Parse(time.RFC3339Nano, window.ResetsAt)
		if err != nil || !reset.After(now) {
			return false
		}
		prefix := "Anthropic-Ratelimit-Unified-" + name + "-"
		headers.Set(prefix+"Utilization", strconv.FormatFloat(*window.Utilization/100, 'f', -1, 64))
		headers.Set(prefix+"Reset", reset.Format(time.RFC3339Nano))
		status := "allowed"
		if *window.Utilization >= 100 {
			status = "rejected"
		}
		headers.Set(prefix+"Status", status)
		return true
	}
	if !add("7d", payload.SevenDay) {
		return nil, errors.New("missing usable weekly window")
	}
	add("5h", payload.FiveHour)
	fable := payload.Fable
	if fable != nil && (fable.LimitDollars != nil || fable.UsedDollars != nil || fable.RemainingDollars != nil) {
		fable = nil
	}
	scoped := false
	for _, limit := range payload.Limits {
		name := strings.ToLower(strings.TrimSpace(limit.Scope.Model.DisplayName))
		if strings.EqualFold(strings.TrimSpace(limit.Kind), "weekly_scoped") && (name == "fable" || name == "fable 5") && limit.Percent != nil {
			if !scoped || limit.Active {
				fable = &claudeUsageWindow{Utilization: limit.Percent, ResetsAt: limit.ResetsAt}
				scoped = true
			}
			if limit.Active {
				break
			}
		}
	}
	add("7d_oi", fable)
	return headers, nil
}

func validQuotaPercentage(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

type codexUsageWindow struct {
	UsedPercent  *float64 `json:"used_percent"`
	LimitSeconds int64    `json:"limit_window_seconds"`
	ResetAt      int64    `json:"reset_at"`
	ResetAfter   *int64   `json:"reset_after_seconds"`
}

func parseCodexQuotaSample(body []byte, now time.Time) (http.Header, error) {
	var payload struct {
		Plan      string `json:"plan_type"`
		RateLimit struct {
			Primary   *codexUsageWindow `json:"primary_window"`
			Secondary *codexUsageWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		Credits struct {
			HasCredits *bool           `json:"has_credits"`
			Unlimited  *bool           `json:"unlimited"`
			Balance    json.RawMessage `json:"balance"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("invalid usage payload")
	}
	headers := make(http.Header)
	weekly := false
	for _, item := range []struct {
		name   string
		window *codexUsageWindow
	}{{"Primary", payload.RateLimit.Primary}, {"Secondary", payload.RateLimit.Secondary}} {
		w := item.window
		if w == nil || w.UsedPercent == nil || !validQuotaPercentage(*w.UsedPercent) || w.LimitSeconds <= 0 {
			continue
		}
		reset := time.Unix(w.ResetAt, 0)
		if w.ResetAt == 0 && w.ResetAfter != nil {
			reset = now.Add(time.Duration(*w.ResetAfter) * time.Second)
		}
		if !reset.After(now) {
			continue
		}
		prefix := "X-Codex-" + item.name + "-"
		headers.Set(prefix+"Used-Percent", strconv.FormatFloat(*w.UsedPercent, 'f', -1, 64))
		headers.Set(prefix+"Window-Minutes", strconv.FormatInt(w.LimitSeconds/60, 10))
		headers.Set(prefix+"Reset-At", strconv.FormatInt(reset.Unix(), 10))
		weekly = weekly || w.LimitSeconds == 7*24*60*60
	}
	if !weekly {
		return nil, errors.New("missing usable weekly window")
	}
	if validQuotaSignalValue(payload.Plan) {
		headers.Set("X-Codex-Plan-Type", payload.Plan)
	}
	if payload.Credits.HasCredits != nil {
		headers.Set("X-Codex-Credits-Has-Credits", strconv.FormatBool(*payload.Credits.HasCredits))
	}
	if payload.Credits.Unlimited != nil {
		headers.Set("X-Codex-Credits-Unlimited", strconv.FormatBool(*payload.Credits.Unlimited))
	}
	balance := strings.Trim(string(payload.Credits.Balance), "\"")
	if value, err := strconv.ParseFloat(balance, 64); err == nil && validQuotaPercentage(value) {
		headers.Set("X-Codex-Credits-Balance", balance)
	}
	return headers, nil
}
