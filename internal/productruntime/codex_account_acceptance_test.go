package productruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in: VIBERMATE_CODEX_ACCEPTANCE=/absolute/path/to/codex go test
// ./internal/productruntime -run TestInstalledCodexReadsManagedAccount -count=1.
// Never opens the user's CODEX_HOME, keychain, or live provider accounts.
func TestInstalledCodexReadsManagedAccount(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected local Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("acceptance CLI path must be absolute")
	}
	f := newAccountReadFixture(t)
	f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AllowAccountHistory = true
	proxyURL := f.serveProxy(t)
	directory := t.TempDir()
	claims, _ := json.Marshal(map[string]any{
		"email": "original-a@example.com", "sub": "user-A", "exp": time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-A", "chatgpt_user_id": "user-A", "chatgpt_plan_type": "plus"},
	})
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens":       map[string]any{"id_token": token, "access_token": token, "refresh_token": "synthetic-A-refresh", "account_id": "workspace-A"},
		"last_refresh": time.Now().UTC().Format(time.RFC3339Nano)})
	authPath := filepath.Join(directory, "auth.json")
	if err := os.WriteFile(authPath, auth, 0600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(directory, "test-ca.pem")
	if err := os.WriteFile(rootPath, f.runtime.LocalRootCertificate().CertificatePEM(), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "app-server", "--stdio", "-c", `cli_auth_credentials_store="file"`, "-c", "analytics.enabled=false", "-c", "feedback.enabled=false")
	command.Dir = directory
	command.Env = []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "CODEX_HOME=" + directory, "HTTP_PROXY=" + proxyURL.String(), "HTTPS_PROXY=" + proxyURL.String(), "NO_PROXY=localhost,127.0.0.1", "SSL_CERT_FILE=" + rootPath}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); cancel(); _ = command.Wait() }()
	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	nextID := 0
	rpc := func(method string, params any) json.RawMessage {
		nextID++
		if err := encoder.Encode(map[string]any{"id": nextID, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for scanner.Scan() {
			var reply struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &reply) != nil || reply.ID != nextID {
				continue
			}
			if reply.Error != nil {
				t.Fatalf("native %s rejected request: RPC %d", method, reply.Error.Code)
			}
			return reply.Result
		}
		t.Fatalf("native %s did not complete (context=%v)", method, ctx.Err())
		return nil
	}
	rpc("initialize", map[string]any{"clientInfo": map[string]string{"name": "vibermate_acceptance", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
	if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
		t.Fatal(err)
	}
	local := rpc("account/read", map[string]bool{"refreshToken": false})
	if !bytes.Contains(local, []byte("original-a@example.com")) {
		t.Fatal("native local identity no longer reports A")
	}
	quota := rpc("account/rateLimits/read", nil)
	if !bytes.Contains(quota, []byte(`"usedPercent":25`)) {
		t.Fatal("native CLI did not project the managed B quota")
	}
	history := rpc("account/usage/read", nil)
	if !bytes.Contains(history, []byte("1200")) {
		t.Fatal("native CLI did not project the explicitly authorized B history")
	}
	saved, err := os.ReadFile(authPath)
	if err != nil || !bytes.Equal(saved, auth) {
		t.Fatal("client auth file was changed")
	}
	f.wire.mu.Lock()
	defer f.wire.mu.Unlock()
	if len(f.wire.requests) < 2 {
		t.Fatal("native account reads did not reach the controlled provider")
	}
	for _, request := range f.wire.requests {
		if request.Header.Get("Authorization") != "Bearer "+f.token || request.Header.Get("Chatgpt-Account-Id") != "workspace-B" {
			t.Fatal("native request escaped managed B identity")
		}
	}
}
