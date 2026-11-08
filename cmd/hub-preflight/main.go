// hub-preflight checks enrollment without starting a service or contacting a provider.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/hubauth"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

func main() {
	path := flag.String("config", "", "source config YAML")
	dir := flag.String("auth-dir", "", "source auth directory (defaults to config oauth.auth-dir)")
	flag.Parse()
	log.SetOutput(io.Discard)
	fail := func(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
	if *path == "" {
		fail("hub preflight requires -config")
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		fail("hub preflight cannot read config")
	}
	var cfg config.Config
	// UnmarshalYAML understands native and legacy layouts. LoadConfig is deliberately
	// avoided: it can rewrite plaintext management keys in the source file.
	if yaml.Unmarshal(raw, &cfg) != nil {
		fail("hub preflight cannot parse config")
	}
	source := *dir
	if source == "" {
		source = cfg.AuthDir
	}
	source, err = util.ResolveAuthDir(source)
	if err != nil {
		fail("hub preflight cannot resolve auth directory")
	}
	rows, err := hubauth.Preflight(&cfg, source)
	if err != nil {
		fail(err.Error())
	}
	if err = json.NewEncoder(os.Stdout).Encode(rows); err != nil {
		fail("hub preflight output failed")
	}
}
