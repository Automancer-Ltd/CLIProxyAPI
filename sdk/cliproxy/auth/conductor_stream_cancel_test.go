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
	for range result.Chunks {
	}
	if reading := CreditReadingOf(auth); !reading.Unknown {
		t.Fatal("canceled stream with generated output: reading still known; want unknown")
	}
}
