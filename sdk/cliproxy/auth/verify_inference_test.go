package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// An operator-triggered inference check is a real provider request, so a held account
// or one whose included allowance is used up must refuse it before any upstream call.
func TestVerifyInferenceRefusesHeldAndSpentAccounts(t *testing.T) {
	now := time.Now()
	held := &Auth{ID: "verify-held", Provider: "codex", Metadata: map[string]any{
		MetadataHoldUntil: now.Add(time.Hour).UTC().Format(time.RFC3339),
	}}
	spent := &Auth{ID: "verify-spent", Provider: "codex", Metadata: map[string]any{},
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "100",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
			"X-Codex-Credits-Has-Credits":    "True",
		}}}

	executor := &authFallbackExecutor{id: "codex"}
	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.RegisterExecutor(executor)
	for _, auth := range []*Auth{held, spent} {
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	for _, id := range []string{held.ID, spent.ID} {
		if _, errVerify := m.VerifyInference(context.Background(), id, "gpt-5"); errVerify == nil {
			t.Fatalf("VerifyInference(%s) succeeded; want a refusal", id)
		}
	}
	executor.mu.Lock()
	calls := len(executor.executeCalls) + len(executor.streamCalls)
	executor.mu.Unlock()
	if calls != 0 {
		t.Fatalf("VerifyInference sent %d provider requests for held or spent accounts; want 0", calls)
	}
}

// VerifyInference must find the executor the way request execution does: a kimi-ai
// credential is served by the executor registered as "kimi".
func TestVerifyInferenceResolvesExecutorLikeExecution(t *testing.T) {
	executor := &authFallbackExecutor{id: "kimi"}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	auth := &Auth{ID: "verify-kimi-ai", Provider: "kimi-ai"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register %s: %v", auth.ID, errRegister)
	}

	_, errVerify := m.VerifyInference(context.Background(), auth.ID, "kimi-test-model")
	var authErr *Error
	if errors.As(errVerify, &authErr) && authErr.Code == "executor_not_found" {
		t.Fatalf("VerifyInference(kimi-ai) = %v; want it to reach the kimi executor", errVerify)
	}
	executor.mu.Lock()
	calls := len(executor.executeCalls) + len(executor.streamCalls)
	executor.mu.Unlock()
	if calls == 0 {
		t.Fatal("VerifyInference(kimi-ai) sent no request to the kimi executor")
	}
}
