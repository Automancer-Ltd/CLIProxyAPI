package hubauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

var ErrUnavailable = errors.New("hub credential authority unavailable")
var ErrQuarantined = errors.New("hub credential generation quarantined")

// Record binds encrypted credential data to the grant generation. Runtime
// counters in the embedding application must not be substituted for this one.
type Record struct {
	Generation int64           `json:"generation"`
	Data       json.RawMessage `json:"data"`
}
type snapshot struct {
	Accounts map[string]Record `json:"accounts"`
}
type result struct {
	Complete   bool            `json:"complete"`
	AccountID  string          `json:"account_id"`
	Generation int64           `json:"generation"`
	IntentID   string          `json:"intent_id"`
	Data       json.RawMessage `json:"data"`
}

// Coordinator serializes local metadata/snapshot mutations, but never holds its
// mutex across a provider call. Other accounts and handback can progress while
// one provider is stalled. Tracker serializes all hosts.
type Coordinator struct {
	mu          sync.Mutex
	client      *Client
	objects     *Objects
	fence       Fence
	state       State
	deadline    time.Time
	now         func() time.Time
	accounts    map[string]Account
	snapshot    snapshot
	loaded      *Head
	stopped     bool
	mergeResult func(current, refreshed json.RawMessage) (json.RawMessage, error)
	view        atomic.Pointer[authorizationView]
	pending     map[string]int64 // attempted begins survive an older accounts.list reply
}

// Immutable, verified snapshot data. Serving reads this without taking the
// mutation mutex. New requests check the live owner/head with Tracker; frames
// within an admitted session use this view until its lease expires or it is
// invalidated by observed ownership/control loss. It grants no new request turns.
type authorizationView struct {
	fence    Fence
	head     Head
	deadline time.Time
	accounts map[string]Account
	records  map[string]Record
}

// Called only under mu. Publish copies so a concurrent refresh cannot mutate a
// serving reader's account/quarantine state in place.
func (c *Coordinator) publishAuthorization() {
	o := c.state.Ownership
	if !c.owned() || !o.CanServe || o.Phase != "serving" || c.loaded == nil || !sameHead(*c.loaded, c.state.Head) || o.HydratedRevision == nil || *o.HydratedRevision != c.loaded.Revision {
		c.view.Store(nil)
		return
	}
	v := &authorizationView{fence: c.fence, head: *c.loaded, deadline: c.deadline, accounts: make(map[string]Account, len(c.accounts)), records: make(map[string]Record, len(c.snapshot.Accounts))}
	for id, a := range c.accounts {
		v.accounts[id] = a
	}
	for id, r := range c.snapshot.Accounts {
		v.records[id] = r
	}
	c.view.Store(v)
}

func NewCoordinator(client *Client, objects *Objects, host string) (*Coordinator, error) {
	if client == nil || objects == nil || (host != "m6" && host != "auto-vps") {
		return nil, errors.New("hub coordinator configuration invalid")
	}
	return &Coordinator{client: client, objects: objects, fence: Fence{Host: host, InstanceID: uuid.NewString()}, now: time.Now, pending: make(map[string]int64)}, nil
}

