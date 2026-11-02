package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
)

type hubTransport struct {
	base http.RoundTripper
	auth *Auth
}

func (t hubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.auth.HubAuthority.Authorize(req.Context(), t.auth); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// GuardHubTransport rechecks ownership at actual HTTP dispatch, including a
// provider's internal retry or redirect after lengthy request preparation.
func GuardHubTransport(base http.RoundTripper, a *Auth) http.RoundTripper {
	if a == nil || a.HubAuthority == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return hubTransport{base, a}
}

// HubSessionAuthority binds a connection to its opening process epoch and grant.
// A later acquisition cannot grant an old socket a new authority.
type HubSessionAuthority interface {
	OpenHubSession(context.Context, *Auth) (func(context.Context) error, error)
}

// GuardHubDial checks before opening a socket and at every network read/write.
// Failure is permanent for this connection, including buffered duplex frames.
func GuardHubDial(ctx context.Context, a *Auth, dial func(context.Context, string, string) (net.Conn, error)) (func(context.Context, string, string) (net.Conn, error), error) {
	if a == nil || a.HubAuthority == nil {
		return dial, nil
	}
	authority, ok := a.HubAuthority.(HubSessionAuthority)
	if !ok {
		return nil, errors.New("hub transport cannot bind a session")
	}
	check, err := authority.OpenHubSession(ctx, a.Clone())
	if err != nil {
		return nil, err
	}
	return func(dialCtx context.Context, network, addr string) (net.Conn, error) {
		if err := check(dialCtx); err != nil {
			return nil, err
		}
		conn, err := dial(dialCtx, network, addr)
		if err != nil {
			return nil, err
		}
		guarded := &hubConn{Conn: conn, ctx: context.WithoutCancel(ctx), check: check}
		if err := guarded.authorize(); err != nil {
			return nil, err
		}
		return guarded, nil
	}, nil
}

type hubConn struct {
	net.Conn
	ctx    context.Context
	check  func(context.Context) error
	failed atomic.Bool
}

func (c *hubConn) authorize() error {
	if c.failed.Load() {
		return errors.New("hub session authority lost")
	}
	if err := c.check(c.ctx); err != nil {
		c.failed.Store(true)
		_ = c.Conn.Close()
		return err
	}
	if c.failed.Load() {
		return errors.New("hub session authority lost")
	}
	return nil
}
func (c *hubConn) Write(p []byte) (int, error) {
	if err := c.authorize(); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}
func (c *hubConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		if guardErr := c.authorize(); guardErr != nil {
			return 0, guardErr
		}
	}
	return n, err
}

// CheckHubConn rechecks a decoded or queued frame, even if TLS/websocket buffers
// satisfied the read without touching the guarded socket. TLS exposes NetConn.
func CheckHubConn(conn net.Conn) error {
	for conn != nil {
		if guarded, ok := conn.(*hubConn); ok {
			return guarded.authorize()
		}
		nested, ok := conn.(interface{ NetConn() net.Conn })
		if !ok {
			return nil
		}
		conn = nested.NetConn()
	}
	return nil
}
