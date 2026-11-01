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

// creditLedger holds the one credit reading per credential ID that the paid-credits
// guard reads. It lives outside Auth on purpose: the manager, the scheduler's per-model
// snapshots and in-flight updates all hold copies of an Auth, and a reading carried in
// a copy can be older than the latest one. Keyed by ID, every copy sees the same figure.
type creditLedger struct {
	mu       sync.Mutex
	readings map[string]CreditReading
}

var credits = &creditLedger{readings: make(map[string]CreditReading)}

func (l *creditLedger) get(id string) CreditReading {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readings[id]
}

func (l *creditLedger) forget(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.readings, id)
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

// ObserveCredits records a usage poll's credit reading for a credential.
func (m *Manager) ObserveCredits(id string, reading CreditReading) bool {
	if m == nil || strings.TrimSpace(id) == "" {
		return false
	}
	return credits.observePoll(id, reading)
}

// CreditReadingOf returns the credit reading the guard uses for a credential.
func CreditReadingOf(id string) CreditReading {
	return credits.get(id)
}

// observeCreditSpend applies a served Codex response to the credential's reading.
func observeCreditSpend(auth *Auth, provider string, headers http.Header, now time.Time) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return
	}
	reported, errParse := strconv.ParseFloat(strings.TrimSpace(headers.Get("X-Codex-Credits-Balance")), 64)
	hasReported := errParse == nil && !math.IsNaN(reported) && !math.IsInf(reported, 0)
	credits.observeResponse(auth.ID, reported, hasReported, !includedExhaustedUntil(auth.Quota, now).IsZero(), now)
}
