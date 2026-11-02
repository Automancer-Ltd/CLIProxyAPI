package auth

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// A live response or stream event may take the grant away (has_credits=false,
// unlimited=true); only a poll may give it back (t10647 review).
func TestLiveCreditFlagsRevokeTheGrant(t *testing.T) {
	now := time.Now()
	auth := &Auth{ID: "live-credit-flags", Provider: "codex",
		Metadata: map[string]any{MetadataCreditsFloor: 1000.0},
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "100",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}}}
	t.Cleanup(func() { credits.forget(creditLedgerKey(auth)) })
	m := NewManager(nil, nil, nil)
	reading := CreditReading{Balance: 5000, HasCredits: true, ReadAt: now}
	m.ObserveCredits(auth, reading)
	if _, spent := authIncludedSpentUntil(auth, now); spent {
		t.Fatal("balance above the floor: refused; want served")
	}

	// An HTTP response reports the grant is gone.
	hasCreditsFalse := http.Header{}
	hasCreditsFalse.Set("X-Codex-Credits-Has-Credits", "false")
	hasCreditsFalse.Set("X-Codex-Credits-Balance", "5000")
	observeCreditSpend(auth, "codex", hasCreditsFalse, now)
	if _, ok := AuthCreditBalance(auth, now); ok {
		t.Fatal("has_credits=false: balance still known; want refused")
	}
	if _, spent := authIncludedSpentUntil(auth, now); !spent {
		t.Fatal("has_credits=false: served; want refused")
	}

	// A newer poll grants it again.
	reading.ReadAt = now.Add(time.Second)
	m.ObserveCredits(auth, reading)
	if _, spent := authIncludedSpentUntil(auth, now.Add(time.Second)); spent {
		t.Fatal("new poll with credits: refused; want served")
	}

	// A stream event reports unlimited credits, which this guard treats as paid.
	unlimited := http.Header{}
	unlimited.Set("X-Codex-Credits-Unlimited", "true")
	ObserveStreamQuota(auth, unlimited, now.Add(2*time.Second))
	if _, spent := authIncludedSpentUntil(auth, now.Add(2*time.Second)); !spent {
		t.Fatal("unlimited=true: served; want refused")
	}

	// A new poll clears unlimited; a live report then revokes has_credits, and a
	// later live has_credits=true must not re-grant it.
	reading.ReadAt = now.Add(3 * time.Second)
	m.ObserveCredits(auth, reading)
	revoke := http.Header{}
	revoke.Set("X-Codex-Credits-Has-Credits", "false")
	revoke.Set("X-Codex-Credits-Balance", "5000")
	observeCreditSpend(auth, "codex", revoke, now.Add(4*time.Second))
	grant := http.Header{}
	grant.Set("X-Codex-Credits-Has-Credits", "true")
	grant.Set("X-Codex-Credits-Balance", "5000")
	observeCreditSpend(auth, "codex", grant, now.Add(5*time.Second))
	if _, spent := authIncludedSpentUntil(auth, now.Add(5*time.Second)); !spent {
		t.Fatal("a live has_credits=true re-granted the permission: served; want refused")
	}
}
