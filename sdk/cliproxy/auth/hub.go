package auth

import (
	"context"
	"errors"
	"reflect"

	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// HubAuthority owns shared grants. It is deliberately separate from local
// scheduler generations and from the Dispatcher watcher lease.
type HubAuthority interface {
	Authorize(context.Context, *Auth) error
	Refresh(context.Context, *Auth, func(context.Context, *Auth) (*Auth, error)) (*Auth, error)
}

type hubRefreshKey struct{}
type hubRefreshPermission struct {
	id         string
	generation int64
}

// RunHubRefresh routes direct executor calls through the same authority as the
// Manager. The private context marker permits only its one authorized callback.
func RunHubRefresh(ctx context.Context, a *Auth, refresh func(context.Context, *Auth) (*Auth, error)) (*Auth, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil || a.HubAuthority == nil {
		return nil, false, nil
	}
	if p, ok := ctx.Value(hubRefreshKey{}).(hubRefreshPermission); ok && p.id == a.HubAccountID && p.generation == a.HubGeneration {
		return nil, false, nil
	}
	out, err := a.HubAuthority.Refresh(ctx, a, func(inner context.Context, b *Auth) (*Auth, error) {
		return refresh(context.WithValue(inner, hubRefreshKey{}, hubRefreshPermission{b.HubAccountID, b.HubGeneration}), b)
	})
	return out, true, err
}

func (m *Manager) hubAuthority() HubAuthority {
	m.mu.RLock()
	defer m.mu.RUnlock()
	authority, _ := m.store.(HubAuthority)
	return authority
}

// AuthorizeHub checks the current encrypted generation at every public provider
// boundary, including management usage and raw HTTP helpers.
func (m *Manager) AuthorizeHub(ctx context.Context, a *Auth) error {
	if m == nil {
		return errors.New("hub manager unavailable")
	}
	if authority := m.hubAuthority(); authority != nil {
		return authority.Authorize(ctx, a)
	}
	return nil
}

func (m *Manager) executeAuthorized(ctx context.Context, e ProviderExecutor, a *Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	if err := m.AuthorizeHub(ctx, a); err != nil {
		return executor.Response{}, err
	}
	release, err := m.admitAttempt(ctx, a, r.Model, true)
	if err != nil {
		return executor.Response{}, err
	}
	if release != nil {
		defer release()
	}
	return e.Execute(ctx, a, r, o)
}
func (m *Manager) countAuthorized(ctx context.Context, e ProviderExecutor, a *Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	if err := m.AuthorizeHub(ctx, a); err != nil {
		return executor.Response{}, err
	}
	// Counting on a gated (local) group uses a local tokenizer, so only the pairing applies.
	if _, err := m.admitAttempt(ctx, a, r.Model, false); err != nil {
		return executor.Response{}, err
	}
	return e.CountTokens(ctx, a, r, o)
}
func (m *Manager) streamAuthorized(ctx context.Context, e ProviderExecutor, a *Auth, r executor.Request, o executor.Options) (*executor.StreamResult, error) {
	if err := m.AuthorizeHub(ctx, a); err != nil {
		return nil, err
	}
	release, err := m.admitAttempt(ctx, a, r.Model, true)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return e.ExecuteStream(ctx, a, r, o)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release() // also on a panic in stream setup
		}
	}()
	result, err := e.ExecuteStream(ctx, a, r, o)
	if err != nil || result == nil || result.Chunks == nil {
		return result, err
	}
	handedOff = true
	return releaseWhenDrained(ctx, result, release), nil
}

// releaseWhenDrained calls release once the stream has ended or the request is gone.
func releaseWhenDrained(ctx context.Context, result *executor.StreamResult, release func()) *executor.StreamResult {
	if ctx == nil {
		ctx = context.Background()
	}
	source := result.Chunks
	chunks := make(chan executor.StreamChunk)
	go func() {
		defer close(chunks)
		defer release() // runs first: the gate is free by the time the reader sees the end
		for chunk := range source {
			select {
			case chunks <- chunk:
			case <-ctx.Done():
				for range source {
				}
				return
			}
		}
	}()
	wrapped := *result
	wrapped.Chunks = chunks
	return &wrapped
}

func sameHubContent(a, b *Auth) bool {
	return a.HubGeneration == b.HubGeneration && string(a.HubBaseline) == string(b.HubBaseline) && a.Disabled == b.Disabled && a.Label == b.Label && a.Prefix == b.Prefix && a.ProxyURL == b.ProxyURL && reflect.DeepEqual(a.Metadata, b.Metadata) && reflect.DeepEqual(a.Attributes, b.Attributes)
}

// executeAdmitted runs one upstream attempt behind admitAttempt, for Home paths that do
// not pass executeAuthorized.
func (m *Manager) executeAdmitted(ctx context.Context, e ProviderExecutor, a *Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	release, err := m.admitAttempt(ctx, a, r.Model, true)
	if err != nil {
		return executor.Response{}, err
	}
	if release != nil {
		defer release()
	}
	return e.Execute(ctx, a, r, o)
}
