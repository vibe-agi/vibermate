package productruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

// Opt-in: VIBERMATE_CODEX_ACCEPTANCE=/absolute/path/to/codex go test
// ./internal/productruntime -run TestInstalledCodexReadsManagedAccount -count=1.
// Never opens the user's CODEX_HOME, keychain, or live provider accounts.
func TestInstalledCodexReadsManagedAccount(t *testing.T) {
	testInstalledCodexReadsManagedAccount(t, "plus")
}

func TestInstalledCodexReadsEnterpriseWorkspaceRequirements(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected native Codex")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("acceptance CLI path must be absolute")
	}
	home := t.TempDir()
	command := exec.CommandContext(t.Context(), binary, "--version")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home}
	version, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(version)) != "codex-cli 0.163.0" {
		t.Skip("Enterprise bundle acceptance is pinned separately to 0.163.0")
	}
	testInstalledCodexReadsManagedAccount(t, "enterprise")
}

func testInstalledCodexReadsManagedAccount(t *testing.T, plan string) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected local Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("acceptance CLI path must be absolute")
	}
	f := newAccountReadFixtureWithDriver(t, providerauth.CodexOAuthDriverRef())
	f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AllowAccountHistory = true
	f.aggregate.ClientEndpoints[0].ProtocolPlans[0].EgressProfile = egressprofile.Inherit()
	if _, err := f.runtime.accounts.SetSettings(t.Context(), provideraccount.SettingsCommand{ID: f.account.Account.ID, ExpectedRevision: f.account.Account.SettingsRevision, EgressProfile: egressprofile.Direct(), AutomaticRefresh: f.account.Account.AutomaticRefresh, CyberAccessProgram: f.account.Account.CyberAccessProgram}); err != nil {
		t.Fatal(err)
	}
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:9"}
	profile, err := f.runtime.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.workspace-a", DisplayName: "Synthetic workspace exit", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
		t.Fatal(err)
	}
	original := &codexWorkspaceOriginal{t: t, policy: policy}
	proxyURL := f.serveProxyWithOriginal(t, original)
	directory := t.TempDir()
	claims, _ := json.Marshal(map[string]any{
		"email": "original-a@example.com", "sub": "user-A", "exp": time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-A", "chatgpt_user_id": "user-A", "chatgpt_plan_type": plan},
	})
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	original.token = token
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
	arguments := []string{"app-server", "--stdio", "-c", `cli_auth_credentials_store="file"`, "-c", "analytics.enabled=false", "-c", "feedback.enabled=false", "-c", "features.respect_system_proxy=false", "-c", "features.system_proxy_fallback=false"}
	if runtime.GOOS == "darwin" {
		arguments = append([]string{"-p", `(version 1) (allow default) (deny network*) (allow network-outbound (remote ip "localhost:*"))`, binary}, arguments...)
		binary = "/usr/bin/sandbox-exec"
	}
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Dir = directory
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "CODEX_HOME=" + directory, "HTTP_PROXY=" + proxyURL.String(), "HTTPS_PROXY=" + proxyURL.String(), "NO_PROXY=localhost,127.0.0.1", "SSL_CERT_FILE=" + rootPath}
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
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &reply) != nil || reply.ID != nextID {
				continue
			}
			if reply.Error != nil {
				t.Fatalf("native %s rejected request: RPC %d: %s", method, reply.Error.Code, reply.Error.Message)
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
	if plan == "enterprise" {
		var requirements struct {
			Requirements struct {
				AllowedApprovalPolicies []string        `json:"allowedApprovalPolicies"`
				FeatureRequirements     map[string]bool `json:"featureRequirements"`
			} `json:"requirements"`
		}
		if err := json.Unmarshal(rpc("configRequirements/read", nil), &requirements); err != nil || len(requirements.Requirements.AllowedApprovalPolicies) != 1 || requirements.Requirements.AllowedApprovalPolicies[0] != "never" || !requirements.Requirements.FeatureRequirements["system_proxy_fallback"] {
			t.Fatalf("native workspace did not apply original A requirements: %+v err=%v", requirements, err)
		}
	}
	quota := rpc("account/rateLimits/read", nil)
	if !bytes.Contains(quota, []byte(`"usedPercent":25`)) {
		t.Fatal("native CLI did not project the managed B quota")
	}
	quota = rpc("account/rateLimits/read", map[string]bool{"supportsLunaReserve": true, "excludeResetCreditDetails": false})
	if !bytes.Contains(quota, []byte(`"usedPercent":25`)) {
		t.Fatal("native TUI refresh did not project the managed B quota")
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
		if request.URL.Path == "/backend-api/wham/config/bundle" {
			t.Fatal("native workspace policy was read using managed B")
		}
		if request.Header.Get("Authorization") != "Bearer "+f.token || request.Header.Get("Chatgpt-Account-Id") != "workspace-B" {
			t.Fatal("native request escaped managed B identity")
		}
	}
	original.mu.Lock()
	defer original.mu.Unlock()
	if plan == "enterprise" && original.bundles == 0 || plan == "plus" && original.bundles != 0 {
		t.Fatalf("native %s bundle reads=%d", plan, original.bundles)
	}
}

// This fixture terminates original-origin traffic and has no outbound socket.
// Its nonempty policy makes replacing the bundle with a fabricated empty result
// observable in the native requirements reader.
type codexWorkspaceOriginal struct {
	t       *testing.T
	mu      sync.Mutex
	token   string
	bundles int
	policy  egressnetwork.Policy
}

func (fixture *codexWorkspaceOriginal) Do(ctx context.Context, request originaltransport.Request) (*http.Response, error) {
	if request.Path() != "/backend-api/wham/config/bundle" {
		return (accountFixtureOriginal{}).Do(ctx, request)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.bundles++
	if request.EgressPolicy() != fixture.policy {
		fixture.t.Error("workspace policy did not retain the selected original exit")
	}
	if request.Method() != "GET" || request.Origin().String() != "https://chatgpt.com" || request.RawQuery() != "" || len(request.Body()) != 0 || request.Headers().Get("Authorization") != "Bearer "+fixture.token || request.Headers().Get("Chatgpt-Account-Id") != "workspace-A" {
		fixture.t.Error("workspace policy lost original native identity or exact request scope")
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("invalid synthetic workspace scope"))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"requirements_toml":{"enterprise_managed":[{"id":"workspace-A-policy","name":"Workspace A approvals","contents":"allowed_approval_policies = [\"never\"]\n[features]\nsystem_proxy_fallback = true"}]}}`))}, nil
}
