package runlauncher_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

// The proxy rejects every native request, including CONNECT, without opening an
// upstream socket. Auth fixtures are synthetic and each child owns a fresh home.
func TestInstalledCodexObservesNativeDestinationsThroughLauncher(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("set VIBERMATE_CODEX_ACCEPTANCE to an absolute native Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native Codex binary must be absolute")
	}
	probeContext, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer probeCancel()
	version := exec.CommandContext(probeContext, binary, "--version")
	version.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "CODEX_HOME=" + t.TempDir()}
	output, err := version.Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(strings.TrimSpace(string(output)))
	idToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"synthetic@example.invalid","exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic-account","chatgpt_plan_type":"plus"}}`)) + ".synthetic"
	for _, test := range []struct {
		name, auth, config, want string
		extra                    []string
	}{
		{name: "stored API key", auth: `{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-file-api-key"}`, want: "api.openai.com:443"},
		{name: "stored ChatGPT tokens", auth: `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":` + string(mustJSON(t, idToken)) + `,"access_token":"synthetic-access-token","refresh_token":"synthetic-refresh-token","account_id":"synthetic-account"},"last_refresh":` + string(mustJSON(t, time.Now().UTC().Format(time.RFC3339))) + `}`, want: "chatgpt.com:443"},
		{name: "custom persisted provider", config: "model_provider=\"custom\"\n[model_providers.custom]\nname=\"Synthetic\"\nbase_url=\"https://custom.invalid/v1\"\nwire_api=\"responses\"\nrequires_openai_auth=false\n", want: "custom.invalid:443"},
		{name: "explicit base", auth: `{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-file-api-key"}`, extra: []string{"CODEX_BASE_URL=https://explicit.invalid/v1"}, want: "explicit.invalid:443"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			config := "cli_auth_credentials_store=\"file\"\nanalytics.enabled=false\nfeedback.enabled=false\ncheck_for_update_on_startup=false\nfeatures.apps=false\nfeatures.remote_plugin=false\n" + test.config
			if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			if test.auth != "" {
				if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(test.auth), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := []string{"codex", "exec", "--skip-git-repo-check", "--ephemeral", "--model", "gpt-5.2", "synthetic no-network fixture"}
			control := &controlFixture{t: t, executable: binary, workspace: directory, rootPath: filepath.Join(directory, "root.pem"), credential: capability(0x11), proxy: capability(0x22), run: capability(0x33), expectedCommand: command, recipe: clientadapter.LaunchCodexResponsesHTTP, recognition: clientadapter.RecognitionVerified, authorities: []string{"api.openai.com:443", "chatgpt.com:443", "explicit.invalid:443"}, adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{ID: "codex-cli", Revision: 1, Version: "synthetic-recipe", CatalogRevision: 7, Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP}, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault}}
			observed := make(chan string, 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect && strings.HasPrefix(r.URL.Path, "/api/v1/") {
					control.ServeHTTP(w, r)
					return
				}
				select {
				case observed <- r.Host:
				default:
				}
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer server.Close()
			launcher, err := runlauncher.New(runlauncher.Config{Discovery: fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}}, BaseEnvironment: append([]string{"PATH=/usr/bin:/bin", "HOME=" + directory, "CODEX_HOME=" + directory}, test.extra...), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, Getwd: func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return binary, nil }})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: command})
			}()
			var hosts []string
			matched := false
			for !matched {
				select {
				case host := <-observed:
					hosts = append(hosts, host)
					matched = host == test.want
				case <-done:
					t.Fatalf("native client exited without %s; observed %v", test.want, hosts)
				case <-ctx.Done():
					<-done
					t.Fatalf("native destination timeout; observed %v", hosts)
				}
			}
			cancel()
			<-done
			t.Logf("rejected native destinations (may include startup requests): %v", hosts)
		})
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
