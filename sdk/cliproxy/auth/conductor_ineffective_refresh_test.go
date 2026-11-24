package auth

import (
	"context"
	"testing"
	"time"
)

func TestIneffectiveRefreshDelayBacksOffToHalfRemaining(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		expiresIn time.Duration
		min       time.Duration
		max       time.Duration
	}{
		{"codex lead", CodexOuterRefreshLead, CodexOuterRefreshLead/2 - time.Minute, CodexOuterRefreshLead/2 + time.Minute},
		{"short lived", 40 * time.Second, refreshIneffectiveBackoff, refreshIneffectiveBackoff},
		{"already expired", -time.Minute, refreshIneffectiveBackoff, refreshIneffectiveBackoff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Auth{Metadata: map[string]any{
				"access_token": "opaque",
				"expires_at":   now.Add(tc.expiresIn).UTC().Format(time.RFC3339),
			}}
			got := ineffectiveRefreshDelay(a, now)
			if got < tc.min || got > tc.max {
				t.Fatalf("ineffectiveRefreshDelay() = %v, want between %v and %v", got, tc.min, tc.max)
			}
		})
	}
	if got := ineffectiveRefreshDelay(nil, now); got != refreshIneffectiveBackoff {
		t.Fatalf("ineffectiveRefreshDelay(nil) = %v, want %v", got, refreshIneffectiveBackoff)
	}
}

func TestSuccessfulIneffectiveRefreshBacksOffBeyondQuietWindow(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	e := &mockOAuthErrorExecutor{id: "codex-ineffective"}
	m.RegisterExecutor(e)
	setRefreshLeadFactory(t, e.id, func() *time.Duration { d := CodexOuterRefreshLead; return &d })

	now := time.Now()
	a := &Auth{
		ID:              "codex-ineffective-auth",
		Provider:        e.id,
		Status:          StatusActive,
		LastRefreshedAt: now.Add(-time.Hour),
		Metadata: map[string]any{
			"access_token":  "opaque",
			"refresh_token": "refresh-1",
			"expires_at":    now.Add(CodexOuterRefreshLead).UTC().Format(time.RFC3339),
		},
	}
	registered, err := m.Register(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	job := m.markRefreshPending(nil, a.ID, registered.RegistrationEpoch, now)
	if job == nil {
		t.Fatal("refresh was not queued")
	}
	if !m.beginRefreshJob(ctx, job) {
		t.Fatal("refresh did not begin")
	}
	if _, err := m.refreshAuthForRequestAtEpoch(ctx, a.ID, "", job.registrationEpoch); err != nil {
		t.Fatal(err)
	}
	m.finishRefreshJob(job, time.Time{}, false)

	m.mu.RLock()
	current := m.auths[a.ID].Clone()
	m.mu.RUnlock()
	delay := current.NextRefreshAfter.Sub(current.LastRefreshedAt)
	if delay < CodexOuterRefreshLead/2-time.Minute || delay > CodexOuterRefreshLead/2+time.Minute {
		t.Fatalf("next refresh in %v, want about half of %v", delay, CodexOuterRefreshLead)
	}
	if delay <= refreshIneffectiveBackoff {
		t.Fatalf("next refresh in %v, want well beyond %v", delay, refreshIneffectiveBackoff)
	}
	if m.shouldRefresh(current, current.LastRefreshedAt.Add(time.Hour)) {
		t.Fatal("credential still refreshes an hour after an ineffective refresh")
	}
}
