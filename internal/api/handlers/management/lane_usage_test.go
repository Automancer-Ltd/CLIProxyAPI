package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// The tracker prices a lane from this ledger: tokens must land in the right lane, model and hour,
// survive a hub restart, and be summed only inside the asked window.
func TestLaneLedgerRecordsPersistsAndSummarisesByWindow(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 30, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), laneUsageName)
	ledger := &laneLedger{now: func() time.Time { return now }}
	ledger.load(path)

	claude := func(at time.Time, lane string, input, cacheRead, output int64) coreusage.Record {
		return coreusage.Record{Lane: lane, Provider: "claude", Model: "claude-opus-4-5", RequestedAt: at,
			Detail: coreusage.Detail{InputTokens: input, CacheReadTokens: cacheRead, OutputTokens: output, TotalTokens: input + cacheRead + output}}
	}
	ledger.HandleUsage(nil, claude(now.Add(-3*time.Hour), "ln-aaaaaaaa", 100, 1000, 10)) // 09:30, before the window
	ledger.HandleUsage(nil, claude(now.Add(-time.Hour), "ln-aaaaaaaa", 200, 2000, 20))   // 11:30
	ledger.HandleUsage(nil, claude(now.Add(-50*time.Minute), "ln-aaaaaaaa", 300, 3000, 30))
	ledger.HandleUsage(nil, coreusage.Record{Lane: "ln-aaaaaaaa", Provider: "codex", Model: "gpt-5.5", RequestedAt: now, Failed: true})
	ledger.HandleUsage(nil, claude(now, "ln-bbbbbbbb", 1, 0, 1))
	ledger.HandleUsage(nil, claude(now, "", 5, 0, 5))         // no lane header: not kept
	ledger.HandleUsage(nil, claude(now, "bad lane", 5, 0, 5)) // not a lane name: not kept
	ledger.flush()

	// A restarted hub reads the same figures back.
	h := &Handler{}
	h.lanes.now = func() time.Time { return now }
	h.lanes.load(path)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/usage/lanes?since=2026-11-01T11:10:00Z&lane=ln-aaaaaaaa", nil)
	h.GetLaneUsage(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Lanes []laneUsageSummary `json:"lanes"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &out); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(out.Lanes) != 1 {
		t.Fatalf("lanes = %+v, want only ln-aaaaaaaa", out.Lanes)
	}
	got := out.Lanes[0]
	want := laneUsageTotals{Requests: 3, Failed: 1, InputTokens: 500, CacheReadTokens: 5000, OutputTokens: 50, TotalTokens: 5550}
	if got.Lane != "ln-aaaaaaaa" || got.laneUsageTotals != want {
		t.Fatalf("ln-aaaaaaaa totals = %+v, want %+v", got.laneUsageTotals, want)
	}
	if !got.FirstHour.Equal(time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)) || !got.LastHour.Equal(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("hours = %s..%s, want 11:00..12:00", got.FirstHour, got.LastHour)
	}
	if len(got.Models) != 2 || got.Models[0].Model != "claude-opus-4-5" || got.Models[0].TotalTokens != 5550 || got.Models[1].Failed != 1 {
		t.Fatalf("models = %+v", got.Models)
	}
}
