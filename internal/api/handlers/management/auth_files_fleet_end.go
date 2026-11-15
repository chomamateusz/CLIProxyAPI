package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

const (
	fleetCancelledField      = "fleet_cancelled"
	fleetEndActionField      = "fleet_end_action"
	fleetEndActionNextPeriod = "next_period"
)

type fleetEndPatchKind int

const (
	fleetEndPatchNone fleetEndPatchKind = iota
	fleetEndPatchSet
	fleetEndPatchClear
	fleetEndPatchCancel
	fleetEndPatchNextPeriod
)

type fleetEndPatch struct {
	kind   fleetEndPatchKind
	endsAt time.Time
	anchor time.Time
}

func isFleetEndWritableField(root string) bool {
	return root == coreauth.MetadataFleetEndsAt || root == fleetCancelledField || root == fleetEndActionField
}

func isFleetEndReadOnlyField(root string) bool {
	return root == coreauth.MetadataFleetEndsAtSource || root == coreauth.MetadataFleetCancelledAt
}

func takeFleetEndPatch(req map[string]json.RawMessage) (fleetEndPatch, error) {
	var patch fleetEndPatch
	var taken []string
	for key, raw := range req {
		fieldPath := strings.TrimSpace(key)
		root := rootAuthFileField(fieldPath)
		switch {
		case isFleetEndReadOnlyField(root):
			return fleetEndPatch{}, fmt.Errorf("%s is read-only", root)
		case !isFleetEndWritableField(root):
			continue
		case fieldPath != root:
			return fleetEndPatch{}, fmt.Errorf("%s does not support nested fields", root)
		}
		parsed, err := parseFleetEndPatchField(root, raw)
		if err != nil {
			return fleetEndPatch{}, err
		}
		patch = parsed
		taken = append(taken, key)
	}
	if len(taken) > 1 {
		return fleetEndPatch{}, fmt.Errorf("%s, %s and %s cannot be combined", coreauth.MetadataFleetEndsAt, fleetCancelledField, fleetEndActionField)
	}
	for _, key := range taken {
		delete(req, key)
	}
	return patch, nil
}

func parseFleetEndPatchField(root string, raw json.RawMessage) (fleetEndPatch, error) {
	if trimmed := bytes.TrimSpace(raw); bytes.Equal(trimmed, []byte("null")) {
		if root == coreauth.MetadataFleetEndsAt {
			return fleetEndPatch{kind: fleetEndPatchClear}, nil
		}
		return fleetEndPatch{}, fleetEndFieldError(root)
	}
	switch root {
	case coreauth.MetadataFleetEndsAt:
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return fleetEndPatch{}, fleetEndFieldError(root)
		}
		endsAt, err := coreauth.ParseFleetEndsAt(value)
		if err != nil {
			return fleetEndPatch{}, err
		}
		return fleetEndPatch{kind: fleetEndPatchSet, endsAt: endsAt}, nil
	case fleetCancelledField:
		var cancelled bool
		if json.Unmarshal(raw, &cancelled) != nil {
			return fleetEndPatch{}, fleetEndFieldError(root)
		}
		if cancelled {
			return fleetEndPatch{kind: fleetEndPatchCancel}, nil
		}
		return fleetEndPatch{kind: fleetEndPatchClear}, nil
	default:
		var action string
		if json.Unmarshal(raw, &action) != nil || action != fleetEndActionNextPeriod {
			return fleetEndPatch{}, fleetEndFieldError(root)
		}
		return fleetEndPatch{kind: fleetEndPatchNextPeriod}, nil
	}
}

func fleetEndFieldError(root string) error {
	switch root {
	case fleetCancelledField:
		return fmt.Errorf("%s must be true or false", fleetCancelledField)
	case fleetEndActionField:
		return fmt.Errorf("%s must be %q", fleetEndActionField, fleetEndActionNextPeriod)
	default:
		return fmt.Errorf("%s must be an RFC3339 timestamp or null", coreauth.MetadataFleetEndsAt)
	}
}

func (p fleetEndPatch) needsBillingAnchor(metadata map[string]any) bool {
	return p.kind == fleetEndPatchCancel || p.kind == fleetEndPatchNextPeriod && coreauth.FleetEndNeedsBillingAnchor(metadata)
}

func (p fleetEndPatch) apply(metadata map[string]any, now time.Time) error {
	switch p.kind {
	case fleetEndPatchSet:
		coreauth.SetManualFleetEnd(metadata, p.endsAt)
	case fleetEndPatchClear:
		coreauth.ClearFleetEnd(metadata)
	case fleetEndPatchCancel:
		return coreauth.CancelAtBillingAnniversary(metadata, p.anchor, now)
	case fleetEndPatchNextPeriod:
		return coreauth.MoveFleetEndToNextPeriod(metadata, p.anchor)
	}
	return nil
}

func optionalFleetTime(entry gin.H, key string, value time.Time) {
	if !value.IsZero() {
		entry[key] = coreauth.FormatFleetEndsAt(value)
	}
}

func addFleetEndFields(entry gin.H, end coreauth.FleetCredentialEnd) {
	entry["fleet_end_state"] = string(end.State)
	warnings := end.Warnings
	if warnings == nil {
		warnings = []coreauth.FleetEndWarning{}
	}
	entry["fleet_end_warnings"] = warnings
	optionalFleetTime(entry, "fleet_billing_anchor", end.BillingAnchor)
	optionalFleetTime(entry, "fleet_end_checked_at", end.CheckedAt)
	if end.State == coreauth.FleetEndStateNone {
		return
	}
	entry[coreauth.MetadataFleetEndsAt] = coreauth.FormatFleetEndsAt(end.EndsAt)
	entry["fleet_ended"] = end.State == coreauth.FleetEndStateEnded
	entry[coreauth.MetadataFleetEndsAtSource] = end.Source
	optionalFleetTime(entry, coreauth.MetadataFleetCancelledAt, end.CancelledAt)
}

func addAuthFleetEndFields(entry gin.H, auth *coreauth.Auth, now time.Time) {
	addFleetEndFields(entry, coreauth.FleetCredentialEndFor(auth, now))
}

func addAuthFileJSONFleetEndFields(entry gin.H, data []byte, now time.Time) {
	metadata := make(map[string]any, 3)
	for _, key := range []string{coreauth.MetadataFleetEndsAt, coreauth.MetadataFleetEndsAtSource, coreauth.MetadataFleetCancelledAt} {
		if value := gjson.GetBytes(data, key); value.Type == gjson.String {
			metadata[key] = value.String()
		}
	}
	addFleetEndFields(entry, coreauth.FleetCredentialEndFromMetadata(metadata, now))
}
