package auth

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	// MetadataHoldUntil holds a credential out of every selection path until the
	// RFC3339 instant it stores. It lives in the auth JSON, so it can be set or
	// cleared without a restart (file edit or the credentials/fields endpoint).
	MetadataHoldUntil = "hold_until"
	// MetadataGroup names the credential groups an account pin can select.
	// The value is a comma-separated string, for example "personal,pro".
	MetadataGroup = "group"
	// MetadataPinOnly keeps a credential out of unpinned routing: only a request
	// whose account pin matches it can use it. Holds and disabled still apply.
	MetadataPinOnly = "pin_only"
	// MetadataAllowCredits lets a credential keep serving after an included window is
	// used up. The provider then bills the overage (Codex credits, Claude extra usage),
	// so it is off unless set on the account.
	MetadataAllowCredits = "allow_credits"
	// MetadataCreditsFloor lets a Codex account keep serving past its included window
	// while its credit balance stays above this number of credits. It is for a granted,
	// expiring credit pool (the ChatGPT Pro grants, t10647): the floor caps how much of
	// the balance the hub may spend, and an unknown balance is refused.
	MetadataCreditsFloor = "credits_floor"
)

// AuthPinOnly reports whether the credential serves pinned requests only.
func AuthPinOnly(auth *Auth) bool {
	return authMetadataFlag(auth, MetadataPinOnly)
}

// AuthAllowCredits reports whether the credential may serve past its included allowance.
func AuthAllowCredits(auth *Auth) bool {
	return authMetadataFlag(auth, MetadataAllowCredits)
}

// AuthCreditsFloor returns the account's credits floor when one is set.
func AuthCreditsFloor(auth *Auth) (float64, bool) {
	if auth == nil {
		return 0, false
	}
	var floor float64
	switch value := auth.Metadata[MetadataCreditsFloor].(type) {
	case float64:
		floor = value
	case int:
		floor = float64(value)
	case int64:
		floor = float64(value)
	case json.Number:
		parsed, errParse := value.Float64()
		if errParse != nil {
			return 0, false
		}
		floor = parsed
	case string:
		parsed, errParse := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if errParse != nil {
			return 0, false
		}
		floor = parsed
	default:
		return 0, false
	}
	if math.IsNaN(floor) || math.IsInf(floor, 0) || floor < 0 {
		return 0, false
	}
	return floor, true
}

// CreditReadingMaxAge is how long a credit reading may authorise spending. The usage
// poll reads an account with a credits floor every interval (10 minutes on the estate
// hub); an older reading, or none, is unknown and the guard refuses the account.
const CreditReadingMaxAge = 30 * time.Minute

// AuthCreditBalance returns the credit balance the usage poll last read, while that
// reading is recent. It reports nothing when the account has no credits or unlimited
// credits, which are billed.
func AuthCreditBalance(auth *Auth, now time.Time) (float64, bool) {
	if auth == nil {
		return 0, false
	}
	reading := auth.Quota.Credits
	if reading.ReadAt.IsZero() || reading.Unknown || now.Sub(reading.ReadAt) > CreditReadingMaxAge ||
		!reading.HasCredits || reading.Unlimited || math.IsNaN(reading.Balance) || math.IsInf(reading.Balance, 0) {
		return 0, false
	}
	return reading.Balance, true
}

// authCreditsAboveFloor reports whether the account may spend its credit balance:
// a floor is set and a recent, known balance is above it. Traffic lowers the reading
// (trackCreditSpend); a response reports the balance before its own cost, so requests
// in flight are not counted and the floor must cover what concurrent requests spend.
func authCreditsAboveFloor(auth *Auth, now time.Time) bool {
	floor, okFloor := AuthCreditsFloor(auth)
	if !okFloor {
		return false
	}
	balance, okBalance := AuthCreditBalance(auth, now)
	return okBalance && balance > floor
}

func authMetadataFlag(auth *Auth, key string) bool {
	if auth == nil {
		return false
	}
	switch value := auth.Metadata[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	}
	return false
}

