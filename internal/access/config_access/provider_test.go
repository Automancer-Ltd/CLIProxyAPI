package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// The provider-access role comes from the same snapshot that accepts the key, so a key
// removed from access.antigravity-keys is refused rather than turning ordinary (t15617).
func TestAntigravityKeyRoleComesFromTheSnapshotThatAcceptsIt(t *testing.T) {
	role := func(key string) (coreauth.ProviderAccess, bool) {
		manager := sdkaccess.NewManager()
		manager.SetProviders(sdkaccess.RegisteredProviders())
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		result, err := manager.Authenticate(context.Background(), req)
		if err != nil || result == nil || result.Principal != key {
			return "", false
		}
		return coreauth.ProviderAccess(result.Metadata[coreauth.ProviderAccessMetadataKey]), true
	}
	t.Cleanup(func() { Register(nil) })

	Register(&sdkconfig.SDKConfig{APIKeys: []string{"normal", "both"}, AntigravityKeys: []string{"agy", "both"}})
	for key, want := range map[string]coreauth.ProviderAccess{
		"normal": coreauth.ProviderAccessNoAntigravity,
		"agy":    coreauth.ProviderAccessAntigravityOnly,
		"both":   coreauth.ProviderAccessAntigravityOnly,
	} {
		if got, ok := role(key); !ok || got != want {
			t.Fatalf("key %s: role %q accepted %v; want %q", key, got, ok, want)
		}
	}

	Register(&sdkconfig.SDKConfig{APIKeys: []string{"normal"}})
	if _, ok := role("agy"); ok {
		t.Fatal("a removed antigravity key still authenticates")
	}
	if got, _ := role("normal"); got != coreauth.ProviderAccessOpen {
		t.Fatalf("without paired keys the ordinary role = %q, want open", got)
	}
}
