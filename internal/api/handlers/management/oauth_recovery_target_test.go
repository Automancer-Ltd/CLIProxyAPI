package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A targeted Codex re-login must rewrite the target credential where it is stored,
// not write a second file at the top of the auth directory and leave the target stale.
func TestRequestCodexTokenRecoveryRewritesNestedTargetFile(t *testing.T) {
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		return &fakeCodexOAuthService{}
	}
	defer func() {
		newCodexOAuthService = originalNewCodexOAuthService
	}()

	authDir := filepath.Join(t.TempDir(), "auths")
	targetPath := filepath.Join(authDir, "nested", "account.json")
	if errMkdir := os.MkdirAll(filepath.Dir(targetPath), 0o700); errMkdir != nil {
		t.Fatalf("create nested auth dir: %v", errMkdir)
	}
	stale := `{"type":"codex","email":"codex-test-code@example.test","access_token":"stale-access"}`
	if errWrite := os.WriteFile(targetPath, []byte(stale), 0o600); errWrite != nil {
		t.Fatalf("write target credential: %v", errWrite)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/codex-auth-url", func(c *gin.Context) {
		c.Set(oauthSessionTargetContextKey, oauthRecoveryTarget{
			AuthID:   "nested/account.json",
			FileName: "account.json",
			Path:     targetPath,
			Provider: "codex",
			Email:    "codex-test-code@example.test",
		})
		handler.RequestCodexToken(c)
	})

	state := requestCodexTokenState(t, router)
	defer CompleteOAuthSession(state)
	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", state, "test-code", ""); errWrite != nil {
		t.Fatalf("write callback file: %v", errWrite)
	}
	waitForOAuthSessionDone(t, state)

	data, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("read target credential: %v", errRead)
	}
	if !strings.Contains(string(data), "access-test-code") {
		t.Fatalf("target credential was not rewritten with the new login: %s", data)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "account.json")); !os.IsNotExist(errStat) {
		t.Fatalf("recovery wrote a stray top-level account.json (stat error %v)", errStat)
	}
}

// A built-in login that ignores the recovery target must refuse a targeted request:
// otherwise it creates a new credential and reports success while the selected
// credential stays broken.
func TestStartOAuthV8RefusesTargetForLoginsThatIgnoreIt(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-a.json",
		FileName: "claude-a.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "claude", "email": "a@example.test"},
	}); errRegister != nil {
		t.Fatalf("register claude-a.json: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(ConfigV8ContextKey, true)
		c.Next()
	})
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)

	for _, body := range []string{`{"name":"claude-a.json"}`, `{"provider":"claude","name":"claude-a.json"}`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not supported") {
			t.Fatalf("targeted claude login %s = %d %s; want 400 not supported", body, w.Code, w.Body.String())
		}
	}
}
