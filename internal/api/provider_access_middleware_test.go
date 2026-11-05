package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// The role decided at authentication reaches the request context every handler and the
// auth manager read.
func TestAuthMiddlewareCarriesProviderAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configaccess.Register(&sdkconfig.SDKConfig{APIKeys: []string{"normal"}, AntigravityKeys: []string{"agy"}})
	t.Cleanup(func() { configaccess.Register(nil) })
	manager := sdkaccess.NewManager()
	manager.SetProviders(sdkaccess.RegisteredProviders())
	engine := gin.New()
	engine.GET("/role", AuthMiddleware(manager), func(c *gin.Context) {
		c.String(http.StatusOK, string(coreauth.ProviderAccessFromContext(c.Request.Context())))
	})
	for key, want := range map[string]string{"agy": "antigravity-only", "normal": "no-antigravity"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/role", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Fatalf("key %s: %d %q, want %q", key, rec.Code, rec.Body.String(), want)
		}
	}
}
