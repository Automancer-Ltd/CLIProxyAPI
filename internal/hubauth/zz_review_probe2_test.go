package hubauth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestReviewProbeWindow(t *testing.T) {
	ctx := context.Background()
	_, _, s, _ := setupStores(t)
	a := loginFixture(t, s, "codex", "probe-a.json")
	b := loginFixture(t, s, "claude", "probe-b.json")
	check, err := s.OpenHubSession(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var fails, total atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			total.Add(1)
			if check(ctx) != nil {
				fails.Add(1)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()
	for i := 0; i < 20; i++ {
		edited := b.Clone()
		edited.Metadata["priority"] = i + 1
		if _, err := s.Save(ctx, edited); err != nil {
			t.Fatal(err)
		}
		b = edited
	}
	close(stop)
	<-done
	t.Logf("metadata edits on unrelated account: frame checks failed %d of %d", fails.Load(), total.Load())
	_ = coreauth.StatusActive
}
