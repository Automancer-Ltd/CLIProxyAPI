package hubauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	native "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type refreshReceiptHook struct {
	coreauth.NoopHook
	done chan *coreauth.Auth
}

func (h *refreshReceiptHook) OnAuthUpdated(_ context.Context, a *coreauth.Auth) {
	if a.HubGeneration > 1 {
		select {
		case h.done <- a.Clone():
		default:
		}
	}
}

// The production usage loop mints Meta keys. Its first poll must commit that key
// under an intent before it can become the next serving credential.
func TestManagementMetaUsageCommitsMintedKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f, _, s, _ := setupStores(t)
	a := nativeLogin(t, s, "meta")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"api_key": "fixture-secret-minted", "subs_tier_name": "fixture", "subs_usage": map[string]any{}})
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", localProviderTransport{target})
	hook := &refreshReceiptHook{done: make(chan *coreauth.Auth, 1)}
	m := coreauth.NewManager(s, nil, hook)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	m.RegisterExecutor(native.NewMetaExecutor(&config.Config{}))
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.Routing.UsageRefresh.Interval = "10m"
	h := management.NewHandler(cfg, "", m)
	h.StartUsageRefresher(ctx)
	select {
	case updated := <-hook.done:
		if updated.Metadata["api_key"] != "fixture-secret-minted" || updated.HubGeneration != 2 {
			t.Fatal("usage discarded minted key")
		}
	case <-ctx.Done():
		t.Fatal("usage failed to publish minted credential")
	}
	cancel()
	records, accounts, err := s.coordinator.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeCredential(records[a.HubAccountID].Data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["api_key"] != "fixture-secret-minted" || accounts[a.HubAccountID].Quarantined || calls.Load() != 1 {
		t.Fatal("usage result not durable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.intents) != 1 {
		t.Fatal("usage bypassed refresh intent")
	}
}

func TestManagementRejectsUnenrolledLoginBeforeEffects(t *testing.T) {
	_, _, s, _ := setupStores(t)
	m := coreauth.NewManager(s, nil, nil)
	old := sdkauth.GetTokenStore()
	sdkauth.RegisterTokenStore(s)
	defer sdkauth.RegisterTokenStore(old)
	h := management.NewHandler(&config.Config{AuthDir: t.TempDir()}, "", m)
	r := gin.New()
	r.POST("/oauth/auth-url", h.StartOAuthV8)
	r.POST("/oauth/import", h.ImportOAuthV8)
	for _, body := range []string{`{"provider":"kimi"}`, `{"provider":"gemini-cli"}`, `{"provider":"plugin-fixture"}`} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/oauth/auth-url", strings.NewReader(body)))
		if w.Code != 409 {
			t.Fatalf("unenrolled login status %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/oauth/import?provider=vertex", strings.NewReader(`{}`)))
	if w.Code != 409 {
		t.Fatalf("unenrolled import status %d", w.Code)
	}
}
