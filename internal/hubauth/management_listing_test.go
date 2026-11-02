package hubauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Store-backed logins have no auth-file path. The management listing must show
// them anyway, in both the credentials list and the usage list the hub door
// reads, or the Dispatcher loses every Claude and Codex allowance key. The
// listing change must stay scoped to store-backed logins: config-synthesized
// API-key routes keep their config listing shape and never expose their keys.
func TestManagementListsHydratedStoreCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	_, _, store, _ := setupStores(t)

	staticKey := "fixture-secret-static-api-key"
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "fixture-compat", BaseURL: "https://fixture.example.test/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: staticKey}}},
		},
	}
	static, err := ConfigAuths(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(static) != 1 {
		t.Fatalf("static route synthesis produced %d routes, want 1", len(static))
	}
	if err = store.ConfigureStatic(static); err != nil {
		t.Fatal(err)
	}

	claude := loginFixture(t, store, "claude", "claude-listing@example.test")
	codex := loginFixture(t, store, "codex", "codex-listing@example.test")
	disabled := &coreauth.Auth{
		ID:       "claude-disabled-listing@example.test",
		Provider: "claude",
		Disabled: true,
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"refresh_token": "fixture-secret-disabled", "access_token": "fixture-secret-access"},
	}
	if _, err := store.Save(coreauth.WithAuthCreationIntent(ctx), disabled); err != nil {
		t.Fatal(err)
	}

	manager := coreauth.NewManager(store, nil, nil)
	if err := manager.Load(ctx); err != nil {
		t.Fatal(err)
	}
	handler := management.NewHandler(&config.Config{AuthDir: t.TempDir()}, "", manager)
	router := gin.New()
	v8 := router.Group("/v8/management", func(c *gin.Context) {
		c.Set(management.ConfigV8ContextKey, true)
		c.Next()
	})
	v8.GET("/credentials", handler.ListAuthFiles)
	v8.GET("/credentials/usage", handler.ListCredentialUsage)

	secretKeys := []string{staticKey, "fixture-secret-disabled", "fixture-secret-access"}
	want := map[string]string{
		claude.ID:   "claude",
		codex.ID:    "codex",
		disabled.ID: "claude",
	}
	type listing struct {
		entries []map[string]any
		raw     string
	}
	fetch := func(path string, field string) listing {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s body: %v", path, err)
		}
		var entries []map[string]any
		if err := json.Unmarshal(body[field], &entries); err != nil {
			t.Fatalf("%s %s: %v", path, field, err)
		}
		li := listing{entries: entries, raw: string(rec.Body.Bytes())}
		// No listing may leak a credential secret: neither the static config
		// key nor any store-backed login's token material.
		for _, secret := range secretKeys {
			if containsString(li.raw, secret) {
				t.Fatalf("%s leaked fixture secret %q", path, secret)
			}
		}
		return li
	}
	byID := func(li listing, id string) map[string]any {
		t.Helper()
		for _, entry := range li.entries {
			if id, _ := entry["id"].(string); id == id {
				return entry
			}
		}
		return nil
	}

	credentials := fetch("/v8/management/credentials", "files")
	usage := fetch("/v8/management/credentials/usage", "accounts")

	for id, provider := range want {
		for name, li := range map[string]listing{"credentials": credentials, "usage": usage} {
			entry := byID(li, id)
			if entry == nil {
				t.Fatalf("/v8/management/%s dropped store-backed credential %q", name, id)
			}
			if entry["provider"] != provider {
				t.Fatalf("/v8/management/%s credential %q provider = %v, want %s", name, id, entry["provider"], provider)
			}
			if entry["runtime_only"] != true {
				t.Fatalf("/v8/management/%s credential %q runtime_only = %v, want true", name, id, entry["runtime_only"])
			}
			if _, ok := entry["path"]; ok {
				t.Fatalf("/v8/management/%s credential %q exposed an auth-file path", name, id)
			}
		}
	}

	// The config-synthesized static route never becomes a file-style entry: it
	// keeps its config listing in the usage view, and the credentials file list
	// leaves API-key config routes out as before.
	staticID := static[0].ID
	if byID(credentials, staticID) != nil {
		t.Fatalf("/v8/management/credentials listed config route %q as a file credential", staticID)
	}
	staticEntry := byID(usage, staticID)
	if staticEntry == nil {
		t.Fatalf("/v8/management/credentials/usage dropped config route %q", staticID)
	}
	if staticEntry["source"] != "config" {
		t.Fatalf("config route %q source = %v, want config", staticID, staticEntry["source"])
	}
	if staticEntry["name"] != static[0].Label {
		t.Fatalf("config route %q name = %v, want its config label %q", staticID, staticEntry["name"], static[0].Label)
	}
	if _, ok := staticEntry["account"]; ok {
		t.Fatalf("config route %q exposed an account identifier", staticID)
	}

	// Hydration marks the credential runtime-only in the listing, not by adding a
	// persisted attribute: the manager skips persisting any auth that carries
	// runtime_only. Keep the marker derived, and prove a control edit on the
	// hydrated credential still reaches the shared store.
	hydrated, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var target *coreauth.Auth
	for _, auth := range hydrated {
		if auth.ID == claude.ID {
			target = auth
			break
		}
	}
	if target == nil {
		t.Fatal("hydrated credential not found")
	}
	target.Label = "renamed after hydration"
	if _, err := manager.Update(ctx, target); err != nil {
		t.Fatalf("control edit after hydration: %v", err)
	}
	persisted, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range persisted {
		if auth.ID != claude.ID {
			continue
		}
		if auth.Label != "renamed after hydration" {
			t.Fatalf("store kept label %q after the persisted control edit", auth.Label)
		}
		return
	}
	t.Fatal("persisted credential not found in the store")
}

func containsString(haystack, needle string) bool {
	return len(needle) > 0 && strings.Contains(haystack, needle)
}
