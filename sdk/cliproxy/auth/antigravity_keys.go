package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const antigravityProvider = "antigravity"

// antigravityKeys holds the client keys paired exclusively with Antigravity
// (access.antigravity-keys). setConfigSnapshotLocked refreshes it on every config load.
// ponytail: process-wide, which fits the one Manager a hub process runs.
var antigravityKeys atomic.Pointer[map[string]struct{}]

func setAntigravityKeys(cfg *internalconfig.Config) {
	keys := make(map[string]struct{})
	if cfg != nil {
		for _, key := range cfg.AntigravityKeys {
			if key = strings.TrimSpace(key); key != "" {
				keys[key] = struct{}{}
			}
		}
	}
	antigravityKeys.Store(&keys)
}

// AntigravityAccess reports how the request's client key may use Antigravity.
// restricted is false while no key is paired: every key may use every provider.
// Otherwise a paired key reaches Antigravity only, and every other key never does.
func AntigravityAccess(ctx context.Context) (restricted, paired bool) {
	keys := antigravityKeys.Load()
	if keys == nil || len(*keys) == 0 {
		return false, false
	}
	_, paired = (*keys)[coreusage.APIKeyFromContext(ctx)]
	return true, paired
}

// AntigravityAllowsProvider reports whether a restricted request may use provider:
// a paired key only Antigravity, any other key anything but Antigravity.
func AntigravityAllowsProvider(paired bool, provider string) bool {
	return paired == strings.EqualFold(strings.TrimSpace(provider), antigravityProvider)
}

// antigravityPairingProviders keeps the providers the request's key may use, or
// refuses the request when none is left.
func antigravityPairingProviders(ctx context.Context, providers []string, model string) ([]string, error) {
	restricted, paired := AntigravityAccess(ctx)
	if !restricted {
		return providers, nil
	}
	allowed := make([]string, 0, len(providers))
	for _, provider := range providers {
		if AntigravityAllowsProvider(paired, provider) {
			allowed = append(allowed, provider)
		}
	}
	if len(allowed) > 0 {
		return allowed, nil
	}
	message := fmt.Sprintf("model %q is served only to the Antigravity client key", model)
	if paired {
		message = fmt.Sprintf("this API key reaches Antigravity only, and Antigravity does not serve model %q", model)
	}
	return nil, &Error{Code: "model_not_allowed", Message: message, HTTPStatus: http.StatusForbidden}
}
