package handlers

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestModelCatalogsFollowAntigravityPairing(t *testing.T) {
	const (
		shared     = "claude-list-pair-shared"
		claudeOnly = "claude-list-pair-only"
		agyOnly    = "list-pair-gemini-agy" // the Claude catalog cloaks this id
	)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("list-pair-claude", "claude", []*registry.ModelInfo{{ID: shared}, {ID: claudeOnly}})
	reg.RegisterClient("list-pair-agy", "antigravity", []*registry.ModelInfo{{ID: shared}, {ID: agyOnly}})
	t.Cleanup(func() {
		reg.UnregisterClient("list-pair-claude")
		reg.UnregisterClient("list-pair-agy")
	})
	roleCtx := func(role coreauth.ProviderAccess) context.Context {
		return coreauth.WithProviderAccess(context.Background(), string(role))
	}
	ours := func(models []map[string]any) []string {
		var ids []string
		for _, model := range models {
			id, _ := model["id"].(string)
			if strings.Contains(id, "list-pair") || strings.Contains(id, "riap-tsil") {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		return ids
	}
	for _, tc := range []struct {
		role coreauth.ProviderAccess
		want []string
	}{
		{coreauth.ProviderAccessAntigravityOnly, []string{shared, agyOnly}},
		{coreauth.ProviderAccessNoAntigravity, []string{claudeOnly, shared}},
		{coreauth.ProviderAccessOpen, []string{claudeOnly, shared, agyOnly}},
	} {
		got := ours(ListedModels(roleCtx(tc.role), "openai"))
		want := append([]string(nil), tc.want...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s listing = %v, want %v", tc.role, got, want)
		}
	}

	// The Claude catalog renames non-Claude ids; filtering before that transform keeps
	// the Antigravity-only model out for ordinary keys, and pagination names only
	// listed models.
	catalog := claudemodels.BuildResponse(ListedModels(roleCtx(coreauth.ProviderAccessNoAntigravity), "claude"), false)
	cloaked := claudemodels.EnsureClaudeModelIDPrefix(agyOnly)
	ids := map[string]bool{}
	for _, model := range catalog["data"].([]map[string]any) {
		ids[model["id"].(string)] = true
	}
	if ids[cloaked] || !ids[shared] || !ids[catalog["first_id"].(string)] || !ids[catalog["last_id"].(string)] {
		t.Fatalf("ordinary Claude catalog ids %v, first %v last %v; want %s hidden", ids, catalog["first_id"], catalog["last_id"], cloaked)
	}

	// An interceptor cannot add back an entry the key may not see; pagination is recomputed.
	allowed := catalogIDs([]byte(`{"data":[{"id":"` + shared + `"}],"first_id":"` + shared + `","last_id":"` + shared + `"}`))
	rewritten := keepCatalogIDs([]byte(`{"data":[{"id":"`+cloaked+`"},{"id":"`+shared+`"}],"first_id":"`+cloaked+`","last_id":"`+shared+`","has_more":false}`), allowed)
	var out struct {
		Data    []struct{ ID string } `json:"data"`
		FirstID string                `json:"first_id"`
		LastID  string                `json:"last_id"`
	}
	if err := json.Unmarshal(rewritten, &out); err != nil || len(out.Data) != 1 || out.Data[0].ID != shared || out.FirstID != shared || out.LastID != shared {
		t.Fatalf("after interceptor filter = %s (%v)", rewritten, err)
	}
	if string(keepCatalogIDs([]byte("not json"), allowed)) != `{"data":[]}` {
		t.Fatal("an unreadable intercepted catalog must fail closed")
	}
}
