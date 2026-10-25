package auth

import "context"

type credentialFilterContextKey struct{}

func WithCredentialFilter(ctx context.Context, filter func(*Auth) bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if previous := credentialFilterFromContext(ctx); previous != nil && filter != nil {
		next := filter
		filter = func(auth *Auth) bool { return previous(auth) && next(auth) }
	}
	return context.WithValue(ctx, credentialFilterContextKey{}, filter)
}

func credentialFilterFromContext(ctx context.Context) func(*Auth) bool {
	if ctx == nil {
		return nil
	}
	filter, _ := ctx.Value(credentialFilterContextKey{}).(func(*Auth) bool)
	return filter
}

func filterCredentialCandidates(ctx context.Context, auths []*Auth) []*Auth {
	filter := credentialFilterFromContext(ctx)
	if filter == nil {
		return auths
	}
	filtered := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if auth != nil && filter(auth) {
			filtered = append(filtered, auth)
		}
	}
	return filtered
}
