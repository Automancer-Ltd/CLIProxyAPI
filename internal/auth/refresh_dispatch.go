package auth

import (
	"context"
	"net/http"
)

type refreshDispatchKey struct{}

// WithRefreshDispatch carries the durable intent's final, one-use admission
// check to the HTTP boundary. It survives request cancellation for late results.
func WithRefreshDispatch(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, refreshDispatchKey{}, check)
}
func CheckRefreshDispatch(ctx context.Context) error {
	if check, ok := ctx.Value(refreshDispatchKey{}).(func(context.Context) error); ok {
		return check(ctx)
	}
	return nil
}

type refreshDispatchTransport struct{ base http.RoundTripper }

func (t refreshDispatchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := CheckRefreshDispatch(req.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}
func GuardRefreshDispatch(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return refreshDispatchTransport{base}
}

// HasRefreshDispatch identifies a refresh already enrolled in shared authority.
func HasRefreshDispatch(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(refreshDispatchKey{}).(func(context.Context) error)
	return ok
}

// RefreshClient leaves ordinary login behavior intact. For an enrolled refresh,
// the final transport consumes one permission, with redirects disabled.
func RefreshClient(ctx context.Context, original *http.Client) *http.Client {
	if !HasRefreshDispatch(ctx) {
		return original
	}
	client := *original
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		client.Transport = rt
	}
	client.Transport = GuardRefreshDispatch(client.Transport)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
