package executor

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type antigravityTransportHub struct {
	calls  atomic.Int32
	denied atomic.Bool
}

func (h *antigravityTransportHub) Authorize(context.Context, *cliproxyauth.Auth) error {
	h.calls.Add(1)
	if h.denied.Load() {
		return errors.New("fixture ownership lost")
	}
	return nil
}

func (*antigravityTransportHub) Refresh(context.Context, *cliproxyauth.Auth, func(context.Context, *cliproxyauth.Auth) (*cliproxyauth.Auth, error)) (*cliproxyauth.Auth, error) {
	return nil, errors.New("unexpected fixture refresh")
}

func TestAntigravityHubHttpRequestPreservesNativeTransport(t *testing.T) {
	for _, source := range []string{"default", "context"} {
		t.Run(source, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				w.Header().Set("X-Connection", r.RemoteAddr)
				w.Header().Set("X-Authorization", r.Header.Get("Authorization"))
				_, _ = io.WriteString(w, "fixture response")
			}))
			server.EnableHTTP2 = true
			server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				if len(hello.SupportedProtos) != 0 {
					t.Errorf("ClientHello ALPN = %v, want no ALPN extension", hello.SupportedProtos)
				}
				return nil, nil
			}}
			server.StartTLS()
			t.Cleanup(server.Close)
			base := server.Client().Transport.(*http.Transport).Clone()
			base.ForceAttemptHTTP2 = true
			t.Cleanup(base.CloseIdleConnections)
			t.Cleanup(ResetAntigravityTransports)
			ctx := context.Background()
			if source == "context" {
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", base)
			} else {
				previousDefault, previousBase := http.DefaultTransport, antigravityBaseTransport
				http.DefaultTransport, antigravityBaseTransport = base, base
				t.Cleanup(func() {
					http.DefaultTransport, antigravityBaseTransport = previousDefault, previousBase
				})
			}

			enabled := true
			executor := NewAntigravityExecutor(&config.Config{Antigravity: config.AntigravityConfig{
				ConnectionPool: config.AntigravityConnectionPoolConfig{Enabled: &enabled},
			}})
			hub := &antigravityTransportHub{}
			a := antigravityAuthWithIDAndProxy(t.Name()+"-a", "")
			a.HubAuthority = hub
			b := antigravityAuthWithIDAndProxy(t.Name()+"-b", "")
			b.HubAuthority = hub
			b.Metadata["access_token"] = "fixture-token-b"
			connections := make([]string, 0, 3)
			for i, auth := range []*cliproxyauth.Auth{a, a.Clone(), b} {
				req, err := http.NewRequest(http.MethodGet, server.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := executor.HttpRequest(ctx, auth, req)
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.Proto != "HTTP/1.1" || resp.TLS.NegotiatedProtocol != "" || resp.TLS.Version != tls.VersionTLS13 {
					t.Errorf("request %d: protocol=%s ALPN=%q TLS=%#x, want HTTP/1.1 without ALPN over TLS 1.3", i, resp.Proto, resp.TLS.NegotiatedProtocol, resp.TLS.Version)
				}
				if got := resp.Header.Get("X-Authorization"); got != "Bearer "+auth.Metadata["access_token"].(string) {
					t.Errorf("request %d used the wrong credential", i)
				}
				connections = append(connections, resp.Header.Get("X-Connection"))
				if got := hub.calls.Load(); got != int32(i+1) {
					t.Errorf("request %d: authorization calls=%d, want %d", i, got, i+1)
				}
			}
			if connections[0] != connections[1] {
				t.Error("same credential did not reuse its connection")
			}
			if connections[0] == connections[2] {
				t.Error("different credentials shared a connection")
			}

			// A cached pool must still check ownership before its next dispatch.
			hub.denied.Store(true)
			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp, err := executor.HttpRequest(ctx, a, req); err == nil {
				_ = resp.Body.Close()
				t.Error("revoked credential dispatched a request")
			}
			if got := upstreamCalls.Load(); got != 3 {
				t.Errorf("upstream calls=%d, want 3", got)
			}
			if got := hub.calls.Load(); got != 4 {
				t.Errorf("authorization calls after revocation=%d, want 4", got)
			}
		})
	}
}
