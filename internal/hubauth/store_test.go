package hubauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type providerFixture struct {
	provider string
	refresh  func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)
	served   atomic.Int32
}

func (e *providerFixture) Identifier() string { return e.provider }
func (e *providerFixture) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return e.refresh(ctx, a)
}
func (e *providerFixture) HttpRequest(_ context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	e.served.Add(1)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (e *providerFixture) Execute(context.Context, *coreauth.Auth, runtimeexecutor.Request, runtimeexecutor.Options) (runtimeexecutor.Response, error) {
	e.served.Add(1)
	return runtimeexecutor.Response{}, nil
}
func (e *providerFixture) CountTokens(context.Context, *coreauth.Auth, runtimeexecutor.Request, runtimeexecutor.Options) (runtimeexecutor.Response, error) {
	e.served.Add(1)
	return runtimeexecutor.Response{}, nil
}
func (e *providerFixture) ExecuteStream(context.Context, *coreauth.Auth, runtimeexecutor.Request, runtimeexecutor.Options) (*runtimeexecutor.StreamResult, error) {
	e.served.Add(1)
	return &runtimeexecutor.StreamResult{}, nil
}

func setupStores(t *testing.T) (*authorityFixture, *memoryBlobs, *Store, *Store) {
	t.Helper()
	f := newAuthorityFixture()
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	b := &memoryBlobs{data: map[string][]byte{}}
	primary := NewStore(fixtureCoordinator(t, server.URL, "m6", b))
	standby := NewStore(fixtureCoordinator(t, server.URL, "auto-vps", b))
	if err := primary.coordinator.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f, b, primary, standby
}
func loginFixture(t *testing.T, s *Store, provider, id string) *coreauth.Auth {
	t.Helper()
	a := &coreauth.Auth{ID: id, Provider: provider, Metadata: map[string]any{"refresh_token": "fixture-secret-" + id, "access_token": "fixture-secret-access", "expired": "2000-01-01T00:00:00Z", "nested_provider_grant": map[string]any{"opaque": "fixture-secret-unknown"}}, Status: coreauth.StatusActive}
	if _, err := s.Save(coreauth.WithAuthCreationIntent(context.Background()), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPublicManagerPartitionRestartAndMetadata(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			f, _, primary, standby := setupStores(t)
			a := loginFixture(t, primary, provider, "shared.json")
			first := coreauth.NewManager(primary, nil, nil)
			if err := first.Load(ctx); err != nil {
				t.Fatal(err)
			}
			calls := atomic.Int32{}
			entered, finish := make(chan struct{}), make(chan struct{})
			exec := &providerFixture{provider: provider, refresh: func(_ context.Context, b *coreauth.Auth) (*coreauth.Auth, error) {
				calls.Add(1)
				close(entered)
				<-finish
				b.Metadata["refresh_token"] = "fixture-secret-rotated"
				b.Metadata["access_token"] = "fixture-secret-new-access"
				return b, nil
			}}
			first.RegisterExecutor(exec)
			complete := make(chan error, 1)
			go func() { _, err := first.ForceRefreshAuth(ctx, a.ID); complete <- err }()
			<-entered
			// Watcher changes are advisory. Both hub processes remain alive.
			f.mu.Lock()
			f.state.Ownership.Live = false
			f.mu.Unlock()
			if err := standby.coordinator.Step(ctx); err != nil {
				t.Fatal(err)
			}
			second := coreauth.NewManager(standby, nil, nil)
			if err := second.Load(ctx); err != nil {
				t.Fatal(err)
			}
			second.RegisterExecutor(exec)
			if _, err := second.ForceRefreshAuth(ctx, a.ID); err == nil {
				t.Fatal("standby refreshed quarantined generation")
			}
			req, _ := http.NewRequest("GET", "https://fixture.invalid/", nil)
			if _, err := first.HttpRequest(ctx, a, req); err == nil {
				t.Fatal("old process served after takeover")
			}
			current, _ := second.GetByID(a.ID)
			current.Metadata["group"] = "edited-during-refresh"
			current.Metadata["hold_until"] = "2000-01-01T00:00:00Z"
			if _, err := second.Update(ctx, current); err != nil {
				t.Fatal(err)
			}
			if _, err := standby.coordinator.Control(ctx, a.HubAccountID, 1, "hold"); err != nil {
				t.Fatal(err)
			}
			other := loginFixture(t, standby, provider, "other.json")
			if err := standby.Authorize(ctx, other); err != nil {
				t.Fatal("unrelated account stopped", err)
			}
			close(finish)
			if err := <-complete; err == nil {
				t.Fatal("expired process committed")
			}
			if err := standby.coordinator.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			records, controls, err := standby.coordinator.Current(ctx)
			if err != nil {
				t.Fatal(err)
			}
			data, err := decodeCredential(records[a.HubAccountID].Data)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || exec.served.Load() != 0 || controls[a.HubAccountID].Status != "held" || data.Metadata["group"] != "edited-during-refresh" || data.Metadata["refresh_token"] != "fixture-secret-rotated" {
				t.Fatal("duplicate call or lost grant/control")
			}
			restarted := NewStore(fixtureCoordinator(t, primary.coordinator.client.endpoint, "m6", primary.coordinator.objects.blobs.(*memoryBlobs)))
			if err := restarted.coordinator.Step(ctx); err == nil {
				t.Fatal("restart stole epoch")
			}
			if err := standby.coordinator.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if err := restarted.coordinator.Step(ctx); err != nil {
				t.Fatal(err)
			}
			recovered, err := restarted.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range recovered {
				if a.ID == "shared.json" && (!a.Disabled || a.HubGeneration != 2 || a.Metadata["refresh_token"] != "fixture-secret-rotated") {
					t.Fatal("restart served old snapshot")
				}
			}
		})
	}
}

