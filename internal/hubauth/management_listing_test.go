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
// reads, or the Dispatcher loses every Claude and Codex allowance key.
func TestManagementListsHydratedStoreCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	_, _, store, _ := setupStores(t)
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

	want := map[string]string{
		claude.ID:   "claude",
		codex.ID:    "codex",
		disabled.ID: "claude",
	}
	for _, tc := range []struct {
		path  string
		field string
	}{
		{path: "/v8/management/credentials", field: "files"},
		{path: "/v8/management/credentials/usage", field: "accounts"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", tc.path, rec.Code, rec.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s body: %v", tc.path, err)
		}
		var entries []map[string]any
		if err := json.Unmarshal(body[tc.field], &entries); err != nil {
			t.Fatalf("%s %s: %v", tc.path, tc.field, err)
		}
		seen := make(map[string]map[string]any, len(entries))
		for _, entry := range entries {
			id, _ := entry["id"].(string)
			seen[id] = entry
		}
		for id, provider := range want {
			entry := seen[id]
			if entry == nil {
				t.Fatalf("%s dropped store-backed credential %q", tc.path, id)
			}
			if entry["provider"] != provider {
				t.Fatalf("%s credential %q provider = %v, want %s", tc.path, id, entry["provider"], provider)
			}
			if entry["runtime_only"] != true {
				t.Fatalf("%s credential %q runtime_only = %v, want true", tc.path, id, entry["runtime_only"])
			}
			if _, ok := entry["path"]; ok {
				t.Fatalf("%s credential %q exposed an auth-file path", tc.path, id)
			}
		}
	}

	// Hydration marks the credential runtime-only in the listing, not by adding a
	// persisted attribute: the manager skips persisting any auth that carries
	// runtime_only. Keep the marker derived, and prove a control edit on the
	// hydrated credential still reaches the shared store.
	records, _, err := store.coordinator.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(records[claude.HubAccountID].Data), coreauth.AttributeRuntimeOnly) {
		t.Fatal("store record kept a runtime-only marker")
	}
	hydrated, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range hydrated {
		if auth.ID != claude.ID {
			continue
		}
		auth.Label = "renamed after hydration"
		if _, err := manager.Update(ctx, auth); err != nil {
			t.Fatalf("control edit after hydration: %v", err)
		}
		return
	}
	t.Fatal("hydrated credential not found")
}
