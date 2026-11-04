package claude

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeTokenSavePublishesCompleteReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, []byte(`{"type":"claude","access_token":"old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	prior, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer prior.Close()
	token := &ClaudeTokenStorage{AccessToken: "new"}
	if err = token.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(prior)
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err = json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if old["access_token"] != "old" {
		t.Fatal("save modified the prior credential document")
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var current map[string]any
	if err = json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	if current["access_token"] != "new" {
		t.Fatal("new token not published")
	}
}
