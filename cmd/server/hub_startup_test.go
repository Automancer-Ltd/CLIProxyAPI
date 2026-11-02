package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exercise the deployed entry point: SDK-only tests miss the bootstrap host and
// the distinction between the local config and a storage-backed final config.
func TestServerHubAuthorityStartup(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		name             string
		bootstrapPlugins bool
		finalPlugins     bool
		storeConfig      bool
	}{
		{name: "clean local config"},
		{name: "clean git config", storeConfig: true},
		{name: "bootstrap plugins with guarded git config", bootstrapPlugins: true, storeConfig: true},
		{name: "plugins in final config", finalPlugins: true, storeConfig: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, data string) string {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			secret := write("fixture-secret", "synthetic-only")
			key := write("fixture-key", base64.StdEncoding.EncodeToString(make([]byte, 32)))
			authorityCalls := make(chan struct{}, 1)
			authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case authorityCalls <- struct{}{}:
				default:
				}
				http.Error(w, "synthetic authority unavailable", http.StatusServiceUnavailable)
			}))
			defer authority.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			cfg := fmt.Sprintf(`host: 127.0.0.1
port: %d
auth-dir: %q
remote-management:
  disable-control-panel: true
plugins:
  enabled: %t
  dir: %q
hub-authority:
  host: m6
  url: %q
  access-id-file: %q
  access-secret-file: %q
  b2-endpoint: 127.0.0.1:1
  b2-bucket: fixture
  b2-id-file: %q
  b2-key-file: %q
  encryption-key-file: %q
  spool: %q
`, port, filepath.Join(dir, "auths"), tc.finalPlugins, filepath.Join(dir, "plugins"), authority.URL,
				secret, secret, secret, secret, key, filepath.Join(dir, "spool"))
			configPath := write("config.yaml", cfg)
			env := []string{"HOME=" + dir, "PATH=" + os.Getenv("PATH"), "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "NO_PROXY=127.0.0.1"}
			if tc.storeConfig {
				remote := filepath.Join(dir, "remote")
				if err := os.MkdirAll(filepath.Join(remote, "config"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(remote, "config", "config.yaml"), []byte(cfg), 0600); err != nil {
					t.Fatal(err)
				}
				for _, args := range [][]string{{"init", "-q"}, {"add", "config/config.yaml"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"}} {
					git := exec.Command("git", args...)
					git.Dir, git.Env = remote, env
					if out, err := git.CombinedOutput(); err != nil {
						t.Fatalf("git fixture: %v\n%s", err, out)
					}
				}
				env = append(env, "GITSTORE_GIT_URL="+remote, "GITSTORE_LOCAL_PATH="+filepath.Join(dir, "store"))
				write("config.yaml", fmt.Sprintf("plugins:\n  enabled: %t\n  dir: %q\n", tc.bootstrapPlugins, filepath.Join(dir, "plugins")))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			server := exec.CommandContext(ctx, binary, "--config", configPath, "--local-model")
			server.Dir, server.Env = dir, env
			var output bytes.Buffer
			server.Stdout, server.Stderr = &output, &output
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- server.Wait() }()
			refused := tc.bootstrapPlugins || tc.finalPlugins
			select {
			case <-authorityCalls:
				if refused {
					cancel()
					<-done
					t.Fatalf("guarded hub contacted authority after plugins were enabled\n%s", output.String())
				}
				// The fixture denies ownership. The actual listener must still start
				// as a cold standby and report that it cannot serve credentials.
				client := &http.Client{Timeout: time.Second}
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/hub/ready", port))
					if err == nil {
						body, readErr := io.ReadAll(resp.Body)
						_ = resp.Body.Close()
						cancel()
						<-done
						if readErr != nil || resp.StatusCode != http.StatusServiceUnavailable || string(body) != `{"serving":false}` {
							t.Fatalf("unexpected readiness: status %d, body %s, error %v", resp.StatusCode, body, readErr)
						}
						return
					}
					select {
					case <-ticker.C:
					case err := <-done:
						t.Fatalf("server exited before readiness: %v\n%s", err, output.String())
					case <-ctx.Done():
						<-done
						t.Fatalf("server readiness timed out\n%s", output.String())
					}
				}
			case err := <-done:
				if !refused || !strings.Contains(output.String(), "shared hub authority cannot run Home or dynamic plugins") {
					t.Fatalf("unexpected server exit: %v\n%s", err, output.String())
				}
				select {
				case <-authorityCalls:
					t.Fatal("refused server contacted authority")
				default:
				}
			case <-ctx.Done():
				<-done
				t.Fatalf("server startup timed out\n%s", output.String())
			}
		})
	}
}