// SetResultMerger installs the embedding application's credential/control boundary.
// Configure it before starting any operations. The merger must preserve current
// controls and must not infer a login from a runtime metadata write.
func (c *Coordinator) SetResultMerger(merge func(current, refreshed json.RawMessage) (json.RawMessage, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mergeResult = merge
}

// Metadata rebases a recognized non-grant edit onto the latest complete record.
// The callback validates the entire grant and edits only its intended controls.
func (c *Coordinator) Metadata(ctx context.Context, id string, expected int64, edit func(json.RawMessage) (json.RawMessage, error)) (Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.sync(ctx); err != nil {
		return Receipt{}, err
	}
	a, ok := c.accounts[id]
	if !ok || a.Status == "removed" || a.Generation != expected {
		return Receipt{}, &Refusal{Code: "HUB_GENERATION_CONFLICT", Status: 422}
	}
	data, err := edit(bytes.Clone(c.snapshot.Accounts[id].Data))
	if err != nil {
		return Receipt{}, err
	}
	if !json.Valid(data) {
		return Receipt{}, errors.New("hub metadata payload invalid")
	}
	next := c.copySnapshot()
	next.Accounts[id] = Record{Generation: expected, Data: data}
	return c.publishCommit(ctx, next, Change{Kind: "metadata", AccountID: id, ExpectedGeneration: expected})
}

func sameHead(a, b Head) bool {
	if a.Revision != b.Revision {
		return false
	}
	if a.Snapshot == nil || b.Snapshot == nil {
		return a.Snapshot == nil && b.Snapshot == nil
	}
	return *a.Snapshot == *b.Snapshot
}

func (c *Coordinator) observe(state State, start time.Time) {
	c.state = state
	// Request-start is monotonic under time.Now. Leave a margin for the next
	// local instructions; delayed replies can only shorten, never extend a lease.
	ttlMS := state.Ownership.ExpiresInMS
	if ttlMS > 30000 {
		ttlMS = 30000
	}
	if ttlMS < 0 || state.Head.Revision < 0 || state.Head.Revision > 9007199254740991 || state.Ownership.Epoch < 0 || state.Ownership.Epoch > 9007199254740991 {
		ttlMS = 0
	}
	ttl := time.Duration(ttlMS)*time.Millisecond - time.Second
	c.deadline = start.Add(ttl)
	// Existing sessions consume the published view without a network read.
	// Invalidate it as soon as a live observation disproves its ownership or
	// head, including when subsequent acquisition/hydration cannot complete.
	if v := c.view.Load(); v != nil {
		o := state.Ownership
		if !c.owned() || !o.CanServe || o.Phase != "serving" || !sameHead(v.head, state.Head) || o.HydratedRevision == nil || *o.HydratedRevision != v.head.Revision {
			c.view.Store(nil)
		}
	}
}

func (c *Coordinator) owned() bool {
	o := c.state.Ownership
	return !c.stopped && o.Live && o.Host == c.fence.Host && o.InstanceID == c.fence.InstanceID && o.Epoch == c.fence.Epoch && c.fence.Epoch > 0 && c.now().Before(c.deadline)
}

func (c *Coordinator) stateCall(ctx context.Context, tool string, input any) error {
	if tool == "drain" || tool == "release" {
		c.view.Store(nil)
	}
	start := c.now()
	var state State
	if err := c.client.call(ctx, tool, input, &state); err != nil {
		c.deadline = time.Time{}
		c.view.Store(nil)
		return err
	}
	c.observe(state, start)
	return nil
}

func (c *Coordinator) fenced() any {
	return struct {
		Authority Fence `json:"authority"`
	}{c.fence}
}

// Step acquires/renews this process, hydrates its head and cooperatively returns
// from standby to m6. A watcher request alone never authorizes this process.
func (c *Coordinator) Step(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return ErrUnavailable
	}
	if err := c.stateCall(ctx, "get", struct{}{}); err != nil {
		return err
	}
	if c.owned() {
		if c.fence.Host == "auto-vps" && c.state.Transfer != nil && c.state.Transfer.To == "m6" {
			if err := c.stateCall(ctx, "drain", c.fenced()); err != nil {
				return err
			}
			if err := c.stateCall(ctx, "release", c.fenced()); err != nil {
				return err
			}
			c.fence.Epoch = 0
			c.loaded = nil
			return ErrUnavailable
		}
		if err := c.stateCall(ctx, "renew", c.fenced()); err != nil {
			return err
		}
	} else {
		start := c.now()
		var reply struct {
			Acquired bool  `json:"acquired"`
			State    State `json:"state"`
		}
		input := struct {
			Host       string `json:"host"`
			InstanceID string `json:"instance_id"`
		}{c.fence.Host, c.fence.InstanceID}
		if err := c.client.call(ctx, "acquire", input, &reply); err != nil {
			return err
		}
		c.observe(reply.State, start)
		if !reply.Acquired {
			return ErrUnavailable
		}
		c.fence.Epoch = reply.State.Ownership.Epoch
		c.loaded = nil
	}
	if err := c.sync(ctx); err != nil {
		return err
	}
	return nil
}

