package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

type FleetAlertsConfig struct {
	WebhookURL    string `yaml:"webhook-url" json:"webhook-url"`
	StuckAfter    string `yaml:"stuck-after" json:"stuck-after"`
	CheckInterval string `yaml:"check-interval" json:"check-interval"`
}

func (c FleetAlertsConfig) Durations() (time.Duration, time.Duration, error) {
	if strings.TrimSpace(c.WebhookURL) == "" {
		return 0, 0, nil
	}
	u, err := url.Parse(c.WebhookURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return 0, 0, fmt.Errorf("fleet-alerts.webhook-url must be an absolute HTTP(S) URL without userinfo or fragment")
	}
	stuck, interval := time.Hour, time.Minute
	if c.StuckAfter != "" {
		stuck, err = time.ParseDuration(c.StuckAfter)
		if err != nil || stuck <= 0 {
			return 0, 0, fmt.Errorf("fleet-alerts.stuck-after must be a positive duration")
		}
	}
	if c.CheckInterval != "" {
		interval, err = time.ParseDuration(c.CheckInterval)
		if err != nil || interval <= 0 {
			return 0, 0, fmt.Errorf("fleet-alerts.check-interval must be a positive duration")
		}
	}
	return stuck, interval, nil
}
