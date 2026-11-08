package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// oneModelGate lets an OpenAI-compatible group marked one-model-at-a-time serve one
// upstream model at a time: its GPU host swaps models in and out of memory, so a request
// for a second model would evict the first mid-answer. Requests for the model in
// flight pass, whatever alias they used.
type oneModelGate struct {
	mu     sync.Mutex
	active map[string]oneModelUse // group provider key -> upstream model in flight
}

type oneModelUse struct {
	model string
	count int
}

// oneModelGroup returns the provider key of the credential's group when that group is
// one-model-at-a-time, or "".
func (m *Manager) oneModelGroup(auth *Auth) string {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || auth == nil {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(auth.Provider))
	if auth.Attributes != nil && strings.TrimSpace(auth.Attributes["provider_key"]) != "" {
		key = strings.ToLower(strings.TrimSpace(auth.Attributes["provider_key"]))
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.OneModelAtATime && util.OpenAICompatibleProviderKey(compat.Name) == key {
			return key
		}
	}
	return ""
}

// acquireOneModel admits an attempt on the credential's group for the resolved upstream
// model, or refuses it (503, request-scoped so the credential does not cool down) while
// the group serves a different model. A non-nil release must be called once the attempt
// has finished; it is nil when the group is not gated.
func (m *Manager) acquireOneModel(auth *Auth, upstreamModel string) (func(), error) {
	group := m.oneModelGroup(auth)
	if group == "" {
		return nil, nil
	}
	model := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(upstreamModel).ModelName))
	gate := &m.oneModel
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if use := gate.active[group]; use.count > 0 && use.model != model {
		return nil, NewRequestScopedError(fmt.Sprintf("%s serves one model at a time and is busy with %q; retry %q when that request finishes",
			strings.TrimPrefix(group, "openai-compatible-"), use.model, model), http.StatusServiceUnavailable)
	}
	if gate.active == nil {
		gate.active = make(map[string]oneModelUse)
	}
	gate.active[group] = oneModelUse{model: model, count: gate.active[group].count + 1}
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			defer gate.mu.Unlock()
			if use := gate.active[group]; use.count > 1 {
				use.count--
				gate.active[group] = use
			} else {
				delete(gate.active, group)
			}
		})
	}, nil
}

// admitAttempt is the one boundary every upstream attempt passes: the Antigravity
// pairing, then (when gate is set) the one-model gate for the selected group and the
// resolved upstream model.
func (m *Manager) admitAttempt(ctx context.Context, auth *Auth, upstreamModel string, gate bool) (func(), error) {
	if auth == nil {
		return nil, nil
	}
	if err := m.AdmitProvider(ctx, auth.Provider); err != nil {
		return nil, err
	}
	if !gate {
		return nil, nil
	}
	return m.acquireOneModel(auth, upstreamModel)
}