func (c *Coordinator) sync(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			c.view.Store(nil)
		}
	}()
	if err := c.stateCall(ctx, "get", struct{}{}); err != nil {
		return err
	}
	if !c.owned() || c.state.Ownership.Phase == "draining" {
		return ErrUnavailable
	}
	head := c.state.Head
	accounts := make(map[string]Account)
	var after *string
	for {
		var page struct {
			Revision int64     `json:"revision"`
			Accounts []Account `json:"accounts"`
			Next     *string   `json:"next_after"`
		}
		if err := c.client.call(ctx, "accounts.list", struct {
			After *string `json:"after"`
			Limit int     `json:"limit"`
		}{after, 100}, &page); err != nil {
			return err
		}
		if page.Revision != head.Revision {
			return &Refusal{Code: "HUB_STALE_HEAD", Status: 422}
		}
		for _, a := range page.Accounts {
			parsed, err := uuid.Parse(a.ID)
			if err != nil || parsed.Version() != 4 || parsed.String() != a.ID || a.Generation < 1 || a.Generation > 9007199254740991 || a.Revision < 0 || a.Revision > head.Revision || (a.Status != "active" && a.Status != "held" && a.Status != "removed") {
				return errors.New("hub account state invalid")
			}
			if _, duplicate := accounts[a.ID]; duplicate {
				return errors.New("hub account pagination repeated a record")
			}
			accounts[a.ID] = a
		}
		if page.Next == nil {
			break
		}
		if after != nil && *page.Next <= *after {
			return errors.New("hub account cursor failed to advance")
		}
		after = page.Next
	}
	if c.loaded == nil || !sameHead(*c.loaded, head) {
		next := snapshot{Accounts: make(map[string]Record)}
		if c.loaded != nil && c.loaded.Snapshot != nil && head.Snapshot != nil && *c.loaded.Snapshot == *head.Snapshot {
			next = c.snapshot
		} else if head.Snapshot != nil {
			plain, err := c.objects.Read(ctx, *head.Snapshot)
			if err != nil {
				return err
			}
			if json.Unmarshal(plain, &next) != nil || next.Accounts == nil {
				return errors.New("hub snapshot payload invalid")
			}
		}
		for id, a := range accounts {
			if a.Status == "removed" {
				continue
			}
			r, ok := next.Accounts[id]
			if !ok || r.Generation != a.Generation || !json.Valid(r.Data) {
				return errors.New("hub snapshot generation mismatch")
			}
		}
		c.snapshot = next
		copyHead := head
		c.loaded = &copyHead
	}
	// A timed-out begin can still reach Tracker after this list was produced.
	// Only a definite rejection or a later generation resolves local uncertainty.
	for id, generation := range c.pending {
		a, ok := accounts[id]
		if ok && a.Generation > generation {
			delete(c.pending, id)
		} else if ok && a.Generation == generation {
			a.Quarantined = true
			accounts[id] = a
		}
	}
	c.accounts = accounts
	if c.owned() && c.state.Ownership.CanServe && c.state.Ownership.HydratedRevision != nil && *c.state.Ownership.HydratedRevision == head.Revision {
		c.publishAuthorization()
		return nil
	}
	var sha *string
	if head.Snapshot != nil {
		sha = &head.Snapshot.SHA256
	}
	input := struct {
		Authority        Fence   `json:"authority"`
		ExpectedRevision int64   `json:"expected_revision"`
		SHA256           *string `json:"ciphertext_sha256"`
	}{c.fence, head.Revision, sha}
	if err := c.stateCall(ctx, "hydrate", input); err != nil {
		return err
	}
	if !c.owned() || !c.state.Ownership.CanServe || !sameHead(c.state.Head, head) {
		return ErrUnavailable
	}
	c.publishAuthorization()
	return nil
}

