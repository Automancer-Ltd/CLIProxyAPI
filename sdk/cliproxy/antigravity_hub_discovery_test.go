package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type discoveryHubAuthority struct{ denied bool }

func (h *discoveryHubAuthority) Authorize(context.Context, *coreauth.Auth) error {
	if h.denied {
		return errors.New("fixture ownership lost")
	}
	return nil
}
func (*discoveryHubAuthority) Refresh(context.Context, *coreauth.Auth, func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)) (*coreauth.Auth, error) {
	return nil, errors.New("unexpected fixture refresh")
}

// Model discovery sends a bearer grant too: extraction into sdk/auth must not
// let a stale owner send it after the authority revokes that process.
func TestAntigravityDiscoveryChecksHubOwnershipAtDispatch(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != antigravityModelsPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"fixture-model":{}},"webSearchModelIds":[]}`))
	}))
	defer upstream.Close()
	authority := &discoveryHubAuthority{}
	auth := &coreauth.Auth{HubAuthority: authority}
	service := &Service{}
	for _, denied := range []bool{false, true} {
		authority.denied = denied
		hints, status := service.probeAntigravityModelCapabilityHints(context.Background(), auth, []string{upstream.URL}, "", "fixture-token", "fixture-client")
		if denied {
			if status != antigravityProbeStatusTransientError {
				t.Fatalf("revoked owner status = %v", status)
			}
		} else {
			if status != antigravityProbeStatusSuccess {
				t.Fatalf("active owner status = %v", status)
			}
			if _, ok := hints.ModelIDs["fixture-model"]; !ok {
				t.Fatal("catalog not returned")
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, revoked owner must not dispatch", calls.Load())
	}
}
