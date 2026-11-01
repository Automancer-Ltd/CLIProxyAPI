package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// claudeQuotaAuth builds a Claude credential whose last response reported the given
// weekly (7d) and five-hour utilization (0-1) and resets.
func claudeQuotaAuth(id string, weeklyUsed float64, weeklyReset time.Duration, fiveHourUsed float64) *Auth {
	now := time.Now()
	return &Auth{ID: id, Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Utilization": strconv.FormatFloat(weeklyUsed, 'f', -1, 64),
		"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(now.Add(weeklyReset).Unix(), 10),
		"Anthropic-Ratelimit-Unified-5h-Utilization": strconv.FormatFloat(fiveHourUsed, 'f', -1, 64),
		"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10),
	}}}
}

func TestConsumeFirstSelectorRanksBySoonestWeeklyReset(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	now := time.Now()

	tests := []struct {
		name  string
		auths []*Auth
		want  string
	}{
		{
			name:  "sooner weekly reset wins over more headroom",
			auths: []*Auth{claudeQuotaAuth("a-late", 0.2, 5*day, 0.1), claudeQuotaAuth("b-soon", 0.6, day, 0.1)},
			want:  "b-soon",
		},
		{
			name:  "spent weekly window sorts last",
			auths: []*Auth{claudeQuotaAuth("a-late", 0.2, 5*day, 0.1), claudeQuotaAuth("b-spent", 0.97, day, 0.1)},
			want:  "a-late",
		},
		{
			name:  "spent five-hour window sorts last",
			auths: []*Auth{claudeQuotaAuth("a-late", 0.2, 5*day, 0.1), claudeQuotaAuth("b-busy", 0.5, day, 0.96)},
			want:  "a-late",
		},
		{
			name:  "unknown reset sorts after known reset",
			auths: []*Auth{{ID: "a-unknown", Provider: "claude"}, claudeQuotaAuth("b-known", 0.5, 6*day, 0.1)},
			want:  "b-known",
		},
		{
			name:  "window that has not started counts as unknown",
			auths: []*Auth{claudeQuotaAuth("a-unstarted", 0, 7*day, 0), claudeQuotaAuth("b-known", 0.5, 6*day, 0.1)},
			want:  "b-known",
		},
		{
			name:  "reset already passed counts as unknown",
			auths: []*Auth{claudeQuotaAuth("a-stale", 0.5, -time.Hour, 0.1), claudeQuotaAuth("b-known", 0.5, 6*day, 0.1)},
			want:  "b-known",
		},
		{
			name:  "no reset data anywhere falls back to ID order",
			auths: []*Auth{{ID: "b", Provider: "gemini"}, {ID: "a", Provider: "gemini"}},
			want:  "a",
		},
		{
			name: "codex weekly reset relative to the observation",
			auths: []*Auth{
				{ID: "a-codex-late", Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
					"X-Codex-Primary-Used-Percent": "10", "X-Codex-Primary-Window-Minutes": "10080",
					"X-Codex-Primary-Reset-After-Seconds": strconv.Itoa(int((5 * day).Seconds())),
				}}},
				{ID: "b-codex-soon", Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
					"X-Codex-Primary-Used-Percent": "40", "X-Codex-Primary-Window-Minutes": "300",
					"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
					"X-Codex-Secondary-Used-Percent": "70", "X-Codex-Secondary-Window-Minutes": "10080",
					"X-Codex-Secondary-Reset-At": strconv.FormatInt(now.Add(2*day).Unix(), 10),
				}}},
			},
			want: "b-codex-soon",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, errPick := (&ConsumeFirstSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, tt.auths)
			if errPick != nil {
				t.Fatalf("Pick() error = %v", errPick)
			}
			if got.ID != tt.want {
				t.Fatalf("Pick() = %q, want %q", got.ID, tt.want)
			}
		})
	}
}

func TestConsumeFirstSelectorKeepsSessionOnItsAccount(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	selector := NewSessionAffinitySelector(&ConsumeFirstSelector{})
	defer selector.Stop()
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	first, errPick := selector.Pick(context.Background(), "claude", "claude-x", opts, []*Auth{
		claudeQuotaAuth("a", 0.5, 2*day, 0.1), claudeQuotaAuth("b", 0.5, 4*day, 0.1),
	})
	if errPick != nil || first.ID != "a" {
		t.Fatalf("first Pick() = %v, %v; want a", first, errPick)
	}
	// b now resets sooner, but the bound session must stay on a for its cache.
	again, errPick := selector.Pick(context.Background(), "claude", "claude-x", opts, []*Auth{
		claudeQuotaAuth("a", 0.6, 2*day, 0.1), claudeQuotaAuth("b", 0.5, day, 0.1),
	})
	if errPick != nil || again.ID != "a" {
		t.Fatalf("second Pick() = %v, %v; want the bound account a", again, errPick)
	}
}

