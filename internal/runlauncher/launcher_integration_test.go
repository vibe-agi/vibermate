package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/launcherdiscovery"
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
		launcher:   capability(0x11),
		proxy:      capability(0x22),
		run:        capability(0x33),
	}
	server := httptest.NewServer(control)
	defer server.Close()
	if !strings.HasPrefix(server.URL, "http://127.0.0.1:") {
		t.Fatalf("test control server is not literal loopback: %s", server.URL)
	}
	discovery := fixedDiscovery{session: launcherdiscovery.Session{
		Schema:        launcherdiscovery.SchemaV1,
		InstanceID:    base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 20)),
		ProcessID:     os.Getpid(),
		BaseURL:       server.URL,
		LauncherToken: control.launcher,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
	}}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: discovery,
		BaseEnvironment: []string{
			"PATH=/usr/bin:/bin",
			"LAUNCH_TEST_OUTPUT=" + outputPath,
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
		[]string{"agent", "first", "two words"},
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
		lines["node_proxy"] != "1" {
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
		Discovery: fixedDiscovery{session: launcherdiscovery.Session{
			BaseURL:       server.URL,
			LauncherToken: capability(0x51),
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
	if _, err := launcher.Run(context.Background(), []string{"echo"}); err == nil ||
		!strings.Contains(err.Error(), "redirect") {
		t.Fatalf("Run() redirect error = %v", err)
	}
}

type fixedDiscovery struct {
	session launcherdiscovery.Session
	err     error
}

func (discovery fixedDiscovery) Load() (launcherdiscovery.Session, error) {
	return discovery.session, discovery.err
}

type controlFixture struct {
	t          *testing.T
	executable string
	workspace  string
	rootPath   string
	launcher   string
	proxy      string
	run        string

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
			request.Header.Get("Authorization") != "Bearer "+fixture.launcher {
			writeControlProblem(writer, http.StatusUnauthorized)
			return
		}
		var input capturecontrol.CreateRequest
		decodeRequest(fixture.t, request, &input)
		if input.CWD != fixture.workspace ||
			input.ExecutablePath != fixture.executable ||
			len(input.Command) != 3 ||
			input.Command[0] != "agent" ||
			input.Command[2] != "two words" {
			fixture.t.Errorf("create request = %+v", input)
		}
		fixture.createCalls++
		writeControlJSON(writer, http.StatusCreated, capturecontrol.LaunchGrant{
			Run: capturerun.View{
				ID:              "capture-run-1",
				ExecutableLabel: "agent",
				CWD:             fixture.workspace,
				State:           capturerun.StateCreated,
			},
			LaunchRecipe:         clientadapter.LaunchNodeEnvProxy,
			ExecutablePath:       fixture.executable,
			ProxyOrigin:          "http://" + request.Host,
			ProxyCapability:      fixture.proxy,
			RunCapability:        fixture.run,
			RootPEMPath:          fixture.rootPath,
			ProtectedAuthorities: []string{"api.anthropic.com:443"},
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
		writeControlJSON(writer, http.StatusOK, capturerun.View{
			ID:        "capture-run-1",
			ProcessID: input.ProcessID,
			State:     capturerun.StateAttached,
		})
	case "/api/v1/capture-runs/capture-run-1/actions/heartbeat":
		if !fixture.authorizeRun(request) {
			writeControlProblem(writer, http.StatusForbidden)
			return
		}
		fixture.heartbeatCalls++
		writeControlJSON(writer, http.StatusOK, capturerun.View{
			ID:    "capture-run-1",
			State: capturerun.StateAttached,
		})
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
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"status":     status,
		"reasonCode": capturecontrol.ReasonInvalidRoute,
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
