package productruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// Metadata has its own explicit ChatGPT client-authority route selecting the
// API account. No previous inference request establishes this authority.
func officialAPICatalogFixture(t *testing.T) accountReadFixture {
	t.Helper()
	f := newAccountReadFixtureWithDriver(t, providerauth.StaticHeaderDriverRef())
	const token = "synthetic-official-api-key-B"
	material, err := providerauth.NewMaterial(token, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	secret, err := secretstore.NewValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	account, err := f.runtime.accounts.Create(t.Context(), provideraccount.CreateCommand{ID: "official-api-b", DisplayName: "Official API B", UpstreamEndpointID: upstreamendpoint.OpenAIPlatformID, Driver: providerauth.StaticHeaderDriverRef(), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := f.runtime.endpoints.Get(t.Context(), upstreamendpoint.OpenAIPlatformID)
	if err != nil {
		t.Fatal(err)
	}
	route := &f.aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	route.ProviderTarget = environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}
	route.AccountPolicy.FixedAccountID = account.Account.ID.String()
	route.AccountPolicy.Accounts = []environment.RouteAccountReference{{ID: account.Account.ID.String(), Revision: environment.Revision(account.Account.Revision), DisplayName: account.Account.DisplayName}}
	f.account = account
	f.token = token
	return f
}

func TestOfficialAPICatalogExplicitRouteThroughCONNECT(t *testing.T) {
	f := officialAPICatalogFixture(t)
	proxyURL := f.serveProxy(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	request, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models?client_version=0.163.0", nil)
	request.Header.Set("Authorization", "Bearer original-native-A")
	request.Header.Set("Cookie", "original-cookie")
	request.Header.Set("Chatgpt-Account-Id", "original-A")
	request.Header.Set("If-None-Match", `"global"`)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(body) != managedCodexCatalogFixture {
		t.Fatalf("API catalog failed: status=%d body=%s err=%v", response.StatusCode, body, err)
	}
	if response.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(response.Header.Get("Etag"), `"vibermate-models-v1-`) {
		t.Fatal("catalog cache lost account scope")
	}
	f.wire.mu.Lock()
	defer f.wire.mu.Unlock()
	if len(f.wire.requests) != 1 {
		t.Fatalf("metadata sends=%d", len(f.wire.requests))
	}
	outbound := f.wire.requests[0]
	if outbound.URL.String() != "https://chatgpt.com/backend-api/codex/models?client_version=0.163.0" || outbound.Header.Get("Authorization") != "Bearer "+f.token || outbound.Header.Get("Cookie") != "" || outbound.Header.Get("Chatgpt-Account-Id") != "" || outbound.Header.Get("If-None-Match") != "" {
		t.Fatalf("metadata credential destination invalid: %s", outbound.URL)
	}
}

// Opt-in, separately pinned native contract. All auth is synthetic and every
// network request terminates at the loopback capture fixture.
func TestInstalledCodexOfficialAPIModelCatalog(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires explicitly selected Codex 0.163.0")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native path must be absolute")
	}
	home := t.TempDir()
	version := exec.CommandContext(t.Context(), binary, "--version")
	version.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home}
	output, err := version.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "codex-cli 0.163.0" {
		t.Skip("official API catalog acceptance is pinned to 0.163.0")
	}
	f := officialAPICatalogFixture(t)
	f.wire.catalogBody = `{"models":[{"slug":"task5-api-capability","display_name":"Task5 API capability","description":"Synthetic API catalog model","model_specialty":"cyber","available_access_programs":{"cyber":["standard","daybreak_blue","daybreak_red"]},"default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low","description":"Low"}],"shell_type":"shell_command","visibility":"list","minimal_client_version":[0,1,0],"supported_in_api":true,"priority":0,"supports_parallel_tool_calls":true,"supports_reasoning_summaries":true,"prefer_websockets":false,"base_instructions":"Synthetic fixture instructions.","support_verbosity":false,"default_verbosity":null,"apply_patch_tool_type":null,"truncation_policy":{"mode":"bytes","limit":10000},"supports_image_detail_original":false,"multi_agent_version":"v2","context_window":272000,"max_context_window":272000,"experimental_supported_tools":[]}]}`
	proxyURL := f.serveProxy(t)
	auth := []byte(`{"OPENAI_API_KEY":"synthetic-native-api-key-A"}`)
	authPath := filepath.Join(home, "auth.json")
	if err := os.WriteFile(authPath, auth, 0600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(home, "test-ca.pem")
	if err := os.WriteFile(rootPath, f.runtime.LocalRootCertificate().CertificatePEM(), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	args := []string{"app-server", "--stdio", "-c", `cli_auth_credentials_store="file"`, "-c", "analytics.enabled=false", "-c", "feedback.enabled=false", "-c", "features.api_key_model_discovery=true", "--disable", "respect_system_proxy", "--disable", "system_proxy_fallback"}
	if runtime.GOOS == "darwin" {
		args = append([]string{"-p", `(version 1) (allow default) (deny network*) (allow network-outbound (remote ip "localhost:*"))`, binary}, args...)
		binary = "/usr/bin/sandbox-exec"
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = home
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home, "HTTP_PROXY=" + proxyURL.String(), "HTTPS_PROXY=" + proxyURL.String(), "NO_PROXY=localhost,127.0.0.1", "SSL_CERT_FILE=" + rootPath}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cancel(); command.Wait() }()
	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	rpc := func(id int, method string, params any) json.RawMessage {
		if err := encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for scanner.Scan() {
			var reply struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &reply) != nil || reply.ID != id {
				continue
			}
			if len(reply.Error) != 0 {
				t.Fatalf("native %s: %s", method, reply.Error)
			}
			return reply.Result
		}
		t.Fatalf("native %s did not complete: %v", method, ctx.Err())
		return nil
	}
	rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "vibermate_acceptance", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
	if err := encoder.Encode(map[string]string{"method": "initialized"}); err != nil {
		t.Fatal(err)
	}
	models := rpc(2, "model/list", map[string]any{"includeHidden": true})
	if !bytes.Contains(models, []byte("task5-api-capability")) || !bytes.Contains(models, []byte("daybreakBlue")) || !bytes.Contains(models, []byte("daybreakRed")) {
		cancel()
		command.Wait()
		f.wire.mu.Lock()
		sends := len(f.wire.requests)
		f.wire.mu.Unlock()
		t.Fatalf("native API catalog missing exact model/modes: sends=%d stderr=%s", sends, stderr.String()+" models="+string(models))
	}
	saved, err := os.ReadFile(authPath)
	if err != nil || !bytes.Equal(saved, auth) {
		t.Fatal("native API login changed")
	}
	f.wire.mu.Lock()
	defer f.wire.mu.Unlock()
	if len(f.wire.requests) == 0 {
		t.Fatal("native model catalog did not reach explicit managed route")
	}
	for _, request := range f.wire.requests {
		if request.URL.Host != "chatgpt.com" || request.URL.Path != "/backend-api/codex/models" || request.URL.Query().Get("client_version") != "0.163.0" || request.Header.Get("Authorization") != "Bearer "+f.token {
			t.Fatalf("native API metadata lost fixed destination/account: %s", request.URL)
		}
	}
}
