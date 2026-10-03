package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

// This catches routing through a pre-existing backend whose environment still
// holds the preceding Capture's proxy, even though the launcher child exits OK.
func TestCodexCaptureIsolationOwnsRequestsWithExistingBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires POSIX process groups")
	}
	directory := t.TempDir()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var oldRequests atomic.Int32
	oldProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oldRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer oldProxy.Close()
	ready := filepath.Join(directory, "backend-ready")
	backend := exec.Command(testBinary, "-test.run=^TestCodexIsolationFixtureProcess$")
	backend.Env = []string{"CODEX_ISOLATION_ROLE=backend", "CODEX_ISOLATION_READY=" + ready, "HTTP_PROXY=" + oldProxy.URL}
	if err := backend.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Process.Kill(); _ = backend.Wait() }()
	backendURL := waitCodexIsolationFile(t, ready)
	// Prove that this already-started backend owns and uses the old proxy.
	response, err := http.Get(backendURL + "/request")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if oldRequests.Load() != 1 {
		t.Fatal("old backend did not retain its proxy")
	}
	oldRequests.Store(0)
	executable := filepath.Join(directory, "codex")
	script := "#!/bin/sh\nexec " + strconv.Quote(testBinary) + " -test.run=^TestCodexIsolationFixtureProcess$ -- \"$@\"\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"resume", "fixture-thread"}, {"fork", "fixture-thread"}, {"exec", "fixture prompt"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			oldRequests.Store(0)
			command := append([]string{"codex"}, args...)
			alive := filepath.Join(directory, "new-backend-alive")
			control := &controlFixture{
				t: t, executable: executable, workspace: directory, rootPath: filepath.Join(directory, "root.pem"),
				credential: capability(0x11), proxy: capability(0x22), run: capability(0x33),
				expectedCommand: command, recipe: clientadapter.LaunchCodexResponsesHTTP, recognition: clientadapter.RecognitionVerified,
				adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{
					ID: "codex-cli", Revision: 1, Version: "0.160.0", CatalogRevision: 7,
					Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary,
					LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP,
				}, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault},
			}
			var currentRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Host == "codex-isolation-fixture.invalid" {
					if r.Header.Get("Proxy-Authorization") == "" {
						t.Error("request omitted current Capture capability")
					}
					currentRequests.Add(1)
					w.WriteHeader(http.StatusOK)
					return
				}
				control.ServeHTTP(w, r)
			}))
			defer server.Close()
			launcher, err := runlauncher.New(runlauncher.Config{
				Discovery: fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}},
				// The race-instrumented test binary otherwise waits one second in
				// os.Exit, unlike native CLI help; keep the real probe deadline.
				BaseEnvironment: []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0", "CODEX_ISOLATION_ROLE=client", "CODEX_ISOLATION_BACKEND=" + backendURL,
					"CODEX_ISOLATION_ALIVE=" + alive, "CODEX_ISOLATION_BINARY=" + testBinary},
				Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
				TerminationTimeout: 150 * time.Millisecond,
				Getwd:              func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return executable, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: command})
			if err != nil || code != 0 {
				t.Fatalf("launch: code=%d err=%v", code, err)
			}
			if currentRequests.Load() != 1 || oldRequests.Load() != 0 {
				t.Fatalf("Capture request ownership: new proxy=%d, old proxy=%d", currentRequests.Load(), oldRequests.Load())
			}
			if _, err := os.Stat(alive); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new backend outlived captured child: %v", err)
			}
			if control.attachCalls != 1 || control.finishCalls != 1 {
				t.Fatalf("control lifecycle attach=%d finish=%d", control.attachCalls, control.finishCalls)
			}
		})
	}
	response, err = http.Get(backendURL + "/alive")
	if err != nil {
		t.Fatalf("launcher terminated unrelated existing backend: %v", err)
	}
	response.Body.Close()
}

func waitCodexIsolationFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) != 0 {
			return string(data)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fixture did not become ready: %s", filepath.Base(path))
	return ""
}