func TestStoreRejectsUnknownGrantChangesAndPreservesQuarantine(t *testing.T) {
	ctx := context.Background()
	_, _, s, _ := setupStores(t)
	a := loginFixture(t, s, "codex", "a.json")
	_, err := s.Refresh(ctx, a, func(_ context.Context, b *coreauth.Auth) (*coreauth.Auth, error) {
		return nil, errors.New("ambiguous fixture")
	})
	if err == nil {
		t.Fatal("ambiguous refresh accepted")
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a = list[0]
	a.Metadata["nested_provider_grant"] = map[string]any{"opaque": "changed"}
	if _, err = s.Save(ctx, a); err == nil {
		t.Fatal("unknown credential change mislabeled metadata")
	}
	list, _ = s.List(ctx)
	a = list[0]
	a.Metadata["group"] = "changed"
	if _, err = s.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	_, accounts, err := s.coordinator.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[a.HubAccountID].Generation != 1 || !accounts[a.HubAccountID].Quarantined {
		t.Fatal("metadata cleared unresolved intent")
	}
}

func TestFailedHeadPublicationKeepsSingleProviderResult(t *testing.T) {
	ctx := context.Background()
	_, b, s, _ := setupStores(t)
	a := loginFixture(t, s, "codex", "a.json")
	calls := 0
	// Let the result upload succeed, then make the complete-head upload fail.
	b.failHeads = true
	_, err := s.Refresh(ctx, a, func(_ context.Context, b *coreauth.Auth) (*coreauth.Auth, error) {
		calls++
		b.Metadata["refresh_token"] = "fixture-secret-rotated"
		return b, nil
	})
	if err == nil {
		t.Fatal("failed head acknowledged")
	}
	if _, err = s.Refresh(ctx, a, func(context.Context, *coreauth.Auth) (*coreauth.Auth, error) { calls++; return nil, nil }); err == nil {
		t.Fatal("failed copyback retried provider")
	}
	b.failHeads = false
	if err = s.coordinator.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || current[0].HubGeneration != 2 || current[0].Metadata["refresh_token"] != "fixture-secret-rotated" {
		t.Fatal("failed handback lost or repeated rotation")
	}
}

func TestPartialResultPreservedWithoutClearingIntent(t *testing.T) {
	ctx := context.Background()
	f, b, s, _ := setupStores(t)
	a := loginFixture(t, s, "codex", "a.json")
	_, err := s.Refresh(ctx, a, func(_ context.Context, b *coreauth.Auth) (*coreauth.Auth, error) {
		b.Metadata["refresh_token"] = "fixture-secret-partial"
		return b, errors.New("partial ambiguous result")
	})
	if err == nil {
		t.Fatal("partial refresh accepted")
	}
	if err = s.coordinator.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Authorize(ctx, a); !errors.Is(err, ErrQuarantined) {
		t.Fatal("partial result unquarantined", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, i := range f.intents {
		raw, _, err := s.coordinator.objects.ReadResult(ctx, i.ResultKey)
		if err != nil {
			t.Fatal(err)
		}
		var r result
		if json.Unmarshal(raw, &r) != nil || r.Complete || !strings.Contains(string(r.Data), "fixture-secret-partial") {
			t.Fatal("partial bytes not retained")
		}
	}
	if len(b.data) < 2 {
		t.Fatal("result missing")
	}
}

func TestDuplicateGrantAndUnenrolledModeCannotAcquirePermission(t *testing.T) {
	ctx := context.Background()
	f, _, s, _ := setupStores(t)
	a := loginFixture(t, s, "codex", "a.json")
	duplicate := a.Clone()
	duplicate.ID = "renamed.json"
	duplicate.HubAccountID = ""
	duplicate.HubGeneration = 0
	if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), duplicate); err == nil {
		t.Fatal("copied grant gained a second account")
	}
	a.Metadata["siwc"] = map[string]any{"refresh_token": "fixture-secret-siwc"}
	calls := 0
	if _, err := s.Refresh(ctx, a, func(context.Context, *coreauth.Auth) (*coreauth.Auth, error) { calls++; return nil, nil }); err == nil {
		t.Fatal("dual mode accepted")
	}
	if calls != 0 || len(f.intents) != 0 {
		t.Fatal("unenrolled mode reached provider intent")
	}
}

func TestManagementWritesUseEncryptedHeadAndNeverCopiedFiles(t *testing.T) {
	ctx := context.Background()
	_, _, s, _ := setupStores(t)
	a := loginFixture(t, s, "codex", "fixture.json")
	manager := coreauth.NewManager(s, nil, nil)
	if err := manager.Load(ctx); err != nil {
		t.Fatal(err)
	}
	oldStore := sdkauth.GetTokenStore()
	sdkauth.RegisterTokenStore(s)
	t.Cleanup(func() { sdkauth.RegisterTokenStore(oldStore) })
	dir := t.TempDir()
	// A stale copied file must neither be ingested nor returned by download.
	stale := []byte(`{"type":"codex","refresh_token":"fixture-secret-stale-disk"}`)
	if err := os.WriteFile(filepath.Join(dir, a.ID), stale, 0600); err != nil {
		t.Fatal(err)
	}
	handler := management.NewHandler(&config.Config{AuthDir: dir}, "", manager)
	router := gin.New()
	router.PATCH("/fields", handler.PatchAuthFileFields)
	router.PATCH("/status", handler.PatchAuthFileStatus)
	router.POST("/credentials", handler.UploadAuthFileV8)
	router.GET("/download", handler.DownloadAuthFile)
	router.DELETE("/credentials", handler.DeleteAuthFileV8)
	call := func(method, path, body string) int {
		t.Helper()
		r := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(r, req)
		return r.Code
	}
	if code := call("PATCH", "/fields", `{"name":"fixture.json","group":"changed","hold_until":"2000-01-01T00:00:00Z"}`); code != 200 {
		t.Fatal("metadata edit failed", code)
	}
	if _, err := s.coordinator.Control(ctx, a.HubAccountID, 1, "hold"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if code := call("PATCH", "/status", `{"name":"fixture.json","disabled":false}`); code != 200 {
		t.Fatal("native disabled clear failed", code)
	}
	if code := call("PATCH", "/fields", `{"name":"fixture.json","nested_provider_grant":{"opaque":"replacement"}}`); code == 200 {
		t.Fatal("unknown grant edit accepted")
	}
	if code := call("POST", "/credentials?name=fixture.json", `{"type":"codex","refresh_token":"fixture-secret-relogin","access_token":"fixture-secret-access-new"}`); code != 200 {
		t.Fatal("explicit login failed", code)
	}
	records, accounts, err := s.coordinator.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := decodeCredential(records[a.HubAccountID].Data)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[a.HubAccountID].Status != "held" || data.Metadata["group"] != "changed" || data.Metadata["refresh_token"] != "fixture-secret-relogin" {
		t.Fatal("relogin lost controls")
	}
	if code := call("GET", "/download?name=fixture.json", ""); code != 409 {
		t.Fatal("download returned stale disk")
	}
	disk, err := os.ReadFile(filepath.Join(dir, a.ID))
	if err != nil || string(disk) != string(stale) {
		t.Fatal("shared write changed legacy file")
	}
	if code := call("DELETE", "/credentials?name=fixture.json", ""); code != 200 {
		t.Fatal("shared removal failed", code)
	}
	_, accounts, err = s.coordinator.Current(ctx)
	if err != nil || accounts[a.HubAccountID].Status != "removed" {
		t.Fatal("removal not authoritative")
	}
}

func TestConcurrentRenamedLoginsCannotDuplicateGrant(t *testing.T) {
	ctx := context.Background()
	_, _, s, _ := setupStores(t)
	var group sync.WaitGroup
	var accepted atomic.Int32
	start := make(chan struct{})
	for i := range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			a := &coreauth.Auth{ID: fmt.Sprintf("copy-%d.json", i), Provider: "codex", Metadata: map[string]any{"refresh_token": "fixture-secret-shared-grant"}}
			if _, err := s.Save(coreauth.WithAuthCreationIntent(ctx), a); err == nil {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	group.Wait()
	if accepted.Load() != 1 {
		t.Fatal("renamed simultaneous uploads created multiple refresh identities")
	}
	accounts, err := s.List(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatal("duplicate durable grant", err)
	}
}
