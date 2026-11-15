package management

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func applyFleetEndsAtPatch(metadata map[string]any, fieldPath string, value any) error {
	if fieldPath != coreauth.MetadataFleetEndsAt {
		return fmt.Errorf("%s does not support nested fields", coreauth.MetadataFleetEndsAt)
	}
	if value == nil {
		delete(metadata, coreauth.MetadataFleetEndsAt)
		return nil
	}
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be an RFC3339 timestamp or null", coreauth.MetadataFleetEndsAt)
	}
	endsAt, err := coreauth.ParseFleetEndsAt(raw)
	if err != nil {
		return err
	}
	metadata[coreauth.MetadataFleetEndsAt] = coreauth.FormatFleetEndsAt(endsAt)
	return nil
}

func addFleetEndFields(entry gin.H, endsAt time.Time, now time.Time) {
	entry[coreauth.MetadataFleetEndsAt] = coreauth.FormatFleetEndsAt(endsAt)
	entry["fleet_ended"] = !now.Before(endsAt)
}

func addAuthFleetEndFields(entry gin.H, auth *coreauth.Auth, now time.Time) {
	if endsAt, ok := coreauth.FleetCredentialEndsAt(auth); ok {
		addFleetEndFields(entry, endsAt, now)
	}
}

func addAuthFileJSONFleetEndFields(entry gin.H, data []byte, now time.Time) {
	value := gjson.GetBytes(data, coreauth.MetadataFleetEndsAt)
	if value.Type != gjson.String {
		return
	}
	if endsAt, err := coreauth.ParseFleetEndsAt(value.String()); err == nil {
		addFleetEndFields(entry, endsAt, now)
	}
}
