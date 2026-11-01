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
