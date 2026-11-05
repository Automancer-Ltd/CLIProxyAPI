package handlers

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// ModelVisibleToKey applies the Antigravity pairing (access.antigravity-keys) to a model
// listing: a paired key sees only models Antigravity serves, and any other key does not
// see models that only Antigravity serves. Unknown ids stay visible to unpaired keys.
func ModelVisibleToKey(ctx context.Context, modelID string) bool {
	restricted, paired := coreauth.AntigravityAccess(ctx)
	if !restricted {
		return true
	}
	providers := registry.GetGlobalRegistry().GetModelProviders(strings.TrimPrefix(modelID, "models/"))
	if len(providers) == 0 {
		return !paired
	}
	for _, provider := range providers {
		if coreauth.AntigravityAllowsProvider(paired, provider) {
			return true
		}
	}
	return false
}

// filterModelListForKey drops the entries of a model catalog ("data" or "models")
// that ModelVisibleToKey hides from the request's client key.
func filterModelListForKey(ctx context.Context, body []byte) []byte {
	if restricted, _ := coreauth.AntigravityAccess(ctx); !restricted {
		return body
	}
	var catalog map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil {
		return body
	}
	for _, field := range []string{"data", "models"} {
		var entries []json.RawMessage
		if json.Unmarshal(catalog[field], &entries) != nil {
			continue
		}
		kept := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			id := gjson.GetBytes(entry, "id").String()
			if id == "" {
				id = gjson.GetBytes(entry, "slug").String()
			}
			if id == "" {
				id = gjson.GetBytes(entry, "name").String()
			}
			if ModelVisibleToKey(ctx, id) {
				kept = append(kept, entry)
			}
		}
		catalog[field], _ = json.Marshal(kept)
	}
	filtered, errMarshal := json.Marshal(catalog)
	if errMarshal != nil {
		return body
	}
	return filtered
}
