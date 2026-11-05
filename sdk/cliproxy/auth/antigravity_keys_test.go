package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Not parallel: the pairing is process-wide, and parallel tests start only after
// the sequential ones have finished and restored it.
func TestAntigravityPairingRoutesPairedKeyOnlyToAntigravity(t *testing.T) {
	const (
		shared     = "pair-test-claude-shared" // served by Claude and, through an alias, Antigravity
		claudeOnly = "pair-test-claude-only"
		agyOnly    = "pair-test-gemini-agy"
		agyKey     = "agy-client-key"
	)
	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.RegisterExecutor(&authFallbackExecutor{id: "claude"})
	m.RegisterExecutor(&authFallbackExecutor{id: "antigravity"})
	claude := &Auth{ID: "pair-claude", Provider: "claude"}
	agy := &Auth{ID: "pair-agy", Provider: "antigravity"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(claude.ID, "claude", []*registry.ModelInfo{{ID: shared}, {ID: claudeOnly}})
	reg.RegisterClient(agy.ID, "antigravity", []*registry.ModelInfo{{ID: shared}, {ID: agyOnly}})
	for _, auth := range []*Auth{claude, agy} {
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	m.SetConfig(&internalconfig.Config{SDKConfig: internalconfig.SDKConfig{AntigravityKeys: []string{agyKey}}})
	t.Cleanup(func() { setAntigravityKeys(nil) })

	keyCtx := func(key string) context.Context { return coreusage.WithAPIKey(context.Background(), key) }
	providers := func(model string) []string {
		switch model {
		case shared:
			return []string{"claude", "antigravity"}
		case claudeOnly:
			return []string{"claude"}
		}
		return []string{"antigravity"}
	}
	execute := func(key, model string) (string, error) {
		resp, errExec := m.Execute(keyCtx(key), providers(model), cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		return string(resp.Payload), errExec
	}
	stream := func(key, model string) (string, error) {
		result, errStream := m.ExecuteStream(keyCtx(key), providers(model), cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if errStream != nil {
			return "", errStream
		}
		var got string
		for chunk := range result.Chunks {
			got += string(chunk.Payload)
		}
		return got, nil
	}
	refused := func(err error) bool {
		var authErr *Error
		return errors.As(err, &authErr) && authErr.StatusCode() == http.StatusForbidden && authErr.Code == "model_not_allowed"
	}

	for _, call := range []struct {
		name string
		run  func(key, model string) (string, error)
	}{{"Execute", execute}, {"ExecuteStream", stream}} {
		if got, err := call.run(agyKey, shared); err != nil || got != agy.ID {
			t.Fatalf("%s(paired key, %s) = %q, %v; want Antigravity, never the Claude account", call.name, shared, got, err)
		}
		if got, err := call.run(agyKey, claudeOnly); !refused(err) {
			t.Fatalf("%s(paired key, %s) = %q, %v; want a 403 refusal", call.name, claudeOnly, got, err)
		}
		if got, err := call.run("other-key", shared); err != nil || got != claude.ID {
			t.Fatalf("%s(other key, %s) = %q, %v; want the Claude account", call.name, shared, got, err)
		}
		for _, key := range []string{"other-key", ""} {
			if got, err := call.run(key, agyOnly); !refused(err) {
				t.Fatalf("%s(key %q, %s) = %q, %v; want a 403 refusal", call.name, key, agyOnly, got, err)
			}
		}
	}

	// Direct selection paths (Codex live, alpha search) are gated the same way.
	if _, err := m.SelectAuth(keyCtx(agyKey), "claude", shared, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("SelectAuth(paired key, claude) succeeded; want no Claude credential")
	}
	if _, err := m.SelectAuth(keyCtx("other-key"), "antigravity", agyOnly, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("SelectAuth(other key, antigravity) succeeded; want no Antigravity credential")
	}

	// A reload without paired keys opens Antigravity to every key again.
	m.SetConfig(&internalconfig.Config{})
	if got, err := execute("other-key", agyOnly); err != nil || got != agy.ID {
		t.Fatalf("after reload Execute(other key, %s) = %q, %v; want Antigravity", agyOnly, got, err)
	}
}

func TestOneModelAtATimeRefusesSecondModelUntilFirstFinishes(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{
		{Name: "local", OneModelAtATime: true},
		{Name: "cloud"},
	}})
	local := []string{"openai-compatible-local"}

	releaseA, err := m.acquireOneModel(local, "local/a")
	if err != nil || releaseA == nil {
		t.Fatalf("first request: release=%v err=%v", releaseA != nil, err)
	}
	releaseA2, err := m.acquireOneModel(local, "local/a")
	if err != nil {
		t.Fatalf("same model while busy: %v; want admitted", err)
	}
	var authErr *Error
	if _, err := m.acquireOneModel(local, "local/b"); !errors.As(err, &authErr) || authErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("other model while busy: %v; want 503", err)
	}
	if release, err := m.acquireOneModel([]string{"openai-compatible-cloud"}, "cloud/x"); err != nil || release != nil {
		t.Fatalf("ungated group: release=%v err=%v; want no gate", release != nil, err)
	}
	releaseA()
	releaseA() // a second call is a no-op
	if _, err := m.acquireOneModel(local, "local/b"); err == nil {
		t.Fatal("other model with one request for a still in flight: want 503")
	}
	releaseA2()

	// A stream holds the model until its chunks are drained.
	releaseB, err := m.acquireOneModel(local, "local/b")
	if err != nil {
		t.Fatalf("other model after release: %v", err)
	}
	source := make(chan cliproxyexecutor.StreamChunk, 1)
	source <- cliproxyexecutor.StreamChunk{Payload: []byte("x")}
	wrapped := releaseWhenDrained(context.Background(), &cliproxyexecutor.StreamResult{Chunks: source}, releaseB)
	<-wrapped.Chunks
	if _, err := m.acquireOneModel(local, "local/a"); err == nil {
		t.Fatal("other model while a stream is open: want 503")
	}
	close(source)
	for range wrapped.Chunks {
	}
	release, err := m.acquireOneModel(local, "local/a")
	if err != nil {
		t.Fatalf("other model after the stream ended: %v", err)
	}
	release()
}
