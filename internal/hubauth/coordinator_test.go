package hubauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// This fixture implements the published HTTP contract, not the production
// authority. Its transaction semantics are covered by Tracker's Worker tests;
// these tests observe client/provider calls and encrypted object durability.
type authorityFixture struct {
	mu         sync.Mutex
	state      State
	accounts   map[string]Account
	intents    map[string]Intent
	receipts   map[string]Receipt
	failReply  string
	afterBegin func()
}

func newAuthorityFixture() *authorityFixture {
	return &authorityFixture{accounts: map[string]Account{}, intents: map[string]Intent{}, receipts: map[string]Receipt{}}
}

func (f *authorityFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var call struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if json.NewDecoder(r.Body).Decode(&call) != nil {
		w.WriteHeader(400)
		return
	}
	if strings.Contains(string(call.Input), "fixture-secret") {
		panic("credential payload crossed metadata interface")
	}
	var in struct {
		Host             string  `json:"host"`
		InstanceID       string  `json:"instance_id"`
		Authority        Fence   `json:"authority"`
		ExpectedRevision int64   `json:"expected_revision"`
		AccountID        string  `json:"account_id"`
		Generation       int64   `json:"generation"`
		IntentID         string  `json:"intent_id"`
		OperationID      string  `json:"operation_id"`
		Change           Change  `json:"change"`
		SHA256           *string `json:"ciphertext_sha256"`
	}
	_ = json.Unmarshal(call.Input, &in)
	tool := strings.TrimPrefix(call.Tool, "tracker.hub.")
	refuse := func(code string) {
		w.WriteHeader(422)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"code": code}})
	}
	owned := func() bool {
		o := f.state.Ownership
		return o.Live && o.Host == in.Authority.Host && o.InstanceID == in.Authority.InstanceID && o.Epoch == in.Authority.Epoch
	}
	var value any
	switch tool {
	case "get":
		value = f.state
	case "acquire":
		if !f.state.Ownership.Live {
			epoch := f.state.Ownership.Epoch + 1
			f.state.Ownership = Ownership{Host: in.Host, InstanceID: in.InstanceID, Epoch: epoch, Live: true, ExpiresInMS: 30000, Phase: "hydrating"}
		}
		o := f.state.Ownership
		value = map[string]any{"acquired": o.Host == in.Host && o.InstanceID == in.InstanceID, "state": f.state}
	case "renew", "hydrate", "drain", "release":
		if !owned() {
			refuse("HUB_STALE_OWNER")
			return
		}
		switch tool {
		case "renew":
			f.state.Ownership.ExpiresInMS = 30000
		case "hydrate":
			if in.ExpectedRevision != f.state.Head.Revision {
				refuse("HUB_STALE_HEAD")
				return
			}
			if p := f.state.Head.Snapshot; p != nil && (in.SHA256 == nil || *in.SHA256 != p.SHA256) {
				refuse("HUB_INVALID_SNAPSHOT")
				return
			}
			f.state.Ownership.HydratedRevision = &in.ExpectedRevision
			f.state.Ownership.CanServe = f.state.Ownership.Phase != "draining"
			if f.state.Ownership.CanServe {
				f.state.Ownership.Phase = "serving"
			}
		case "drain":
			f.state.Ownership.Phase = "draining"
			f.state.Ownership.CanServe = false
		case "release":
			f.state.Ownership.Live = false
			f.state.Ownership.CanServe = false
		}
		value = f.state
	case "accounts.list":
		accounts := []Account{}
		for _, a := range f.accounts {
			accounts = append(accounts, a)
		}
		value = map[string]any{"revision": f.state.Head.Revision, "accounts": accounts, "next_after": nil}
	case "refresh.begin":
		if !owned() {
			refuse("HUB_STALE_OWNER")
			return
		}
		if f.state.Head.Revision != in.ExpectedRevision {
			refuse("HUB_STALE_HEAD")
			return
		}
		if !f.state.Ownership.CanServe {
			refuse("HUB_NOT_HYDRATED")
			return
		}
		a, ok := f.accounts[in.AccountID]
		if !ok || a.Generation != in.Generation || a.Status != "active" {
			refuse("HUB_ACCOUNT_UNAVAILABLE")
			return
		}
		if a.Quarantined {
			refuse("HUB_INTENT_CONFLICT")
			return
		}
		intent := Intent{ID: in.IntentID, AccountID: a.ID, Generation: a.Generation, ExpectedRevision: in.ExpectedRevision, Host: in.Authority.Host, InstanceID: in.Authority.InstanceID, Epoch: in.Authority.Epoch, State: "unresolved", ResultKey: objectPrefix + "results/" + in.IntentID}
		f.intents[in.IntentID] = intent
		a.Quarantined = true
		f.accounts[a.ID] = a
		value = map[string]any{"provider_call_allowed": true, "intent": intent, "state": f.state}
		if f.afterBegin != nil {
			f.afterBegin()
		}
	case "refresh.get":
		var found *Intent
		if i, ok := f.intents[in.IntentID]; ok {
			found = &i
		}
		value = map[string]any{"intent": found}
	case "refresh.unresolved":
		intents := []Intent{}
		for _, i := range f.intents {
			if i.State == "unresolved" {
				intents = append(intents, i)
			}
		}
		value = map[string]any{"intents": intents, "next_after": nil}
	case "operation.get":
		var receipt *Receipt
		if found, ok := f.receipts[in.OperationID]; ok {
			receipt = &found
		}
		value = map[string]any{"receipt": receipt}
	case "commit":
		if !owned() {
			refuse("HUB_STALE_OWNER")
			return
		}
		if receipt, ok := f.receipts[in.OperationID]; ok {
			value = map[string]any{"replayed": true, "receipt": receipt}
			break
		}
		if in.ExpectedRevision != f.state.Head.Revision {
			refuse("HUB_STALE_HEAD")
			return
		}
		a := f.accounts[in.Change.AccountID]
		if a.Generation != in.Change.ExpectedGeneration {
			refuse("HUB_GENERATION_CONFLICT")
			return
		}
		a.ID = in.Change.AccountID
		switch in.Change.Kind {
		case "login":
			a.Generation++
			if a.Status != "held" {
				a.Status = "active"
			}
			a.Quarantined = false
			for id, i := range f.intents {
				if i.AccountID == a.ID && i.State == "unresolved" {
					i.State = "superseded"
					f.intents[id] = i
				}
			}
		case "refresh":
			i, ok := f.intents[in.Change.IntentID]
			if !ok || i.State != "unresolved" || i.Generation != a.Generation || a.Status == "removed" {
				refuse("HUB_INTENT_CONFLICT")
				return
			}
			i.State = "committed"
			i.ResultSHA256 = &in.Change.ResultSHA256
			f.intents[i.ID] = i
			a.Generation++
			a.Quarantined = false
		case "metadata":
			if a.Status == "removed" || a.Generation == 0 {
				refuse("HUB_ACCOUNT_UNAVAILABLE")
				return
			}
		case "hold":
			a.Status = "held"
		case "unhold":
			a.Status = "active"
		case "remove":
			a.Status = "removed"
			a.Generation++
			a.Quarantined = false
		default:
			refuse("HUB_INVALID_SNAPSHOT")
			return
		}
		f.state.Head.Revision++
		if in.Change.Snapshot != nil {
			f.state.Head.Snapshot = in.Change.Snapshot
		}
		a.Revision = f.state.Head.Revision
		f.accounts[a.ID] = a
		f.state.Ownership.CanServe = false
		f.state.Ownership.HydratedRevision = nil
		receipt := Receipt{OperationID: in.OperationID, Head: f.state.Head, Account: a, Kind: in.Change.Kind}
		if in.Change.IntentID != "" {
			receipt.IntentID = &in.Change.IntentID
		}
		f.receipts[in.OperationID] = receipt
		value = map[string]any{"replayed": false, "receipt": receipt}
	default:
		refuse("UNKNOWN_TOOL")
		return
	}
	if f.failReply == tool {
		f.failReply = ""
		w.WriteHeader(503)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "value": value})
}