func TestAccountPinServesOrRefusesNamedAccount(t *testing.T) {
	const model = "claude-pin-test-model"
	now := time.Now()
	team := &Auth{ID: "pin-team", Provider: "claude", Metadata: map[string]any{MetadataGroup: "team, pro", "email": "team@example.com"}}
	held := &Auth{ID: "pin-held", Provider: "claude", Metadata: map[string]any{MetadataHoldUntil: now.Add(3 * time.Hour).UTC().Format(time.RFC3339)}}
	cooling := &Auth{ID: "pin-cooling", Provider: "claude", Unavailable: true, NextRetryAfter: now.Add(time.Hour),
		Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}}

	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.RegisterExecutor(&authFallbackExecutor{id: "claude"})
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*Auth{team, held, cooling} {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	execute := func(pin string) (string, error) {
		opts := cliproxyexecutor.Options{}
		if pin != "" {
			opts.Metadata = map[string]any{cliproxyexecutor.AccountPinMetadataKey: pin}
		}
		resp, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
		return string(resp.Payload), errExec
	}

	for _, pin := range []string{"", "TEAM", "pro", "team@example.com", "pin-team"} {
		if got, errExec := execute(pin); errExec != nil || got != team.ID {
			t.Fatalf("Execute(pin=%q) = %q, %v; want %q (held and cooling accounts are skipped)", pin, got, errExec, team.ID)
		}
	}

	for _, tc := range []struct {
		pin, wantText string
		wantStatus    int
	}{
		{pin: "pin-held", wantText: "held until", wantStatus: http.StatusTooManyRequests},
		{pin: "pin-cooling", wantText: "at its limit until", wantStatus: http.StatusTooManyRequests},
		{pin: "nobody", wantText: "no credential matches account", wantStatus: http.StatusBadRequest},
	} {
		_, errExec := execute(tc.pin)
		var refusal *accountPinError
		if !errors.As(errExec, &refusal) {
			t.Fatalf("Execute(pin=%q) error = %v, want an account pin refusal", tc.pin, errExec)
		}
		if refusal.StatusCode() != tc.wantStatus || !strings.Contains(refusal.Error(), tc.wantText) {
			t.Fatalf("Execute(pin=%q) = %d %q, want %d containing %q", tc.pin, refusal.StatusCode(), refusal.Error(), tc.wantStatus, tc.wantText)
		}
		if tc.wantStatus == http.StatusTooManyRequests && refusal.Headers().Get("Retry-After") == "" {
			t.Fatalf("Execute(pin=%q) refusal has no Retry-After header", tc.pin)
		}
	}
}