func TestCodexCaptureIsolationRequiresExactLegacyEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX client fixture")
	}
	for _, test := range []struct {
		name, version             string
		recipe                    clientadapter.LaunchRecipe
		signer, withheld, unknown bool
		wantLaunch                bool
	}{
		{"verified legacy", "0.145.0", clientadapter.LaunchCodexResponsesHTTP, false, false, false, true},
		{"different exact release", "0.160.0", clientadapter.LaunchCodexResponsesHTTP, false, false, false, false},
		{"unproven release", "fixture", clientadapter.LaunchCodexResponsesHTTP, false, false, false, false},
		{"different launch recipe", "0.145.0", clientadapter.LaunchNodeEnvProxy, false, false, false, false},
		{"signer recognition only", "", clientadapter.LaunchCodexResponsesHTTP, true, false, false, false},
		{"legacy recipe withheld", "0.145.0", clientadapter.LaunchGeneric, false, true, false, false},
		{"unknown client is never probed", "", clientadapter.LaunchGeneric, false, false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			executable := filepath.Join(directory, "codex")
			started, probed := filepath.Join(directory, "started"), filepath.Join(directory, "probed")
			script := "#!/bin/sh\nif [ \"$1\" = --help ]; then\n touch " + strconv.Quote(probed) + "\n printf 'codex-cli 0.145.0\\nOptions:\\n'\n exit 0\nfi\ntouch " + strconv.Quote(started) + "\n"
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			control := &controlFixture{t: t, executable: executable, workspace: directory,
				credential: capability(0x11), proxy: capability(0x22), run: capability(0x33), expectedCommand: []string{"codex", "resume", "fixture-thread"},
				recipe: test.recipe, recognition: clientadapter.RecognitionVerified, rootPath: filepath.Join(directory, "root.pem"),
			}
			identity := capturecontrol.ClientAdapterView{ID: "codex-cli", Revision: 1, Version: test.version, CatalogRevision: 7,
				Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: test.recipe}
			switch {
			case test.unknown:
				control.recognition, control.rootPath = clientadapter.RecognitionUnknown, ""
			case test.signer:
				control.recognition = clientadapter.RecognitionRecognized
				control.signer = &capturecontrol.ClientSignerView{ID: "codex-cli", Revision: 1, CatalogRevision: 7,
					InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: test.recipe, SignedPath: executable}
			case test.withheld:
				control.rootPath = ""
				identity.LaunchRecipe = clientadapter.LaunchCodexResponsesHTTP
				control.runAdapter = &identity
			default:
				control.adapter = &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: identity, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault}
			}
			server := httptest.NewServer(control)
			defer server.Close()
			launcher, err := runlauncher.New(runlauncher.Config{
				Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}},
				BaseEnvironment: []string{"PATH=/usr/bin:/bin"}, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
				Getwd: func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return executable, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			code, err := launcher.Run(context.Background(), runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
			_, startedErr := os.Stat(started)
			if test.wantLaunch {
				if err != nil || code != 0 || startedErr != nil || control.attachCalls != 1 {
					t.Fatalf("authorized launch: code=%d err=%v started=%v attach=%d", code, err, startedErr, control.attachCalls)
				}
				if _, err := os.Stat(probed); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("already proven legacy or unknown family was probed")
				}
			} else if !errors.Is(err, runlauncher.ErrCodexIsolationUnsupported) || code != 1 || !errors.Is(startedErr, os.ErrNotExist) || control.attachCalls != 0 {
				t.Fatalf("unproven interactive launch: code=%d err=%v started=%v attach=%d", code, err, startedErr, control.attachCalls)
			}
			if control.finishCalls != 1 {
				t.Fatalf("unused Capture not finalized: %d", control.finishCalls)
			}
		})
	}
}

