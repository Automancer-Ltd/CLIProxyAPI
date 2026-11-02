package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// A result that is stale for credential health because the token rotated mid-request
// still belongs to the same provider account: its below-floor balance must reach the
// ledger, or later requests spend against the old balance (t10647 review).
func TestStaleTokenResultStillAccountsCreditSpend(t *testing.T) {
	const model = "codex-stale-token-test-model"
	now := time.Now()
	auth := &Auth{ID: "stale-token-spend", Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{MetadataCreditsFloor: 1000.0},
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "100",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}}}
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	t.Cleanup(func() { credits.forget(creditLedgerKey(auth)) })
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	m.ObserveCredits(auth, CreditReading{Balance: 5000, HasCredits: true, ReadAt: now})

	// The token rotated during the request: the manager's credential is newer.
	m.mu.Lock()
	m.auths[auth.ID].CredentialVersion = 2
	m.mu.Unlock()

	headers := http.Header{}
	headers.Set("X-Codex-Credits-Balance", "900")
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, headers)
	m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "codex", Model: model, Success: true, CredentialVersion: 1})

	if reading := CreditReadingOf(auth); reading.Balance != 900 {
		t.Fatalf("stale token result balance = %v; want 900", reading.Balance)
	}
	if _, spent := authIncludedSpentUntil(auth, time.Now()); !spent {
		t.Fatal("stale token result at balance 900 below the floor: served; want refused")
	}
}
