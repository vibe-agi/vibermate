//go:build darwin

package runlauncher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstalledCodexStableExecCyberSyntax(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected native Codex")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native binary must be absolute")
	}
	home := t.TempDir()
	environment := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home}
	version, err := probeCodex(t.Context(), binary, []string{"--version"}, environment, home, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(version)) != "codex-cli 0.163.0" {
		t.Skip("Cyber syntax acceptance is pinned separately to 0.163.0")
	}
	for _, value := range []string{"standard", "daybreak_blue", "daybreak_red"} {
		for _, args := range [][]string{{"exec", "--cyber-access-program", value, "--help"}, {"exec", "resume", "--last", "--cyber-access-program=" + value, "--help"}} {
			output, err := probeCodex(t.Context(), binary, args, environment, home, 5*time.Second)
			if err != nil || !strings.Contains(string(output), "--cyber-access-program") {
				t.Fatalf("native Cyber syntax %v failed: %v", args, err)
			}
		}
	}
}

func TestInstalledCodexProxyGuardPreservesUnknownFeatureErrors(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected native Codex")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native binary must be absolute")
	}
	home := t.TempDir()
	environment := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	args, err := prepareCodexProxyArguments(ctx, binary, []string{"--enable", "vibermate_fixture_unknown_feature", "app-server", "--stdio"}, environment)
	if err != nil {
		t.Fatal(err)
	}
	args = append([]string{"-p", `(version 1) (allow default) (deny network*)`, binary}, args...)
	command := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", args...)
	command.Env, command.Dir = environment, home
	output, err := command.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("Unknown feature flag: vibermate_fixture_unknown_feature")) {
		t.Fatalf("native unknown-feature rejection changed: err=%v output=%s", err, output)
	}
}

// Both candidate routes terminate on loopback. The first proxy drops the
// request; the second server proves native bootstrap can choose OS Direct
// rather than the injected environment proxy. The sandbox forbids all remote
// traffic without changing the machine's OS proxy settings.
func TestInstalledCodexBootstrapProxyFallbackStaysCaptureOwned(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected native Codex")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native binary must be absolute")
	}
	probeHome := t.TempDir()
	version, err := probeCodex(t.Context(), binary, []string{"--version"}, []string{"PATH=/usr/bin:/bin", "HOME=" + probeHome, "CODEX_HOME=" + probeHome}, probeHome, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(version)) != "codex-cli 0.163.0" {
		t.Skip("bootstrap fallback acceptance is pinned separately to 0.163.0")
	}
	for _, guarded := range []bool{false, true} {
		name := "native_fallback_control"
		if guarded {
			name = "capture_owned"
		}
		t.Run(name, func(t *testing.T) {
			var directCalls, proxyCalls atomic.Int32
			direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				directCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/config/bundle") {
					_, _ = io.WriteString(w, `{"requirements_toml":{"enterprise_managed":[{"id":"fixture-policy","name":"Fixture approvals","contents":"allowed_approval_policies = [\"never\"]"}]}}`)
					return
				}
				_, _ = io.WriteString(w, `{"accounts":[{"id":"workspace-A","workspace_backend_origin":"https://chatgpt.com","account_routing_override":"NO_CONSTRAINT"}],"default_account_id":"workspace-A"}`)
			}))
			defer direct.Close()
			proxy, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer proxy.Close()
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				for {
					connection, err := proxy.Accept()
					if err != nil {
						return
					}
					proxyCalls.Add(1)
					_ = connection.Close()
				}
			}()
			t.Cleanup(func() { _ = proxy.Close(); <-stopped })
			home := t.TempDir()
			claims, _ := json.Marshal(map[string]any{"sub": "user-A", "email": "synthetic-A@example.invalid", "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-A", "chatgpt_user_id": "user-A", "chatgpt_plan_type": "enterprise"}})
			token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
			auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"id_token": token, "access_token": token, "refresh_token": "synthetic-refresh", "account_id": "workspace-A"}, "last_refresh": time.Now().UTC().Format(time.RFC3339Nano)})
			if err := os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600); err != nil {
				t.Fatal(err)
			}
			config := "cli_auth_credentials_store=\"file\"\nchatgpt_base_url=" + strconv.Quote(direct.URL+"/backend-api") + "\nanalytics.enabled=false\nfeedback.enabled=false\ncheck_for_update_on_startup=false\nfeatures.apps=false\nfeatures.remote_plugin=false\nfeatures.respect_system_proxy=false\nfeatures.system_proxy_fallback=true\n"
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			environment := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home, "HTTP_PROXY=http://" + proxy.Addr().String(), "HTTPS_PROXY=http://" + proxy.Addr().String(), "NO_PROXY=", "no_proxy="}
			arguments := []string{"app-server", "--stdio"}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if guarded {
				arguments, err = prepareCodexProxyArguments(ctx, binary, arguments, environment)
				if err != nil {
					t.Fatal(err)
				}
			}
			arguments = append([]string{"-p", `(version 1) (allow default) (deny network*) (allow network-outbound (remote ip "localhost:*"))`, binary}, arguments...)
			command := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", arguments...)
			command.Env, command.Dir = environment, home
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = io.Discard
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); cancel(); _ = command.Wait() }()
			scanner := bufio.NewScanner(output)
			scanner.Buffer(make([]byte, 4096), 2<<20)
			encoder := json.NewEncoder(input)
			read := func(id int, method string, params any) (json.RawMessage, json.RawMessage) {
				t.Helper()
				if err := encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
					t.Fatal(err)
				}
				for scanner.Scan() {
					var response struct {
						ID     int             `json:"id"`
						Result json.RawMessage `json:"result"`
						Error  json.RawMessage `json:"error"`
					}
					if json.Unmarshal(scanner.Bytes(), &response) == nil && response.ID == id {
						return response.Result, response.Error
					}
				}
				t.Fatalf("native %s did not finish (context=%v)", method, ctx.Err())
				return nil, nil
			}
			_, initialError := read(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "vibermate_proxy_fixture", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
			if len(initialError) != 0 {
				t.Fatal("native initialize failed")
			}
			if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
				t.Fatal(err)
			}
			_, accountError := read(2, "account/read", map[string]bool{"refreshToken": false})
			if guarded {
				if !bytes.Contains(accountError, []byte("failed to load workspace requirements")) || directCalls.Load() != 0 {
					t.Fatalf("capture bootstrap escaped proxy: direct=%d proxy=%d error=%s", directCalls.Load(), proxyCalls.Load(), accountError)
				}
			} else if len(accountError) != 0 || directCalls.Load() == 0 {
				t.Fatalf("native fallback control did not reach alternate direct route: direct=%d proxy=%d error=%s", directCalls.Load(), proxyCalls.Load(), accountError)
			}
			if proxyCalls.Load() == 0 {
				t.Fatal("native bootstrap never used the injected proxy")
			}
			t.Logf("bootstrap routes: guarded=%v capture=%d alternate-direct=%d", guarded, proxyCalls.Load(), directCalls.Load())
		})
	}
}
