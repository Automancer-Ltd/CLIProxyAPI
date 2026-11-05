package handlers

import (
	"context"
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// restrictedAccess returns the request's Antigravity pairing role when it limits providers.
func restrictedAccess(ctx context.Context) (coreauth.ProviderAccess, bool) {
	role := coreauth.ProviderAccessFromContext(ctx)
	return role, role != "" && role != coreauth.ProviderAccessOpen
}

// ProviderKeep reports whether a model served by providers is visible to the request's key.
func ProviderKeep(ctx context.Context) func(providers []string) bool {
	role, restricted := restrictedAccess(ctx)
	return func(providers []string) bool {
		if !restricted {
			return true
		}
		for _, provider := range providers {
			if role.AllowsProvider(provider) {
				return true
			}
		}
		return false
	}
}

// ListedModels returns the catalog the request's client key may see (access.antigravity-keys):
// the registry filters on provider identity in the same snapshot it lists from, before any
// protocol transform renames an id.
func ListedModels(ctx context.Context, handlerType string) []map[string]any {
	reg := registry.GetGlobalRegistry()
	if _, restricted := restrictedAccess(ctx); !restricted {
		return reg.GetAvailableModels(handlerType)
	}
	return reg.GetAvailableModelsWhere(handlerType, ProviderKeep(ctx))
}

// ListedModelInfos is ListedModels for callers that need model metadata.
func ListedModelInfos(ctx context.Context) []*registry.ModelInfo {
	return registry.GetGlobalRegistry().GetAvailableModelInfosWhere(ProviderKeep(ctx))
}

// catalogEntryID returns a catalog entry's id in any of the formats the hub writes.
func catalogEntryID(entry json.RawMessage) string {
	for _, field := range []string{"id", "slug", "name"} {
		if id := gjson.GetBytes(entry, field).String(); id != "" {
			return id
		}
	}
	return ""
}

// catalogIDs returns the ids in a catalog body ("data" or "models").
func catalogIDs(body []byte) map[string]struct{} {
	ids := make(map[string]struct{})
	var catalog map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil {
		return ids
	}
	for _, field := range []string{"data", "models"} {
		var entries []json.RawMessage
		if json.Unmarshal(catalog[field], &entries) == nil {
			for _, entry := range entries {
				ids[catalogEntryID(entry)] = struct{}{}
			}
		}
	}
	return ids
}

// keepCatalogIDs drops catalog entries whose id is not in allowed and recomputes the
// first_id/last_id pagination fields. A body it cannot read becomes an empty catalog:
// under restricted access an unverifiable catalog fails closed.
func keepCatalogIDs(body []byte, allowed map[string]struct{}) []byte {
	var catalog map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil {
		return []byte(`{"data":[]}`)
	}
	for _, field := range []string{"data", "models"} {
		var entries []json.RawMessage
		if json.Unmarshal(catalog[field], &entries) != nil {
			continue
		}
		kept := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			if _, ok := allowed[catalogEntryID(entry)]; ok {
				kept = append(kept, entry)
			}
		}
		catalog[field], _ = json.Marshal(kept)
		if field == "data" {
			for key, index := range map[string]int{"first_id": 0, "last_id": len(kept) - 1} {
				if _, present := catalog[key]; !present {
					continue
				}
				catalog[key] = json.RawMessage(`null`)
				if len(kept) > 0 {
					catalog[key], _ = json.Marshal(catalogEntryID(kept[index]))
				}
			}
		}
	}
	filtered, errMarshal := json.Marshal(catalog)
	if errMarshal != nil {
		return []byte(`{"data":[]}`)
	}
	return filtered
}

// RequestContext returns the request's context, or a background one when there is no request.
func RequestContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}
