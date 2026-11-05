package devin

import (
	"testing"
	"time"
)

// Protobuf omits a zero remaining percent, so a window without a reset in the same
// status was not reported and must not read as 100% used.
func TestQuotaSignalsReportWindowsOnlyWithTheirReset(t *testing.T) {
	reset := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	signals := (&DevinUserStatus{DailyQuotaRemainingPercent: 75, DailyQuotaResetAt: reset}).QuotaSignals()
	if signals["daily_quota_remaining_percent"] != "75%" || signals["daily_quota_reset_at"] != reset.Format(time.RFC3339) {
		t.Fatalf("daily signals = %v", signals)
	}
	if _, ok := signals["weekly_quota_remaining_percent"]; ok {
		t.Fatalf("weekly window without a reset was reported: %v", signals)
	}
}