type memoryBlobs struct {
	mu           sync.Mutex
	data         map[string][]byte
	failPut      bool
	failHeads    bool
	lostPutReply bool
}

func (b *memoryBlobs) Get(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.data[key]
	if !ok {
		return nil, ErrObjectMissing
	}
	return append([]byte(nil), v...), nil
}
func (b *memoryBlobs) Put(_ context.Context, key string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failPut || (b.failHeads && strings.Contains(key, "/heads/")) {
		return errors.New("fixture unavailable")
	}
	b.data[key] = append([]byte(nil), data...)
	if b.lostPutReply {
		return errors.New("fixture reply lost")
	}
	return nil
}

func fixtureCoordinator(t *testing.T, url, host string, blobs *memoryBlobs) *Coordinator {
	t.Helper()
	dir := t.TempDir()
	for name, value := range map[string]string{"id": host, "secret": "fixture-access-key", "encryption": base64.StdEncoding.EncodeToString(make([]byte, 32))} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := NewClient(url, filepath.Join(dir, "id"), filepath.Join(dir, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := NewObjects(blobs, filepath.Join(dir, "encryption"), filepath.Join(dir, "spool"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCoordinator(client, objects, host)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func setupPair(t *testing.T) (*authorityFixture, *memoryBlobs, *Coordinator, *Coordinator, string) {
	t.Helper()
	f := newAuthorityFixture()
	s := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	b := &memoryBlobs{data: map[string][]byte{}}
	a := fixtureCoordinator(t, s.URL, "m6", b)
	standby := fixtureCoordinator(t, s.URL, "auto-vps", b)
	ctx := context.Background()
	if err := a.Step(ctx); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := a.Login(ctx, id, 0, json.RawMessage(`{"refresh_token":"fixture-secret-old","hold_until":""}`)); err != nil {
		t.Fatal(err)
	}
	return f, b, a, standby, id
}

func TestPartitionRestartAndLateRefresh(t *testing.T) {
	f, _, primary, standby, id := setupPair(t)
	ctx := context.Background()
	entered, complete := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := primary.Refresh(ctx, id, 1, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			if err := baseauth.CheckRefreshDispatch(ctx); err != nil {
				return nil, err
			}

			close(entered)
			<-complete
			return json.RawMessage(`{"refresh_token":"fixture-secret-new","hold_until":""}`), nil
		})
		finished <- err
	}()
	<-entered
	// The old process and its provider call stay alive after authority expires.
	f.mu.Lock()
	f.state.Ownership.Live = false
	f.mu.Unlock()
	if err := standby.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.Authorize(ctx, id, 1); err == nil {
		t.Fatal("partitioned primary served")
	}
	if _, err := standby.Authorize(ctx, id, 1); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("unresolved grant served: %v", err)
	}
	calls := 0
	if _, err := standby.Refresh(ctx, id, 1, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		if err := baseauth.CheckRefreshDispatch(ctx); err != nil {
			return nil, err
		}
		calls++
		return nil, nil
	}); err == nil || calls != 0 {
		t.Fatal("standby repeated unresolved refresh")
	}
	// Another usable account keeps working despite the quarantined grant.
	other := uuid.NewString()
	if _, err := standby.Login(ctx, other, 0, json.RawMessage(`{"refresh_token":"fixture-secret-other"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := standby.Authorize(ctx, other, 1); err != nil {
		t.Fatal(err)
	}
	// Hold the account while the old provider is still working.
	if _, err := standby.Control(ctx, id, 1, "hold"); err != nil {
		t.Fatal(err)
	}
	close(complete)
	if err := <-finished; err == nil {
		t.Fatal("old epoch committed a provider result")
	}
	if err := standby.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	records, accounts, err := standby.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[id].Status != "held" || accounts[id].Generation != 2 || !strings.Contains(string(records[id].Data), "fixture-secret-new") {
		t.Fatal("late refresh lost new tokens or lifted hold")
	}
	if _, ok := records[other]; !ok {
		t.Fatal("late result overwrote another new login")
	}
	// A restarted primary cannot serve its old local material or steal ownership.
	restarted := fixtureCoordinator(t, primary.client.endpoint, "m6", standby.objects.blobs.(*memoryBlobs))
	if err := restarted.Step(ctx); err == nil {
		t.Fatal("restarted primary stole live standby epoch")
	}
	if err := standby.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Step(ctx); err != nil {
		t.Fatal(err)
	}
	latest, controls, err := restarted.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if controls[id].Status != "held" || !strings.Contains(string(latest[id].Data), "fixture-secret-new") {
		t.Fatal("handback failed to hydrate latest tokens and hold")
	}
}

func TestLostBeginReplyNeverCallsProvider(t *testing.T) {
	f, _, primary, _, id := setupPair(t)
	ctx := context.Background()
	f.failReply = "refresh.begin"
	calls := 0
	for range 2 {
		_, _ = primary.Refresh(ctx, id, 1, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			if err := baseauth.CheckRefreshDispatch(ctx); err != nil {
				return nil, err
			}
			calls++
			return nil, nil
		})
	}
	if calls != 0 {
		t.Fatal("ambiguous begin caused provider call")
	}
	if _, err := primary.Authorize(ctx, id, 1); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("ambiguous grant not quarantined: %v", err)
	}
}

func TestFailedPublicationAndNewerLogin(t *testing.T) {
	f, b, primary, _, id := setupPair(t)
	ctx := context.Background()
	b.failPut = true
	_, err := primary.Refresh(ctx, id, 1, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		if err := baseauth.CheckRefreshDispatch(ctx); err != nil {
			return nil, err
		}

		return json.RawMessage(`{"refresh_token":"fixture-secret-rotated"}`), nil
	})
	if err == nil {
		t.Fatal("unpublished rotation reported committed")
	}
	b.failPut = false
	// A fresh explicit login supersedes the unknown generation. Recovery of
	// the spooled old result must leave that login untouched.
	if _, err := primary.Login(ctx, id, 1, json.RawMessage(`{"refresh_token":"fixture-secret-relogin"}`)); err != nil {
		t.Fatal(err)
	}
	if err := primary.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := primary.Authorize(ctx, id, 2)
	if err != nil || !strings.Contains(string(r.Data), "fixture-secret-relogin") {
		t.Fatal("recovery overwrote newer login")
	}
	for _, i := range f.intents {
		if i.State != "superseded" {
			t.Fatal("new login left old intent recoverable")
		}
	}
}

func TestUploadAndCommitLostRepliesReadBack(t *testing.T) {
	f, b, primary, _, id := setupPair(t)
	b.lostPutReply = true
	f.failReply = "commit"
	r, err := primary.Refresh(context.Background(), id, 1, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		if err := baseauth.CheckRefreshDispatch(ctx); err != nil {
			return nil, err
		}

		return json.RawMessage(`{"refresh_token":"fixture-secret-rotated"}`), nil
	})
	if err != nil || r.Generation != 2 {
		t.Fatalf("lost acknowledgments not reconciled: %v", err)
	}
	if len(f.intents) != 1 {
		t.Fatal("refresh repeated")
	}
}

func TestDelayedBeginCannotExtendLocalLease(t *testing.T) {
	f, blobs, primary, _, id := setupPair(t)
	ctx := context.Background()
	other := uuid.NewString()
	if _, err := primary.Login(ctx, other, 0, json.RawMessage(`{"refresh_token":"fixture-secret-other"}`)); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	primary.now = func() time.Time { return time.Unix(0, clock.Load()) }
	f.afterBegin = func() { clock.Add(int64(time.Minute)) }
	sends := 0
	transport := baseauth.GuardRefreshDispatch(fixtureTransport(func(*http.Request) (*http.Response, error) {
		sends++
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}))
	_, err := primary.Refresh(ctx, id, 1, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://fixture.invalid/refresh", nil)
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		return nil, err
	})
	if err == nil || sends != 0 {
		t.Fatal("late begin response reached native transport")
	}
	f.mu.Lock()
	f.afterBegin = nil
	if len(f.intents) != 1 || !f.accounts[id].Quarantined || f.accounts[id].Generation != 1 {
		t.Fatal("delayed permit was cleared or rearmed")
	}
	for _, intent := range f.intents {
		if intent.State != "unresolved" {
			t.Fatal("unsent result fabricated")
		}
	}
	f.mu.Unlock()
	blobs.mu.Lock()
	for key := range blobs.data {
		if strings.Contains(key, "/results/") {
			t.Fatal("unsent provider result fabricated")
		}
	}
	blobs.mu.Unlock()
	// The fixture still recognizes this epoch; a successful renewal establishes
	// a fresh deadline without resolving or rearming the old account's intent.
	if err := primary.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.Authorize(ctx, id, 1); !errors.Is(err, ErrQuarantined) {
		t.Fatal("delayed permit lost quarantine", err)
	}
	if _, err := primary.Authorize(ctx, other, 1); err != nil {
		t.Fatal("delayed permit blocked unrelated account", err)
	}
}

func TestObjectAuthenticationAndImmutableRetry(t *testing.T) {
	_, b, primary, _, _ := setupPair(t)
	ctx := context.Background()
	key := objectPrefix + "results/" + uuid.NewString()
	p, err := primary.objects.Publish(ctx, key, []byte(`{"fixture":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = primary.objects.Publish(ctx, key, []byte(`{"different":"payload"}`)); err == nil {
		t.Fatal("object name was overwritten")
	}
	data, _ := b.Get(ctx, key)
	data[len(data)-1] ^= 1
	b.data[key] = data
	if _, err = primary.objects.Read(ctx, p); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
	p.SHA256 = digest(data)
	if _, err = primary.objects.Read(ctx, p); err == nil {
		t.Fatal("forged digest bypassed AEAD")
	}
}

func TestWatcherTransferDoesNotGrantProviderAuthority(t *testing.T) {
	f, _, primary, standby, id := setupPair(t)
	ctx := context.Background()
	f.mu.Lock()
	f.state.Transfer = &struct {
		To string `json:"to"`
	}{To: "auto-vps"}
	f.mu.Unlock()
	if err := standby.Step(ctx); err == nil {
		t.Fatal("watcher transfer stole live process epoch")
	}
	if _, err := primary.Authorize(ctx, id, 1); err != nil {
		t.Fatal("watcher transfer revoked healthy process", err)
	}
	calls := 0
	if _, err := standby.Refresh(ctx, id, 1, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		if err := baseauth.CheckRefreshDispatch(ctx); err != nil {
			return nil, err
		}
		calls++
		return nil, nil
	}); err == nil || calls != 0 {
		t.Fatal("watcher event authorized provider")
	}
}

func TestSpoolSymlinkCannotReplacePublishedResult(t *testing.T) {
	_, _, primary, _, _ := setupPair(t)
	id := uuid.NewString()
	key := objectPrefix + "results/" + id
	target := filepath.Join(t.TempDir(), "not-a-result")
	if err := os.WriteFile(target, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, primary.objects.spoolPath(key)); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.objects.Publish(context.Background(), key, []byte(`{"fixture":true}`)); err == nil {
		t.Fatal("symlink accepted as spool")
	}
}

func TestDelayedNativeDispatchRechecksLeaseAndConsumesPermission(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			f, _, primary, _, id := setupPair(t)
			calls := 0
			_, _ = primary.Refresh(context.Background(), id, 1, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
				if expire {
					f.mu.Lock()
					f.state.Ownership.Live = false
					f.mu.Unlock()
				}
				if err := baseauth.CheckRefreshDispatch(ctx); err != nil {
					return nil, err
				}
				calls++
				if err := baseauth.CheckRefreshDispatch(ctx); err == nil {
					t.Fatal("native callback could repeat provider request")
				}
				return json.RawMessage(`{"refresh_token":"fixture-secret-new"}`), nil
			})
			if expire && calls != 0 {
				t.Fatal("expired callback reached provider")
			}
			if !expire && calls != 1 {
				t.Fatal("one-use callback failed")
			}
		})
	}
}
