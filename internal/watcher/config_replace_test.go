package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// A config deployed by rename must keep accepting edits after that deployment.
func TestConfigReloadAfterAtomicReplacement(t *testing.T) {
	path, authDir := newIssue6631ConfigFile(t)
	if err := writeIssue6631Config(path, authDir, 1); err != nil {
		t.Fatal(err)
	}
	applied := make(chan int, 10)
	w, err := NewWatcher(path, authDir, func(c *config.Config) { applied <- c.RequestRetry })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	w.SetConfig(&config.Config{AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	await := func(want int) {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case got := <-applied:
				if got == want {
					return
				}
			case <-deadline.C:
				t.Fatalf("config edit %d was never applied", want)
			}
		}
	}
	await(0)
	for _, n := range []int{2, 3} {
		staged := filepath.Join(filepath.Dir(path), "config.staged")
		if err = writeIssue6631Config(staged, authDir, n); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(staged, path); err != nil {
			t.Fatal(err)
		}
		await(n)
	}
	if err = writeIssue6631Config(path, authDir, 4); err != nil {
		t.Fatal(err)
	}
	await(4)
}
