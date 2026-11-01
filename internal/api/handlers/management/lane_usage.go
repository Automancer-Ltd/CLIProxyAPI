package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	// laneUsageName lives in the auth directory beside usage-snapshot.cache.
	// It must not end in .json, or the watcher would load it as a credential.
	laneUsageName = "lane-usage.cache"
	// laneUsageRetention covers the tracker's longest window (92 days).
	laneUsageRetention = 100 * 24 * time.Hour
	laneUsageFlushTick = time.Minute
	laneUsagePlugin    = "estate-lane-usage"
)

// laneNamePattern bounds what a client can make the hub keep: an estate lane
// id such as "ln-1a2b3c4d" or a short name. Anything else is not recorded.
var laneNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// laneUsageTotals are one bucket's counts. Token figures are the canonical,
// non-overlapping buckets of usage.TokenBreakdown, so they can be priced.
type laneUsageTotals struct {
	Requests           int64 `json:"requests"`
	Failed             int64 `json:"failed"`
	InputTokens        int64 `json:"input_tokens"`
	CacheReadTokens    int64 `json:"cache_read_tokens"`
	CacheWriteTokens   int64 `json:"cache_write_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
	UnclassifiedTokens int64 `json:"unclassified_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
}

func (t *laneUsageTotals) add(o laneUsageTotals) {
	t.Requests += o.Requests
	t.Failed += o.Failed
	t.InputTokens += o.InputTokens
	t.CacheReadTokens += o.CacheReadTokens
	t.CacheWriteTokens += o.CacheWriteTokens
	t.OutputTokens += o.OutputTokens
	t.ReasoningTokens += o.ReasoningTokens
	t.UnclassifiedTokens += o.UnclassifiedTokens
	t.TotalTokens += o.TotalTokens
}

type laneUsageKey struct {
	Hour     int64 // Unix seconds at the start of the UTC hour
	Lane     string
	Provider string
	Model    string
}

