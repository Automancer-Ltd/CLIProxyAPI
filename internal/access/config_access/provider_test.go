package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityKeysAreClientKeys(t *testing.T) {
	authenticate := func(key string) bool {
		manager := sdkaccess.NewManager()
		manager.SetProviders(sdkaccess.RegisteredProviders())
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		result, err := manager.Authenticate(context.Background(), req)
		return err == nil && result != nil && result.Principal == key
	}
	t.Cleanup(func() { Register(nil) })

	Register(&sdkconfig.SDKConfig{APIKeys: []string{"normal"}, AntigravityKeys: []string{"agy"}})
	if !authenticate("normal") || !authenticate("agy") {
		t.Fatal("api-keys and antigravity-keys entries must both authenticate")
	}
	Register(&sdkconfig.SDKConfig{APIKeys: []string{"normal"}})
	if authenticate("agy") {
		t.Fatal("a removed antigravity key still authenticates after reload")
	}
}
