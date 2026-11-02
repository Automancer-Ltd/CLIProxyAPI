package hubauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestReviewProbeUnrelatedCommitKillsSession(t *testing.T) {
	ctx := context.Background()
	_, _, s, _ := setupStores(t)
	a := loginFixture(t, s, "codex", "probe-a.json")
	b := loginFixture(t, s, "claude", "probe-b.json")
	check, err := s.OpenHubSession(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := check(ctx); err != nil {
		t.Fatal("baseline", err)
	}
	var failedDuring atomic.Int32
	var sampled atomic.Int32
	s.coordinator.client.http.Transport = fixtureTransport(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		var call struct{ Tool string `json:"tool"` }
		_ = json.Unmarshal(body, &call)
		if call.Tool == "commit" || call.Tool == "operation.get" || call.Tool == "accounts.list" {
			sampled.Add(1)
			if check(ctx) != nil {
				failedDuring.Add(1)
			}
		}
		return http.DefaultTransport.RoundTrip(req)
	})
	// Operator edits an unrelated account's priority (control key).
	edited := b.Clone()
	edited.Metadata["priority"] = 7
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	t.Logf("sampled=%d failed=%d; after=%v", sampled.Load(), failedDuring.Load(), check(ctx))
	if _, err := s.coordinator.Control(ctx, b.HubAccountID, b.HubGeneration, "hold"); err != nil {
		t.Logf("hold err %v", err)
	}
	t.Logf("after hold on unrelated account: check=%v", check(ctx))
	_ = coreauth.StatusActive
}
