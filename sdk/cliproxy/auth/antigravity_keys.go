package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const antigravityProvider = "antigravity"

// ProviderAccess is the provider-access role of a request's client key under the
// Antigravity pairing (access.antigravity-keys). The access provider decides it once,
// at authentication, from the same config snapshot that accepted the key, and the
// request carries it through selection, retries and every upstream attempt.
type ProviderAccess string

const (
	// ProviderAccessOpen: no key is paired, so every provider is allowed.
	ProviderAccessOpen ProviderAccess = "open"
	// ProviderAccessAntigravityOnly: a paired key, which reaches Antigravity only.
	ProviderAccessAntigravityOnly ProviderAccess = "antigravity-only"
	// ProviderAccessNoAntigravity: any other key while a key is paired.
	ProviderAccessNoAntigravity ProviderAccess = "no-antigravity"
)

// ProviderAccessMetadataKey names the role in an access Result's metadata.
const ProviderAccessMetadataKey = "provider_access"

type providerAccessContextKey struct{}

func parseProviderAccess(access string) ProviderAccess {
	switch role := ProviderAccess(access); role {
	case ProviderAccessOpen, ProviderAccessAntigravityOnly, ProviderAccessNoAntigravity:
		return role
	}
	return ""
}

// WithProviderAccess carries the role decided at authentication on the request context.
func WithProviderAccess(ctx context.Context, access string) context.Context {
	if role := parseProviderAccess(access); role != "" {
		return context.WithValue(ctx, providerAccessContextKey{}, role)
	}
	return ctx
}

// ProviderAccessFromContext returns the role carried by the request, or "" when the
// request was not authenticated by a client key (internal callers, management).
func ProviderAccessFromContext(ctx context.Context) ProviderAccess {
	if ctx == nil {
		return ""
	}
	if role, ok := ctx.Value(providerAccessContextKey{}).(ProviderAccess); ok {
		return role
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		if meta, ok := ginCtx.Get("accessMetadata"); ok {
			if values, ok := meta.(map[string]string); ok {
				return parseProviderAccess(values[ProviderAccessMetadataKey])
			}
		}
	}
	return ""
}

// AllowsProvider reports whether the role may use provider.
func (role ProviderAccess) AllowsProvider(provider string) bool {
	isAntigravity := strings.EqualFold(strings.TrimSpace(provider), antigravityProvider)
	switch role {
	case ProviderAccessAntigravityOnly:
		return isAntigravity
	case ProviderAccessNoAntigravity:
		return !isAntigravity
	}
	return true
}

// providerAccess resolves the request's role. A request without one (no client key)
// counts as an ordinary key while this Manager's config pairs any key: it fails closed
// for Antigravity.
func (m *Manager) providerAccess(ctx context.Context) ProviderAccess {
	if role := ProviderAccessFromContext(ctx); role != "" {
		return role
	}
	if cfg := m.runtimeConfigSnapshot(); cfg != nil {
		for _, key := range cfg.AntigravityKeys {
			if strings.TrimSpace(key) != "" {
				return ProviderAccessNoAntigravity
			}
		}
	}
	return ProviderAccessOpen
}

// AdmitProvider refuses (403, request-scoped so no credential cools down) an upstream
// attempt on provider that the request's role does not allow. Every upstream attempt
// passes it: the Manager's execute, count and stream boundaries, raw HTTP helpers,
// Home dispatch and direct plugin executors.
func (m *Manager) AdmitProvider(ctx context.Context, provider string) error {
	role := m.providerAccess(ctx)
	if role.AllowsProvider(provider) {
		return nil
	}
	message := fmt.Sprintf("provider %q is served only to the Antigravity client key", provider)
	if role == ProviderAccessAntigravityOnly {
		message = fmt.Sprintf("this API key reaches Antigravity only, not provider %q", provider)
	}
	return NewRequestScopedError(message, http.StatusForbidden)
}

// antigravityPairingProviders keeps the providers the request's role allows, or refuses
// the request when none is left, so the client gets a clear 403 instead of "no auth".
// AdmitProvider still guards each attempt.
func (m *Manager) antigravityPairingProviders(ctx context.Context, providers []string, model string) ([]string, error) {
	role := m.providerAccess(ctx)
	if role == ProviderAccessOpen {
		return providers, nil
	}
	allowed := make([]string, 0, len(providers))
	for _, provider := range providers {
		if role.AllowsProvider(provider) {
			allowed = append(allowed, provider)
		}
	}
	if len(allowed) > 0 {
		return allowed, nil
	}
	message := fmt.Sprintf("model %q is served only to the Antigravity client key", model)
	if role == ProviderAccessAntigravityOnly {
		message = fmt.Sprintf("this API key reaches Antigravity only, and Antigravity does not serve model %q", model)
	}
	return nil, &Error{Code: "model_not_allowed", Message: message, HTTPStatus: http.StatusForbidden}
}
