package auth

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CreditReading is an account's credit balance: read by the usage poll, then only
// lowered by traffic until a poll that is newer than the last change (credits_floor,
// t10647).
type CreditReading struct {
	Balance    float64 `json:"balance"`
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited"`
	// ReadAt is when the last poll read it; UpdatedAt the last change by poll or traffic.
	ReadAt    time.Time `json:"read_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Unknown: a response was served on credits without reporting a balance, so what
	// it cost is unknown until a newer poll.
	Unknown bool `json:"unknown,omitempty"`
}

// creditLedger holds the one credit reading per credential that the paid-credits guard
// reads. It lives outside Auth on purpose: the manager, the scheduler's per-model
// snapshots and in-flight updates all hold copies of an Auth, and a reading carried in
// a copy can be older than the latest one. Keyed by credential, every copy sees the
// same figure. The key includes the provider account behind the credential
// (creditLedgerKey), so an account file replaced by another account's login never
// inherits its predecessor's balance, even from a poll still in flight.
type creditLedger struct {
	mu       sync.Mutex
	readings map[string]CreditReading
}

var credits = &creditLedger{readings: make(map[string]CreditReading)}

// creditLedgerKey names a credential and the provider account it holds (the ChatGPT
// account and email of a Codex login).
func creditLedgerKey(auth *Auth) string {
	if auth == nil {
		return ""
	}
	key := auth.ID
	for _, field := range []string{"account_id", "email"} {
		value, _ := auth.Metadata[field].(string)
		key += "\x00" + strings.ToLower(strings.TrimSpace(value))
	}
	return key
}

func (l *creditLedger) get(key string) CreditReading {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readings[key]
}

func (l *creditLedger) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.readings, key)
}

// observePoll records a poll's reading, read at reading.ReadAt. A poll no newer than the
// last poll is ignored. One newer than the last change replaces the reading; one that
// overlaps a later change by traffic can only lower the balance.
func (l *creditLedger) observePoll(id string, reading CreditReading) bool {
	if reading.ReadAt.IsZero() {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	current, known := l.readings[id]
	if known && !reading.ReadAt.After(current.ReadAt) {
		return false
	}
	if !known || reading.ReadAt.After(current.UpdatedAt) {
		reading.UpdatedAt, reading.Unknown = reading.ReadAt, false
		l.readings[id] = reading
		return true
	}
	current.ReadAt = reading.ReadAt
	current.Balance = math.Min(current.Balance, reading.Balance)
	current.HasCredits = current.HasCredits && reading.HasCredits
	current.Unlimited = current.Unlimited || reading.Unlimited
	l.readings[id] = current
	return true
}

// observeResponse lowers the reading for one served response: to the balance it
// reports when that is lower; with no balance while the included allowance is used up,
// the reading becomes unknown and UpdatedAt moves on, so only a poll that started after
// this response can clear it. It never raises the reading.
func (l *creditLedger) observeResponse(id string, reported float64, hasReported, onCredits bool, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	current, known := l.readings[id]
	if !known {
		return
	}
	switch {
	case hasReported:
		if reported < current.Balance {
			current.Balance, current.UpdatedAt = reported, now
		}
	case onCredits:
		current.Unknown, current.UpdatedAt = true, now
	default:
		return
	}
	l.readings[id] = current
}

// ObserveCredits records a usage poll's credit reading for the credential as the poll
// saw it, so the reading belongs to the provider account that was polled.
func (m *Manager) ObserveCredits(auth *Auth, reading CreditReading) bool {
	if m == nil || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return false
	}
	return credits.observePoll(creditLedgerKey(auth), reading)
}

// CreditReadingOf returns the credit reading the guard uses for a credential.
func CreditReadingOf(auth *Auth) CreditReading {
	return credits.get(creditLedgerKey(auth))
}

// AuthServesWebsocketTurn reports whether a bound websocket may forward another turn
// on the credential. Follow-up turns skip selection, so a credential past its included
// allowance serves none unless allow_credits is set: with credits_floor, each turn on
// credits then ends its stream, is accounted (MarkResult), and the next turn goes
// through selection and the floor again.
func AuthServesWebsocketTurn(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	return AuthAllowCredits(auth) || includedExhaustedUntil(auth.Quota, now).IsZero()
}

// observeCreditSpend applies a served Codex response to the credential's reading.
func observeCreditSpend(auth *Auth, provider string, headers http.Header, now time.Time) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return
	}
	reported, errParse := strconv.ParseFloat(strings.TrimSpace(headers.Get("X-Codex-Credits-Balance")), 64)
	hasReported := errParse == nil && !math.IsNaN(reported) && !math.IsInf(reported, 0)
	credits.observeResponse(creditLedgerKey(auth), reported, hasReported, !includedExhaustedUntil(auth.Quota, now).IsZero(), now)
}
