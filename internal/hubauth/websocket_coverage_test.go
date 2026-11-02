package hubauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	native "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Real Manager -> CodexAuto -> duplex socket. A live turn first accepts steering;
// after ownership, control or generation loss its next control must never arrive.
func TestCodexDuplexFencesSubsequentFrames(t *testing.T) {
	for _, loss := range []string{"hold", "generation", "takeover"} {
		t.Run(loss, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f, _, s, standby := setupStores(t)
			var frames atomic.Int32
			serverDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
				for i := 0; ; i++ {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					frames.Add(1)
					payload := `{"type":"response.created","response":{"id":"fixture-r1","output":[]}}`
					if i > 0 {
						payload = `{"type":"response.steer.accepted","steer":{"id":"fixture-s1","previous_response_id":"fixture-r1"}}`
					}
					if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			a := &coreauth.Auth{ID: "codex-" + loss, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": server.URL, "websockets": "true"}, Metadata: map[string]any{"access_token": "fixture-secret-access", "refresh_token": "fixture-secret-refresh", "expired": "2099-01-01T00:00:00Z"}}
			if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), a); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Codex: config.CodexConfig{ResponseSteering: true}}
			e := native.NewCodexAutoExecutor(cfg)
			defer e.CloseAuthExecutionSessions(a.ID)
			m := coreauth.NewManager(s, nil, nil)
			m.SetConfig(cfg)
			if err := m.Load(ctx); err != nil {
				t.Fatal(err)
			}
			m.RegisterExecutor(e)
			registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "gpt-6-astra", Object: "model", OwnedBy: "openai"}})
			defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
			input := make(chan core.WebsocketInput, 4)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			req := core.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[],"instructions":"fixture"}`)}
			opts := core.Options{SourceFormat: translator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: "hub-fixture-" + loss}}
			stream, err := m.ExecuteStream(ctx, []string{"codex"}, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			receive := func(want string) {
				t.Helper()
				for {
					select {
					case c, ok := <-stream.Chunks:
						if !ok || c.Err != nil {
							t.Fatal("stream failed", c.Err)
						}
						if gjson.GetBytes(c.Payload, "type").String() == want {
							return
						}
					case <-ctx.Done():
						t.Fatal("fixture timed out")
					}
				}
			}
			receive("response.created")
			steer := core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"fixture-r1","input":"fixture"}`)}
			input <- steer
			receive("response.steer.accepted")
			switch loss {
			case "hold":
				if _, err := s.coordinator.Control(ctx, a.HubAccountID, a.HubGeneration, "hold"); err != nil {
					t.Fatal(err)
				}
			case "generation":
				next := a.Clone()
				next.Metadata["refresh_token"] = "fixture-secret-new"
				next.Metadata["access_token"] = "fixture-secret-new-access"
				if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), next); err != nil {
					t.Fatal(err)
				}
			case "takeover":
				f.mu.Lock()
				f.state.Ownership.Live = false
				f.mu.Unlock()
				if err := standby.coordinator.Step(ctx); err != nil {
					t.Fatal(err)
				}
			}
			input <- steer
			select {
			case <-serverDone:
			case <-ctx.Done():
				t.Fatal("revoked duplex socket was not closed")
			}
			if frames.Load() != 2 {
				t.Fatalf("revoked frame reached provider: %d", frames.Load())
			}
		})
	}
}

func TestOpenSessionCannotAdoptReacquiredEpoch(t *testing.T) {
	ctx := context.Background()
	f, _, s, standby := setupStores(t)
	a := loginFixture(t, s, "codex", "session.json")
	check, err := s.OpenHubSession(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.state.Ownership.Live = false
	f.mu.Unlock()
	if err = standby.coordinator.Step(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.state.Ownership.Live = false
	f.mu.Unlock()
	if err = s.coordinator.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Authorize(ctx, a); err != nil {
		t.Fatal("same grant should work in new session", err)
	}
	if err = check(ctx); err == nil {
		t.Fatal("old session inherited new epoch")
	}
}

func TestCodexDownstreamWebsocketKeepsHTTPWhenUpstreamFlagAbsent(t *testing.T) {
	ctx := context.Background()
	_, _, s, _ := setupStores(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if websocket.IsWebSocketUpgrade(r) {
			t.Error("invented upstream websocket mode")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()
	a := &coreauth.Auth{ID: "codex-http", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "fixture-secret-access", "refresh_token": "fixture-secret-refresh"}}
	if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), a); err != nil {
		t.Fatal(err)
	}
	e := native.NewCodexAutoExecutor(&config.Config{})
	ctx = core.WithDownstreamWebsocket(ctx)
	stream, err := e.ExecuteStream(ctx, a, core.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)}, core.Options{SourceFormat: translator.FromString("codex")})
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("HTTP route did not serve")
	}
	_ = s.coordinator.Stop(ctx)
	if _, err = e.ExecuteStream(ctx, a, core.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)}, core.Options{SourceFormat: translator.FromString("codex")}); err == nil {
		t.Fatal("HTTP route bypassed authority")
	}
	if calls.Load() != 1 {
		t.Fatal("revoked HTTP request dispatched")
	}
}