// Only the external client is substituted. Launcher, grant validation, proxy
// authentication, argv consumption and descendant cleanup remain production.
func TestCodexIsolationFixtureProcess(t *testing.T) {
	role := os.Getenv("CODEX_ISOLATION_ROLE")
	if role == "" {
		return
	}
	request := func() error {
		proxy, err := url.Parse(os.Getenv("HTTP_PROXY"))
		if err != nil {
			return err
		}
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: time.Second}
		response, err := client.Post("http://codex-isolation-fixture.invalid/v1/responses", "application/json", strings.NewReader(`{"prompt":"fixture"}`))
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	}
	switch role {
	case "backend":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("CODEX_ISOLATION_READY"), []byte("http://"+listener.Addr().String()), 0600); err != nil {
			os.Exit(2)
		}
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/request" {
				if err := request(); err != nil {
					w.WriteHeader(502)
					return
				}
			}
			w.WriteHeader(200)
		}))
	case "descendant":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		_ = os.WriteFile(os.Getenv("CODEX_ISOLATION_ALIVE"), []byte("alive"), 0600)
		<-signals
		_ = os.Remove(os.Getenv("CODEX_ISOLATION_ALIVE"))
		os.Exit(0)
	case "client":
		args := os.Args[slices.Index(os.Args, "--")+1:]
		if slices.Contains(args, "--help") {
			fmt.Println("Options:\n      --no-daemon  Run without the shared background server")
			os.Exit(0)
		}
		if !slices.Contains(args, "--no-daemon") && !slices.Contains(args, "exec") {
			response, err := http.Get(os.Getenv("CODEX_ISOLATION_BACKEND") + "/request")
			if err != nil {
				os.Exit(2)
			}
			response.Body.Close()
			os.Exit(0)
		}
		descendant := exec.Command(os.Getenv("CODEX_ISOLATION_BINARY"), "-test.run=^TestCodexIsolationFixtureProcess$")
		descendant.Env = append(os.Environ(), "CODEX_ISOLATION_ROLE=descendant")
		if err := descendant.Start(); err != nil {
			os.Exit(2)
		}
		waitCodexIsolationFile(t, os.Getenv("CODEX_ISOLATION_ALIVE"))
		if err := request(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(2)
}

func TestLauncherKeepsChildThroughTemporaryControlFailure(t *testing.T) {
	t.Parallel()
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoke=%t", revoke), func(t *testing.T) {
			directory := t.TempDir()
			executable := filepath.Join(directory, "agent")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nread line\nprintf 'survived:%s' \"$line\"\nexit 7\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			input, send, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer send.Close()
			control := &controlFixture{
				t: t, executable: executable, workspace: directory,
				credential: capability(0x11), proxy: capability(0x22), run: capability(0x33),
				expectedCommand: []string{"agent"}, recipe: clientadapter.LaunchGeneric, recognition: clientadapter.RecognitionUnknown,
			}
			var attempts atomic.Int32
			var revoked atomic.Bool
			recovered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/heartbeat") {
					if revoked.Load() {
						http.Error(w, "revoked", http.StatusForbidden)
						return
					}
					switch attempts.Add(1) {
					case 1:
						http.Error(w, "unavailable", http.StatusServiceUnavailable) // intermediary HTML/plain text
						return
					case 2:
						<-r.Context().Done() // one bounded request times out across suspension
						return
					case 3:
						http.Error(w, "busy", http.StatusTooManyRequests)
						return
					case 5:
						close(recovered) // prior heartbeat succeeded; the same child is still waiting
					}
				}
				control.ServeHTTP(w, r)
			}))
			defer server.Close()
			var output, diagnostic bytes.Buffer
			launcher, err := runlauncher.New(runlauncher.Config{
				Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}},
				BaseEnvironment: []string{"PATH=/usr/bin:/bin"}, Stdin: input, Stdout: &output, Stderr: &diagnostic,
				HeartbeatInterval: 10 * time.Millisecond, ControlTimeout: 100 * time.Millisecond,
				Getwd: func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return executable, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type outcome struct {
				code int
				err  error
			}
			finished := make(chan outcome, 1)
			go func() {
				code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: []string{"agent"}})
				finished <- outcome{code, err}
			}()
			select {
			case <-recovered:
			case done := <-finished:
				t.Fatalf("temporary heartbeat failure killed child: code=%d err=%v", done.code, done.err)
			case <-ctx.Done():
				t.Fatal("heartbeat did not reconnect")
			}
			if revoke {
				revoked.Store(true)
				done := <-finished
				if done.code != 1 || !errors.Is(done.err, runlauncher.ErrCaptureSupervisionFailed) {
					t.Fatalf("revocation did not stop child: code=%d err=%v", done.code, done.err)
				}
				if output.Len() != 0 {
					t.Fatal("revoked child kept running")
				}
				return
			}
			if _, err := send.WriteString("awake\n"); err != nil {
				t.Fatal(err)
			}
			done := <-finished
			if done.err != nil || done.code != 7 || output.String() != "survived:awake" {
				t.Fatalf("child result=%d %v, output=%q", done.code, done.err, output.String())
			}
			control.mu.Lock()
			defer control.mu.Unlock()
			if control.createCalls != 1 || control.attachCalls != 1 || control.finishCalls != 1 {
				t.Fatalf("reconnection recreated the run or child: create=%d attach=%d finish=%d", control.createCalls, control.attachCalls, control.finishCalls)
			}
			if strings.Count(diagnostic.String(), "Runtime connection interrupted") != 1 ||
				strings.Count(diagnostic.String(), "Runtime connection restored") != 1 {
				t.Fatalf("reconnection diagnostics=%q", diagnostic.String())
			}
		})
	}
}

