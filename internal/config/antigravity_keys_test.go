package config

import "testing"

func TestAntigravityKeysLoadFromAccess(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("config-version: 8\naccess:\n  api-keys: [normal]\n  antigravity-keys: [agy]\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if len(cfg.AntigravityKeys) != 1 || cfg.AntigravityKeys[0] != "agy" || len(cfg.APIKeys) != 1 {
		t.Fatalf("api-keys %v, antigravity-keys %v; want [normal] and [agy]", cfg.APIKeys, cfg.AntigravityKeys)
	}
}
