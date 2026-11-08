package config

import "testing"

func TestConfigMigrationPreservesHubAuthority(t *testing.T) {
	raw := []byte("port: 8317\nhub-authority:\n  host: m6\n  url: https://tracker.automancer.uk/v1/call\n  access-id-file: /fixture/id\n  access-secret-file: /fixture/secret\n  b2-endpoint: s3.eu-central-003.backblazeb2.com\n  b2-bucket: automancer-offsite\n  b2-id-file: /fixture/b2-id\n  b2-key-file: /fixture/b2-key\n  encryption-key-file: /fixture/encryption\n  spool: /fixture/spool\n")
	migrated, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HubAuthority == nil || cfg.HubAuthority.Host != "m6" || cfg.HubAuthority.AccessSecretFile != "/fixture/secret" || cfg.HubAuthority.Spool != "/fixture/spool" {
		t.Fatal("config migration silently disabled shared credential authority")
	}
}
