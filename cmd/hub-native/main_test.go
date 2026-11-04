package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPrepareConfigPreservesNativeNamesAndRemovesOnlyTrialAliases(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	output := filepath.Join(dir, "config.yaml")
	raw := `config-version: 8
hub-authority: {host: m6}
oauth:
  auth-dir: /primary
  model-alias:
    claude:
      - {name: claude-haiku-5-5, alias: cheap-claude, fork: true}
      - {name: claude-haiku-5-5, alias: haiku, fork: true}
    codex:
      - {name: gpt-6-luna, alias: cheap-codex, fork: true}
      - {name: gpt-6-luna, alias: codex-luna, fork: true}
server: {port: 8317}
`
	if err := os.WriteFile(source, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(source, output, "/standby", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["hub-authority"]; ok {
		t.Fatal("old authority retained")
	}
	text := string(data)
	for _, name := range []string{"cheap-claude", "cheap-codex", "/primary"} {
		if strings.Contains(text, name) {
			t.Fatalf("retained %s", name)
		}
	}
	for _, name := range []string{"haiku", "codex-luna", "claude-haiku-5-5", "gpt-6-luna", "/standby"} {
		if !strings.Contains(text, name) {
			t.Fatalf("lost %s", name)
		}
	}
}
