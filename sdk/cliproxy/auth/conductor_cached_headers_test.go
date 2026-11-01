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

// A cached response header carries the time it was observed, not the time its result is
// recorded: an older 60% must not clear a newer socket's 100% and let a below-floor
// account spend (t10647 review).
func TestCachedResponseHeadersCannotClearANewerExhaustion(t *testing.T) {
	const model = "codex-cached-headers-test-model"
	now := time.Now()
	auth := &Auth{ID: "cached-response-headers", Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{MetadataCreditsFloor: 1000.0},
		Quota: QuotaState{ObservedAt: now.Add(-time.Minute), Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "60",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
		}}}
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	t.Cleanup(func() { credits.forget(creditLedgerKey(auth)) })
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	m.ObserveCredits(auth, CreditReading{Balance: 900, HasCredits: true, ReadAt: now})

	// The request context keeps the headers of an older response that reported 60%.
	cached := http.Header{}
	cached.Set("X-Codex-Primary-Used-Percent", "60")
	cached.Set("X-Codex-Primary-Window-Minutes", "10080")
	cached.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10))
	cached.Set("X-Codex-Credits-Balance", "900")
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, cached)

	// Another socket then reports the window used up.
	exhausted := http.Header{}
	exhausted.Set("X-Codex-Primary-Used-Percent", "100")
	exhausted.Set("X-Codex-Primary-Window-Minutes", "10080")
	exhausted.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(time.Hour).Unix(), 10))
	ObserveStreamQuota(auth, exhausted, time.Now())
	if _, spent := authIncludedSpentUntil(auth, time.Now()); !spent {
		t.Fatal("setup: 100% with balance 900 below the floor: served; want refused")
	}

	current, _ := m.GetByID(auth.ID)
	m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "codex", Model: model, Success: true,
		CredentialVersion: current.CredentialVersion, RegistrationEpoch: current.RegistrationEpoch})

	if _, spent := authIncludedSpentUntil(auth, time.Now()); !spent {
		t.Fatal("cached 60% headers cleared a newer 100%: served; want refused")
	}
}
