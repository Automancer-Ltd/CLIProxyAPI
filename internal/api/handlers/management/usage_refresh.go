package management

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/MoeForge/moego/json"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gjson "github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// usageSnapshotName lives in the auth directory. It must not end in .json,
	// or the watcher would load it as a credential.
	usageSnapshotName = "usage-snapshot.cache"
	usageRefreshTick  = time.Minute
	// Meta locks out a login that is polled often (a day-long lockout on
	// 2026-09-02 at shorter intervals), so Muse is asked at most this often.
	usageMetaFloor = 3 * time.Hour
	// Banked resets only come from the poll, so a Codex account that is always
	// serving traffic is still polled this often.
	usageBankedFloor = time.Hour
)

// usageRecord is what the last usage poll returned for one credential.
type usageRecord struct {
	Plan         string                 `json:"plan,omitempty"`
	Windows      []coreauth.UsageWindow `json:"windows,omitempty"`
	Balance      *usageBalance          `json:"balance,omitempty"`
	BankedResets []usageCredit          `json:"banked_resets,omitempty"`
	// FetchedAt is the last successful poll; AttemptedAt the last try.
	FetchedAt       time.Time `json:"fetched_at,omitzero"`
	AttemptedAt     time.Time `json:"attempted_at,omitzero"`
	Error           string    `json:"error,omitempty"`
	NextPollAt      time.Time `json:"next_poll_at,omitzero"`
	PollFailures    int       `json:"poll_failures,omitempty"`
	PollStopped     bool      `json:"poll_stopped,omitempty"`
	CredentialStamp string    `json:"credential_stamp,omitempty"`
	WasDisabled     bool      `json:"was_disabled,omitempty"`
	// Signals and ObservedAt carry the Claude/Codex quota headers across a
	// restart, so consume-first does not rank every account as unknown.
	Signals    map[string]string `json:"signals,omitempty"`
	ObservedAt time.Time         `json:"observed_at,omitzero"`
}

type usageStore struct {
	mu      sync.Mutex
	records map[string]usageRecord
	path    string
}

func (s *usageStore) get(id string) usageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[id]
}

func (s *usageStore) set(id string, record usageRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]usageRecord)
	}
	s.records[id] = record
}

