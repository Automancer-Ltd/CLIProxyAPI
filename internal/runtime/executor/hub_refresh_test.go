package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type refusingHub struct{ calls int }

func (h *refusingHub) Authorize(context.Context, *coreauth.Auth) error {
	return errors.New("fixture standby")
}
func (h *refusingHub) Refresh(context.Context, *coreauth.Auth, func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)) (*coreauth.Auth, error) {
	h.calls++
	return nil, errors.New("fixture standby")
}
func TestDirectNativeRefreshCannotBypassHubAuthority(t *testing.T) {
	for _, e := range []coreauth.ProviderExecutor{NewClaudeExecutor(&config.Config{}), NewCodexExecutor(&config.Config{}), NewCodexAutoExecutor(&config.Config{})} {
		guard := &refusingHub{}
		a := &coreauth.Auth{ID: "fixture.json", Provider: e.Identifier(), HubAuthority: guard, Metadata: map[string]any{"refresh_token": "fixture-secret"}}
		if _, err := e.Refresh(context.Background(), a); err == nil || guard.calls != 1 {
			t.Fatal("native refresh bypassed authority", e.Identifier())
		}
	}
}
