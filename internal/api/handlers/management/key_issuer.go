package management

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var issuedKeyName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type issuedKeyView struct {
	Name           string   `json:"name"`
	IssuedBy       string   `json:"issued-by"`
	CreatedAt      string   `json:"created-at"`
	ExpiresAt      string   `json:"expires-at"`
	Models         []string `json:"models"`
	ExcludedModels []string `json:"excluded-models"`
}

func issuedKeyMetadata(p access.KeyPolicy) issuedKeyView {
	models, excluded := p.Models, p.ExcludedModels
	if models == nil {
		models = []string{}
	}
	if excluded == nil {
		excluded = []string{}
	}
	return issuedKeyView{p.Name, p.IssuedBy, p.CreatedAt, p.ExpiresAt, models, excluded}
}

func (h *Handler) IssuedKeys(c *gin.Context) {
	value, _ := c.Get(managementPrincipalKey)
	principal, ok := value.(managementPrincipal)
	if !ok {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Header("Cache-Control", "no-store")
	var created access.KeyPolicy
	if c.Request.Method == http.MethodPost {
		var body struct {
			Name      string   `json:"name"`
			Models    []string `json:"models"`
			ExpiresAt string   `json:"expires-at"`
			TTL       string   `json:"ttl"`
		}
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || !issuedKeyName.MatchString(body.Name) || (body.TTL != "" && body.ExpiresAt != "") {
			clientKeyPolicyError(c, 400, "invalid_body", "Expected a valid name, models, and either expires-at or ttl")
			return
		}
		now := time.Now().UTC()
		expires := body.ExpiresAt
		if body.TTL != "" {
			ttl, err := time.ParseDuration(body.TTL)
			if err != nil || ttl <= 0 {
				clientKeyPolicyError(c, 400, "invalid_ttl", "TTL must be a positive Go duration")
				return
			}
			expires = now.Add(ttl).Format(time.RFC3339Nano)
		}
		if expires != "" {
			parsed, err := time.Parse(time.RFC3339, expires)
			if err != nil || !parsed.After(now) {
				clientKeyPolicyError(c, 400, "invalid_expiry", "Expiry must be a future RFC3339 timestamp")
				return
			}
		}
		created = access.KeyPolicy{Name: body.Name, IssuedBy: principal.Name, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: expires, Models: body.Models}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := os.ReadFile(h.configFilePath)
	if err != nil {
		clientKeyPolicyError(c, 500, "read_failed", "Could not read configuration")
		return
	}
	current, err := config.ParseConfigBytes(raw)
	if err != nil {
		clientKeyPolicyError(c, 500, "invalid_config", "Could not read client keys")
		return
	}
	if c.Request.Method == http.MethodGet {
		entries := make([]issuedKeyView, 0)
		for _, policy := range current.APIKeyPolicies {
			if policy.IssuedBy != "" && (principal.Kind == "management" || policy.IssuedBy == principal.Name) {
				entries = append(entries, issuedKeyMetadata(policy))
			}
		}
		c.JSON(200, entries)
		return
	}
	removed := make([]access.KeyPolicy, 0)
	if c.Request.Method == http.MethodPost {
		for _, policy := range current.APIKeyPolicies {
			if policy.Name == created.Name {
				clientKeyPolicyError(c, 409, "name_exists", "Client key name already exists")
				return
			}
		}
		var random [32]byte
		if _, err = rand.Read(random[:]); err != nil {
			clientKeyPolicyError(c, 500, "generation_failed", "Could not generate client key")
			return
		}
		created.Key = "sk-fleet-" + base64.RawURLEncoding.EncodeToString(random[:])
	} else {
		name, issuer := c.Param("name"), c.Query("issued-by")
		if name == "" && (principal.Kind != "management" || issuer == "") {
			clientKeyPolicyError(c, 400, "invalid_issuer", "Bulk revocation requires issued-by")
			return
		}
		for _, policy := range current.APIKeyPolicies {
			if policy.IssuedBy == "" || (principal.Kind != "management" && policy.IssuedBy != principal.Name) {
				continue
			}
			if (name != "" && policy.Name == name) || (name == "" && policy.IssuedBy == issuer) {
				removed = append(removed, policy)
			}
		}
		if name != "" && len(removed) == 0 {
			clientKeyPolicyError(c, 404, "not_found", "Issued key not found")
			return
		}
	}
	if created.Key == "" && len(removed) == 0 {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "revoked": 0})
		return
	}
	var node *yaml.Node
	if created.Key != "" {
		node = new(yaml.Node)
		if err = node.Encode(created); err != nil {
			clientKeyPolicyError(c, 500, "encode_failed", "Could not encode client key")
			return
		}
	}
	if created.Key == "" && len(current.APIKeys) == len(removed) && len(current.KeyIssuerKeys) == 0 {
		clientKeyPolicyError(c, http.StatusConflict, "last_client_key", "revoking the last client key would disable authentication")
		return
	}
	data, err := issuedKeysYAML(raw, node, removed)
	if err != nil {
		clientKeyPolicyError(c, 500, "invalid_config", "Could not preserve configuration around client keys")
		return
	}
	next, err := config.ParseConfigBytes(data)
	if err != nil || config.ValidateV8Config(data) != nil {
		clientKeyPolicyError(c, 500, "invalid_config", "Updated configuration is invalid")
		return
	}
	if err = WriteConfig(h.configFilePath, data, true); err != nil {
		clientKeyPolicyError(c, 500, "write_failed", "Could not save configuration")
		return
	}
	if h.cfg != nil {
		next.Home = h.cfg.Home
	}
	h.cfg = next
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), h.reloadSnapshotConfigLocked())
	if created.Key != "" {
		log.WithFields(log.Fields{"name": created.Name, "issuer": created.IssuedBy}).Info("Client key issued")
		c.JSON(201, struct {
			issuedKeyView
			Key string `json:"key"`
		}{issuedKeyMetadata(created), created.Key})
		return
	}
	for _, policy := range removed {
		log.WithFields(log.Fields{"name": policy.Name, "issuer": policy.IssuedBy}).Info("Client key revoked")
	}
	c.JSON(200, gin.H{"status": "ok", "revoked": len(removed)})
}
