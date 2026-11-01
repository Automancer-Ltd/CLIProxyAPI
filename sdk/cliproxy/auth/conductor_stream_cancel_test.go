package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A stream canceled after it generated output still spent credits: wrapStreamResult
// must account that output before it returns, or the guard keeps authorising spend
// against the balance it had before (t10647 review).
func TestCanceledStreamAccountsGeneratedOutput(t *testing.T) {
	now := time.Now()
	auth := &Auth{ID: "canceled-stream", Provider: "codex",
		Metadata: map[string]any{MetadataCreditsFloor: 1000.0},
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "100",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}}}
	t.Cleanup(func() { credits.forget(creditLedgerKey(auth)) })
	m := NewManager(nil, nil, nil)
	m.ObserveCredits(auth, CreditReading{Balance: 5000, HasCredits: true, ReadAt: now})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = internallogging.WithResponseHeadersHolder(ctx)
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Used-Percent", "100")
	headers.Set("X-Codex-Primary-Window-Minutes", "10080")
	headers.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(time.Hour).Unix(), 10))
	internallogging.SetResponseHeaders(ctx, headers)

	remaining := make(chan cliproxyexecutor.StreamChunk, 2)
	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}
	result := m.wrapStreamResult(ctx, auth, "codex", "gpt-test", "gpt-test", nil, nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{}, 1)
	if chunk := <-result.Chunks; len(chunk.Payload) == 0 {
		t.Fatal("first chunk missing")
	}
	if reading := CreditReadingOf(auth); reading.Unknown {
		t.Fatal("before cancellation: reading unknown")
	}
	cancel()
	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.output_text.delta","delta":"hi"}`)}
	// Do not read the output while the canceled delivery is being dropped: a reader would
	// race the context check. Wait for the ledger instead, then drain the closed channel.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !CreditReadingOf(auth).Unknown {
		time.Sleep(5 * time.Millisecond)
	}
	if reading := CreditReadingOf(auth); !reading.Unknown {
		t.Fatal("canceled stream with generated output: reading still known; want unknown")
	}
	for range result.Chunks {
	}
}

// Cached stream headers carry no observation time: a canceled delivery must not stamp
// them afresh and clear a newer socket's used-up window (t10647 review).
func TestCanceledStreamStaleHeadersDoNotClearNewerExhaustion(t *testing.T) {
	now := time.Now()
	auth := &Auth{ID: "canceled-stale-headers", Provider: "codex",
		Metadata: map[string]any{MetadataCreditsFloor: 1000.0},
		Quota: QuotaState{ObservedAt: now.Add(-time.Minute), Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "60",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
		}}}
	t.Cleanup(func() { credits.forget(creditLedgerKey(auth)) })
	m := NewManager(nil, nil, nil)
	m.ObserveCredits(auth, CreditReading{Balance: 900, HasCredits: true, ReadAt: now})

	// Another socket reports the window used up, after this Auth copy was observed.
	exhausted := http.Header{}
	exhausted.Set("X-Codex-Primary-Used-Percent", "100")
	exhausted.Set("X-Codex-Primary-Window-Minutes", "10080")
	exhausted.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(time.Hour).Unix(), 10))
	ObserveStreamQuota(auth, exhausted, now)
	if _, spent := authIncludedSpentUntil(auth, now); !spent {
		t.Fatal("setup: 100% with balance 900 below the floor: served; want refused")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = internallogging.WithResponseHeadersHolder(ctx)
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Used-Percent", "60")
	headers.Set("X-Codex-Primary-Window-Minutes", "10080")
	headers.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10))
	headers.Set("X-Codex-Credits-Balance", "400")
	internallogging.SetResponseHeaders(ctx, headers)

	remaining := make(chan cliproxyexecutor.StreamChunk, 2)
	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}
	result := m.wrapStreamResult(ctx, auth, "codex", "gpt-test", "gpt-test", nil, nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{}, 1)
	if chunk := <-result.Chunks; len(chunk.Payload) == 0 {
		t.Fatal("first chunk missing")
	}
	cancel()
	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.output_text.delta","delta":"hi"}`)}
	// Do not read the output until the cached balance has been applied: a reader would
	// race the cancellation select.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && CreditReadingOf(auth).Balance != 400 {
		time.Sleep(5 * time.Millisecond)
	}
	if _, spent := authIncludedSpentUntil(auth, time.Now()); !spent {
		t.Fatal("canceling an older 60% stream cleared a newer 100%: served; want refused")
	}
	for range result.Chunks {
	}
}