// authIncludedSpentUntil reports when the credential's included allowance returns while
// any observed window is fully used and not yet reset. Past that point a Codex account
// with credits, or a Claude account with extra usage, still answers and bills the
// request, so the hub routes nothing to it (no paid fallback, t9765) unless
// allow_credits is set, or credits_floor is set and the credit balance is above it
// (a granted credit pool, t10647). The usage refresher keeps polling it, so an early
// reset lifts this.
func authIncludedSpentUntil(auth *Auth, now time.Time) (time.Time, bool) {
	if auth == nil || AuthAllowCredits(auth) {
		return time.Time{}, false
	}
	until := includedExhaustedUntil(auth.Quota, now)
	if until.IsZero() || authCreditsAboveFloor(auth, now) {
		return time.Time{}, false
	}
	return until, true
}

// includedExhaustedUntil returns the latest reset among the observed windows that are
// fully used and not yet reset, or zero when none is.
func includedExhaustedUntil(quota QuotaState, now time.Time) time.Time {
	var until time.Time
	for _, window := range quotaWindowsFromSignals(quota.Signals, quota.ObservedAt) {
		if window.used >= 100 && window.resetAt.After(now) && window.resetAt.After(until) {
			until = window.resetAt
		}
	}
	return until
}

// trackCreditSpend lowers the credit reading as traffic spends it: to the balance a
// Codex response reports when that is lower. A response served past the included
// allowance that reports no balance makes the reading unknown, so the guard refuses
// the account until a newer poll. It never raises the reading; only a poll does
// (ObserveCredits). It reports whether the reading changed.
func (q *QuotaState) trackCreditSpend(provider string, headers http.Header, now time.Time) bool {
	if q == nil || q.Credits.ReadAt.IsZero() || !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return false
	}
	if reported, errParse := strconv.ParseFloat(strings.TrimSpace(headers.Get("X-Codex-Credits-Balance")), 64); errParse == nil &&
		!math.IsNaN(reported) && !math.IsInf(reported, 0) {
		if reported >= q.Credits.Balance {
			return false
		}
		q.Credits.Balance, q.Credits.UpdatedAt = reported, now
		return true
	}
	if includedExhaustedUntil(*q, now).IsZero() || q.Credits.Unknown {
		return false
	}
	q.Credits.Unknown, q.Credits.UpdatedAt = true, now
	return true
}

func accountPinFromMetadata(meta map[string]any) string {
	value, _ := meta[cliproxyexecutor.AccountPinMetadataKey].(string)
	return strings.TrimSpace(value)
}

// authHoldUntil reports the hold deadline when the credential is held at now.
func authHoldUntil(auth *Auth, now time.Time) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	raw, _ := auth.Metadata[MetadataHoldUntil].(string)
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	until, errParse := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if errParse != nil || !until.After(now) {
		return time.Time{}, false
	}
	return until, true
}

// authMatchesAccountPin matches a pin case-insensitively against the credential ID,
// file name (with or without .json), label, email, or any entry of its group field.
func authMatchesAccountPin(auth *Auth, pin string) bool {
	if auth == nil {
		return false
	}
	pin = strings.TrimSpace(pin)
	names := []string{auth.ID, auth.Label}
	if auth.FileName != "" {
		base := filepath.Base(auth.FileName)
		names = append(names, base, strings.TrimSuffix(base, ".json"))
	}
	if email, _ := auth.Metadata["email"].(string); email != "" {
		names = append(names, email)
	}
	if groups, _ := auth.Metadata[MetadataGroup].(string); groups != "" {
		names = append(names, strings.Split(groups, ",")...)
	}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && strings.EqualFold(name, pin) {
			return true
		}
	}
	return false
}

// accountPinError refuses a pinned request outright instead of rotating it to
// another account. It carries Retry-After when the pinned account has a known
// return time.
type accountPinError struct {
	err        *Error
	retryAfter time.Duration
}

