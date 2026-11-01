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
	// ExhaustedUntil is when the latest quota observation says the included allowance
	// returns (zero when it is not used up), observed at QuotaObservedAt. Copies of the
	// Auth can carry older windows, so the guard also reads this one.
	ExhaustedUntil  time.Time `json:"exhausted_until,omitzero"`
	QuotaObservedAt time.Time `json:"quota_observed_at,omitzero"`
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
	current := l.readings[id]
	known := !current.ReadAt.IsZero()
	if known && !reading.ReadAt.After(current.ReadAt) {
		return false
	}
	if !known || reading.ReadAt.After(current.UpdatedAt) {
		current.Balance, current.HasCredits, current.Unlimited = reading.Balance, reading.HasCredits, reading.Unlimited
		current.ReadAt, current.UpdatedAt, current.Unknown = reading.ReadAt, reading.ReadAt, false
		l.readings[id] = current
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
	current := l.readings[id]
	if current.ReadAt.IsZero() {
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

// observeExhaustion records when the included allowance returns, as of observedAt. A
// complete observation (a response's or a poll's full quota snapshot) replaces an older
// one; a partial one (a mid-stream quota event) can only move the return later, since
// it may omit the window that is used up.
func (l *creditLedger) observeExhaustion(key string, until, observedAt time.Time, complete bool) {
	if observedAt.IsZero() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	current := l.readings[key]
	if !observedAt.After(current.QuotaObservedAt) {
		return
	}
	if complete || until.After(current.ExhaustedUntil) {
		current.ExhaustedUntil, current.QuotaObservedAt = until, observedAt
		l.readings[key] = current
	}
}

// recordQuotaExhaustion stores the credential's current quota snapshot in the ledger.
// Only a snapshot that carries usage windows is complete; one without (a response that
// reported only credits) can only move the return later.
func recordQuotaExhaustion(auth *Auth, now time.Time) {
	if auth == nil || auth.Quota.ObservedAt.IsZero() {
		return
	}
	complete := len(quotaWindowsFromSignals(auth.Quota.Signals, auth.Quota.ObservedAt)) > 0
	credits.observeExhaustion(creditLedgerKey(auth), includedExhaustedUntil(auth.Quota, now), auth.Quota.ObservedAt, complete)
}

// ObserveStreamQuota applies quota figures that arrive inside a running stream (Codex
// websocket rate-limit events) before the stream ends: a used-up window and a lower
// credit balance take effect at once, so a follow-up turn is checked against them.
func ObserveStreamQuota(auth *Auth, headers http.Header, now time.Time) {
	if auth == nil || len(headers) == 0 {
		return
	}
	signals := collectQuotaSignals(auth.Provider, headers)
	if len(signals) == 0 {
		return
	}
	key := creditLedgerKey(auth)
	credits.observeExhaustion(key, includedExhaustedUntil(QuotaState{Signals: signals, ObservedAt: now}, now), now, false)
	if reported, errParse := strconv.ParseFloat(strings.TrimSpace(signals["X-Codex-Credits-Balance"]), 64); errParse == nil &&
		!math.IsNaN(reported) && !math.IsInf(reported, 0) {
		credits.observeResponse(key, reported, true, false, now)
	}
}

// ledgerExhaustedUntil returns the ledger's return time for the credential's included
// allowance while it is still ahead.
func ledgerExhaustedUntil(auth *Auth, now time.Time) time.Time {
	until := credits.get(creditLedgerKey(auth)).ExhaustedUntil
	if until.After(now) {
		return until
	}
	return time.Time{}
}

// authExhaustedUntil returns when the credential's included allowance returns, by the
// later of the Auth copy's windows and the ledger's latest observation, or zero when
// neither says it is used up. Every check of "used up" goes through this.
func authExhaustedUntil(auth *Auth, now time.Time) time.Time {
	if auth == nil {
		return time.Time{}
	}
	until := includedExhaustedUntil(auth.Quota, now)
	if ledger := ledgerExhaustedUntil(auth, now); ledger.After(until) {
		until = ledger
	}
	return until
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
// on the credential. Follow-up turns skip selection, so it applies the selection guard
// on every turn: an account whose included allowance is used up serves none unless its
// credit balance is above its floor (or allow_credits is set). A credit-funded account
// keeps its session, because a refusal closes the socket instead of returning the
// client to selection; the floor is re-checked on each turn, and each turn's spend
// lowers the reading (or makes it unknown) at completion.
func AuthServesWebsocketTurn(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	_, spent := authIncludedSpentUntil(auth, now)
	return !spent
}

// accountServedResponse applies a served response's quota and credit figures to the
// ledger without touching credential health: a canceled delivery, or a result that is
// stale for health because the token rotated during the request. The figures are a
// partial observation, so a cached snapshot can only move a used-up window later, never
// clear a newer socket's exhaustion.
func accountServedResponse(auth *Auth, provider string, headers http.Header, now time.Time) {
	if auth == nil {
		return
	}
	ObserveStreamQuota(auth, headers, now)
	observeCreditSpend(auth, provider, headers, now)
}

// ObserveStreamResponse accounts a completed duplex response whose stream reported no
// credit balance: its cost is unknown until a newer poll, so the guard refuses the
// account in the meantime. It is the stream counterpart of MarkResult's credit
// accounting, which otherwise waits for the socket to close.
func ObserveStreamResponse(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	observeCreditSpend(auth, auth.Provider, nil, now)
}

// observeCreditSpend applies a served Codex response to the credential's reading.
func observeCreditSpend(auth *Auth, provider string, headers http.Header, now time.Time) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return
	}
	reported, errParse := strconv.ParseFloat(strings.TrimSpace(headers.Get("X-Codex-Credits-Balance")), 64)
	hasReported := errParse == nil && !math.IsNaN(reported) && !math.IsInf(reported, 0)
	credits.observeResponse(creditLedgerKey(auth), reported, hasReported, !authExhaustedUntil(auth, now).IsZero(), now)
}
