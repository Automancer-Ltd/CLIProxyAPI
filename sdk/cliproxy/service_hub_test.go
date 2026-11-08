package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type stoppedHubAuthority struct{}

func (stoppedHubAuthority) Authorize(context.Context, *coreauth.Auth) error {
	return errors.New("fixture authority unavailable")
}
func (stoppedHubAuthority) Refresh(context.Context, *coreauth.Auth, func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)) (*coreauth.Auth, error) {
	return nil, errors.New("fixture authority unavailable")
}

func TestHubBuilderRejectsIncompatibleConfigurationBeforeStartup(t *testing.T) {
	for _, mode := range []string{"Home", "plugins", "native-key"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{HubAuthority: &config.HubAuthorityConfig{}}
			switch mode {
			case "Home":
				cfg.Home.Enabled = true
			case "plugins":
				cfg.Plugins.Enabled = true
			case "native-key":
				cfg.CodexKey = []config.CodexKey{{APIKey: "fixture-secret"}}
			}
			_, err := NewBuilder().WithConfig(cfg).WithConfigPath("does-not-exist").Build()
			if err == nil || !strings.Contains(err.Error(), "hub") {
				t.Fatal("invalid shared configuration accepted", err)
			}
		})
	}
}
func TestHubAntigravityDiscoveryDoesNotBypassAuthority(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{"models":{}}`)) }))
	defer server.Close()
	s := &Service{}
	a := &coreauth.Auth{ID: "fixture", Provider: "antigravity", HubAuthority: stoppedHubAuthority{}, Metadata: map[string]any{"project_id": "fixture"}}
	_, status := s.probeAntigravityModelCapabilityHints(context.Background(), a, []string{server.URL}, "direct://", "fixture-secret", "fixture")
	if calls != 0 || status == antigravityProbeStatusSuccess {
		t.Fatal("model discovery used revoked credential")
	}
}
