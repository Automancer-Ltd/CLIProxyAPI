package hubauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ValidateCredential is the closed deployment enrollment, shared by login,
// authorization and activation preflight. Unknown modes never reach a provider.
func ValidateCredential(a *coreauth.Auth) error {
	if a == nil {
		return ErrUnavailable
	}
	switch a.Provider {
	case "claude", "codex", "antigravity", "xai", "meta", "devin":
	default:
		return errors.New("hub provider path is not enrolled")
	}
	switch a.Provider {
	case "claude":
		if stringField(a.Metadata, "refresh_token") == "" && stringField(a.Metadata, "refreshToken") == "" {
			return errors.New("hub Claude enrollment requires an OAuth refresh grant")
		}
	case "codex", "antigravity", "xai":
		if stringField(a.Metadata, "refresh_token") == "" {
			return errors.New("hub OAuth enrollment requires a refresh grant")
		}
	case "meta":
		if !strings.HasPrefix(stringField(a.Metadata, "dca_token"), "dca:") && !strings.HasPrefix(stringField(a.Metadata, "access_token"), "dca:") {
			return errors.New("hub Meta enrollment requires a DCA grant")
		}
	case "devin":
		if stringField(a.Metadata, "session_token") == "" && stringField(a.Metadata, "api_key") == "" {
			return errors.New("hub Devin enrollment requires a session grant")
		}
	}
	if a.Provider == "xai" && stringField(a.Metadata, "token_endpoint") == "" {
		return errors.New("hub xAI enrollment requires an existing token_endpoint")
	}
	if a.Provider == "codex" && (a.Metadata["siwc"] != nil || coreauth.IsCodexUseSIWC(a)) {
		return errors.New("hub Codex SIWC mode is not enrolled")
	}
	if a.Provider == "antigravity" && strings.TrimSpace(stringField(a.Metadata, "project_id")) == "" {
		return errors.New("hub Antigravity enrollment requires an existing project_id")
	}
	// xAI has a separate native websocket implementation; it is not enrolled.
	if a.Provider == "xai" && (enabledFlag(a.Metadata["websockets"]) || enabledFlag(a.Attributes["websockets"])) {
		return errors.New("hub xAI upstream websocket mode is not enrolled")
	}
	return nil
}
func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}
func enabledFlag(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(v))
		return parsed
	}
	return false
}

// ConfigureStatic binds config-synthesized keys/local routes before service start.
// They retain their native config identity and never invent OAuth generations.
// Serving still requires the live process and current hydration. Changing a key
// requires the ordinary coordinated configuration release, not an auth-file edit.
func (s *Store) ConfigureStatic(auths []*coreauth.Auth) error {
	s.static = make(map[string]*coreauth.Auth, len(auths))
	for _, original := range auths {
		if original == nil || original.AuthSourceKind() != coreauth.AuthSourceConfig {
			return errors.New("hub static route must be synthesized from config")
		}
		if !strings.HasPrefix(original.Provider, "openai-compatible-") && original.Provider != "openai-compatibility" {
			return errors.New("hub native config-key transport is not enrolled")
		}
		a := original.Clone()
		if a.Metadata == nil {
			a.Metadata = map[string]any{}
		}
		raw, err := encodeCredential(a)
		if err != nil {
			return err
		}
		a.HubAuthority = s
		a.HubBaseline = raw
		s.static[a.ID] = a
	}
	return nil
}
func (s *Store) sameStatic(a *coreauth.Auth) bool {
	original := s.static[a.ID]
	if original == nil || a.HubAuthority != s || a.HubAccountID != "" || a.HubGeneration != 0 {
		return false
	}
	raw, err := encodeCredential(a)
	return err == nil && bytes.Equal(raw, original.HubBaseline) && bytes.Equal(a.HubBaseline, original.HubBaseline)
}

var devinStatusKeys = map[string]bool{"email": true, "user_name": true, "user_id": true, "team_id": true, "plan": true, "org_id": true, "org_name": true}

// Devin refresh reads status; it does not rotate its session grant. Store those
// observations without a refresh intent or a successful generation advance.
func (s *Store) refreshStatus(ctx context.Context, a *coreauth.Auth, provider func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)) (*coreauth.Auth, error) {
	updated, err := provider(ctx, a.Clone())
	if err != nil || updated == nil {
		return nil, err
	}
	raw, err := encodeCredential(updated)
	if err != nil {
		return nil, err
	}
	candidate, err := decodeCredential(raw)
	if err != nil {
		return nil, err
	}
	baseline, err := decodeCredential(a.HubBaseline)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(grantBytes(candidate), grantBytes(baseline)) {
		return nil, errors.New("hub status read changed a grant")
	}
	if err = s.Authorize(ctx, a); err != nil {
		return nil, err
	}
	_, err = s.coordinator.Metadata(ctx, a.HubAccountID, a.HubGeneration, func(current json.RawMessage) (json.RawMessage, error) {
		latest, err := decodeCredential(current)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(grantBytes(latest), grantBytes(baseline)) {
			return nil, ErrUnavailable
		}
		if latest.Attributes == nil {
			latest.Attributes = map[string]string{}
		}
		for k := range devinStatusKeys {
			if v, ok := candidate.Metadata[k]; ok {
				latest.Metadata[k] = v
			}
			if v, ok := candidate.Attributes[k]; ok {
				latest.Attributes[k] = v
			}
		}
		return json.Marshal(latest)
	})
	if err != nil {
		return nil, err
	}
	records, accounts, err := s.coordinator.Current(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.materialize(a.HubAccountID, records[a.HubAccountID], accounts[a.HubAccountID])
	if err != nil {
		return nil, err
	}
	out.RegistrationEpoch = a.RegistrationEpoch
	out.CredentialVersion = a.CredentialVersion
	out.Generation = a.Generation
	out.Quota = updated.Quota
	out.LastRefreshedAt = updated.LastRefreshedAt
	return out, nil
}

// OpenHubSession admits a connection with live authority, then returns an I/O-free
// frame check against the locally hydrated view and its conservative lease deadline.
// New user turns require Authorize again; frames never wait for Tracker. The
// captured process fence and account baseline cannot adopt a later grant or epoch.
func (s *Store) OpenHubSession(ctx context.Context, a *coreauth.Auth) (func(context.Context) error, error) {
	a = a.Clone()
	if err := s.Authorize(ctx, a); err != nil {
		return nil, err
	}
	view := s.coordinator.view.Load()
	if view == nil {
		return nil, ErrUnavailable
	}
	fence := view.fence
	return func(callCtx context.Context) error {
		if err := callCtx.Err(); err != nil {
			return err
		}
		for attempt := 0; attempt < 2; attempt++ {
			current := s.coordinator.view.Load()
			if current == nil || current.fence != fence || !s.coordinator.now().Before(current.deadline) {
				return ErrUnavailable
			}
			if s.static[a.ID] == nil {
				account, ok := current.accounts[a.HubAccountID]
				if !ok || account.Status != "active" || account.Generation != a.HubGeneration {
					return ErrUnavailable
				}
				if account.Quarantined {
					return ErrQuarantined
				}
				record := current.records[a.HubAccountID]
				if record.Generation != a.HubGeneration || !bytes.Equal(record.Data, a.HubBaseline) {
					return ErrUnavailable
				}
			}
			if s.coordinator.view.Load() == current {
				return nil
			}
			// A concurrent heartbeat can replace an equivalent view. Recheck it
			// locally; a lost fence, changed account or expired lease still refuses.
		}
		return ErrUnavailable
	}, nil
}
