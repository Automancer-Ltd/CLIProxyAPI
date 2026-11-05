package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// oneModelGate lets an OpenAI-compatible group marked one-model-at-a-time serve one
// model at a time: its GPU host swaps models in and out of memory, so a request for a
// second model would evict the first mid-answer. Requests for the model in flight pass.
type oneModelGate struct {
	mu     sync.Mutex
	active map[string]oneModelUse // provider key -> model in flight
}

type oneModelUse struct {
	model string
	count int
}

// oneModelProviders returns the providers in the list whose group is one-model-at-a-time.
func (m *Manager) oneModelProviders(providers []string) []string {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		return nil
	}
	var gated []string
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if !compat.OneModelAtATime || compat.Disabled {
			continue
		}
		key := util.OpenAICompatibleProviderKey(compat.Name)
		for _, provider := range providers {
			if strings.EqualFold(strings.TrimSpace(provider), key) {
				gated = append(gated, key)
				break
			}
		}
	}
	return gated
}

// acquireOneModel admits a request for model, or refuses it with 503 while a gated
// provider it could use is serving a different model. A non-nil release must be called
// once the request has finished; it is nil when no gated provider is involved.
func (m *Manager) acquireOneModel(providers []string, model string) (func(), error) {
	gated := m.oneModelProviders(providers)
	if len(gated) == 0 {
		return nil, nil
	}
	model = thinking.ParseSuffix(model).ModelName // an effort suffix is the same model
	gate := &m.oneModel
	gate.mu.Lock()
	defer gate.mu.Unlock()
	for _, provider := range gated {
		if use := gate.active[provider]; use.count > 0 && !strings.EqualFold(use.model, model) {
			return nil, &Error{
				Code:       "local_model_busy",
				HTTPStatus: http.StatusServiceUnavailable,
				Message: fmt.Sprintf("%s serves one model at a time and is busy with %q; retry %q when that request finishes",
					strings.TrimPrefix(provider, "openai-compatible-"), use.model, model),
			}
		}
	}
	if gate.active == nil {
		gate.active = make(map[string]oneModelUse)
	}
	for _, provider := range gated {
		gate.active[provider] = oneModelUse{model: model, count: gate.active[provider].count + 1}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			defer gate.mu.Unlock()
			for _, provider := range gated {
				if use := gate.active[provider]; use.count > 1 {
					use.count--
					gate.active[provider] = use
				} else {
					delete(gate.active, provider)
				}
			}
		})
	}, nil
}

// releaseWhenDrained calls release once the stream has ended or the request is gone.
func releaseWhenDrained(ctx context.Context, result *cliproxyexecutor.StreamResult, release func()) *cliproxyexecutor.StreamResult {
	if ctx == nil {
		ctx = context.Background()
	}
	source := result.Chunks
	chunks := make(chan cliproxyexecutor.StreamChunk)
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
