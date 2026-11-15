package auth

import (
	"fmt"
	"strings"
	"time"
)

// Providers expose no cancellation date, so this value is only ever set by hand.
const MetadataFleetEndsAt = "fleet_ends_at"

func ParseFleetEndsAt(raw string) (time.Time, error) {
	endsAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp or null", MetadataFleetEndsAt)
	}
	return endsAt.UTC().Truncate(time.Second), nil
}

func FormatFleetEndsAt(endsAt time.Time) string {
	return endsAt.UTC().Format(time.RFC3339)
}

func FleetCredentialEndsAt(auth *Auth) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	raw, _ := auth.Metadata[MetadataFleetEndsAt].(string)
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	endsAt, err := ParseFleetEndsAt(raw)
	return endsAt, err == nil
}

func FleetCredentialEnded(auth *Auth, now time.Time) bool {
	endsAt, ok := FleetCredentialEndsAt(auth)
	return ok && !now.Before(endsAt)
}

func (s *UseOrLoseSelector) drainsEndingCredentials() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.DrainEndingCredentials
}

func (s *UseOrLoseSelector) drainEndingCredential(auth *Auth, sample useOrLoseSample) useOrLoseSample {
	if !s.drainsEndingCredentials() {
		return sample
	}
	endsAt, ok := FleetCredentialEndsAt(auth)
	if ok && (sample.window.reset.IsZero() || endsAt.Before(sample.window.reset)) {
		sample.window.reset = endsAt
	}
	return sample
}
