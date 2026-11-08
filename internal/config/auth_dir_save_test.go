package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The standby hub mirrors m6's config byte for byte; a saved absolute home path
// stops it starting on another host (t15021).
func TestSaveConfigPreserveComments_KeepsPortableAuthDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, raw := range []string{"oauth:\n  auth-dir: ~/.local/hub/auths-native\n", "auth-dir: ~/.local/hub/auths-native\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := ParseConfigBytes([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		cfg.AuthDir = filepath.Join(home, ".local", "hub", "auths-native")
		if err = SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "auth-dir: ~/.local/hub/auths-native") || strings.Contains(string(data), home) {
			t.Fatalf("auth-dir not kept portable for %q:\n%s", raw, data)
		}
		cfg.AuthDir = filepath.Join(home, "elsewhere")
		if err = SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatal(err)
		}
		if data, _ = os.ReadFile(path); !strings.Contains(string(data), cfg.AuthDir) {
			t.Fatalf("changed auth-dir was not saved for %q:\n%s", raw, data)
		}
	}
}
