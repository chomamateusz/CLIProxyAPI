package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tidwall/sjson"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

type modelExcludedError struct {
	model    string
	provider string
	until    time.Time
}

func (e *modelExcludedError) Error() string {
	return string(e.body(""))
}

func (e *modelExcludedError) message() string {
	until := "until further notice"
	if !e.until.IsZero() {
		until = "until " + e.until.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("model %s is excluded on this proxy %s (oauth-excluded-models, provider %s); pick another model", e.model, until, e.provider)
}

func (e *modelExcludedError) body(entryProtocol string) []byte {
	body := `{"error":{"type":"model_excluded","code":"model_excluded","param":"model","message":"","until":null}}`
	if entryProtocol == constant.Claude {
		body = `{"type":"error","error":{"type":"model_excluded","message":"","until":null}}`
	}
	if updated, errSet := sjson.Set(body, "error.message", e.message()); errSet == nil {
		body = updated
	}
	if !e.until.IsZero() {
		if updated, errSet := sjson.Set(body, "error.until", e.until.UTC().Format(time.RFC3339)); errSet == nil {
			body = updated
		}
	}
	return []byte(body)
}

func (h *BaseAPIHandler) modelExcludedErrorMessage(modelName, baseModel string) *interfaces.ErrorMessage {
	if h == nil || h.AuthManager == nil {
		return nil
	}
	exclusion, ok := h.AuthManager.FindOAuthModelExclusion(baseModel, time.Now())
	if !ok {
		return nil
	}
	excluded := &modelExcludedError{model: modelName, provider: exclusion.Provider, until: exclusion.Until}
	return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: excluded, DirectResponse: true, Body: excluded.body("")}
}

func modelExcludedForProtocol(entryProtocol string, msg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	var excluded *modelExcludedError
	if msg == nil || !errors.As(msg.Error, &excluded) {
		return msg
	}
	adapted := *msg
	adapted.Body = excluded.body(entryProtocol)
	return &adapted
}