// laneUsageRow is the persisted form of one hourly bucket.
type laneUsageRow struct {
	Hour     time.Time `json:"hour"`
	Lane     string    `json:"lane"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	laneUsageTotals
}

// laneLedger keeps token usage per lane, provider, model and UTC hour for the
// requests whose client sent usage.LaneHeader. It is a usage plugin.
type laneLedger struct {
	mu      sync.Mutex
	buckets map[laneUsageKey]*laneUsageTotals
	path    string
	dirty   bool
	now     func() time.Time
}

// HandleUsage adds one usage record to its lane's hourly bucket.
func (l *laneLedger) HandleUsage(_ context.Context, record coreusage.Record) {
	lane := strings.TrimSpace(record.Lane)
	if lane == "" || !laneNamePattern.MatchString(lane) {
		return
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = l.clock()
	}
	breakdown := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType).TokenBreakdown
	add := laneUsageTotals{
		Requests:           1,
		InputTokens:        breakdown.Input.UncachedTokens,
		CacheReadTokens:    breakdown.Input.CacheReadTokens,
		CacheWriteTokens:   breakdown.Input.CacheWriteTokens,
		OutputTokens:       breakdown.Output.NonReasoningTokens,
		ReasoningTokens:    breakdown.Output.ReasoningTokens,
		UnclassifiedTokens: breakdown.UnclassifiedTokens,
		TotalTokens:        breakdown.TotalTokens,
	}
	if record.Failed {
		add.Failed = 1
	}
	key := laneUsageKey{
		Hour:     at.UTC().Truncate(time.Hour).Unix(),
		Lane:     lane,
		Provider: strings.TrimSpace(record.Provider),
		Model:    strings.TrimSpace(record.Model),
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = make(map[laneUsageKey]*laneUsageTotals)
	}
	totals := l.buckets[key]
	if totals == nil {
		totals = &laneUsageTotals{}
		l.buckets[key] = totals
	}
	totals.add(add)
	l.dirty = true
}

func (l *laneLedger) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// load reads the persisted ledger, keeping whatever was recorded meanwhile.
func (l *laneLedger) load(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = path
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if !errors.Is(errRead, os.ErrNotExist) {
			log.WithError(errRead).Warn("lane usage: failed to read ledger")
		}
		return
	}
	var rows []laneUsageRow
	if errUnmarshal := json.Unmarshal(data, &rows); errUnmarshal != nil {
		log.WithError(errUnmarshal).Warn("lane usage: ignoring unreadable ledger")
		return
	}
	if l.buckets == nil {
		l.buckets = make(map[laneUsageKey]*laneUsageTotals, len(rows))
	}
	for _, row := range rows {
		key := laneUsageKey{Hour: row.Hour.UTC().Truncate(time.Hour).Unix(), Lane: row.Lane, Provider: row.Provider, Model: row.Model}
		totals := l.buckets[key]
		if totals == nil {
			totals = &laneUsageTotals{}
			l.buckets[key] = totals
		}
		totals.add(row.laneUsageTotals)
	}
}

// flush drops buckets past retention and writes the ledger when it changed.
func (l *laneLedger) flush() {
	l.mu.Lock()
	if !l.dirty || l.path == "" {
		l.mu.Unlock()
		return
	}
	cutoff := l.clock().Add(-laneUsageRetention).Unix()
	rows := make([]laneUsageRow, 0, len(l.buckets))
	for key, totals := range l.buckets {
		if key.Hour < cutoff {
			delete(l.buckets, key)
			continue
		}
		rows = append(rows, laneUsageRow{Hour: time.Unix(key.Hour, 0).UTC(), Lane: key.Lane, Provider: key.Provider, Model: key.Model, laneUsageTotals: *totals})
	}
	l.dirty = false
	path := l.path
	l.mu.Unlock()

	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if !a.Hour.Equal(b.Hour) {
			return a.Hour.Before(b.Hour)
		}
		if a.Lane != b.Lane {
			return a.Lane < b.Lane
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	data, errMarshal := json.Marshal(rows)
	if errMarshal != nil {
		log.WithError(errMarshal).Warn("lane usage: failed to encode ledger")
		return
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		log.WithError(errWrite).Warn("lane usage: failed to write ledger")
		l.markDirty()
		return
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		log.WithError(errRename).Warn("lane usage: failed to replace ledger")
		l.markDirty()
	}
}

func (l *laneLedger) markDirty() {
	l.mu.Lock()
	l.dirty = true
	l.mu.Unlock()
}

// laneUsageModel is one provider and model a lane spent within the window.
type laneUsageModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	laneUsageTotals
}

// laneUsageSummary is one lane's spend within the window.
type laneUsageSummary struct {
	Lane      string    `json:"lane"`
	FirstHour time.Time `json:"first_hour"`
	LastHour  time.Time `json:"last_hour"`
	laneUsageTotals
	Models []laneUsageModel `json:"models"`
}

// summary totals the buckets whose hour starts in [since, until), per lane.
// An empty lanes set selects every lane.
func (l *laneLedger) summary(since, until time.Time, lanes map[string]bool) []laneUsageSummary {
	from, to := since.UTC().Truncate(time.Hour).Unix(), until.Unix()
	type laneAcc struct {
		summary laneUsageSummary
		models  map[[2]string]*laneUsageModel
	}
	acc := make(map[string]*laneAcc)
	l.mu.Lock()
	for key, totals := range l.buckets {
		if key.Hour < from || key.Hour >= to || (len(lanes) > 0 && !lanes[key.Lane]) {
			continue
		}
		hour := time.Unix(key.Hour, 0).UTC()
		a := acc[key.Lane]
		if a == nil {
			a = &laneAcc{summary: laneUsageSummary{Lane: key.Lane, FirstHour: hour, LastHour: hour}, models: make(map[[2]string]*laneUsageModel)}
			acc[key.Lane] = a
		}
		if hour.Before(a.summary.FirstHour) {
			a.summary.FirstHour = hour
		}
		if hour.After(a.summary.LastHour) {
			a.summary.LastHour = hour
		}
		a.summary.add(*totals)
		mk := [2]string{key.Provider, key.Model}
		m := a.models[mk]
		if m == nil {
			m = &laneUsageModel{Provider: key.Provider, Model: key.Model}
			a.models[mk] = m
		}
		m.add(*totals)
	}
	l.mu.Unlock()

	out := make([]laneUsageSummary, 0, len(acc))
	for _, a := range acc {
		a.summary.Models = make([]laneUsageModel, 0, len(a.models))
		for _, m := range a.models {
			a.summary.Models = append(a.summary.Models, *m)
		}
		sort.Slice(a.summary.Models, func(i, j int) bool {
			if a.summary.Models[i].TotalTokens != a.summary.Models[j].TotalTokens {
				return a.summary.Models[i].TotalTokens > a.summary.Models[j].TotalTokens
			}
			return a.summary.Models[i].Provider+"/"+a.summary.Models[i].Model < a.summary.Models[j].Provider+"/"+a.summary.Models[j].Model
		})
		out = append(out, a.summary)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastHour.Equal(out[j].LastHour) {
			return out[i].LastHour.After(out[j].LastHour)
		}
		return out[i].Lane < out[j].Lane
	})
	return out
}

// StartLaneUsage loads the lane ledger from the auth directory, registers it
// as a usage plugin and saves it every minute until ctx ends.
func (h *Handler) StartLaneUsage(ctx context.Context) {
	if h == nil {
		return
	}
	if cfg := h.configSnapshot(); cfg != nil {
		if dir, errDir := util.ResolveAuthDir(cfg.AuthDir); errDir == nil && dir != "" {
			h.lanes.load(filepath.Join(dir, laneUsageName))
		}
	}
	coreusage.RegisterNamedPlugin(laneUsagePlugin, &h.lanes)
	go func() {
		ticker := time.NewTicker(laneUsageFlushTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.lanes.flush()
			}
		}
	}()
}

// FlushLaneUsage saves the lane ledger now (on shutdown).
func (h *Handler) FlushLaneUsage() {
	if h != nil {
		h.lanes.flush()
	}
}

// GetLaneUsage returns token usage per lane for a window:
// GET /v8/management/observability/usage/lanes?since=&until=&lane= where since and until are
// RFC 3339 times or Unix milliseconds (default: the last 24 hours) and lane may
// repeat or hold a comma-separated list. Buckets are whole UTC hours: an hour
// counts when it starts in [since rounded down to the hour, until).
func (h *Handler) GetLaneUsage(c *gin.Context) {
	now := h.lanes.clock()
	until, errUntil := parseLaneUsageTime(c.Query("until"), now)
	since, errSince := parseLaneUsageTime(c.Query("since"), until.Add(-24*time.Hour))
	if errUntil != nil || errSince != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "since and until must be RFC 3339 times or Unix milliseconds"})
		return
	}
	if !since.Before(until) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "since must be before until"})
		return
	}
	lanes := make(map[string]bool)
	for _, value := range c.QueryArray("lane") {
		for _, lane := range strings.Split(value, ",") {
			if lane = strings.TrimSpace(lane); lane != "" {
				lanes[lane] = true
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"since": since.UTC(),
		"until": until.UTC(),
		"lanes": h.lanes.summary(since, until, lanes),
	})
}

func parseLaneUsageTime(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	if ms, errInt := strconv.ParseInt(value, 10, 64); errInt == nil {
		return time.UnixMilli(ms), nil
	}
	return time.Parse(time.RFC3339, value)
}