func TestPinOnlyAccountServesPinsAndNeverUnpinnedTraffic(t *testing.T) {
	const model = "claude-pin-only-test-model"
	const day = 24 * time.Hour
	now := time.Now()
	// The pin-only account resets soonest, so consume-first would pick it first.
	team := claudeQuotaAuth("pin-only-team", 0.1, day, 0.1)
	team.Metadata = map[string]any{MetadataGroup: "team", MetadataPinOnly: true}
	pro := claudeQuotaAuth("pin-only-pro", 0.1, 5*day, 0.1)

	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.RegisterExecutor(&authFallbackExecutor{id: "claude"})
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*Auth{team, pro} {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	execute := func(pin string) (string, error) {
		opts := cliproxyexecutor.Options{}
		if pin != "" {
			opts.Metadata = map[string]any{cliproxyexecutor.AccountPinMetadataKey: pin}
		}
		resp, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
		return string(resp.Payload), errExec
	}

	if got, errExec := execute(""); errExec != nil || got != pro.ID {
		t.Fatalf("unpinned Execute = %q, %v; want %q (pin-only accounts are skipped)", got, errExec, pro.ID)
	}
	if got, errExec := execute("team"); errExec != nil || got != team.ID {
		t.Fatalf("Execute(pin=team) = %q, %v; want %q", got, errExec, team.ID)
	}

	// With every other account unusable, unpinned traffic is refused rather than
	// falling through to the pin-only account.
	pro.Disabled = true
	if _, errUpdate := m.Update(context.Background(), pro); errUpdate != nil {
		t.Fatalf("update pro: %v", errUpdate)
	}
	if got, errExec := execute(""); errExec == nil {
		t.Fatalf("unpinned Execute = %q with only a pin-only account left; want an error", got)
	}
	if got, errExec := execute("team"); errExec != nil || got != team.ID {
		t.Fatalf("Execute(pin=team) = %q, %v; want %q", got, errExec, team.ID)
	}

	// A hold still applies to a pin-only account.
	team.Metadata[MetadataHoldUntil] = now.Add(time.Hour).UTC().Format(time.RFC3339)
	if _, errUpdate := m.Update(context.Background(), team); errUpdate != nil {
		t.Fatalf("update team: %v", errUpdate)
	}
	_, errExec := execute("team")
	var refusal *accountPinError
	if !errors.As(errExec, &refusal) || refusal.StatusCode() != http.StatusTooManyRequests || !strings.Contains(refusal.Error(), "held until") {
		t.Fatalf("Execute(pin=team) on a held pin-only account = %v; want 429 held until", errExec)
	}
}

// A Codex account whose weekly window reads 100% keeps answering when its workspace
// has credits, and each answer is billed (t9765). The hub must stop routing to it,
// pinned or not, until the window resets or allow_credits is set.
func TestSpentIncludedAllowanceRefusesPaidCredits(t *testing.T) {
	const model = "codex-credits-guard-test-model"
	now := time.Now()
	codexAuth := func(id, group string, used int) *Auth {
		return &Auth{ID: id, Provider: "codex", Metadata: map[string]any{MetadataGroup: group},
			Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
				"X-Codex-Primary-Used-Percent":   strconv.Itoa(used),
				"X-Codex-Primary-Window-Minutes": "10080",
				"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
				"X-Codex-Credits-Has-Credits":    "True",
			}}}
	}
	// The spent account resets sooner, so consume-first alone would still use it last-resort.
	team := codexAuth("credits-team", "team", 100)
	pro := codexAuth("credits-pro", "pro", 50)
	pro.Quota.Signals["X-Codex-Primary-Reset-At"] = strconv.FormatInt(now.Add(72*time.Hour).Unix(), 10)

	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.RegisterExecutor(&authFallbackExecutor{id: "codex"})
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*Auth{team, pro} {
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	execute := func(pin string) (string, error) {
		opts := cliproxyexecutor.Options{}
		if pin != "" {
			opts.Metadata = map[string]any{cliproxyexecutor.AccountPinMetadataKey: pin}
		}
		resp, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
		return string(resp.Payload), errExec
	}

	if got, errExec := execute(""); errExec != nil || got != pro.ID {
		t.Fatalf("unpinned Execute = %q, %v; want %q (spent account skipped)", got, errExec, pro.ID)
	}
	_, errExec := execute("team")
	var refusal *accountPinError
	if !errors.As(errExec, &refusal) || refusal.StatusCode() != http.StatusTooManyRequests || !strings.Contains(refusal.Error(), "paid credits refused") {
		t.Fatalf("Execute(pin=team) on a spent account = %v; want 429 paid credits refused", errExec)
	}

	team.Metadata[MetadataAllowCredits] = true
	if _, errUpdate := m.Update(context.Background(), team); errUpdate != nil {
		t.Fatalf("update team: %v", errUpdate)
	}
	if got, errExec := execute("team"); errExec != nil || got != team.ID {
		t.Fatalf("Execute(pin=team) with allow_credits = %q, %v; want %q", got, errExec, team.ID)
	}
}