// StartUsageRefresher polls provider usage endpoints for idle credentials until
// ctx ends. It does nothing while routing.usage-refresh.interval is unset, and
// reads the config on every tick, so it follows hot reloads.
func (h *Handler) StartUsageRefresher(ctx context.Context) {
	if h == nil || h.authManager == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(usageRefreshTick)
		defer ticker.Stop()
		for {
			h.refreshUsage(ctx, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func usageRefreshInterval(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Home.Enabled {
		return 0
	}
	interval, errParse := time.ParseDuration(strings.TrimSpace(cfg.Routing.UsageRefresh.Interval))
	if errParse != nil {
		return 0
	}
	return interval
}

func usageProviderEnabled(cfg *config.Config, provider string) bool {
	if len(cfg.Routing.UsageRefresh.Providers) == 0 {
		return true
	}
	for _, name := range cfg.Routing.UsageRefresh.Providers {
		if strings.EqualFold(strings.TrimSpace(name), provider) {
			return true
		}
	}
	return false
}

// refreshUsage runs one pass: restore persisted quota headers, poll every
// credential that is due, and save the snapshot when anything changed.
func (h *Handler) refreshUsage(ctx context.Context, now time.Time) {
	cfg := h.configSnapshot()
	interval := usageRefreshInterval(cfg)
	if interval <= 0 {
		return
	}
	h.loadUsageSnapshot(cfg)
	changed := false
	for _, auth := range h.authManager.List() {
		if ctx.Err() != nil {
			return
		}
		record := h.usage.get(auth.ID)
		stamp := usageCredentialStamp(auth)
		disabled := auth.Disabled || auth.Status == coreauth.StatusDisabled
		if record.CredentialStamp != stamp || (record.WasDisabled && !disabled) {
			record.PollStopped, record.PollFailures, record.NextPollAt = false, 0, time.Time{}
		}
		if record.CredentialStamp != stamp || record.WasDisabled != disabled {
			changed = true
		}
		// Upgrade legacy snapshots without immediately retrying every refused account.
		if record.CredentialStamp == "" && record.Error != "" {
			if strings.Contains(record.Error, "HTTP 429") || strings.Contains(record.Error, "HTTP 503") {
				status := 429
				if strings.Contains(record.Error, "HTTP 503") {
					status = 503
				}
				record = usagePollFailure(record, usageHTTPError{status: status}, now, rand.Float64())
			} else if strings.Contains(record.Error, "HTTP 401") || strings.Contains(record.Error, "invalid_grant") {
				record.PollStopped = true
			}
		}
		record.CredentialStamp, record.WasDisabled = stamp, disabled
		if disabled || auth.RequiresSignIn || usageAuthTerminal(auth) {
			if !record.PollStopped {
				changed = true
			}
			record.PollStopped = true
		}
		observes := coreauth.ProviderSupportsQuotaObservation(auth.Provider)
		if observes && len(record.Signals) > 0 {
			// Credentials register after startup, so restoring is retried each pass;
			// ObserveQuota ignores it once newer headers exist.
			if strings.EqualFold(auth.Provider, "devin") {
				// Devin's signals are not headers; restore them as recorded.
				h.authManager.ObserveQuotaSignals(auth.ID, record.Signals, record.ObservedAt)
			} else {
				h.authManager.ObserveQuota(auth.ID, signalHeaders(record.Signals), record.ObservedAt)
			}
		}
		kind := usageKind(auth)
		fetch := usageFetchers[kind]
		every := interval
		if kind == "claude" {
			every = max(every, 30*time.Minute)
		}
		if kind == "meta" {
			every = max(every, usageMetaFloor)
		}
		observed := auth.Quota.ObservedAt
		if record.ObservedAt.After(observed) {
			observed = record.ObservedAt
		}
		// An account serving traffic already reports its own headers.
		busy := observes && now.Sub(observed) < every &&
			!(kind == "codex" && now.Sub(record.AttemptedAt) >= usageBankedFloor)
		due := !record.PollStopped && !disabled && !now.Before(record.NextPollAt) && fetch != nil && usageProviderEnabled(cfg, kind) && now.Sub(record.AttemptedAt) >= every && !busy
		if due {
			record = h.pollUsage(ctx, auth, fetch, record, now)
			if record.PollFailures == 0 {
				record.NextPollAt = now.Add(every + usageNormalJitter(auth.ID, every))
			}
			changed = true
		}
		if current, ok := h.authManager.GetByID(auth.ID); ok && observes && current.Quota.ObservedAt.After(record.ObservedAt) {
			record.Signals, record.ObservedAt = current.Quota.Signals, current.Quota.ObservedAt
			changed = true
		}
		h.usage.set(auth.ID, record)
	}
	if changed {
		h.saveUsageSnapshot()
	}
}

func (h *Handler) pollUsage(ctx context.Context, auth *coreauth.Auth, fetch usageFetcher, record usageRecord, now time.Time) usageRecord {
	ctx, cancel := context.WithTimeout(ctx, defaultAPICallTimeout)
	defer cancel()
	record.AttemptedAt = now
	// Only poll state changes on failure. Inference/auth cooldowns are independent.
	if auth.HubAuthority != nil && auth.Provider == "meta" {
		updated, err := h.authManager.ForceRefreshAuth(ctx, auth.ID)
		if err != nil || updated == nil {
			record.Error = "hub Meta usage refresh unavailable"
			return record
		}
		raw, _ := json.Marshal(map[string]any{"subs_tier_name": updated.Metadata["subs_tier_name"], "subs_usage": updated.Metadata["subs_usage"]})
		result := parseMetaUsage(gjson.ParseBytes(raw))
		record.Plan, record.Windows, record.FetchedAt, record.Error = result.plan, result.windows, now, ""
		return record
	}
	token, errToken := h.usageToken(ctx, auth)
	if errToken == nil && token == "" {
		errToken = errors.New("no token on the credential")
	}
	if errToken != nil {
		record = usagePollFailure(record, fmt.Errorf("token: %w", errToken), now, rand.Float64())
		return record
	}
	client := &http.Client{Transport: h.apiCallTransport(auth, "")}
	result, errFetch := fetch(ctx, client, auth, token)
	if kind := usageKind(auth); usageUnauthorized(errFetch) && (kind == "claude" || kind == "codex") {
		// The stored access token can be refused before it expires, and nothing
		// else refreshes an idle login, so refresh once and read again (t10411).
		refreshed, errRefresh := h.authManager.RefreshCredentialAfterUnauthorized(ctx, auth.ID, token)
		if errRefresh != nil {
			log.WithError(errRefresh).Warnf("usage refresh: %s %s: token refresh after HTTP 401 failed", auth.Provider, auth.ID)
			record = usagePollFailure(record, fmt.Errorf("%w; token refresh failed: %v", errFetch, errRefresh), now, rand.Float64())
			if current, ok := h.authManager.GetByID(auth.ID); ok {
				record.CredentialStamp = usageCredentialStamp(current)
			}
			return record
		}
		auth, token = refreshed, tokenValueForAuth(refreshed)
		if err := h.authManager.AuthorizeHub(ctx, auth); err != nil {
			record.Error = err.Error()
			return record
		}
		client.Transport = h.apiCallTransport(auth, "")
		result, errFetch = fetch(ctx, client, auth, token)
	}
	if errFetch != nil {
		log.WithError(errFetch).Debugf("usage refresh: %s %s", auth.Provider, auth.ID)
		record = usagePollFailure(record, errFetch, now, rand.Float64())
		return record
	}
	if len(result.headers) > 0 {
		h.authManager.ObserveQuota(auth.ID, result.headers, now)
	}
	if len(result.signals) > 0 {
		h.authManager.ObserveQuotaSignals(auth.ID, result.signals, now)
	}
	record.PollFailures, record.PollStopped = 0, false
	record.CredentialStamp = usageCredentialStamp(auth)
	record.Plan, record.Windows, record.Balance, record.FetchedAt, record.Error = result.plan, result.windows, result.balance, now, ""
	if result.bankedRead {
		record.BankedResets = result.banked
	}
	return record
}

// usageToken returns the bearer token a provider's usage endpoint accepts.
// Muse reports usage on the key-mint call, which takes the device (dca) token.
func (h *Handler) usageToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if h.authManager != nil {
		if err := h.authManager.AuthorizeHub(ctx, auth); err != nil {
			return "", err
		}
	}
	if strings.EqualFold(auth.Provider, "meta") {
		for _, key := range []string{"dca_token", "access_token"} {
			if value, _ := auth.Metadata[key].(string); strings.HasPrefix(strings.TrimSpace(value), "dca:") {
				return strings.TrimSpace(value), nil
			}
		}
		return "", nil
	}
	return h.resolveTokenForAuth(ctx, auth, "")
}

func signalHeaders(signals map[string]string) http.Header {
	headers := make(http.Header, len(signals))
	for key, value := range signals {
		headers.Set(key, value)
	}
	return headers
}

// loadUsageSnapshot reads the persisted snapshot once per auth directory.
func (h *Handler) loadUsageSnapshot(cfg *config.Config) {
	dir, errDir := util.ResolveAuthDir(cfg.AuthDir)
	if errDir != nil || dir == "" {
		return
	}
	path := filepath.Join(dir, usageSnapshotName)
	h.usage.mu.Lock()
	defer h.usage.mu.Unlock()
	if h.usage.path == path {
		return
	}
	h.usage.path = path
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.WithError(errRead).Warn("usage refresh: failed to read snapshot")
		}
		return
	}
	var records map[string]usageRecord
	if errUnmarshal := json.Unmarshal(data, &records); errUnmarshal != nil {
		log.WithError(errUnmarshal).Warn("usage refresh: ignoring unreadable snapshot")
		return
	}
	if h.usage.records == nil {
		h.usage.records = make(map[string]usageRecord, len(records))
	}
	for id, record := range records {
		if _, exists := h.usage.records[id]; !exists {
			h.usage.records[id] = record
		}
	}
}