func TestLauncherSupervisesExactChildAndCaptureRunLifecycle(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	outputPath := filepath.Join(directory, "child-output")
	rootPath := filepath.Join(directory, "root.pem")
	if err := os.WriteFile(rootPath, []byte("test Root"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "agent")
	script := `#!/bin/sh
{
  printf 'arg1=%s\n' "$1"
  printf 'arg2=%s\n' "$2"
  printf 'http=%s\n' "$HTTP_PROXY"
  printf 'https=%s\n' "$HTTPS_PROXY"
  printf 'no_proxy=%s\n' "$NO_PROXY"
  printf 'run=%s\n' "$VIBERMATE_CAPTURE_RUN_ID"
  printf 'root=%s\n' "$NODE_EXTRA_CA_CERTS"
  printf 'node_proxy=%s\n' "$NODE_USE_ENV_PROXY"
  printf 'client_key=%s\n' "$ANTHROPIC_API_KEY"
  printf 'client_token=%s\n' "$ANTHROPIC_AUTH_TOKEN"
  printf 'client_oauth=%s\n' "$CLAUDE_CODE_OAUTH_TOKEN"
  printf 'client_origin=%s\n' "$ANTHROPIC_BASE_URL"
  printf 'fallback=%s\n' "$CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK"
} > "$LAUNCH_TEST_OUTPUT"
sleep 0.08
exit 7
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	control := &controlFixture{
		t:          t,
		executable: executable,
		workspace:  directory,
		rootPath:   rootPath,
		credential: capability(0x11),
		proxy:      capability(0x22),
		run:        capability(0x33),
		expectedCommand: []string{
			"agent",
			"first",
			"two words",
		},
		expectedEnvironment: "work",
		userLabel:           "alice",
		runtimeMetadata: &capturecontrol.ClientRuntimeMetadataInput{
			LocalUserName:   "alice",
			HomeDirectory:   "/Users/alice",
			OperatingSystem: runtime.GOOS,
			Architecture:    runtime.GOARCH,
			TimeZone:        "Asia/Singapore",
		},
		recipe:      clientadapter.LaunchNodeEnvProxy,
		recognition: clientadapter.RecognitionVerified,
		adapter: &capturecontrol.ClientLaunchAdapterView{
			ClientAdapterView: capturecontrol.ClientAdapterView{
				ID:              "claude-code",
				Revision:        1,
				Version:         "test",
				CatalogRevision: 7,
				Source: capturecontrol.
					ClientAdapterSourcePrelaunchDigestCatalog,
				InstallShape: clientadapter.InstallNativeSingleBinary,
				LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
			},
			StreamingFallbackPolicy: clientadapter.
				StreamingFallbackCoreOwned,
		},
		authorities: []string{
			"api.anthropic.com:443",
			"ambient.invalid:443",
		},
	}
	server := httptest.NewServer(control)
	defer server.Close()
	if !strings.HasPrefix(server.URL, "http://127.0.0.1:") {
		t.Fatalf("test control server is not literal loopback: %s", server.URL)
	}
	discovery := fixedDiscovery{session: localdiscovery.Session{
		Schema:            localdiscovery.Schema,
		InstanceID:        base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 20)),
		ProcessID:         os.Getpid(),
		BaseURL:           server.URL,
		ControlCredential: control.credential,
		ExpiresAt:         time.Now().UTC().Add(time.Minute),
	}}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: discovery,
		BaseEnvironment: []string{
			"PATH=/usr/bin:/bin",
			"USER=  alice  ",
			"HOME=/Users/alice",
			"TZ=Asia/Singapore",
			"LAUNCH_TEST_OUTPUT=" + outputPath,
			"ANTHROPIC_API_KEY=ambient-api-key",
			"CLAUDE_CODE_OAUTH_TOKEN=ambient-oauth-token",
			"ANTHROPIC_BASE_URL=https://ambient.invalid",
			"NO_PROXY=localhost,.anthropic.com,10.0.0.0/8",
		},
		HeartbeatInterval: 10 * time.Millisecond,
		ControlTimeout:    time.Second,
		Getwd: func() (string, error) {
			return directory, nil
		},
		LookPath: func(name string) (string, error) {
			if name != "agent" {
				return "", fmt.Errorf("unexpected executable label %q", name)
			}
			return executable, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := launcher.Run(
		context.Background(),
		runlauncher.LaunchRequest{
			EnvironmentID: environment.EnvironmentID("work"),
			Command:       []string{"agent", "first", "two words"},
		},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if code != 7 {
		t.Fatalf("Run() exit code = %d, want 7", code)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := parseLines(string(output))
	wantProxy := "http://capture:" + control.proxy + "@" +
		strings.TrimPrefix(server.URL, "http://")
	if lines["arg1"] != "first" ||
		lines["arg2"] != "two words" ||
		lines["http"] != wantProxy ||
		lines["https"] != wantProxy ||
		lines["no_proxy"] != "localhost" ||
		lines["run"] != "capture-run-1" ||
		lines["root"] != rootPath ||
		lines["node_proxy"] != "1" ||
		lines["client_key"] != "" ||
		lines["client_token"] != "vibermate-local-proxy" ||
		lines["client_oauth"] != "" ||
		lines["client_origin"] != "" ||
		lines["fallback"] != "1" {
		t.Fatalf("captured child output = %+v", lines)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.createCalls != 1 ||
		control.attachCalls != 1 ||
		control.heartbeatCalls == 0 ||
		control.finishCalls != 1 ||
		control.attachedPID <= 0 {
		t.Fatalf("control lifecycle = %+v", control)
	}
}

func TestLauncherPreservesTheResolvedInvocationPathAfterCanonicalVerification(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	outputPath := filepath.Join(directory, "invoked-as")
	canonical, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		t.Fatal(err)
	}
	invocation := filepath.Join(directory, "agent")
	if err := os.Symlink(canonical, invocation); err != nil {
		t.Fatal(err)
	}
	command := []string{
		"agent",
		"-test.run=TestLaunchArgv0Helper",
		"--",
	}
	control := &controlFixture{
		t:               t,
		executable:      invocation,
		grantExecutable: canonical,
		workspace:       directory,
		credential:      capability(0x61),
		proxy:           capability(0x62),
		run:             capability(0x63),
		expectedCommand: command,
		recipe:          clientadapter.LaunchGeneric,
		recognition:     clientadapter.RecognitionUnknown,
	}
	server := httptest.NewServer(control)
	defer server.Close()
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: fixedDiscovery{session: localdiscovery.Session{
			Schema:            localdiscovery.Schema,
			InstanceID:        base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x64}, 20)),
			ProcessID:         os.Getpid(),
			BaseURL:           server.URL,
			ControlCredential: control.credential,
			ExpiresAt:         time.Now().UTC().Add(time.Minute),
		}},
		BaseEnvironment: []string{
			"PATH=/usr/bin:/bin",
			"RUNLAUNCHER_ARGV0_OUTPUT=" + outputPath,
		},
		Getwd:    func() (string, error) { return directory, nil },
		LookPath: func(string) (string, error) { return invocation, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := launcher.Run(
		context.Background(),
		runlauncher.LaunchRequest{
			EnvironmentID: environment.SystemTransparentID,
			Command:       command,
		},
	)
	if err != nil || code != 0 {
		t.Fatalf("Run() exit=%d error=%v", code, err)
	}
	invokedAs, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(invokedAs) != invocation {
		t.Fatalf("child argv[0] = %q, want invocation path %q", invokedAs, invocation)
	}
}

func TestLaunchArgv0Helper(t *testing.T) {
	outputPath := os.Getenv("RUNLAUNCHER_ARGV0_OUTPUT")
	if outputPath == "" {
		return
	}
	if err := os.WriteFile(outputPath, []byte(os.Args[0]), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherRejectsControlRedirectWithoutStartingChild(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(writer, request, "http://example.invalid", http.StatusFound)
	}))
	defer server.Close()
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: fixedDiscovery{session: localdiscovery.Session{
			BaseURL:           server.URL,
			ControlCredential: capability(0x51),
		}},
		BaseEnvironment: []string{"PATH=/usr/bin:/bin"},
		Getwd: func() (string, error) {
			return t.TempDir(), nil
		},
		LookPath: func(string) (string, error) {
			return "/bin/echo", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.Run(context.Background(), transparentLaunch("echo")); err == nil ||
		!strings.Contains(err.Error(), "redirect") {
		t.Fatalf("Run() redirect error = %v", err)
	}
}

func TestLauncherBoundsCaptureRunCreation(t *testing.T) {
	t.Parallel()

	releaseRequest := make(chan struct{})
	var stderr bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(
		_ http.ResponseWriter,
		_ *http.Request,
	) {
		<-releaseRequest
	}))
	defer func() {
		close(releaseRequest)
		server.Close()
	}()
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: fixedDiscovery{session: localdiscovery.Session{
			BaseURL:           server.URL,
			ControlCredential: capability(0x52),
		}},
		BaseEnvironment: []string{"PATH=/usr/bin:/bin"},
		Stderr:          &stderr,
		ControlTimeout:  50 * time.Millisecond,
		// Create carries its own budget because it is the only control call
		// that can contain signature verification and a question put to a
		// person. That it is a separate number does not make it unbounded,
		// which is what this test is for.
		CreateTimeout: 50 * time.Millisecond,
		Getwd: func() (string, error) {
			return t.TempDir(), nil
		},
		LookPath: func(string) (string, error) {
			return "/bin/echo", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, runErr := launcher.Run(
			context.Background(),
			transparentLaunch("echo"),
		)
		finished <- runErr
	}()
	select {
	case runErr := <-finished:
		if !errors.Is(runErr, runlauncher.ErrCapturePreparationTimedOut) {
			t.Fatalf("unresponsive CaptureRun creation error = %v", runErr)
		}
		if !strings.Contains(
			stderr.String(),
			"Network and tool approvals still follow your traffic policy",
		) {
			t.Fatalf("stalled create had no actionable progress: %q", stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CaptureRun creation ignored the configured create timeout")
	}
}

func TestLauncherClassifiesAStaleDiscoveryEndpointAsRuntimeUnavailable(
	t *testing.T,
) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: fixedDiscovery{session: localdiscovery.Session{
			BaseURL:           baseURL,
			ControlCredential: capability(0x54),
		}},
		BaseEnvironment: []string{"PATH=/usr/bin:/bin"},
		CreateTimeout:   time.Second,
		Getwd: func() (string, error) {
			return t.TempDir(), nil
		},
		LookPath: func(string) (string, error) {
			return "/bin/echo", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.Run(
		context.Background(), transparentLaunch("echo"),
	); !errors.Is(err, runlauncher.ErrRuntimeUnavailable) {
		t.Fatalf("stale discovery launch error = %v", err)
	}
}

func TestLauncherCancelsCaptureRunCreationFromCallerContext(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(
		_ http.ResponseWriter,
		_ *http.Request,
	) {
		close(requestStarted)
		<-releaseRequest
	}))
	defer func() {
		close(releaseRequest)
		server.Close()
	}()
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: fixedDiscovery{session: localdiscovery.Session{
			BaseURL:           server.URL,
			ControlCredential: capability(0x53),
		}},
		BaseEnvironment: []string{"PATH=/usr/bin:/bin"},
		CreateTimeout:   time.Minute,
		Getwd: func() (string, error) {
			return t.TempDir(), nil
		},
		LookPath: func(string) (string, error) {
			return "/bin/echo", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, runErr := launcher.Run(ctx, transparentLaunch("echo"))
		finished <- runErr
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("CaptureRun creation did not start")
	}
	cancel()
	select {
	case runErr := <-finished:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("Run() cancellation error = %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("CaptureRun creation did not stop after caller cancellation")
	}
}

type fixedDiscovery struct {
	session localdiscovery.Session
	err     error
}

func (discovery fixedDiscovery) Load() (localdiscovery.Session, error) {
	return discovery.session, discovery.err
}

type controlFixture struct {
	t                   *testing.T
	executable          string
	grantExecutable     string
	workspace           string
	rootPath            string
	credential          string
	proxy               string
	run                 string
	expectedCommand     []string
	expectedEnvironment string
	userLabel           string
	runtimeMetadata     *capturecontrol.ClientRuntimeMetadataInput
	recipe              clientadapter.LaunchRecipe
	recognition         clientadapter.Recognition
	adapter             *capturecontrol.ClientLaunchAdapterView
	signer              *capturecontrol.ClientSignerView
	runAdapter          *capturecontrol.ClientAdapterView
	authorities         []string
	rootStatus          *desktopcontrol.RootCAResponse

	mu             sync.Mutex
	createCalls    int
	attachCalls    int
	heartbeatCalls int
	finishCalls    int
	attachedPID    int
}

func (fixture *controlFixture) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/api/v1/platform/root-ca":
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer "+fixture.credential {
			writeControlProblem(writer, http.StatusUnauthorized)
			return
		}
		status := fixture.rootStatus
		if status == nil {
			status = &desktopcontrol.RootCAResponse{Available: true, RootValid: true, CertificatePresent: "present", TrustDecision: "trusted"}
		}
		writeControlJSON(writer, http.StatusOK, status)
	case "/api/v1/capture-runs":
		if request.Method != http.MethodPost ||
			request.Header.Get("Authorization") != "Bearer "+fixture.credential {
			writeControlProblem(writer, http.StatusUnauthorized)
			return
		}
		var input capturecontrol.CreateRequest
		decodeRequest(fixture.t, request, &input)
		if input.EnvironmentInventory == nil || input.EnvironmentInventory.Validate() != nil {
			fixture.t.Error("launcher did not send a bounded name-only inventory")
		}
		expectedEnvironment := fixture.expectedEnvironment
		if expectedEnvironment == "" {
			expectedEnvironment = environment.SystemTransparentID.String()
		}
		if input.CWD != fixture.workspace ||
			input.EnvironmentID != expectedEnvironment ||
			input.ExecutablePath != fixture.executable ||
			input.RuntimeMetadata.LocalUserName != fixture.userLabel ||
			!slices.Equal(input.Command, fixture.expectedCommand) {
			fixture.t.Errorf("create request = %+v", input)
		}
		if fixture.runtimeMetadata != nil {
			got, want := input.RuntimeMetadata, *fixture.runtimeMetadata
			if got.LocalUserName != want.LocalUserName || got.HomeDirectory != want.HomeDirectory ||
				got.OperatingSystem != want.OperatingSystem || got.OperatingSystemVersion == "" ||
				got.Architecture != want.Architecture || got.TimeZone != want.TimeZone {
				fixture.t.Errorf("runtime metadata = %+v, want %+v with an OS version", got, want)
			}
		}
		fixture.createCalls++
		grantExecutable := fixture.grantExecutable
		if grantExecutable == "" {
			grantExecutable = fixture.executable
		}
		writeControlJSON(writer, http.StatusCreated, capturecontrol.LaunchGrant{
			Run:             fixture.runView(0),
			CatalogRevision: 7,
			LaunchRecipe:    fixture.recipe,
			Recognition:     fixture.recognition,
			Adapter:         fixture.adapter,
			Signer:          fixture.signer,
			ExecutablePath:  grantExecutable,
			ProxyAddress:    "http://" + request.Host,
			ProxyDelivery:   capturecontrol.ProxyDeliveryLocalListener,
			ProxyToken:      fixture.proxy,
			RunCapability:   fixture.run,
			RootPEMPath:     fixture.rootPath,
			ProtectedAuthorities: append(
				[]string{},
				fixture.authorities...,
			),
			ManagedCredentialAuthorities: append(
				[]string{},
				fixture.authorities...,
			),
		})
	case "/api/v1/capture-runs/capture-run-1/actions/attach-process":
		if !fixture.authorizeRun(request) {
			writeControlProblem(writer, http.StatusForbidden)
			return
		}
		var input capturecontrol.AttachRequest
		decodeRequest(fixture.t, request, &input)
		fixture.attachCalls++
		fixture.attachedPID = input.ProcessID
		writeControlJSON(writer, http.StatusOK, fixture.runView(input.ProcessID))
	case "/api/v1/capture-runs/capture-run-1/actions/heartbeat":
		if !fixture.authorizeRun(request) {
			writeControlProblem(writer, http.StatusForbidden)
			return
		}
		fixture.heartbeatCalls++
		writeControlJSON(writer, http.StatusOK, fixture.runView(fixture.attachedPID))
	case "/api/v1/capture-runs/capture-run-1/actions/finish":
		if !fixture.authorizeRun(request) {
			writeControlProblem(writer, http.StatusForbidden)
			return
		}
		fixture.finishCalls++
		writer.WriteHeader(http.StatusNoContent)
	default:
		writeControlProblem(writer, http.StatusNotFound)
	}
}

func (fixture *controlFixture) runView(processID int) capturecontrol.CaptureRunView {
	createdAt := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	state := clientadapter.StatusGeneric
	if fixture.adapter != nil {
		state = clientadapter.StatusVerified
	}
	var runAdapter *capturecontrol.ClientAdapterView
	if fixture.adapter != nil {
		identity := fixture.adapter.ClientAdapterView
		runAdapter = &identity
	}
	if fixture.runAdapter != nil {
		runAdapter = fixture.runAdapter
		state = clientadapter.StatusVerified
	}
	return capturecontrol.CaptureRunView{
		ID:                 "capture-run-1",
		ExecutableLabel:    "agent",
		CWD:                fixture.workspace,
		ProcessID:          processID,
		CreatedAt:          createdAt,
		ExpiresAt:          createdAt.Add(time.Minute),
		ClientAdapterState: state,
		ClientRecognition:  fixture.recognition,
		CatalogRevision:    7,
		ClientAdapter:      runAdapter,
	}
}

func (fixture *controlFixture) authorizeRun(request *http.Request) bool {
	return request.Method == http.MethodPost &&
		request.Header.Get(capturecontrol.RunCapabilityHeader) == fixture.run &&
		request.Header.Get("Authorization") == ""
}

func decodeRequest(t *testing.T, request *http.Request, output any) {
	t.Helper()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		t.Errorf("decode control request: %v", err)
	}
}

func writeControlJSON(writer http.ResponseWriter, status int, value any) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeControlProblem(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(status)
	code := capturecontrol.ReasonInvalidRoute
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"type":   "urn:vibermate:error:control-route-not-found",
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
	})
}

func capability(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func parseLines(input string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(input), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}