// A Pro account's granted credits may be spent past its included window, but only
// down to credits_floor, and only on a recent poll reading: at or below the floor,
// with no credits, or with a stale or missing reading, the hub refuses it as before,
// so it never spends into bought credits (t10647).
func TestCreditsFloorSpendsGrantedCreditsDownToTheFloor(t *testing.T) {
	const model = "codex-credits-floor-test-model"
	now := time.Now()
	spent := &Auth{ID: "floor-pro", Provider: "codex", Metadata: map[string]any{MetadataGroup: "pro", MetadataCreditsFloor: json.Number("1000")},
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "100",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
			// Traffic headers are not trusted for the floor.
			"X-Codex-Credits-Has-Credits": "True",
			"X-Codex-Credits-Balance":     "50000",
		}}}

	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.RegisterExecutor(newCreditHeadersExecutor(now))
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(spent.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(spent.ID) })
	t.Cleanup(func() { credits.forget(spent.ID) })
	if _, errRegister := m.Register(context.Background(), spent); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	served := func() bool {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.AccountPinMetadataKey: "pro"}}
		_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
		var refusal *accountPinError
		if errExec != nil && (!errors.As(errExec, &refusal) || !strings.Contains(refusal.Error(), "paid credits refused")) {
			t.Fatalf("Execute = %v; want served or paid credits refused", errExec)
		}
		return errExec == nil
	}
	if served() {
		t.Fatal("no poll reading: served; want refused")
	}

	steps := []struct {
		name    string
		reading CreditReading
		applied bool
		want    bool
	}{
		{"stale reading above the floor", CreditReading{Balance: 1500, HasCredits: true, ReadAt: now.Add(-CreditReadingMaxAge - time.Minute)}, true, false},
		{"recent reading above the floor", CreditReading{Balance: 1500, HasCredits: true, ReadAt: now.Add(-time.Minute)}, true, true},
		{"older reading is ignored", CreditReading{Balance: 900, HasCredits: true, ReadAt: now.Add(-2 * time.Minute)}, false, true},
		{"newer reading below the floor", CreditReading{Balance: 900, HasCredits: true, ReadAt: now.Add(-30 * time.Second)}, true, false},
		{"at the floor", CreditReading{Balance: 1000, HasCredits: true, ReadAt: now.Add(-20 * time.Second)}, true, false},
		{"unlimited credits", CreditReading{Balance: 5000, HasCredits: true, Unlimited: true, ReadAt: now.Add(-10 * time.Second)}, true, false},
		{"no credits", CreditReading{Balance: 5000, ReadAt: now.Add(-5 * time.Second)}, true, false},
	}
	for _, step := range steps {
		if applied := m.ObserveCredits(spent.ID, step.reading); applied != step.applied {
			t.Fatalf("%s: ObserveCredits applied = %v; want %v", step.name, applied, step.applied)
		}
		if got := served(); got != step.want {
			t.Fatalf("%s: served = %v; want %v", step.name, got, step.want)
		}
	}

	// Traffic only lowers the reading until a newer poll.
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: now})
	respond := func(balance string) {
		headers := http.Header{}
		headers.Set("X-Codex-Primary-Used-Percent", "100")
		headers.Set("X-Codex-Primary-Window-Minutes", "10080")
		headers.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10))
		if balance != "" {
			headers.Set("X-Codex-Credits-Balance", balance)
		}
		ctx := internallogging.WithResponseHeadersHolder(context.Background())
		internallogging.SetResponseHeaders(ctx, headers)
		m.MarkResult(ctx, Result{AuthID: spent.ID, Provider: "codex", Model: model, Success: true})
	}
	respond("1200")
	respond("1400") // a delayed, higher figure is ignored
	if !served() {
		t.Fatal("response reporting 1200: refused; want served")
	}
	respond("")
	if served() {
		t.Fatal("response on credits without a balance: served; want refused until a newer poll")
	}
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: time.Now().Add(time.Millisecond)})
	if !served() {
		t.Fatal("newer poll 1500: refused; want served")
	}
	// A poll read before a later response cannot raise the balance that response reported.
	pollStarted := time.Now()
	time.Sleep(2 * time.Millisecond)
	respond("950")
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: pollStarted})
	if served() {
		t.Fatal("overlapping poll 1500 after a response reporting 950: served; want refused")
	}
	// Each response of unknown cost moves the reading on, so a poll that started
	// between two of them cannot clear it.
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: time.Now().Add(time.Millisecond)})
	time.Sleep(2 * time.Millisecond)
	respond("")
	pollStarted = time.Now()
	time.Sleep(2 * time.Millisecond)
	respond("")
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: pollStarted})
	if served() {
		t.Fatal("poll started between two responses of unknown cost: served; want refused")
	}
}

