package auth

import (
	"context"
	"sync"
	"testing"
)

// Synthetic cold-standby copies: two independent managers start with the same
// grant. The provider fixture records submissions without contacting a provider.
// This probe must fail until host ownership fences one of the managers.
func TestHubOwnershipProbeIndependentManagers(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			executor := &countingRefreshExecutor{id: provider}
			managers := make([]*Manager, 2)
			for i := range managers {
				manager := NewManager(nil, &RoundRobinSelector{}, nil)
				manager.RegisterExecutor(executor)
				_, err := manager.Register(ctx, &Auth{
					ID: "synthetic-shared-login", Provider: provider,
					Metadata: map[string]any{
						"access_token": "fixture-access-generation-1",
						"refresh_token": "fixture-refresh-generation-1",
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				managers[i] = manager
			}
			start := make(chan struct{})
			errs := make(chan error, len(managers))
			var done sync.WaitGroup
			for _, manager := range managers {
				done.Go(func() {
					<-start
					_, err := manager.ForceRefreshAuth(ctx, "synthetic-shared-login")
					errs <- err
				})
			}
			close(start)
			done.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Logf("manager refresh refused: %v", err)
				}
			}
			if calls := executor.refreshCalls.Load(); calls != 1 {
				t.Fatalf("same fixture generation submitted %d times; want one authorized refresher", calls)
			}
		})
	}
}
