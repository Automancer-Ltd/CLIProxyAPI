package auth

import "net/http"

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
