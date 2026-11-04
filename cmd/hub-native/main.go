// hub-native prepares native hub config and exports the current fenced snapshot.
// It never contacts a model provider and never writes credential data to stdout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/hubauth"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

func main() {
	source := flag.String("config", "", "source configuration")
	output := flag.String("output", "", "new configuration file or empty export directory")
	authDir := flag.String("auth-dir", "", "native auth directory for config rendering")
	export := flag.Bool("export", false, "export current fenced shared snapshot; both provider processes must be stopped")
	flag.Parse()
	log.SetOutput(io.Discard)
	if err := run(*source, *output, *authDir, *export); err != nil {
		fmt.Fprintln(os.Stderr, "hub-native:", err)
		os.Exit(1)
	}
}

func run(source, output, authDir string, export bool) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("cannot read configuration")
	}
	var cfg config.Config
	if yaml.Unmarshal(raw, &cfg) != nil || output == "" {
		return fmt.Errorf("invalid configuration or output")
	}
	if !export {
		if authDir == "" {
			return fmt.Errorf("auth-dir required")
		}
		var doc map[string]any
		if yaml.Unmarshal(raw, &doc) != nil {
			return fmt.Errorf("invalid configuration")
		}
		delete(doc, "hub-authority")
		oauth, ok := doc["oauth"].(map[string]any)
		if !ok {
			return fmt.Errorf("native v8 oauth configuration required")
		}
		oauth["auth-dir"] = authDir
		if groups, ok := oauth["model-alias"].(map[string]any); ok {
			for group, value := range groups {
				aliases, ok := value.([]any)
				if !ok {
					return fmt.Errorf("invalid alias list")
				}
				kept := []any{}
				for _, value := range aliases {
					alias, ok := value.(map[string]any)
					if !ok {
						return fmt.Errorf("invalid alias")
					}
					switch alias["alias"] {
					case "cheap-claude", "medium-claude", "premium-claude", "cheap-codex", "medium-codex", "premium-codex":
						continue
					}
					kept = append(kept, value)
				}
				groups[group] = kept
			}
		}
		data, err := yaml.Marshal(doc)
		if err != nil {
			return fmt.Errorf("cannot render configuration")
		}
		if err = os.WriteFile(output, data, 0600); err != nil {
			return fmt.Errorf("cannot write configuration")
		}
		return nil
	}
	h := cfg.HubAuthority
	if h == nil {
		return fmt.Errorf("shared authority configuration required for export")
	}
	// Exclusive output avoids accidentally mixing current grants with stale files.
	if err = os.Mkdir(output, 0700); err != nil {
		return fmt.Errorf("export directory must not exist")
	}
	client, err := hubauth.NewClient(h.URL, h.AccessIDFile, h.AccessSecretFile)
	if err != nil {
		return err
	}
	blobs, err := hubauth.NewB2(h.B2Endpoint, h.B2Bucket, h.B2IDFile, h.B2KeyFile)
	if err != nil {
		return err
	}
	objects, err := hubauth.NewObjects(blobs, h.EncryptionKeyFile, h.Spool)
	if err != nil {
		return err
	}
	coordinator, err := hubauth.NewCoordinator(client, objects, h.Host)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err = coordinator.Step(ctx); err != nil {
		return fmt.Errorf("cannot acquire export authority; keep both hubs stopped and retry")
	}
	defer coordinator.Stop(ctx)
	if err = coordinator.Recover(ctx); err != nil {
		return fmt.Errorf("cannot reconcile current snapshot")
	}
	records, err := hubauth.NewStore(coordinator).List(ctx)
	if err != nil {
		return fmt.Errorf("cannot read current snapshot")
	}
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(output)
	for _, a := range records {
		// Use the encrypted account UUID as the filename, never an untrusted stored path.
		if a.HubAccountID == "" || strings.ContainsAny(a.HubAccountID, `/\\`) {
			return fmt.Errorf("invalid account identity")
		}
		a.ID = a.HubAccountID + ".json"
		a.FileName = a.ID
		a.Attributes = nil
		a.HubAuthority = nil
		a.Metadata["type"] = a.Provider
		for k, v := range map[string]any{"disabled": a.Disabled, "label": a.Label, "prefix": a.Prefix, "proxy_url": a.ProxyURL} {
			a.Metadata[k] = v
		}
		if _, err = store.Save(coreauth.WithAuthCreationIntent(ctx), a); err != nil {
			return fmt.Errorf("cannot export credential")
		}
	}
	// A manifest proves complete output without exposing accounts or secrets.
	manifest, _ := json.Marshal(map[string]int{"accounts": len(records)})
	if err = os.WriteFile(filepath.Join(output, "export-manifest"), manifest, 0600); err != nil {
		return err
	}
	fmt.Printf("exported %d current accounts\n", len(records))
	return nil
}
