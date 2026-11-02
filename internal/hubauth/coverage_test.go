package hubauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	native "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"google.golang.org/protobuf/encoding/protowire"
	"gopkg.in/yaml.v3"
)

func staticConfig(t *testing.T) *config.Config {
	t.Helper()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(`api-keys:
  openai-compatibility:
    - name: deepseek
      base-url: https://fixture.invalid/v1
      keys: [{api-key: fixture-secret-key}]
    - name: zai
      base-url: https://fixture.invalid/v1
      keys: [{api-key: fixture-secret-key}]
    - name: opencode-go
      base-url: https://fixture.invalid/v1
      keys: [{api-key: fixture-secret-key}]
    - name: local
      base-url: http://fixture.invalid/v1
      keys: []
    - name: gemini-credit
      base-url: https://fixture.invalid/v1
      keys: [{api-key: fixture-secret-key}]
`), &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func TestStaticRoutesRetainRequestsWithoutRefreshIntents(t *testing.T) {
	ctx := context.Background()
	f, _, s, _ := setupStores(t)
	cfg := staticConfig(t)
	auths, err := ConfigAuths(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 5 {
		t.Fatalf("routes lost: %d", len(auths))
	}
	if err = s.ConfigureStatic(auths); err != nil {
		t.Fatal(err)
	}
	m := coreauth.NewManager(s, nil, nil)
	if err = m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", localProviderTransport{target})
	for _, a := range m.List() {
		m.RegisterExecutor(native.NewOpenAICompatExecutor(a.Provider, cfg))
		if a.HubGeneration != 0 || a.HubAccountID != "" {
			t.Fatal("static route invented OAuth generation")
		}
		req, _ := http.NewRequest("GET", "https://fixture.invalid/models", nil)
		resp, err := m.HttpRequest(ctx, a, req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if a.Label != "local" && req.Header.Get("Authorization") != "Bearer fixture-secret-key" {
			t.Fatal("static key behavior lost")
		}
		if _, err := m.ForceRefreshAuth(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 5 || len(f.intents) != 0 || len(f.accounts) != 0 {
		t.Fatal("static serving or refresh semantics changed")
	}
	_ = s.coordinator.Stop(ctx)
	for _, a := range m.List() {
		req, _ := http.NewRequest("GET", "https://fixture.invalid/models", nil)
		if _, err := m.HttpRequest(ctx, a, req); err == nil {
			t.Fatal("static route served without authority")
		}
	}
	if calls.Load() != 5 {
		t.Fatal("unguarded provider call")
	}
}

func TestNativeServingRoutesRecheckAtDispatch(t *testing.T) {
	for _, name := range []string{"antigravity", "xai", "meta", "devin"} {
		t.Run(name, func(t *testing.T) {
			_, _, s, _ := setupStores(t)
			a := &coreauth.Auth{ID: name + ".json", Provider: name, Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "fixture-secret-access", "refresh_token": "fixture-secret-refresh", "expired": "2099-01-01T00:00:00Z", "project_id": "fixture", "token_endpoint": "https://fixture.invalid/token", "api_key": "fixture-secret-key", "session_token": "fixture-secret-session", "dca_token": "dca:fixture-secret"}}
			if _, err := s.Save(coreauth.WithAuthCreationIntent(context.Background()), a); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{}`) }))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", localProviderTransport{target})
			e := nativeProvider(name)
			if name == "devin" {
				e = native.NewDevinExecutor(&config.Config{})
			}
			m := coreauth.NewManager(s, nil, nil)
			if err := m.Load(ctx); err != nil {
				t.Fatal(err)
			}
			m.RegisterExecutor(e)
			req, _ := http.NewRequest("GET", "https://fixture.invalid/status", nil)
			resp, err := m.HttpRequest(ctx, a, req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			_ = s.coordinator.Stop(ctx)
			// Direct native callers must also be fenced, independent of Manager's early check.
			if _, err = e.HttpRequest(ctx, a, req); err == nil {
				t.Fatal("direct native request bypassed authority")
			}
			if calls.Load() != 1 {
				t.Fatal("request reached provider after authority loss")
			}
		})
	}
}

func TestDevinStatusDoesNotRotateGrantOrClearHold(t *testing.T) {
	ctx := context.Background()
	f, _, s, _ := setupStores(t)
	a := &coreauth.Auth{ID: "devin.json", Provider: "devin", Status: coreauth.StatusActive, Metadata: map[string]any{"session_token": "fixture-secret-session", "api_key": "fixture-secret-session", "group": "keep"}}
	if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), a); err != nil {
		t.Fatal(err)
	}
	user := protowire.AppendString(protowire.AppendTag(nil, 7, protowire.BytesType), "fixture@example.invalid")
	body := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), user)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write(body) }))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", localProviderTransport{target})
	m := coreauth.NewManager(s, nil, nil)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	m.RegisterExecutor(native.NewDevinExecutor(&config.Config{}))
	updated, err := m.ForceRefreshAuth(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.HubGeneration != 1 || updated.Metadata["email"] != "fixture@example.invalid" || updated.Metadata["group"] != "keep" || len(f.intents) != 0 || calls.Load() != 1 {
		t.Fatal("status changed grant state or lost observations")
	}
	if _, err := s.coordinator.Control(ctx, a.HubAccountID, 1, "hold"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ForceRefreshAuth(ctx, a.ID); err == nil {
		t.Fatal("held status call allowed")
	}
	if calls.Load() != 1 {
		t.Fatal("held grant dispatched")
	}
}

func TestPreflightRejectsUnsupportedSourcesAndReportsOnlyModes(t *testing.T) {
	cfg := staticConfig(t)
	dir := t.TempDir()
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "fixture.json"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"codex","refresh_token":"fixture-secret","websockets":true}`)
	rows, err := Preflight(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rows)
	if len(rows) != 6 || strings.Contains(string(raw), "fixture-secret") {
		t.Fatalf("bad preflight report: %s", raw)
	}
	for _, raw := range []string{`{"type":"codex","siwc":{"refresh_token":"fixture-secret"}}`, `{"type":"antigravity"}`, `{"type":"xai","websockets":true}`, `{"type":"unknown"}`, `bad fixture-secret`} {
		write(raw)
		if _, err := Preflight(cfg, dir); err == nil || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatal("unsupported or leaking preflight", err)
		}
	}
	write(`{"type":"claude","refresh_token":"fixture-secret"}`)
	cfg.Home.Enabled = true
	if _, err := Preflight(cfg, dir); err == nil {
		t.Fatal("Home accepted")
	}
	cfg.Home.Enabled = false
	cfg.Plugins.Enabled = true
	if _, err := Preflight(cfg, dir); err == nil {
		t.Fatal("plugins accepted")
	}
	cfg.Plugins.Enabled = false
	cfg.CodexKey = []config.CodexKey{{APIKey: "fixture-secret"}}
	if _, err := Preflight(cfg, dir); err == nil {
		t.Fatal("unsupported native config key accepted")
	}
}

func TestLegacyGrantAliasesCannotCreateAnotherLogin(t *testing.T) {
	for _, name := range []string{"meta", "devin"} {
		t.Run(name, func(t *testing.T) {
			_, _, s, _ := setupStores(t)
			ctx := context.Background()
			metadata := map[string]any{"access_token": "dca:fixture-secret-alias"}
			if name == "devin" {
				metadata = map[string]any{"api_key": "fixture-secret-session-alias"}
			}
			a := &coreauth.Auth{ID: name + ".json", Provider: name, Metadata: metadata}
			if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), a); err != nil {
				t.Fatal(err)
			}
			duplicate := a.Clone()
			duplicate.ID = "renamed.json"
			duplicate.HubAccountID = ""
			duplicate.HubGeneration = 0
			if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), duplicate); err == nil {
				t.Fatal("grant alias created a second login")
			}
			a.Metadata["note"] = "fixture update"
			if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), a); err != nil {
				t.Fatal(err)
			}
			if a.HubGeneration != 1 {
				t.Fatal("replayed login advanced generation")
			}
		})
	}
}