func (h *Handler) saveUsageSnapshot() {
	h.usage.mu.Lock()
	path := h.usage.path
	data, errMarshal := json.Marshal(h.usage.records)
	h.usage.mu.Unlock()
	if path == "" || errMarshal != nil {
		return
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		log.WithError(errWrite).Warn("usage refresh: failed to write snapshot")
		return
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		log.WithError(errRename).Warn("usage refresh: failed to replace snapshot")
	}
}

// usageEntry is the usage a management view shows for one credential: the main
// Claude/Codex windows from the live quota headers (polled or from traffic),
// plus the windows and plan from the last poll.
func (h *Handler) usageEntry(auth *coreauth.Auth) gin.H {
	record := h.usage.get(auth.ID)
	windows := record.Windows
	// A Devin poll reads the same windows its executor records, so it replaces them.
	if coreauth.ProviderSupportsQuotaObservation(auth.Provider) && !(strings.EqualFold(auth.Provider, "devin") && len(record.Windows) > 0) {
		windows = append(coreauth.QuotaUsageWindows(auth.Quota), record.Windows...)
	}
	entry := gin.H{"windows": windows, "attempted_at": record.AttemptedAt, "next_poll_at": record.NextPollAt, "poll_stopped": record.PollStopped, "poll_failures": record.PollFailures}
	if record.Plan != "" {
		entry["plan"] = record.Plan
	} else if plan := auth.Quota.Signals["plan"]; plan != "" {
		// Devin's executor records its plan with its quota.
		entry["plan"] = plan
	}
	if !record.FetchedAt.IsZero() {
		entry["fetched_at"] = record.FetchedAt
		entry["poll_age_seconds"] = max(0, time.Since(record.FetchedAt).Seconds())
	}
	if record.Error != "" {
		entry["error"] = record.Error
	}
	if record.Balance != nil {
		entry["balance"] = record.Balance
	}
	if len(record.BankedResets) > 0 {
		entry["banked_resets"] = record.BankedResets
	}
	return entry
}

