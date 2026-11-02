package hubauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type CoverageRow struct {
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
	Count    int    `json:"count"`
}

// ConfigAuths validates startup combinations before creating clients, acquiring
// ownership or registering providers. Synthesis does no provider I/O.
func ConfigAuths(cfg *config.Config) ([]*coreauth.Auth, error) {
	if cfg == nil {
		return nil, errors.New("hub configuration missing")
	}
	if cfg.Home.Enabled || cfg.Plugins.Enabled {
		return nil, errors.New("hub authority cannot enroll Home or dynamic plugins")
	}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		return nil, errors.New("hub static route synthesis failed")
	}
	var s Store
	if err = s.ConfigureStatic(auths); err != nil {
		return nil, err
	}
	return auths, nil
}

// Preflight is a read-only migration inventory. It reads source files only for
// validation; startup never ingests copied files into the shared authority.
// Reports contain provider/mode counts, never identifiers or credential bytes.
func Preflight(cfg *config.Config, authDir string) ([]CoverageRow, error) {
	static, err := ConfigAuths(cfg)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(authDir)
	if err != nil {
		return nil, errors.New("hub preflight cannot read source auth directory")
	}
	counts := map[string]int{}
	add := func(provider, mode string) { counts[provider+"\x00"+mode]++ }
	for _, a := range static {
		add(a.Label, "static-config")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("hub preflight requires regular source credential files")
		}
		raw, err := os.ReadFile(filepath.Join(authDir, entry.Name()))
		if err != nil {
			return nil, errors.New("hub preflight cannot read source credential")
		}
		var meta map[string]any
		if json.Unmarshal(raw, &meta) != nil || meta == nil {
			return nil, errors.New("hub preflight found invalid credential JSON")
		}
		provider, _ := meta["type"].(string)
		a := &coreauth.Auth{Provider: provider, Metadata: meta}
		if err := ValidateCredential(a); err != nil {
			return nil, fmt.Errorf("hub preflight: %w", err)
		}
		mode := "mutable-login"
		if provider == "devin" {
			mode = "session-status"
		}
		if provider == "codex" {
			mode = "outer-oauth-http"
			if enabledFlag(meta["websockets"]) {
				mode = "outer-oauth-websocket"
			}
		}
		if meta["disabled"] == true {
			mode += "-disabled"
		}
		add(provider, mode)
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]CoverageRow, 0, len(keys))
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		out = append(out, CoverageRow{parts[0], parts[1], counts[key]})
	}
	return out, nil
}
