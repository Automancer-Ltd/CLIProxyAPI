package hubauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	native "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type localProviderTransport struct{ target *url.URL }

func (rt localProviderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copyReq := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = rt.target.Scheme
	u.Host = rt.target.Host
	copyReq.URL = &u
	return http.DefaultTransport.RoundTrip(copyReq)
}
func nativeProvider(name string) coreauth.ProviderExecutor {
	cfg := &config.Config{}
	switch name {
	case "antigravity":
		return native.NewAntigravityExecutor(cfg)
	case "xai":
		return native.NewXAIExecutor(cfg)
	default:
		return native.NewMetaExecutor(cfg)
	}
}
func nativeLogin(t *testing.T, s *Store, name string) *coreauth.Auth {
	t.Helper()
	a := &coreauth.Auth{ID: name + ".json", Provider: name, Metadata: map[string]any{"access_token": "fixture-secret-access", "refresh_token": "fixture-secret-refresh", "expired": "2000-01-01T00:00:00Z", "project_id": "fixture-project", "token_endpoint": "https://fixture.invalid/token"}, Status: coreauth.StatusActive}
	if name == "meta" {
		delete(a.Metadata, "refresh_token")
		a.Metadata["dca_token"] = "dca:fixture-secret"
		a.Metadata["api_key"] = "fixture-secret-key"
	}
	if _, err := s.Save(coreauth.WithAuthCreationIntent(context.Background()), a); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestNativeProviderRefreshTakeoverPreservesResult(t *testing.T) {
	for _, name := range []string{"antigravity", "xai", "meta"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, b, primary, standby := setupStores(t)
			a := nativeLogin(t, primary, name)
			var calls atomic.Int32
			entered, finish := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(finish) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				close(entered)
				<-finish
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-secret-new-access", "refresh_token": "fixture-secret-new-refresh", "expires_in": 3600, "api_key": "fixture-secret-new-key", "subs_tier_name": "fixture", "subs_usage": map[string]any{"window": map[string]any{"used_percent": 12}}})
			}))
			defer server.Close()
			defer unblock()
			target, _ := url.Parse(server.URL)
			ctx = context.WithValue(ctx, "cliproxy.roundtripper", localProviderTransport{target})
			m := coreauth.NewManager(primary, nil, nil)
			if err := m.Load(ctx); err != nil {
				t.Fatal(err)
			}
			m.RegisterExecutor(nativeProvider(name))
			done := make(chan error, 1)
			go func() { _, err := m.ForceRefreshAuth(ctx, a.ID); done <- err }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatal("no provider dispatch", err)
			}
			controls := a.Clone()
			controls.Metadata["group"] = "fixture-group"
			controls.Metadata["pin_only"] = true
			controls.Metadata["hold_until"] = "2000-01-01T00:00:00Z"
			if _, err := primary.Save(ctx, controls); err != nil {
				t.Fatal(err)
			}
			if _, err := primary.coordinator.Control(ctx, a.HubAccountID, 1, "hold"); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.state.Ownership.Live = false
			f.mu.Unlock()
			if err := standby.coordinator.Step(ctx); err != nil {
				t.Fatal(err)
			}
			second := coreauth.NewManager(standby, nil, nil)
			if err := second.Load(ctx); err != nil {
				t.Fatal(err)
			}
			second.RegisterExecutor(nativeProvider(name))
			if _, err := second.ForceRefreshAuth(ctx, a.ID); err == nil {
				t.Fatal("takeover reused unresolved grant")
			}
			unblock()
			if err := <-done; err == nil {
				t.Fatal("old process committed after takeover")
			}
			if err := standby.coordinator.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			records, accounts, err := standby.coordinator.Current(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeCredential(records[a.HubAccountID].Data)
			if err != nil {
				t.Fatal(err)
			}
			if got.Metadata["access_token"] != "fixture-secret-new-access" && got.Metadata["api_key"] != "fixture-secret-new-key" {
				t.Fatal("new provider bytes lost")
			}
			if accounts[a.HubAccountID].Status != "held" || got.Metadata["group"] != "fixture-group" || got.Metadata["pin_only"] != true || got.Metadata["hold_until"] != "2000-01-01T00:00:00Z" {
				t.Fatal("rotation lost concurrent controls")
			}
			if accounts[a.HubAccountID].Generation != 2 || accounts[a.HubAccountID].Quarantined || calls.Load() != 1 {
				t.Fatalf("wrong durable state: %+v, calls %d", accounts[a.HubAccountID], calls.Load())
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			for _, object := range b.data {
				if strings.Contains(string(object), "fixture-secret") {
					t.Fatal("plaintext provider result persisted")
				}
			}
		})
	}
}
func TestNativeProviderRedirectCannotResubmitGrant(t *testing.T) {
	for _, name := range []string{"antigravity", "xai", "meta"} {
		t.Run(name, func(t *testing.T) {
			f, _, primary, _ := setupStores(t)
			a := nativeLogin(t, primary, name)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/again")
				w.WriteHeader(307)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", localProviderTransport{target})
			if _, err := nativeProvider(name).Refresh(ctx, a); err == nil {
				t.Fatal("redirect considered success")
			}
			if calls.Load() != 1 {
				t.Fatalf("grant sent %d times", calls.Load())
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.intents) != 1 || !f.accounts[a.HubAccountID].Quarantined {
				t.Fatal("ambiguous rotation was not quarantined")
			}
		})
	}
}

func TestNativePartialRotationCannotAdvanceGeneration(t *testing.T) {
	for _, name := range []string{"antigravity", "xai"} {
		t.Run(name, func(t *testing.T) {
			f, _, s, _ := setupStores(t)
			a := nativeLogin(t, s, name)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"refresh_token":"fixture-secret-partial-new"}`))
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", localProviderTransport{target})
			m := coreauth.NewManager(s, nil, nil)
			if err := m.Load(ctx); err != nil {
				t.Fatal(err)
			}
			m.RegisterExecutor(nativeProvider(name))
			if _, err := m.ForceRefreshAuth(ctx, a.ID); err == nil {
				t.Fatal("partial response advanced generation")
			}
			if err := s.coordinator.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.accounts[a.HubAccountID].Quarantined || f.accounts[a.HubAccountID].Generation != 1 {
				t.Fatal("partial result lost quarantine")
			}
			for _, i := range f.intents {
				raw, _, err := s.coordinator.objects.ReadResult(ctx, i.ResultKey)
				if err != nil {
					t.Fatal(err)
				}
				var retained result
				if json.Unmarshal(raw, &retained) != nil || retained.Complete || !strings.Contains(string(retained.Data), "fixture-secret-partial-new") {
					t.Fatal("successful partial grant bytes lost")
				}
			}
		})
	}
}