// ListCredentialUsage returns every credential the router can pick, file-backed
// and config-backed, with its usage, cooldowns and the routing strategy. It is
// the hub dashboard's single data source.
func (h *Handler) ListCredentialUsage(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	cfg := h.configSnapshot()
	now := time.Now().UTC()
	accounts := make([]gin.H, 0)
	for _, auth := range h.authManager.List() {
		entry := h.buildAuthFileEntry(auth, authFileQuotaContext{v8: true})
		if entry == nil {
			if auth.AuthSourceKind() != coreauth.AuthSourceConfig {
				continue
			}
			entry = configCredentialEntry(auth, now)
			entry["usage"] = h.usageEntry(auth)
		}
		entry["cooldowns"] = coreauth.CooldownSnapshotForAuth(auth, now)
		accounts = append(accounts, entry)
	}
	response := gin.H{"observed_at": now, "accounts": accounts}
	if cfg != nil {
		response["strategy"] = cfg.Routing.Strategy
		response["usage_refresh"] = cfg.Routing.UsageRefresh
	}
	c.JSON(http.StatusOK, response)
}

// configCredentialEntry describes an API-key credential from the config file,
// which the credential file listing leaves out.
func configCredentialEntry(auth *coreauth.Auth, now time.Time) gin.H {
	unavailable, status, statusMessage, nextRetry := reconcileAuthFileCooldownState(auth, now)
	name := strings.TrimSpace(auth.Label)
	if name == "" {
		name = auth.ID
	}
	entry := gin.H{
		"id":              auth.ID,
		"name":            name,
		"label":           auth.Label,
		"provider":        strings.TrimSpace(auth.Provider),
		"source":          "config",
		"disabled":        auth.Disabled,
		"status":          status,
		"status_message":  statusMessage,
		"unavailable":     unavailable,
		"recent_requests": auth.RecentRequestsSnapshot(now),
	}
	if !nextRetry.IsZero() {
		entry["next_retry_after"] = nextRetry
	}
	return entry
}

// Fingerprint only secret material, never runtime quota/errors or modification times.
func usageCredentialStamp(auth *coreauth.Auth) string {
	h := sha256.New()
	for _, key := range []string{"access_token", "refresh_token", "api_key", "dca_token"} {
		fmt.Fprintf(h, "%s:%v\x00", key, auth.Metadata[key])
	}
	fmt.Fprint(h, auth.Attributes["api_key"])
	return fmt.Sprintf("%x", h.Sum(nil))
}
func usageAuthTerminal(auth *coreauth.Auth) bool {
	if auth.LastError != nil && (auth.LastError.HTTPStatus == http.StatusUnauthorized || strings.Contains(strings.ToLower(auth.LastError.Message), "invalid_grant")) {
		return true
	}
	message := strings.ToLower(auth.StatusMessage)
	return strings.HasPrefix(message, "unauthorized") || strings.Contains(message, "invalid_grant")
}
func usageNormalJitter(id string, interval time.Duration) time.Duration {
	sum := sha256.Sum256([]byte(id))
	return time.Duration(uint16(sum[0])<<8|uint16(sum[1])) * (interval / 3) / 65536
}
func usagePollFailure(record usageRecord, err error, now time.Time, jitter float64) usageRecord {
	record.Error = err.Error()
	record.PollFailures++
	var httpErr usageHTTPError
	if usageUnauthorized(err) || strings.Contains(strings.ToLower(err.Error()), "invalid_grant") {
		record.PollStopped = true
	}
	if errors.As(err, &httpErr) && (httpErr.status == 429 || httpErr.status == 503) {
		base := 15 * time.Minute
		for i := 1; i < record.PollFailures && base < 3*time.Hour; i++ {
			base *= 2
		}
		delay := min(6*time.Hour, time.Duration(float64(base)*(1+max(0, min(1, jitter)))))
		record.NextPollAt = now.Add(delay)
		if retry := usageRetryAfter(httpErr.retryAfter, now); retry.After(record.NextPollAt) {
			record.NextPollAt = retry
		}
	}
	return record
}
func usageRetryAfter(value string, now time.Time) time.Time {
	if seconds, err := time.ParseDuration(strings.TrimSpace(value) + "s"); err == nil && seconds >= 0 {
		return now.Add(seconds)
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date
	}
	return time.Time{}
}
