package cliproxy

import (
	"context"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (s *Service) fetchClaudeProfile(ctx context.Context, auth *coreauth.Auth, accessToken string) ([]byte, error) {
	return claudeauth.NewClaudeAuthWithProxyURL(s.currentConfig(), auth.ProxyURL).FetchOAuthProfileBody(ctx, accessToken)
}