// Round-robin and fill-first pick from the scheduler's snapshots, so a poll reading
// must reach them too (t10647 review).
func TestCreditReadingReachesSchedulerStrategies(t *testing.T) {
	const model = "codex-credits-scheduler-test-model"
	now := time.Now()
	spent := &Auth{ID: "floor-rr", Provider: "codex", Metadata: map[string]any{MetadataCreditsFloor: 1000.0},
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "100",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
		}}}
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.RegisterExecutor(newCreditHeadersExecutor(now))
	reg := registry.GetGlobalRegistry()
	const otherModel = "codex-credits-scheduler-other-model"
	reg.RegisterClient(spent.ID, "codex", []*registry.ModelInfo{{ID: model}, {ID: otherModel}})
	t.Cleanup(func() { reg.UnregisterClient(spent.ID) })
	t.Cleanup(func() { credits.forget(spent.ID) })
	if _, errRegister := m.Register(context.Background(), spent); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	m.RefreshSchedulerEntry(spent.ID)
	execute := func() error {
		_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		return errExec
	}
	if execute() == nil {
		t.Fatal("no reading: served; want refused")
	}
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: now})
	if errExec := execute(); errExec != nil {
		t.Fatalf("reading 1500: Execute = %v; want served", errExec)
	}
	// A snapshot taken before a newer poll (a delayed result) must not overwrite it.
	current, _ := m.GetByID(spent.ID)
	delayed := current.Clone()
	m.ObserveCredits(spent.ID, CreditReading{Balance: 900, HasCredits: true, ReadAt: now.Add(time.Millisecond)})
	m.scheduler.upsertAuthResult(delayed, nil, true)
	if execute() == nil {
		t.Fatal("delayed snapshot with 1500 after a poll read 900: served; want refused")
	}
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: now.Add(2 * time.Millisecond)})
	if errExec := execute(); errExec != nil {
		t.Fatalf("newer reading 1500: Execute = %v; want served", errExec)
	}
	// A credential edit that started from an earlier copy keeps the newer reading.
	edit, _ := m.GetByID(spent.ID)
	m.ObserveCredits(spent.ID, CreditReading{Balance: 900, HasCredits: true, ReadAt: now.Add(3 * time.Millisecond)})
	edit.Metadata[MetadataGroup] = "edited"
	if _, errUpdate := m.Update(context.Background(), edit); errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}
	if got := CreditReadingOf(spent.ID).Balance; got != 900 {
		t.Fatalf("after an edit from an earlier copy: balance = %v; want 900", got)
	}
	if execute() == nil {
		t.Fatal("edit from an earlier copy restored 1500: served; want refused")
	}
	// A poll that lowers the balance refuses every model, including one that has
	// already been served from its own scheduler snapshot.
	executeModel := func(name string) error {
		_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: name}, cliproxyexecutor.Options{})
		return errExec
	}
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: now.Add(4 * time.Millisecond)})
	if executeModel(model) != nil || executeModel(otherModel) != nil {
		t.Fatal("reading 1500: a model was refused; want both served")
	}
	m.ObserveCredits(spent.ID, CreditReading{Balance: 900, HasCredits: true, ReadAt: now.Add(5 * time.Millisecond)})
	if executeModel(model) == nil || executeModel(otherModel) == nil {
		t.Fatal("poll 900: a model was served; want both refused")
	}
	m.ObserveCredits(spent.ID, CreditReading{Balance: 1500, HasCredits: true, ReadAt: now.Add(6 * time.Millisecond)})

	// A response on another model lowers the balance for every model.
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Used-Percent", "100")
	headers.Set("X-Codex-Primary-Window-Minutes", "10080")
	headers.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10))
	headers.Set("X-Codex-Credits-Balance", "950")
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, headers)
	m.MarkResult(ctx, Result{AuthID: spent.ID, Provider: "codex", Model: otherModel, Success: true})
	if execute() == nil {
		t.Fatal("balance 950 reported on another model: served; want refused")
	}
}

// An account served only on credits ranks after a spent account that still has
// included allowance, even when it resets sooner (t10647 review).
func TestConsumeFirstRanksCreditFundedAccountsLast(t *testing.T) {
	now := time.Now()
	codex := func(used string, reset time.Duration) *Auth {
		return &Auth{Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   used,
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(reset).Unix(), 10),
		}}}
	}
	onCredits := consumeFirstRankOf(codex("100", time.Hour), now)
	nearlySpent := consumeFirstRankOf(codex("96", 72*time.Hour), now)
	if !nearlySpent.before(onCredits) || onCredits.before(nearlySpent) {
		t.Fatalf("rank 96%% (resets in 3d) = %+v should come before 100%% (resets in 1h) = %+v", nearlySpent, onCredits)
	}
}

// creditHeadersExecutor answers like a Codex account past its included allowance that
// reports a large credit balance, so the requests a test makes leave the reading alone.
type creditHeadersExecutor struct {
	*authFallbackExecutor
	headers http.Header
}

func newCreditHeadersExecutor(now time.Time) *creditHeadersExecutor {
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Used-Percent", "100")
	headers.Set("X-Codex-Primary-Window-Minutes", "10080")
	headers.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10))
	headers.Set("X-Codex-Credits-Balance", "1000000")
	return &creditHeadersExecutor{authFallbackExecutor: &authFallbackExecutor{id: "codex"}, headers: headers}
}

func (e *creditHeadersExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	internallogging.SetResponseHeaders(ctx, e.headers.Clone())
	return e.authFallbackExecutor.Execute(ctx, auth, req, opts)
}