func (e *accountPinError) Error() string   { return e.err.Error() }
func (e *accountPinError) StatusCode() int { return e.err.StatusCode() }
func (e *accountPinError) Unwrap() error   { return e.err }

func (e *accountPinError) Headers() http.Header {
	if e == nil || e.retryAfter <= 0 {
		return nil
	}
	headers := make(http.Header)
	headers.Set("Retry-After", strconv.Itoa(int(math.Ceil(e.retryAfter.Seconds()))))
	return headers
}

// accountPinRefusal returns nil when at least one credential matching the request's
// account pin can serve the model now. Otherwise it explains why the pinned account
// cannot: unknown, disabled, held, or at its limit (cooling down).
func (m *Manager) accountPinRefusal(providers []string, model string, opts cliproxyexecutor.Options) error {
	pin := accountPinFromMetadata(opts.Metadata)
	if m == nil || pin == "" {
		return nil
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerSet[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	now := time.Now()
	registryRef := registry.GetGlobalRegistry()
	matched, disabled := 0, 0
	var heldUntil, coolingUntil, spentUntil time.Time

	m.mu.RLock()
	for _, candidate := range m.auths {
		if !authMatchesAccountPin(candidate, pin) {
			continue
		}
		if _, ok := providerSet[canonicalSchedulingProvider(executorKeyFromAuth(candidate))]; !ok {
			continue
		}
		if model != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		matched++
		if candidate.Disabled || candidate.Status == StatusDisabled {
			disabled++
			continue
		}
		if until, held := authHoldUntil(candidate, now); held {
			if heldUntil.IsZero() || until.Before(heldUntil) {
				heldUntil = until
			}
			continue
		}
		if until, spent := authIncludedSpentUntil(candidate, now); spent {
			if spentUntil.IsZero() || until.Before(spentUntil) {
				spentUntil = until
			}
			continue
		}
		blocked, _, next := isAuthBlockedForModel(candidate, m.selectionModelForAuth(candidate, model), now)
		if !blocked {
			m.mu.RUnlock()
			return nil
		}
		if coolingUntil.IsZero() || (!next.IsZero() && next.Before(coolingUntil)) {
			coolingUntil = next
		}
	}
	m.mu.RUnlock()

	if matched == 0 {
		return &accountPinError{err: &Error{
			Code:       "account_not_found",
			Message:    fmt.Sprintf("no credential matches account %q for model %q", pin, model),
			HTTPStatus: http.StatusBadRequest,
		}}
	}
	var reasons []string
	soonest := time.Time{}
	if !coolingUntil.IsZero() {
		reasons = append(reasons, fmt.Sprintf("at its limit until %s", coolingUntil.UTC().Format(time.RFC3339)))
		soonest = coolingUntil
	}
	if !heldUntil.IsZero() {
		reasons = append(reasons, fmt.Sprintf("held until %s", heldUntil.UTC().Format(time.RFC3339)))
		if soonest.IsZero() || heldUntil.Before(soonest) {
			soonest = heldUntil
		}
	}
	if !spentUntil.IsZero() {
		reasons = append(reasons, fmt.Sprintf("included allowance used up until %s (paid credits refused)", spentUntil.UTC().Format(time.RFC3339)))
		if soonest.IsZero() || spentUntil.Before(soonest) {
			soonest = spentUntil
		}
	}
	if disabled > 0 {
		reasons = append(reasons, "disabled")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "unavailable")
	}
	refusal := &accountPinError{err: &Error{
		Code:       "account_unavailable",
		Message:    fmt.Sprintf("account %q cannot serve model %q: %s", pin, model, strings.Join(reasons, "; ")),
		HTTPStatus: http.StatusServiceUnavailable,
	}}
	if !soonest.IsZero() {
		refusal.err.HTTPStatus = http.StatusTooManyRequests
		refusal.retryAfter = soonest.Sub(now)
	}
	return refusal
}
