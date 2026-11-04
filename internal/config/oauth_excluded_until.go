package config

import (
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

func NormalizeOAuthExcludedModelsUntil(entries map[string]map[string]string) map[string]map[string]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(entries))
	for provider, patterns := range entries {
		providerKey := strings.ToLower(strings.TrimSpace(provider))
		if providerKey == "" {
			continue
		}
		for pattern, raw := range patterns {
			patternKey := strings.ToLower(strings.TrimSpace(pattern))
			until, errParse := time.Parse(time.RFC3339, strings.TrimSpace(raw))
			if patternKey == "" {
				continue
			}
			if errParse != nil {
				log.Warnf("oauth-excluded-models-until: ignoring %s/%s, the value is not an RFC3339 time", providerKey, patternKey)
				continue
			}
			if out[providerKey] == nil {
				out[providerKey] = make(map[string]string, len(patterns))
			}
			out[providerKey][patternKey] = until.UTC().Format(time.RFC3339)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
