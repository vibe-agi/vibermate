package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

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
			"decide any client trust request in the App",
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
	authorities         []string

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
	case "/api/v1/capture-runs":
		if request.Method != http.MethodPost ||
			request.Header.Get("Authorization") != "Bearer "+fixture.credential {
			writeControlProblem(writer, http.StatusUnauthorized)
			return
		}
		var input capturecontrol.CreateRequest
		decodeRequest(fixture.t, request, &input)
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
