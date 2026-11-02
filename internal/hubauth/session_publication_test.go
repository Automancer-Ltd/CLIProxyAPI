package hubauth

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHubSessionSurvivesUnrelatedAccountWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, store, _ := setupStores(t)
	codex := loginFixture(t, store, "codex", "probe-a.json")
	claude := loginFixture(t, store, "claude", "probe-b.json")
	check, err := store.OpenHubSession(ctx, codex)
	if err != nil {
		t.Fatal(err)
	}
	var sampled, failed atomic.Int64
	started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		if err := check(ctx); err != nil {
			failed.Add(1)
		}
		sampled.Add(1)
		close(started)
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			default:
			}
			if err := check(ctx); err != nil {
				failed.Add(1)
			}
			sampled.Add(1)
			runtime.Gosched()
		}
	}()
	var once sync.Once
	stopChecks := func() { once.Do(func() { close(stop) }); <-done }
	defer stopChecks()
	<-started
	for i := 0; i < 20; i++ {
		edited := claude.Clone()
		edited.Metadata["priority"] = i + 1
		if _, err := store.Save(ctx, edited); err != nil {
			t.Fatal(err)
		}
		claude = edited
	}
	stopChecks()
	if got := failed.Load(); got != 0 {
		t.Errorf("unrelated priority edits rejected %d of %d frame checks", got, sampled.Load())
	}
	for _, control := range []string{"hold", "unhold", "remove"} {
		if _, err := store.coordinator.Control(ctx, claude.HubAccountID, claude.HubGeneration, control); err != nil {
			t.Fatal(err)
		}
		// No heartbeat or other request has republished the view after this
		// control. The ongoing Codex session must recover its own authorization.
		if err := check(ctx); err != nil {
			t.Errorf("unrelated %s rejected Codex frame: %v", control, err)
		}
	}
	if _, err := store.coordinator.Control(ctx, codex.HubAccountID, codex.HubGeneration, "hold"); err != nil {
		t.Fatal(err)
	}
	if err := check(ctx); err == nil {
		t.Fatal("holding the session's own account still permitted a frame")
	}
}
