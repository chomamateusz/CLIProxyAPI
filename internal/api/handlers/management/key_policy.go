package management

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) rejectAPIKeyPolicyWriteLocked(c *gin.Context) bool {
	if h.cfg != nil && len(h.cfg.APIKeyPolicies) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "api_key_policies_present"})
		return true
	}
	return false
}

func rejectStringListWrite(guards []func() bool) bool {
	for _, guard := range guards {
		if guard() {
			return true
		}
	}
	return false
}
