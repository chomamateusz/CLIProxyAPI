package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func keyPolicyForGin(c *gin.Context) *access.KeyPolicy {
	if c == nil {
		return nil
	}
	if value, exists := c.Get(access.KeyPolicyGinKey); exists {
		policy, _ := value.(*access.KeyPolicy)
		return policy
	}
	if c.Request != nil {
		return access.KeyPolicyFromContext(c.Request.Context())
	}
	return nil
}

type clientRequestedModelContextKey struct{}

func WithClientRequestedModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, clientRequestedModelContextKey{}, model)
}

func inheritKeyPolicy(ctx context.Context, c *gin.Context) context.Context {
	if c == nil {
		return ctx
	}
	if c.Request != nil {
		if model, ok := c.Request.Context().Value(clientRequestedModelContextKey{}).(string); ok {
			ctx = WithClientRequestedModel(ctx, model)
		}
	}
	if _, exists := c.Get(access.KeyPolicyGinKey); exists {
		return access.WithKeyPolicy(ctx, keyPolicyForGin(c))
	}
	if policy := keyPolicyForGin(c); policy != nil {
		return access.WithKeyPolicy(ctx, policy)
	}
	return ctx
}

func applyKeyPolicy(ctx context.Context, model string) (context.Context, *interfaces.ErrorMessage) {
	policy := access.KeyPolicyFromContext(ctx)
	if policy == nil {
		return ctx, nil
	}
	if requested, ok := ctx.Value(clientRequestedModelContextKey{}).(string); ok {
		model = requested
	}
	if !policy.AllowsModel(model) {
		body, _ := json.Marshal(ErrorResponse{Error: ErrorDetail{
			Message: fmt.Sprintf("model %s is not allowed for this API key", model),
			Type:    "permission_error", Code: "model_not_allowed",
		}})
		return ctx, &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errors.New(string(body)), DirectResponse: true, Body: body}
	}
	return coreauth.WithCredentialFilter(ctx, func(auth *coreauth.Auth) bool {
		return auth != nil && policy.AllowsCredential(auth.FileName, auth.ID, auth.Label)
	}), nil
}

func filterKeyPolicyModelList(c *gin.Context, body []byte) ([]byte, error) {
	policy := keyPolicyForGin(c)
	if policy == nil {
		return body, nil
	}
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, errors.New("invalid model catalog")
	}
	for _, field := range []string{"data", "models"} {
		raw, exists := catalog[field]
		if !exists {
			continue
		}
		var models []json.RawMessage
		if err := json.Unmarshal(raw, &models); err != nil {
			return nil, errors.New("invalid model catalog")
		}
		filtered := make([]json.RawMessage, 0, len(models))
		ids := make([]string, 0, len(models))
		for _, model := range models {
			var entry struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(model, &entry); err != nil {
				return nil, errors.New("invalid model catalog")
			}
			id := entry.ID
			if id == "" {
				id = entry.Slug
			}
			if id == "" {
				id = strings.TrimPrefix(entry.Name, "models/")
			}
			if policy.AllowsModel(id) {
				filtered = append(filtered, model)
				ids = append(ids, id)
			}
		}
		catalog[field], _ = json.Marshal(filtered)
		if field == "data" {
			for _, boundary := range []string{"first_id", "last_id"} {
				if _, exists := catalog[boundary]; !exists {
					continue
				}
				id := ""
				if len(ids) > 0 {
					id = ids[0]
					if boundary == "last_id" {
						id = ids[len(ids)-1]
					}
				}
				catalog[boundary], _ = json.Marshal(id)
			}
		}
	}
	c.Writer.Header().Del("Content-Length")
	return json.Marshal(catalog)
}
