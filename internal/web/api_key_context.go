package web

import (
	"context"
	"net/http"
)

type apiKeyOwnerContextKey struct{}

func withAPIKeyOwner(r *http.Request, ownerID string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), apiKeyOwnerContextKey{}, ownerID))
}

func apiKeyOwner(r *http.Request) string {
	return apiKeyOwnerFromContext(r.Context())
}

func apiKeyOwnerFromContext(ctx context.Context) string {
	ownerID, _ := ctx.Value(apiKeyOwnerContextKey{}).(string)
	return ownerID
}
