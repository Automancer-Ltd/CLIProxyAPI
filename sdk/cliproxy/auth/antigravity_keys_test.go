package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func statusOf(err error) int {
	var withStatus interface{ StatusCode() int }
	if errors.As(err, &withStatus) {
		return withStatus.StatusCode()
	}
	return 0
}

func TestAntigravityPairingFollowsTheRoleCarriedByTheRequest(t *testing.T) {
	const (
		shared     = "pair-test-claude-shared" // served by Claude and, through an alias, Antigravity
		claudeOnly = "pair-test-claude-only"
		agyOnly    = "pair-test-gemini-agy"
	)
	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	claudeExec := &authFallbackExecutor{id: "claude"}
	m.RegisterExecutor(claudeExec)
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
	// The Manager's own snapshot pairs nobody: the role the request carries from its
	// authentication must decide (a rotated config cannot widen a request's access).
	m.SetConfig(&internalconfig.Config{})

	roleCtx := func(role ProviderAccess) context.Context {
		return WithProviderAccess(context.Background(), string(role))
	}
	providers := func(model string) []string {
		switch model {
		case shared:
			return []string{"claude", "antigravity"}
		case claudeOnly:
			return []string{"claude"}
		}
		return []string{"antigravity"}
	}
	execute := func(ctx context.Context, model string) (string, error) {
		resp, errExec := m.Execute(ctx, providers(model), cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		return string(resp.Payload), errExec
	}
	stream := func(ctx context.Context, model string) (string, error) {
		result, errStream := m.ExecuteStream(ctx, providers(model), cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if errStream != nil {
			return "", errStream
		}
		var got string
		for chunk := range result.Chunks {
			got += string(chunk.Payload)
		}
		return got, nil
	}
	for _, call := range []struct {
		name string
		run  func(context.Context, string) (string, error)
	}{{"Execute", execute}, {"ExecuteStream", stream}} {
		if got, err := call.run(roleCtx(ProviderAccessAntigravityOnly), shared); err != nil || got != agy.ID {
			t.Fatalf("%s(paired, %s) = %q, %v; want Antigravity, never the Claude account", call.name, shared, got, err)
		}
		if got, err := call.run(roleCtx(ProviderAccessAntigravityOnly), claudeOnly); statusOf(err) != http.StatusForbidden {
			t.Fatalf("%s(paired, %s) = %q, %v; want 403", call.name, claudeOnly, got, err)
		}
		if got, err := call.run(roleCtx(ProviderAccessNoAntigravity), shared); err != nil || got != claude.ID {
			t.Fatalf("%s(ordinary, %s) = %q, %v; want the Claude account", call.name, shared, got, err)
		}
		if got, err := call.run(roleCtx(ProviderAccessNoAntigravity), agyOnly); statusOf(err) != http.StatusForbidden {
			t.Fatalf("%s(ordinary, %s) = %q, %v; want 403", call.name, agyOnly, got, err)
		}
	}

	// The attempt boundary refuses on its own, whatever path selected the credential
	// (Home dispatch, retained sessions, retries), without cooling the credential down.
	_, err := m.executeAuthorized(roleCtx(ProviderAccessAntigravityOnly), claudeExec, claude, cliproxyexecutor.Request{Model: shared}, cliproxyexecutor.Options{})
	if statusOf(err) != http.StatusForbidden || !isRequestScopedError(err) {
		t.Fatalf("executeAuthorized(paired, claude) = %v; want a request-scoped 403", err)
	}
	if _, err := m.streamAuthorized(roleCtx(ProviderAccessAntigravityOnly), claudeExec, claude, cliproxyexecutor.Request{Model: shared}, cliproxyexecutor.Options{}); statusOf(err) != http.StatusForbidden {
		t.Fatalf("streamAuthorized(paired, claude) = %v; want 403", err)
	}
	if _, err := m.SelectAuth(roleCtx(ProviderAccessAntigravityOnly), "claude", shared, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("SelectAuth(paired, claude) succeeded; want no Claude credential")
	}

	// A request with no role (internal caller, management, VerifyInference) fails closed
	// for Antigravity while the Manager's config pairs a key, and is open otherwise.
	if got, err := execute(context.Background(), agyOnly); err != nil || got != agy.ID {
		t.Fatalf("no role, no pairing: %q, %v; want Antigravity", got, err)
	}
	m.SetConfig(&internalconfig.Config{SDKConfig: internalconfig.SDKConfig{AntigravityKeys: []string{"agy-key"}}})
	if got, err := execute(context.Background(), agyOnly); statusOf(err) != http.StatusForbidden {
		t.Fatalf("no role under pairing: %q, %v; want 403", got, err)
	}
	if got, err := execute(context.Background(), shared); err != nil || got != claude.ID {
		t.Fatalf("no role under pairing, shared model: %q, %v; want Claude", got, err)
	}
}

// blockingExecutor answers each request after release is closed, so a test can hold
// an attempt in flight.
type blockingExecutor struct {
	authFallbackExecutor
	started chan string
	release chan struct{}
	panics  bool
}

func (e *blockingExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.started <- req.Model
	<-e.release
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *blockingExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.panics {
		panic("stream setup failed")
	}
	chunks := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		<-e.release
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(req.Model)}
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestOneModelAtATimeGatesTheSelectedGroupPerUpstreamModel(t *testing.T) {
	const group = "openai-compatible-gate-local"
	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{
		{Name: "gate-local", OneModelAtATime: true},
	}})
	exec := &blockingExecutor{authFallbackExecutor: authFallbackExecutor{id: group}, started: make(chan string, 4), release: make(chan struct{})}
	m.RegisterExecutor(exec)
	local := &Auth{ID: "gate-local-key", Provider: group, Attributes: map[string]string{"provider_key": group}}
	reg := registry.GetGlobalRegistry()
	// Two aliases for one upstream model, and a second upstream model.
	reg.RegisterClient(local.ID, group, []*registry.ModelInfo{{ID: "gate/a"}, {ID: "gate/a-alias"}, {ID: "gate/b"}})
	t.Cleanup(func() { reg.UnregisterClient(local.ID) })
	if _, errRegister := m.Register(context.Background(), local); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	execute := func(model string) (string, error) {
		resp, err := m.Execute(context.Background(), []string{group}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		return string(resp.Payload), err
	}

	done := make(chan error, 2)
	go func() { _, err := execute("gate/a"); done <- err }()
	<-exec.started
	go func() { _, err := execute("gate/a"); done <- err }()
	<-exec.started // the same model is admitted while the first is in flight
	if got, err := execute("gate/b"); statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("other model while busy = %q, %v; want 503", got, err)
	}
	close(exec.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("in-flight request: %v", err)
		}
	}
	// The refusal did not cool the credential down.
	if got, err := execute("gate/b"); err != nil || got != "gate/b" {
		t.Fatalf("other model after the first finished = %q, %v; want served", got, err)
	}

	// A stream holds the gate until it is drained.
	exec.release = make(chan struct{})
	result, err := m.streamAuthorized(context.Background(), exec, local, cliproxyexecutor.Request{Model: "gate/a"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := m.acquireOneModel(local, "gate/b"); err == nil {
		t.Fatal("other model while a stream is open: want 503")
	}
	close(exec.release)
	for range result.Chunks {
	}
	release, err := m.acquireOneModel(local, "gate/b")
	if err != nil {
		t.Fatalf("other model after the stream ended: %v", err)
	}
	release()

	// A panic in stream setup releases the gate.
	exec.panics = true
	func() {
		defer func() { _ = recover() }()
		_, _ = m.streamAuthorized(context.Background(), exec, local, cliproxyexecutor.Request{Model: "gate/a"}, cliproxyexecutor.Options{})
	}()
	release, err = m.acquireOneModel(local, "gate/b")
	if err != nil {
		t.Fatalf("gate still held after a panic in stream setup: %v", err)
	}
	release()

	// Another group is gated separately, and an ungated group not at all.
	other := &Auth{ID: "gate-other", Provider: "openai-compatible-gate-other"}
	hold, _ := m.acquireOneModel(local, "gate/a")
	if release, err := m.acquireOneModel(other, "gate/b"); err != nil || release != nil {
		t.Fatalf("ungated group: release=%v err=%v; want no gate", release != nil, err)
	}
	hold()
}
