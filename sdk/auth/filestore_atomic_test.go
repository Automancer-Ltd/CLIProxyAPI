package auth

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A mirror holding the prior file must retain complete JSON while saves publish
// a replacement. Truncation would instead expose the new or partial document.
func TestFileTokenSavePublishesCompleteReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(path, []byte(`{"type":"claude","access_token":"old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	prior, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer prior.Close()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	if _, err = store.Save(context.Background(), &coreauth.Auth{ID: "fixture.json", Metadata: map[string]any{"type": "claude", "access_token": "new"}}); err != nil {
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
		t.Fatal("save rewrote the file held by a concurrent mirror")
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
		t.Fatal("new credential not published")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions widened")
	}
}
