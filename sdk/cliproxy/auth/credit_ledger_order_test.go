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

func orderTestAuth(id string, now time.Time) *Auth {
	return &Auth{ID: id, Provider: "codex", Metadata: map[string]any{MetadataCreditsFloor: 1000.0},
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "100",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}}}
}

// An unknown cost is dated to when its response completed, so a poll that started while
// the response was generating cannot clear it (t10647 review).
func TestPollStartedDuringGenerationCannotClearCompletedUnknown(t *testing.T) {
	const model = "codex-completion-unknown-test-model"
	now := time.Now()
	auth := orderTestAuth("completion-unknown", now)
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	t.Cleanup(func() { credits.forget(creditLedgerKey(auth)) })
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	m.ObserveCredits(auth, CreditReading{Balance: 5000, HasCredits: true, ReadAt: now})

	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Used-Percent", "100")
	headers.Set("X-Codex-Primary-Window-Minutes", "10080")
	headers.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(time.Hour).Unix(), 10))
	internallogging.SetResponseHeaders(ctx, headers)
	pollReadAt := time.Now()

	current, _ := m.GetByID(auth.ID)
	m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "codex", Model: model, Success: true,
		CredentialVersion: current.CredentialVersion, RegistrationEpoch: current.RegistrationEpoch})
	if reading := CreditReadingOf(auth); !reading.Unknown {
		t.Fatalf("response without a balance: reading %+v; want unknown", reading)
	}

	// The poll was started before the response completed; it must not clear the unknown.
	m.ObserveCredits(auth, CreditReading{Balance: 5000, HasCredits: true, ReadAt: pollReadAt})
	if reading := CreditReadingOf(auth); !reading.Unknown {
		t.Fatal("a poll started during generation cleared the completed response's unknown cost: want refused")
	}
}

// A late, older balance observation must not move UpdatedAt backwards behind a newer
// unknown cost and let an overlapping poll restore permission (t10647 review).
func TestOlderLowerBalanceCannotMoveUpdatedAtBehindUnknown(t *testing.T) {
	now := time.Now()
	auth := orderTestAuth("older-lower-balance", now)
	t.Cleanup(func() { credits.forget(creditLedgerKey(auth)) })
	m := NewManager(nil, nil, nil)
	m.ObserveCredits(auth, CreditReading{Balance: 5000, HasCredits: true, ReadAt: now})
	key := creditLedgerKey(auth)

	unknownAt := now.Add(10 * time.Second)
	credits.observeResponse(key, 0, false, true, unknownAt)
	credits.observeResponse(key, 400, true, false, now.Add(5*time.Second))
	if got := CreditReadingOf(auth).UpdatedAt; !got.Equal(unknownAt) {
		t.Fatalf("UpdatedAt = %v; want %v (an older balance moved it backwards)", got, unknownAt)
	}

	m.ObserveCredits(auth, CreditReading{Balance: 5000, HasCredits: true, ReadAt: now.Add(6 * time.Second)})
	if reading := CreditReadingOf(auth); !reading.Unknown {
		t.Fatal("a poll between the older balance and the unknown cost cleared it: want refused")
	}
}

// A repeated revocation must advance UpdatedAt even when the flag is already set, so a
// poll started between two denials cannot re-enable spending (t10647 review).
func TestRepeatedRevocationAdvancesUpdatedAt(t *testing.T) {
	now := time.Now()
	auth := orderTestAuth("repeated-revocation", now)
	t.Cleanup(func() { credits.forget(creditLedgerKey(auth)) })
	m := NewManager(nil, nil, nil)
	m.ObserveCredits(auth, CreditReading{Balance: 5000, HasCredits: true, ReadAt: now})
	key := creditLedgerKey(auth)

	denied := http.Header{}
	denied.Set("X-Codex-Credits-Has-Credits", "false")
	credits.revokeCredits(key, denied, now.Add(time.Second))
	credits.revokeCredits(key, denied, now.Add(3*time.Second))

	m.ObserveCredits(auth, CreditReading{Balance: 5000, HasCredits: true, ReadAt: now.Add(2 * time.Second)})
	if reading := CreditReadingOf(auth); reading.HasCredits {
		t.Fatal("a poll started between two has_credits=false observations re-granted spending: want refused")
	}
}