// Current returns a deep copy with the current controls, after fresh authority
// and hydration checks. It never falls back to a local credential directory.
func (c *Coordinator) Current(ctx context.Context) (map[string]Record, map[string]Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.sync(ctx); err != nil {
		return nil, nil, err
	}
	records := make(map[string]Record)
	accounts := make(map[string]Account)
	for id, a := range c.accounts {
		accounts[id] = a
		if r, ok := c.snapshot.Accounts[id]; ok {
			r.Data = bytes.Clone(r.Data)
			records[id] = r
		}
	}
	return records, accounts, nil
}

// AuthorizeProcess checks live ownership/hydration for immutable config routes.
// It shares the lock-free steady read with OAuth serving, without inventing an account.
func (c *Coordinator) AuthorizeProcess(ctx context.Context) error {
	_, err := c.authorize(ctx, "", 0)
	return err
}
func (c *Coordinator) Authorize(ctx context.Context, id string, generation int64) (Record, error) {
	if id == "" {
		return Record{}, ErrUnavailable
	}
	return c.authorize(ctx, id, generation)
}

// checkSession keeps stable frames local. A missing or concurrently replaced
// view can be a publication gap from an unrelated account's commit, not lost
// authority. Join that mutation and rehydrate once before deciding. The mutex
// coalesces concurrent readers; no stale view authorizes a frame during the gap.
func (c *Coordinator) checkSession(ctx context.Context, fence Fence, id string, generation int64, baseline []byte) error {
	check := func(v *authorizationView) error {
		if v == nil || v.fence != fence || !c.now().Before(v.deadline) {
			return ErrUnavailable
		}
		if id != "" {
			a, ok := v.accounts[id]
			if !ok || a.Status != "active" || a.Generation != generation {
				return ErrUnavailable
			}
			if a.Quarantined {
				return ErrQuarantined
			}
			r := v.records[id]
			if r.Generation != generation || !bytes.Equal(r.Data, baseline) {
				return ErrUnavailable
			}
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if v := c.view.Load(); v != nil {
		err := check(v)
		if c.view.Load() == v {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// A real loss must not be repaired by acquiring a later process epoch.
	if c.fence != fence || !c.owned() || c.state.Ownership.Phase == "draining" {
		return ErrUnavailable
	}
	if c.view.Load() == nil {
		if err := c.sync(ctx); err != nil {
			return err
		}
	}
	return check(c.view.Load())
}

func (c *Coordinator) authorize(ctx context.Context, id string, generation int64) (Record, error) {
	for attempt := 0; attempt < 2; attempt++ {
		// Do not hold the mutation mutex over this read. A delayed reply for one
		// request cannot stall another account or local scheduler accounting.
		start := c.now()
		var state State
		if err := c.client.call(ctx, "get", struct{}{}, &state); err != nil {
			return Record{}, err
		}
		v := c.view.Load()
		if v == nil || !sameHead(state.Head, v.head) {
			c.view.CompareAndSwap(v, nil)
			// Only changed/unhydrated heads need serialized object/control loading.
			// The steady request path never enumerates accounts or writes hydration.
			if attempt != 0 {
				return Record{}, ErrUnavailable
			}
			c.mu.Lock()
			err := c.sync(ctx)
			c.mu.Unlock()
			if err != nil {
				return Record{}, err
			}
			continue
		}
		o := state.Ownership
		ttlMS := o.ExpiresInMS
		if ttlMS > 30000 {
			ttlMS = 30000
		}
		now := c.now()
		if ttlMS <= 1000 || !now.Before(start.Add(time.Duration(ttlMS)*time.Millisecond-time.Second)) || !now.Before(v.deadline) || !o.Live || !o.CanServe || o.Phase != "serving" || o.Host != v.fence.Host || o.InstanceID != v.fence.InstanceID || o.Epoch != v.fence.Epoch || o.HydratedRevision == nil || *o.HydratedRevision != v.head.Revision {
			c.view.CompareAndSwap(v, nil)
			return Record{}, ErrUnavailable
		}
		if id == "" {
			if c.view.Load() != v {
				return Record{}, ErrUnavailable
			}
			return Record{}, nil
		}
		a, ok := v.accounts[id]
		if !ok || a.Status != "active" || a.Generation != generation {
			return Record{}, ErrUnavailable
		}
		if a.Quarantined {
			return Record{}, ErrQuarantined
		}
		r := v.records[id]
		r.Data = bytes.Clone(r.Data)
		// A local begin/commit/drain during the read invalidates this observation.
		if c.view.Load() != v {
			return Record{}, ErrUnavailable
		}
		return r, nil
	}
	return Record{}, ErrUnavailable
}

func (c *Coordinator) commit(ctx context.Context, input Commit) (Receipt, error) {
	c.view.Store(nil)
	var reply struct {
		Receipt Receipt `json:"receipt"`
	}
	err := c.client.call(ctx, "commit", input, &reply)
	// Read back even on success. This also resolves an accepted write whose
	// response was lost, without treating a historical receipt as serving rights.
	var read struct {
		Receipt *Receipt `json:"receipt"`
	}
	readErr := c.client.call(ctx, "operation.get", struct {
		ID string `json:"operation_id"`
	}{input.OperationID}, &read)
	if readErr == nil && read.Receipt != nil {
		r := read.Receipt
		if r.OperationID != input.OperationID || r.Kind != input.Change.Kind || r.Account.ID != input.Change.AccountID {
			return Receipt{}, errors.New("hub commit receipt mismatch")
		}
		c.loaded = nil
		return *r, nil
	}
	if err != nil {
		return Receipt{}, err
	}
	if readErr != nil {
		return Receipt{}, readErr
	}
	return Receipt{}, errors.New("hub commit receipt missing; outcome unknown")
}

// Login is an explicit new login, never an implicit file reload. Its expected
// generation was captured when that login began. A newer login cannot be lost.
func (c *Coordinator) Login(ctx context.Context, id string, expected int64, data json.RawMessage) (Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !json.Valid(data) {
		return Receipt{}, errors.New("hub login payload invalid")
	}
	if err := c.sync(ctx); err != nil {
		return Receipt{}, err
	}
	a := c.accounts[id]
	if a.Generation != expected {
		return Receipt{}, &Refusal{Code: "HUB_GENERATION_CONFLICT", Status: 422}
	}
	next := c.copySnapshot()
	next.Accounts[id] = Record{Generation: expected + 1, Data: bytes.Clone(data)}
	return c.publishCommit(ctx, next, Change{Kind: "login", AccountID: id, ExpectedGeneration: expected})
}

func (c *Coordinator) Control(ctx context.Context, id string, expected int64, kind string) (Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind != "hold" && kind != "unhold" && kind != "remove" {
		return Receipt{}, errors.New("hub control invalid")
	}
	if err := c.sync(ctx); err != nil {
		return Receipt{}, err
	}
	return c.commit(ctx, Commit{Authority: c.fence, ExpectedRevision: c.state.Head.Revision, OperationID: uuid.NewString(), Change: Change{Kind: kind, AccountID: id, ExpectedGeneration: expected}})
}

func (c *Coordinator) copySnapshot() snapshot {
	next := snapshot{Accounts: make(map[string]Record, len(c.snapshot.Accounts))}
	for id, r := range c.snapshot.Accounts {
		next.Accounts[id] = r
	}
	return next
}

func (c *Coordinator) publishCommit(ctx context.Context, next snapshot, change Change) (Receipt, error) {
	operationID := uuid.NewString()
	plain, err := json.Marshal(next)
	if err != nil {
		return Receipt{}, errors.New("hub snapshot encoding failed")
	}
	p, err := c.objects.Publish(ctx, objectPrefix+"heads/"+operationID, plain)
	if err != nil {
		return Receipt{}, err
	}
	change.Snapshot = &p
	return c.commit(ctx, Commit{Authority: c.fence, ExpectedRevision: c.state.Head.Revision, OperationID: operationID, Change: change})
}

// Refresh prepares a request without consuming the grant. The callback must
// use GuardRefreshDispatch at its native HTTP boundary: only that final check
// creates the durable intent. Preparation failures leave no intent to strand.
func (c *Coordinator) Refresh(ctx context.Context, id string, generation int64, provider func(context.Context, json.RawMessage) (json.RawMessage, error)) (Record, error) {
	base, err := c.Authorize(ctx, id, generation)
	if err != nil {
		return Record{}, err
	}
	var dispatchMu sync.Mutex
	var intent Intent
	used, closed := false, false
	dispatchCtx := baseauth.WithRefreshDispatch(ctx, func(callCtx context.Context) error {
		dispatchMu.Lock()
		defer dispatchMu.Unlock()
		if used || closed {
			return errors.New("hub provider permission already consumed")
		}
		used = true
		var err error
		intent, err = c.begin(callCtx, id, generation, base.Data)
		return err
	})
	data, providerErr := provider(dispatchCtx, base.Data)
	dispatchMu.Lock()
	closed = true
	admitted := intent.ID != ""
	dispatchMu.Unlock()
	if !admitted {
		// Never invent provider output or resolve an uncertain accepted begin. The
		// contract retains a lost/delayed permit, but ordinary preparation/refusal
		// before begin consumes nothing and may be retried with fresh account data.
		return Record{}, errors.New("hub refresh was not dispatched")
	}

	if providerErr != nil && !json.Valid(data) {
		return Record{}, errors.New("hub refresh outcome unknown; generation quarantined")
	}
	if !json.Valid(data) {
		return Record{}, errors.New("hub refresh result invalid; generation quarantined")
	}
	plain, err := json.Marshal(result{AccountID: id, Generation: generation, IntentID: intent.ID, Data: data, Complete: providerErr == nil})
	if err != nil {
		return Record{}, errors.New("hub result encoding failed; generation quarantined")
	}
	// Provider rotation may have completed after cancellation or loss of the
	// lease. Preserve its result; only the current owner can advance the head.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err = c.objects.Publish(persistCtx, intent.ResultKey, plain); err != nil {
		return Record{}, err
	}
	if providerErr != nil {
		return Record{}, errors.New("hub partial refresh preserved; generation quarantined")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err = c.recoverIntent(persistCtx, intent); err != nil {
		return Record{}, err
	}
	if err = c.sync(persistCtx); err != nil {
		return Record{}, err
	}
	r := c.snapshot.Accounts[id]
	r.Data = bytes.Clone(r.Data)
	return r, nil
}

func (c *Coordinator) begin(ctx context.Context, id string, generation int64, baseline json.RawMessage) (Intent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.sync(ctx); err != nil {
		return Intent{}, err
	}
	a, ok := c.accounts[id]
	if !ok || a.Status != "active" || a.Generation != generation {
		return Intent{}, ErrUnavailable
	}
	if a.Quarantined {
		return Intent{}, ErrQuarantined
	}
	// Head changes on other accounts do not change this prepared request. A
	// changed target grant/control must be reloaded before creating any intent.
	if !bytes.Equal(c.snapshot.Accounts[id].Data, baseline) {
		return Intent{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Intent{}, err
	}
	input := struct {
		Authority        Fence  `json:"authority"`
		ExpectedRevision int64  `json:"expected_revision"`
		AccountID        string `json:"account_id"`
		Generation       int64  `json:"generation"`
		IntentID         string `json:"intent_id"`
	}{c.fence, c.state.Head.Revision, id, generation, uuid.NewString()}
	var reply struct {
		Allowed bool   `json:"provider_call_allowed"`
		Intent  Intent `json:"intent"`
		State   State  `json:"state"`
	}
	// Begin changes quarantine without advancing the head. Publish the local
	// restriction before its network call, including while its reply is unknown.
	a.Quarantined = true
	c.accounts[id] = a
	c.pending[id] = generation
	c.publishAuthorization()
	start := c.now()
	if err := c.client.call(ctx, "refresh.begin", input, &reply); err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			// A typed authority refusal proves this attempt inserted no intent.
			delete(c.pending, id)
			a.Quarantined = false
			c.accounts[id] = a
			c.publishAuthorization()
		}
		return Intent{}, err
	}
	c.observe(reply.State, start)
	if !reply.Allowed || !c.owned() || !c.state.Ownership.CanServe || c.state.Head.Revision != input.ExpectedRevision {
		return Intent{}, ErrQuarantined
	}
	// The atomic begin response is the final admission, not a historical
	// lookup followed by another global-head check. No provider call is permitted
	// if its first response is lost or arrives outside the conservative deadline.
	i := reply.Intent
	if i.State != "unresolved" || i.ID != input.IntentID || i.AccountID != id || i.Generation != generation || i.Epoch != c.fence.Epoch || i.Host != c.fence.Host || i.InstanceID != c.fence.InstanceID || i.ExpectedRevision != input.ExpectedRevision || i.ResultKey != objectPrefix+"results/"+input.IntentID {
		return Intent{}, ErrQuarantined
	}
	return i, nil
}

func (c *Coordinator) recoverIntent(ctx context.Context, intent Intent) error {
	if err := c.sync(ctx); err != nil {
		return err
	}
	a, ok := c.accounts[intent.AccountID]
	if !ok || a.Status == "removed" || a.Generation != intent.Generation {
		return &Refusal{Code: "HUB_GENERATION_CONFLICT", Status: 422}
	}
	plain, sha, err := c.objects.ReadResult(ctx, intent.ResultKey)
	if err != nil {
		return err
	}
	var r result
	if json.Unmarshal(plain, &r) != nil || r.AccountID != intent.AccountID || r.Generation != intent.Generation || r.IntentID != intent.ID || !json.Valid(r.Data) {
		return errors.New("hub recovery result does not match intent")
	}
	if !r.Complete {
		return ErrQuarantined
	}
	data := r.Data
	if c.mergeResult != nil {
		data, err = c.mergeResult(bytes.Clone(c.snapshot.Accounts[intent.AccountID].Data), bytes.Clone(r.Data))
		if err != nil {
			return err
		}
	}
	next := c.copySnapshot()
	next.Accounts[intent.AccountID] = Record{Generation: intent.Generation + 1, Data: data}
	_, err = c.publishCommit(ctx, next, Change{Kind: "refresh", AccountID: intent.AccountID, ExpectedGeneration: intent.Generation, IntentID: intent.ID, ResultSHA256: sha})
	return err
}

// Recover scans from the start each time. Missing/ambiguous results quarantine
// only their own generation; they do not prevent other accounts from serving.
func (c *Coordinator) Recover(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.sync(ctx); err != nil {
		return err
	}
	var after *string
	for {
		var page struct {
			Intents []Intent `json:"intents"`
			Next    *string  `json:"next_after"`
		}
		if err := c.client.call(ctx, "refresh.unresolved", struct {
			After *string `json:"after"`
			Limit int     `json:"limit"`
		}{after, 100}, &page); err != nil {
			return err
		}
		for _, intent := range page.Intents {
			if intent.State != "unresolved" {
				continue
			}
			// Republish only exact ciphertext already on this process's disk.
			_ = c.objects.RepublishResult(ctx, intent.ID)
			err := c.recoverIntent(ctx, intent)
			if err != nil && !errors.Is(err, ErrObjectMissing) && !errors.Is(err, ErrQuarantined) && !IsRefusal(err, "HUB_GENERATION_CONFLICT") {
				return err
			}
		}
		if page.Next == nil {
			return nil
		}
		if after != nil && *page.Next <= *after {
			return errors.New("hub intent cursor failed to advance")
		}
		after = page.Next
	}
}

func (c *Coordinator) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { c.stopped = true; c.deadline = time.Time{}; c.view.Store(nil) }()
	if !c.owned() {
		return nil
	}
	if err := c.stateCall(ctx, "drain", c.fenced()); err != nil {
		return err
	}
	return c.stateCall(ctx, "release", c.fenced())
}
