package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const managementPrincipalKey = "management.principal"

type managementPrincipal struct {
	Kind string
	Name string
}

func (p managementPrincipal) allows(c *gin.Context) bool {
	if p.Kind == "management" {
		return true
	}
	if p.Kind != "issuer" {
		return false
	}
	switch c.Request.Method {
	case http.MethodGet:
		return c.FullPath() == "/v8/management/issued-keys" || c.FullPath() == "/v8/management/usage/client-keys"
	case http.MethodPost:
		return c.FullPath() == "/v8/management/issued-keys"
	case http.MethodDelete:
		return c.FullPath() == "/v8/management/issued-keys/:name"
	}
	return false
}
