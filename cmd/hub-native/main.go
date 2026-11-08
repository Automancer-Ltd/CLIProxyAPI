// hub-native exports the current fenced snapshot to native files.
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
	output := flag.String("output", "", "new export directory (must not exist)")
	flag.Parse()
	log.SetOutput(io.Discard)
	if err := run(*source, *output); err != nil {
		fmt.Fprintln(os.Stderr, "hub-native:", err)
		os.Exit(1)
	}
}

func run(source, output string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("cannot read configuration")
	}
	var cfg config.Config
	if yaml.Unmarshal(raw, &cfg) != nil || output == "" {
		return fmt.Errorf("invalid configuration or output")
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
	shared := hubauth.NewStore(coordinator)
	ctx := context.Background()
	if err = coordinator.Step(ctx); err != nil {
		return fmt.Errorf("cannot acquire export authority; keep both hubs stopped and retry")
	}
	defer coordinator.Stop(ctx)
	if err = coordinator.Recover(ctx); err != nil {
		return fmt.Errorf("cannot reconcile current snapshot")
	}
	records, err := shared.List(ctx)
	if err != nil {
		return fmt.Errorf("cannot read current snapshot")
	}
	_, states, err := coordinator.Current(ctx)
	if err != nil {
		return fmt.Errorf("cannot read account states")
	}
	counts := map[string]int{"accounts": len(records), "active": 0, "held": 0, "quarantined": 0, "disabled": 0}
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(output)
	for _, a := range records {
		state := states[a.HubAccountID]
		switch {
		case state.Quarantined:
			counts["quarantined"]++
		case state.Status == "held":
			counts["held"]++
		case a.Disabled:
			counts["disabled"]++
		default:
			counts["active"]++
		}
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
	manifest, _ := json.Marshal(counts)
	if err = os.WriteFile(filepath.Join(output, "export-manifest"), manifest, 0600); err != nil {
		return err
	}
	fmt.Printf("exported %d current accounts\n", len(records))
	return nil
}
