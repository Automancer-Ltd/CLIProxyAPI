package handlers

import (
	"context"
	"encoding/json"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Not parallel: the Antigravity pairing is process-wide.
func TestModelListingFollowsAntigravityPairing(t *testing.T) {
	const (
		shared     = "list-pair-claude-shared"
		claudeOnly = "list-pair-claude-only"
		agyOnly    = "list-pair-gemini-agy"
		unknown    = "list-pair-unregistered"
	)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("list-pair-claude", "claude", []*registry.ModelInfo{{ID: shared}, {ID: claudeOnly}})
	reg.RegisterClient("list-pair-agy", "antigravity", []*registry.ModelInfo{{ID: shared}, {ID: agyOnly}})
	t.Cleanup(func() {
		reg.UnregisterClient("list-pair-claude")
		reg.UnregisterClient("list-pair-agy")
	})
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{SDKConfig: internalconfig.SDKConfig{AntigravityKeys: []string{"agy-key"}}})
	t.Cleanup(func() { manager.SetConfig(&internalconfig.Config{}) })

	openaiList := `{"object":"list","data":[{"id":"` + shared + `"},{"id":"` + claudeOnly + `"},{"id":"` + agyOnly + `"},{"id":"` + unknown + `"}]}`
	geminiList := `{"models":[{"name":"models/` + shared + `"},{"name":"models/` + claudeOnly + `"},{"name":"models/` + agyOnly + `"}]}`
	ids := func(body []byte) []string {
		var catalog struct {
			Data   []struct{ ID string }   `json:"data"`
			Models []struct{ Name string } `json:"models"`
		}
		if err := json.Unmarshal(body, &catalog); err != nil {
			t.Fatalf("filtered body is not JSON: %v", err)
		}
		var out []string
		for _, m := range catalog.Data {
			out = append(out, m.ID)
		}
		for _, m := range catalog.Models {
			out = append(out, m.Name)
		}
		return out
	}
	for _, tc := range []struct {
		key, body string
		want      []string
	}{
		{"agy-key", openaiList, []string{shared, agyOnly}},
		{"other-key", openaiList, []string{shared, claudeOnly, unknown}},
		{"agy-key", geminiList, []string{"models/" + shared, "models/" + agyOnly}},
		{"other-key", geminiList, []string{"models/" + shared, "models/" + claudeOnly}},
	} {
		ctx := coreusage.WithAPIKey(context.Background(), tc.key)
		got := ids(filterModelListForKey(ctx, []byte(tc.body)))
		if len(got) != len(tc.want) {
			t.Fatalf("key %s listing = %v, want %v", tc.key, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("key %s listing = %v, want %v", tc.key, got, tc.want)
			}
		}
	}

	manager.SetConfig(&internalconfig.Config{})
	if got := ids(filterModelListForKey(coreusage.WithAPIKey(context.Background(), "other-key"), []byte(openaiList))); len(got) != 4 {
		t.Fatalf("after reload without paired keys listing = %v, want all four", got)
	}
}
