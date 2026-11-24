package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
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

func withClientRequestedModelIfUnset(ctx context.Context, model string) context.Context {
	if _, ok := ctx.Value(clientRequestedModelContextKey{}).(string); ok {
		return ctx
	}
	return WithClientRequestedModel(ctx, model)
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
	if err := checkKeyPolicyRoute(ctx, model, model); err != nil {
		return ctx, err
	}
	return coreauth.WithCredentialFilter(ctx, func(auth *coreauth.Auth) bool {
		return auth != nil && policy.AllowsCredential(auth.FileName, auth.ID, auth.Label)
	}), nil
}

func checkKeyPolicyRoute(ctx context.Context, requested, resolved string) *interfaces.ErrorMessage {
	policy := access.KeyPolicyFromContext(ctx)
	if policy == nil {
		return nil
	}
	if clientModel, ok := ctx.Value(clientRequestedModelContextKey{}).(string); ok {
		requested = clientModel
	}
	if !policy.AllowsModelRoute(requested, resolved) {
		return modelNotAllowed(requested)
	}
	return nil
}

func modelNotAllowed(model string) *interfaces.ErrorMessage {
	body, _ := json.Marshal(ErrorResponse{Error: ErrorDetail{
		Message: fmt.Sprintf("model %s is not allowed for this API key", model),
		Type:    "permission_error", Code: "model_not_allowed",
	}})
	return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errors.New(string(body)), DirectResponse: true, Body: body}
}

func ClaudeModelAliasPolicyError(c *gin.Context, alias, resolved string) *interfaces.ErrorMessage {
	if excludesClaudeModelAlias(keyPolicyForGin(c), alias, resolved) {
		return modelNotAllowed(resolved)
	}
	return nil
}

func excludesClaudeModelAlias(policy *access.KeyPolicy, alias, resolved string) bool {
	if policy == nil || alias == resolved {
		return false
	}
	alias = strings.TrimSpace(alias)
	base := strings.TrimSpace(thinking.ParseSuffix(alias).ModelName)
	for _, pattern := range policy.ExcludedModels {
		pattern = strings.TrimSpace(pattern)
		if claudemodels.IsCloakedModelPattern(pattern) && (access.WildcardMatch(pattern, alias) || access.WildcardMatch(pattern, base)) {
			return true
		}
	}
	return false
}

func filterKeyPolicyModelList(c *gin.Context, sourceFormat string, body []byte) ([]byte, error) {
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
			policyID := id
			if sourceFormat == constant.Claude {
				policyID = claudemodels.ResolveClaudeModelIDPrefix(id)
				if excludesClaudeModelAlias(policy, id, policyID) {
					continue
				}
			}
			if policy.AllowsModel(policyID) {
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
