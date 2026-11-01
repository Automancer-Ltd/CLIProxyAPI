package hubauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Store never reads the legacy auth directory. Its bindings and edit baselines
// are runtime-only: a copied JSON file cannot impersonate a hydrated credential.
type Store struct {
	coordinator *Coordinator
	saves       sync.Mutex
}

func NewStore(c *Coordinator) *Store {
	c.SetResultMerger(mergeCredentialResult)
	return &Store{coordinator: c}
}

// This is a closed set of user controls, not a list of token names to exclude.
// Everything else, including unknown and nested provider fields, is grant data.
var controlKeys = map[string]bool{
	"hold_until": true, "group": true, "pin_only": true, "allow_credits": true,
	"disabled": true, "prefix": true, "proxy_url": true, "label": true,
	"note": true, "user_note": true, "priority": true, "weight": true,
	"request_retry": true, "disable_cooling": true,
}

type storedCredential struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Metadata   map[string]any    `json:"metadata"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Label      string            `json:"label,omitempty"`
	Prefix     string            `json:"prefix,omitempty"`
	ProxyURL   string            `json:"proxy_url,omitempty"`
	Disabled   bool              `json:"disabled,omitempty"`
}

func encodeCredential(a *coreauth.Auth) (json.RawMessage, error) {
	if a == nil || a.ID == "" || a.Provider == "" {
		return nil, errors.New("hub credential identity missing")
	}
	metadata, err := misc.MergeMetadata(a.Storage, a.Metadata)
	if err != nil {
		return nil, errors.New("hub credential encoding failed")
	}
	attrs := make(map[string]string)
	for k, v := range a.Attributes {
		if k != coreauth.AttributePath && k != coreauth.AttributeSource {
			attrs[k] = v
		}
	}
	return json.Marshal(storedCredential{a.ID, strings.ToLower(a.Provider), metadata, attrs, a.Label, a.Prefix, a.ProxyURL, a.Disabled})
}
func decodeCredential(raw json.RawMessage) (storedCredential, error) {
	var a storedCredential
	if json.Unmarshal(raw, &a) != nil || a.ID == "" || a.Provider == "" || a.Metadata == nil {
		return a, errors.New("hub credential payload invalid")
	}
	return a, nil
}
func grantBytes(a storedCredential) []byte {
	metadata := make(map[string]any)
	attrs := make(map[string]string)
	for k, v := range a.Metadata {
		if !controlKeys[k] {
			metadata[k] = v
		}
	}
	for k, v := range a.Attributes {
		if !controlKeys[k] {
			attrs[k] = v
		}
	}
	a.Metadata = metadata
	a.Attributes = attrs
	a.Label = ""
	a.Prefix = ""
	a.ProxyURL = ""
	a.Disabled = false
	raw, _ := json.Marshal(a)
	return raw
}
func mergeCredentialResult(current, refreshed json.RawMessage) (json.RawMessage, error) {
	latest, err := decodeCredential(current)
	if err != nil {
		return nil, err
	}
	next, err := decodeCredential(refreshed)
	if err != nil {
		return nil, err
	}
	if next.ID != latest.ID || next.Provider != latest.Provider {
		return nil, errors.New("hub result changed account identity")
	}
	for k := range controlKeys {
		delete(next.Metadata, k)
		if v, ok := latest.Metadata[k]; ok {
			next.Metadata[k] = v
		}
		delete(next.Attributes, k)
		if v, ok := latest.Attributes[k]; ok {
			if next.Attributes == nil {
				next.Attributes = map[string]string{}
			}
			next.Attributes[k] = v
		}
	}
	next.Label = latest.Label
	next.Prefix = latest.Prefix
	next.ProxyURL = latest.ProxyURL
	next.Disabled = latest.Disabled
	return json.Marshal(next)
}

func (s *Store) materialize(id string, r Record, a Account) (*coreauth.Auth, error) {
	data, err := decodeCredential(r.Data)
	if err != nil {
		return nil, err
	}
	out := &coreauth.Auth{ID: data.ID, Provider: data.Provider, Metadata: data.Metadata, Attributes: data.Attributes, Label: data.Label, Prefix: data.Prefix, ProxyURL: data.ProxyURL, Disabled: data.Disabled, Status: coreauth.StatusActive, HubAccountID: id, HubGeneration: a.Generation, HubBaseline: bytes.Clone(r.Data), HubAuthority: s}
	if a.Status != "active" || a.Quarantined {
		out.Disabled = true
		out.Status = coreauth.StatusDisabled
	}
	out.EnsureIndex()
	return out, nil
}
func (s *Store) List(ctx context.Context) ([]*coreauth.Auth, error) {
	records, accounts, err := s.coordinator.Current(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*coreauth.Auth, 0, len(records))
	for id, r := range records {
		a := accounts[id]
		if a.Status == "removed" {
			continue
		}
		auth, err := s.materialize(id, r, a)
		if err != nil {
			return nil, err
		}
		out = append(out, auth)
	}
	return out, nil
}
func (s *Store) Authorize(ctx context.Context, a *coreauth.Auth) error {
	if a == nil || a.HubAuthority != s || a.HubAccountID == "" || a.HubGeneration < 1 {
		return ErrUnavailable
	}
	if a.Provider != "claude" && a.Provider != "codex" {
		return errors.New("hub provider path is not enrolled")
	}
	if a.Provider == "codex" {
		if siwc, ok := a.Metadata["siwc"].(map[string]any); ok && len(siwc) > 0 {
			return errors.New("hub Codex SIWC mode is not enrolled")
		}
	}
	r, err := s.coordinator.Authorize(ctx, a.HubAccountID, a.HubGeneration)
	if err != nil {
		return err
	}
	// An old metadata snapshot could lift a timed hold or spend restriction.
	// Require the exact current record, even when its grant generation is unchanged.
	if !bytes.Equal(r.Data, a.HubBaseline) {
		return errors.New("hub credential controls changed; reload required")
	}
	latest, err := decodeCredential(r.Data)
	if err != nil {
		return err
	}
	actualRaw, err := encodeCredential(a)
	if err != nil {
		return err
	}
	actual, err := decodeCredential(actualRaw)
	if err != nil {
		return err
	}
	if !bytes.Equal(grantBytes(actual), grantBytes(latest)) {
		return ErrUnavailable
	}
	if latest.Disabled {
		return ErrUnavailable
	}
	if raw, ok := latest.Metadata["hold_until"].(string); ok && raw != "" {
		until, err := time.Parse(time.RFC3339, raw)
		if err != nil || until.After(time.Now()) {
			return ErrUnavailable
		}
	}
	return nil
}
func (s *Store) Refresh(ctx context.Context, a *coreauth.Auth, provider func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)) (*coreauth.Auth, error) {
	if err := s.Authorize(ctx, a); err != nil {
		return nil, err
	}
	r, err := s.coordinator.Refresh(ctx, a.HubAccountID, a.HubGeneration, func(inner context.Context, data json.RawMessage) (json.RawMessage, error) {
		base, err := s.materialize(a.HubAccountID, Record{Generation: a.HubGeneration, Data: data}, Account{Generation: a.HubGeneration, Status: "active"})
		if err != nil {
			return nil, err
		}
		// Preserve only runtime identifiers needed by the manager's local CAS.
		base.RegistrationEpoch = a.RegistrationEpoch
		base.CredentialVersion = a.CredentialVersion
		base.Generation = a.Generation
		updated, providerErr := provider(inner, base)
		if updated == nil {
			return nil, providerErr
		}
		encoded, err := encodeCredential(updated)
		if err != nil {
			return nil, err
		}
		return encoded, providerErr
	})
	if err != nil {
		return nil, err
	}
	out, err := s.materialize(a.HubAccountID, r, Account{Generation: r.Generation, Status: "active"})
	if err != nil {
		return nil, err
	}
	out.RegistrationEpoch = a.RegistrationEpoch
	out.CredentialVersion = a.CredentialVersion
	out.Generation = a.Generation
	return out, nil
}

func (s *Store) Save(ctx context.Context, a *coreauth.Auth) (string, error) {
	raw, err := encodeCredential(a)
	if err != nil {
		return "", err
	}
	// Scheduler counters/status are local runtime state and are not encoded in
	// this store. An unchanged snapshot is not a credential write or a grant of
	// serving rights: leave it alone, even if a remote read is currently blocked.
	if !coreauth.HasAuthCreationIntent(ctx) && a.HubAuthority == s && a.HubAccountID != "" && a.HubGeneration > 0 && bytes.Equal(raw, a.HubBaseline) {
		return "hub:" + a.HubAccountID, nil
	}
	// Serialize actual edits/login deduplication; no-op runtime saves stay out.
	s.saves.Lock()
	defer s.saves.Unlock()
	requested, err := decodeCredential(raw)
	if err != nil {
		return "", err
	}
	records, accounts, err := s.coordinator.Current(ctx)
	if err != nil {
		return "", err
	}
	id := a.HubAccountID
	if id == "" {
		for candidate, r := range records {
			existing, err := decodeCredential(r.Data)
			if err != nil {
				return "", err
			}
			if existing.ID == a.ID {
				id = candidate
				break
			}
		}
	}
	if coreauth.HasAuthCreationIntent(ctx) {
		if requested.Provider != "claude" && requested.Provider != "codex" {
			return "", errors.New("hub login provider is not enrolled")
		}
		if id == "" {
			id = uuid.NewString()
		}
		// A replayed or renamed credential file must never clear an unresolved
		// intent by pretending the unchanged refresh grant is a fresh login.
		for otherID, record := range records {
			old, err := decodeCredential(record.Data)
			if err != nil {
				return "", err
			}
			if old.Provider == requested.Provider && sharedRefreshGrant(old, requested) {
				if otherID != id || accounts[id].Quarantined || accounts[id].Status == "removed" {
					return "", errors.New("hub login reuses an existing refresh grant")
				}
				// An exact no-op login is safe, but grants no new generation/permission.
				if bytes.Equal(grantBytes(old), grantBytes(requested)) {
					updated, err := s.materialize(id, record, accounts[id])
					if err != nil {
						return "", err
					}
					a.Metadata = updated.Metadata
					a.Attributes = updated.Attributes
					a.HubAccountID = id
					a.HubGeneration = updated.HubGeneration
					a.HubBaseline = updated.HubBaseline
					a.HubAuthority = s
					a.Storage = nil
					return "hub:" + id, nil
				}
				return "", errors.New("hub login did not replace the refresh grant")
			}
		}
		expected := accounts[id].Generation
		if a.HubGeneration != 0 && a.HubGeneration != expected {
			return "", ErrUnavailable
		}
		// Explicit re-login retains all current user controls and administrative hold.
		if current, ok := records[id]; ok {
			raw, err = mergeCredentialResult(current.Data, raw)
			if err != nil {
				return "", err
			}
		}
		if _, err = s.coordinator.Login(ctx, id, expected, raw); err != nil {
			return "", err
		}
	} else {
		if a.HubAuthority != s || a.HubAccountID != id || a.HubGeneration != accounts[id].Generation || len(a.HubBaseline) == 0 {
			return "", ErrUnavailable
		}
		baseline, err := decodeCredential(a.HubBaseline)
		if err != nil {
			return "", err
		}
		// Administrative held/quarantine is an independent runtime restriction.
		// It must not be copied back as the user's native disabled setting.
		if accounts[id].Status != "active" || accounts[id].Quarantined {
			requested.Disabled = baseline.Disabled
		}
		if !bytes.Equal(grantBytes(baseline), grantBytes(requested)) {
			return "", errors.New("hub grant change requires explicit login or refresh")
		}
		raw, _ = json.Marshal(requested)
		if !bytes.Equal(raw, a.HubBaseline) {
			_, err = s.coordinator.Metadata(ctx, id, a.HubGeneration, func(latestRaw json.RawMessage) (json.RawMessage, error) {
				latest, err := decodeCredential(latestRaw)
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(grantBytes(latest), grantBytes(baseline)) {
					return nil, ErrUnavailable
				}
				// Rebase only fields changed by this operation. Untouched fields retain
				// concurrent edits; any grant change above is refused in its entirety.
				for k := range controlKeys {
					if !reflect.DeepEqual(baseline.Metadata[k], requested.Metadata[k]) {
						delete(latest.Metadata, k)
						if v, ok := requested.Metadata[k]; ok {
							latest.Metadata[k] = v
						}
					}
					if baseline.Attributes[k] != requested.Attributes[k] {
						delete(latest.Attributes, k)
						if v, ok := requested.Attributes[k]; ok {
							if latest.Attributes == nil {
								latest.Attributes = map[string]string{}
							}
							latest.Attributes[k] = v
						}
					}
				}
				if baseline.Label != requested.Label {
					latest.Label = requested.Label
				}
				if baseline.Prefix != requested.Prefix {
					latest.Prefix = requested.Prefix
				}
				if baseline.ProxyURL != requested.ProxyURL {
					latest.ProxyURL = requested.ProxyURL
				}
				if baseline.Disabled != requested.Disabled {
					latest.Disabled = requested.Disabled
				}
				return json.Marshal(latest)
			})
			if err != nil {
				return "", err
			}
		}
	}
	records, accounts, err = s.coordinator.Current(ctx)
	if err != nil {
		return "", err
	}
	updated, err := s.materialize(id, records[id], accounts[id])
	if err != nil {
		return "", err
	}
	// Keep local manager counters; save enrichment updates only authoritative data.
	a.Metadata = updated.Metadata
	a.Attributes = updated.Attributes
	a.Label = updated.Label
	a.Prefix = updated.Prefix
	a.ProxyURL = updated.ProxyURL
	a.Disabled = updated.Disabled
	a.Storage = nil
	a.HubAccountID = id
	a.HubGeneration = updated.HubGeneration
	a.HubBaseline = updated.HubBaseline
	a.HubAuthority = s
	return "hub:" + id, nil
}
func (s *Store) Delete(ctx context.Context, id string) error {
	records, accounts, err := s.coordinator.Current(ctx)
	if err != nil {
		return err
	}
	for accountID, r := range records {
		a, err := decodeCredential(r.Data)
		if err != nil {
			return err
		}
		if a.ID == id {
			_, err = s.coordinator.Control(ctx, accountID, accounts[accountID].Generation, "remove")
			return err
		}
	}
	return errors.New("hub account not found")
}

func sharedRefreshGrant(a, b storedCredential) bool {
	// Provider enrollment is explicit. Both enrolled single-grant formats use
	// refresh_token (Claude also accepts refreshToken); compare both spellings.
	for _, left := range []string{"refresh_token", "refreshToken"} {
		value, _ := a.Metadata[left].(string)
		if value == "" {
			continue
		}
		for _, right := range []string{"refresh_token", "refreshToken"} {
			if value == b.Metadata[right] {
				return true
			}
		}
	}
	return false
}
