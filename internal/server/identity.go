// Identity is the caller the authentik outpost authenticated, read from
// the headers it forwards and carried through the request context so MCP
// tools can authorize each call.
package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
)

const (
	// headerGroups carries the caller's groups, pipe-separated.
	headerGroups = "X-authentik-groups"

	// headerUsername carries the caller's authentik username.
	headerUsername = "X-authentik-username"
)

// identityKey is the context key holding the authenticated Identity.
type identityKey struct{}

// Identity is the authenticated caller behind an MCP request.
type Identity struct {
	User   string   `json:"user"`
	Groups []string `json:"groups"`
}

// IdentityFromRequest reads the identity headers the authentik outpost
// forwards. Group names are pipe-separated and surrounding spaces are
// trimmed; empty entries are dropped.
func IdentityFromRequest(r *http.Request) Identity {
	identity := Identity{User: strings.TrimSpace(r.Header.Get(headerUsername))}

	for _, group := range strings.Split(r.Header.Get(headerGroups), "|") {
		if group = strings.TrimSpace(group); group != "" {
			identity.Groups = append(identity.Groups, group)
		}
	}

	return identity
}

// InGroup reports whether the identity is a member of name.
func (i Identity) InGroup(name string) bool {
	return slices.Contains(i.Groups, name)
}

// WithIdentity returns a context carrying identity.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFromContext returns the identity carried by ctx, or the zero
// Identity when there is none.
func IdentityFromContext(ctx context.Context) Identity {
	identity, _ := ctx.Value(identityKey{}).(Identity)

	return identity
}

// withIdentity stores the request's identity in its context before
// handing the request to next.
func withIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), IdentityFromRequest(r))))
	})
}
