package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Without a management secret labels stay opaque but change on every restart.
var fleetLimitsProcessLabelKey = func() []byte {
	key := make([]byte, sha256.Size)
	_, _ = rand.Read(key)
	return key
}()

func FleetLimitsLabelKey(cfg *config.Config) []byte {
	if cfg != nil {
		if secret := strings.TrimSpace(cfg.RemoteManagement.SecretKey); secret != "" {
			sum := sha256.Sum256([]byte("fleet-limits-label\x00" + secret))
			return sum[:]
		}
	}
	return append([]byte(nil), fleetLimitsProcessLabelKey...)
}

func FleetLimitsLabel(key []byte, provider, authID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(provider + "\x00" + authID))
	if provider == "" {
		provider = "unknown"
	}
	return provider + "-" + hex.EncodeToString(mac.Sum(nil))[:8]
}
