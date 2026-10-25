package management

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

type clientKeyPolicyLists struct {
	Models              []string `json:"models" yaml:"models"`
	ExcludedModels      []string `json:"excluded-models" yaml:"excluded-models"`
	Credentials         []string `json:"credentials" yaml:"credentials"`
	ExcludedCredentials []string `json:"excluded-credentials" yaml:"excluded-credentials"`
}

type clientKeyPolicyEntry struct {
	IssuedBy  string `json:"issued-by" yaml:"issued-by"`
	ExpiresAt string `json:"expires-at" yaml:"expires-at"`
	Index     int    `json:"index"`
	Name      string `json:"name"`
	HasPolicy bool   `json:"has_policy"`
	clientKeyPolicyLists
}

func (h *Handler) ClientKeyPolicies(c *gin.Context) {
	var replacement *clientKeyPolicyLists
	var patch *struct {
		Add    clientKeyPolicyLists `json:"add"`
		Remove clientKeyPolicyLists `json:"remove"`
	}
	if c.Request.Method != http.MethodGet {
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		var err error
		if c.Request.Method == http.MethodPut {
			err = decoder.Decode(&replacement)
		} else {
			err = decoder.Decode(&patch)
		}
		if err != nil || (replacement == nil && patch == nil) || decoder.Decode(new(any)) != io.EOF {
			clientKeyPolicyError(c, http.StatusBadRequest, "invalid_body", "Expected a policy object containing string lists")
			return
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := os.ReadFile(h.configFilePath)
	if err != nil {
		clientKeyPolicyError(c, http.StatusInternalServerError, "read_failed", "Could not read configuration")
		return
	}
	data, _, err := config.NormalizeConfigLayout(raw, true)
	if err != nil {
		clientKeyPolicyError(c, http.StatusInternalServerError, "invalid_config", "Could not read client key policies")
		return
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		clientKeyPolicyError(c, http.StatusInternalServerError, "invalid_config", "Could not read client key policies")
		return
	}
	keys := configV8Node(doc.Content[0], []string{"access", "api-keys"})
	entries := make([]clientKeyPolicyEntry, 0)
	if keys != nil && keys.Tag != "!!null" {
		if keys.Kind != yaml.SequenceNode {
			clientKeyPolicyError(c, http.StatusInternalServerError, "invalid_config", "Could not read client key policies")
			return
		}
		for i, node := range keys.Content {
			entry := clientKeyPolicyEntry{Index: i, HasPolicy: node.Kind == yaml.MappingNode}
			if entry.HasPolicy {
				if name := configV8Node(node, []string{"name"}); name != nil {
					entry.Name = name.Value
				}
				if issuedBy := configV8Node(node, []string{"issued-by"}); issuedBy != nil {
					entry.IssuedBy = issuedBy.Value
				}
				if expiresAt := configV8Node(node, []string{"expires-at"}); expiresAt != nil {
					entry.ExpiresAt = expiresAt.Value
				}
				if err = node.Decode(&entry.clientKeyPolicyLists); err != nil {
					clientKeyPolicyError(c, http.StatusInternalServerError, "invalid_config", "Could not read client key policies")
					return
				}
			}
			entry.emptyLists()
			entries = append(entries, entry)
		}
	}
	c.Header("Cache-Control", "no-store")
	name := c.Param("name")
	if c.Request.Method == http.MethodGet && name == "" {
		c.JSON(http.StatusOK, entries)
		return
	}
	index := -1
	for i, entry := range entries {
		if entry.Name == name && name != "" {
			if index >= 0 {
				clientKeyPolicyError(c, http.StatusConflict, "ambiguous_name", "Multiple client keys have this name; use an index")
				return
			}
			index = i
		}
	}
	if index < 0 {
		if numeric, errIndex := strconv.Atoi(name); errIndex == nil && numeric >= 0 && numeric < len(entries) {
			index = numeric
		}
	}
	if index < 0 {
		clientKeyPolicyError(c, http.StatusNotFound, "not_found", "Client key policy not found")
		return
	}
	entry := entries[index]
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, entry)
		return
	}
	if replacement != nil {
		entry.clientKeyPolicyLists = *replacement
	} else {
		entry.Models = mergeClientKeyPolicyList(entry.Models, patch.Add.Models, patch.Remove.Models)
		entry.ExcludedModels = mergeClientKeyPolicyList(entry.ExcludedModels, patch.Add.ExcludedModels, patch.Remove.ExcludedModels)
		entry.Credentials = mergeClientKeyPolicyList(entry.Credentials, patch.Add.Credentials, patch.Remove.Credentials)
		entry.ExcludedCredentials = mergeClientKeyPolicyList(entry.ExcludedCredentials, patch.Add.ExcludedCredentials, patch.Remove.ExcludedCredentials)
	}
	entry.HasPolicy = true
	entry.emptyLists()
	node := keys.Content[index]
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		key := *node
		key.HeadComment, key.FootComment = "", ""
		*node = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: node.HeadComment, FootComment: node.FootComment}
		setYAMLMappingValue(node, "key", &key)
	}
	if node.Kind != yaml.MappingNode {
		clientKeyPolicyError(c, http.StatusInternalServerError, "invalid_config", "Could not update client key policy")
		return
	}
	var update yaml.Node
	if err = update.Encode(entry.clientKeyPolicyLists); err != nil {
		clientKeyPolicyError(c, http.StatusInternalServerError, "invalid_config", "Could not update client key policy")
		return
	}
	for i := 0; i < len(update.Content); i += 2 {
		field, value := update.Content[i].Value, update.Content[i+1]
		if previous := configV8Node(node, []string{field}); previous != nil {
			value.HeadComment, value.LineComment, value.FootComment = previous.HeadComment, previous.LineComment, previous.FootComment
			value.Style = previous.Style
			for j, item := range value.Content {
				for _, oldItem := range previous.Content {
					if oldItem.Kind == yaml.ScalarNode && oldItem.Tag == item.Tag && oldItem.Value == item.Value {
						value.Content[j] = cloneConfigV8Node(oldItem)
						break
					}
				}
			}
		}
		setYAMLMappingValue(node, field, value)
	}
	data, err = yaml.Marshal(&doc)
	if err != nil {
		clientKeyPolicyError(c, http.StatusInternalServerError, "invalid_config", "Could not encode client key policy")
		return
	}
	next, err := config.ParseConfigBytes(data)
	if err != nil || config.ValidateV8Config(data) != nil {
		clientKeyPolicyError(c, http.StatusBadRequest, "invalid_config", "Updated configuration is invalid")
		return
	}
	data, preserveLayout, err := clientKeyPolicyYAML(raw, data, index, node)
	if err != nil {
		clientKeyPolicyError(c, http.StatusBadRequest, "invalid_config", "Could not preserve configuration around the policy edit")
		return
	}
	if err = WriteConfig(h.configFilePath, data, preserveLayout); err != nil {
		clientKeyPolicyError(c, http.StatusInternalServerError, "write_failed", "Could not save configuration")
		return
	}
	if h.cfg != nil {
		next.Home = h.cfg.Home
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, struct {
		Status string `json:"status"`
		clientKeyPolicyEntry
	}{Status: "ok", clientKeyPolicyEntry: entry})
}

func (p *clientKeyPolicyLists) emptyLists() {
	for _, list := range []*[]string{&p.Models, &p.ExcludedModels, &p.Credentials, &p.ExcludedCredentials} {
		if *list == nil {
			*list = []string{}
		}
	}
}

func mergeClientKeyPolicyList(current, add, remove []string) []string {
	seen := make(map[string]bool, len(current)+len(add)+len(remove))
	for _, value := range remove {
		seen[value] = true
	}
	result := make([]string, 0, len(current)+len(add))
	for _, list := range [][]string{current, add} {
		for _, value := range list {
			if !seen[value] {
				result = append(result, value)
				seen[value] = true
			}
		}
	}
	return result
}

func clientKeyPolicyError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": code, "message": message})
}
