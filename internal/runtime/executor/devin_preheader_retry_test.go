package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"google.golang.org/protobuf/encoding/protowire"
)

// The upstream (Devin's server, behind an Envoy-style edge) occasionally
// terminates one connection before sending any response header. The hub used
// to surface that to the client as 503, ending whole agent run segments.
// These tests pin the executor-side retry: the same login is retried a small
// number of times, and only when nothing has been streamed yet.

// devinContentTextResponse builds a minimal valid Connect-RPC response stream:
// one content-text frame plus the end-of-stream marker.
func devinContentTextResponse(text string) []byte {
	var frame []byte
	frame = protowire.AppendTag(frame, 3, protowire.BytesType)
	frame = protowire.AppendString(frame, text)
	var buf []byte
	buf = append(buf, helps.WrapConnectEnvelope(frame)...)
	buf = append(buf, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)
	return buf
}

// hijackClose drops the connection without writing any response byte,
// reproducing "disconnect/reset before headers" at the client.
func hijackClose(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("ResponseWriter does not implement http.Hijacker")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatalf("Hijack failed: %v", err)
	}
	_ = conn.Close()
}

func devinPreHeaderRetryTestServer(t *testing.T, dropCount *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the request body so the client finishes sending before we reset.
		_, _ = io.Copy(io.Discard, r.Body)
		if dropCount.Add(-1) >= 0 {
			hijackClose(t, w)
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(devinContentTextResponse("hello after retry"))
	}))
	t.Cleanup(server.Close)
	return server
}

func devinPreHeaderRetryAuth(server *httptest.Server) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "devin-preheader-retry.json",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
}

func devinPreHeaderRetryRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":20000}`)
	req := cliproxyexecutor.Request{Model: "devin/swe-2-1", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}
	return req, opts
}

func TestDevinPreHeaderResetRetriedOnSameLogin(t *testing.T) {
	// Shrink the production backoff for the fixture; restore after.
	prevBackoff := devinPreHeaderRetryBackoff
	devinPreHeaderRetryBackoff = time.Millisecond
	defer func() { devinPreHeaderRetryBackoff = prevBackoff }()

	var drops atomic.Int32
	drops.Store(2) // first two connections reset before headers, third succeeds
	server := devinPreHeaderRetryTestServer(t, &drops)

	exec := NewDevinExecutor(&config.Config{})
	auth := devinPreHeaderRetryAuth(server)
	req, opts := devinPreHeaderRetryRequest()

	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute failed despite retryable pre-header resets: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty response payload after retry")
	}
	if got := drops.Load(); got != -1 {
		t.Fatalf("request count mismatch: after 2 drops exactly 1 success expected (drops counter = %d, want -1)", got)
	}
}

func TestDevinPreHeaderResetRetryStream(t *testing.T) {
	prevBackoff := devinPreHeaderRetryBackoff
	devinPreHeaderRetryBackoff = time.Millisecond
	defer func() { devinPreHeaderRetryBackoff = prevBackoff }()

	var drops atomic.Int32
	drops.Store(1) // first connection resets before headers, second succeeds
	server := devinPreHeaderRetryTestServer(t, &drops)

	exec := NewDevinExecutor(&config.Config{})
	auth := devinPreHeaderRetryAuth(server)
	req, opts := devinPreHeaderRetryRequest()

	stream, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream failed despite retryable pre-header reset: %v", err)
	}
	if stream == nil {
		t.Fatal("expected non-nil stream result after retry")
	}
	// Drain the stream; nothing may error since the retry happened before any byte streamed.
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
}

func TestDevinPreHeaderResetGivesUpAfterRetries(t *testing.T) {
	prevBackoff := devinPreHeaderRetryBackoff
	devinPreHeaderRetryBackoff = time.Millisecond
	defer func() { devinPreHeaderRetryBackoff = prevBackoff }()

	var drops atomic.Int32
	drops.Store(int32(devinPreHeaderRetryAttempts + 2)) // every attempt resets
	server := devinPreHeaderRetryTestServer(t, &drops)

	exec := NewDevinExecutor(&config.Config{})
	auth := devinPreHeaderRetryAuth(server)
	req, opts := devinPreHeaderRetryRequest()

	_, err := exec.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("expected error when every connection resets before headers")
	}
	attempts := int32(1+devinPreHeaderRetryAttempts) - drops.Load()
	// drops went negative once per served (non-dropped) request; none served here,
	// so total attempts = initial + retries.
	total := int32(devinPreHeaderRetryAttempts+2) - drops.Load()
	if total != int32(1+devinPreHeaderRetryAttempts) {
		t.Fatalf("total upstream attempts = %d, want %d (1 initial + %d retries)", total, 1+devinPreHeaderRetryAttempts, devinPreHeaderRetryAttempts)
	}
	_ = attempts
}

func TestDevinNonResetErrorNotRetried(t *testing.T) {
	prevBackoff := devinPreHeaderRetryBackoff
	devinPreHeaderRetryBackoff = time.Millisecond
	defer func() { devinPreHeaderRetryBackoff = prevBackoff }()

	// A server that answers 500 must not be retried: headers were received.
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	exec := NewDevinExecutor(&config.Config{})
	auth := devinPreHeaderRetryAuth(server)
	req, opts := devinPreHeaderRetryRequest()

	_, err := exec.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1 (no retry once headers arrived)", got)
	}
}

func TestIsDevinPreHeaderResetError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"EOF", io.EOF, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"envoy reset before headers", errors.New("upstream connect error or disconnect/reset before headers. reset reason: connection termination"), true},
		{"connection termination", errors.New("upstream connect error or disconnect/reset before headers. reset reason: connection termination, transport failure reason: remote reset"), true},
		{"op error reset", &net.OpError{Op: "read", Err: errors.New("read tcp: connection reset by peer")}, true},
		{"wrapped reset", fmt.Errorf("round trip: %w", errors.New("write tcp: broken pipe")), true},
		{"plain other error", errors.New("tls handshake failure: certificate expired"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDevinPreHeaderResetError(tc.err); got != tc.want {
				t.Fatalf("isDevinPreHeaderResetError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Ensure the retry path replays the exact same request body (same login, same payload).
func TestDevinPreHeaderRetryReplaysSameBody(t *testing.T) {
	prevBackoff := devinPreHeaderRetryBackoff
	devinPreHeaderRetryBackoff = time.Millisecond
	defer func() { devinPreHeaderRetryBackoff = prevBackoff }()

	var bodies [][]byte
	var drops atomic.Int32
	drops.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		if drops.Add(-1) >= 0 {
			hijackClose(t, w)
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(devinContentTextResponse("hello after retry"))
	}))
	defer server.Close()

	exec := NewDevinExecutor(&config.Config{})
	auth := devinPreHeaderRetryAuth(server)
	req, opts := devinPreHeaderRetryRequest()

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 upstream requests, got %d", len(bodies))
	}
	if len(bodies[0]) == 0 || len(bodies[0]) != len(bodies[1]) {
		t.Fatalf("replayed body length mismatch: first %d bytes, retry %d bytes", len(bodies[0]), len(bodies[1]))
	}
	for i := range bodies[0] {
		if bodies[0][i] != bodies[1][i] {
			t.Fatalf("replayed body differs at byte %d", i)
		}
	}
}
