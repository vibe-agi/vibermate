package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
	"github.com/vibe-agi/vibermate/locales"
)

func TestProfilesPermissionOutputLocalized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC()
	credential := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != capturecontrol.EnvironmentsPath || r.Header.Get("Authorization") != "Bearer "+credential {
			t.Errorf("wrong scoped catalog request")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"schema":"vibermate-capture-environments/v1","items":[{"id":"work","name":"Work","clientToolAccessEnabled":true},{"id":"personal","name":"Personal","clientToolAccessEnabled":false}]}`)
	}))
	defer server.Close()
	layout, err := runtimepath.Default()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(layout.RuntimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(localdiscovery.Session{Schema: localdiscovery.Schema, InstanceID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x49}, 16)), ProcessID: os.Getpid(), BaseURL: server.URL, ControlCredential: credential, ExpiresAt: now.Add(time.Hour)})
	if err = os.WriteFile(layout.CLIControlRecord, payload, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ language, on, off string }{{"en_US.UTF-8", "Work · Tool services: On", "Personal · Tool services: Off"}, {"zh_CN.UTF-8", "Work · \u5de5\u5177\u670d\u52a1\uff1a\u5f00\u542f", "Personal · \u5de5\u5177\u670d\u52a1\uff1a\u5173\u95ed"}} {
		var stdout, stderr strings.Builder
		code, key := execute([]string{"profiles"}, []string{"LANG=" + test.language}, strings.NewReader(""), &stdout, &stderr)
		if code != 0 || key != "" || !strings.Contains(stdout.String(), test.on) || !strings.Contains(stdout.String(), test.off) {
			t.Fatalf("localized profiles: code=%d key=%s stdout=%s", code, key, stdout.String())
		}
	}
	var stdout, stderr strings.Builder
	code, key := execute([]string{"profiles", "--json"}, []string{"LANG=zh_CN.UTF-8"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || key != "" || !strings.Contains(stdout.String(), `"clientToolAccessEnabled":true`) || !strings.Contains(stdout.String(), `"clientToolAccessEnabled":false`) {
		t.Fatalf("machine profiles: code=%d key=%s stdout=%s", code, key, stdout.String())
	}
}

func TestHelpExplainsTheFirstCapturedRun(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr strings.Builder
		code, key := execute(
			arguments,
			[]string{"LANG=en_US.UTF-8"},
			strings.NewReader(""),
			&stdout,
			&stderr,
		)
		if code != 0 || key != "" || stderr.Len() != 0 {
			t.Fatalf(
				"execute(%v) code=%d key=%q stdout=%q stderr=%q",
				arguments,
				code,
				key,
				stdout.String(),
				stderr.String(),
			)
		}
		for _, expected := range []string{
			"vibermate run -- codex",
			"vibermate run -- claude",
			"vibermate doctor",
			"vibermate login --server",
			"/resume",
			"F to fork",
			"R to retry",
			"new ID",
		} {
			if !strings.Contains(stdout.String(), expected) {
				t.Fatalf("help for %v lacks %q: %s", arguments, expected, stdout.String())
			}
		}
	}
}

func TestStatusAndDoctorVerifyTheRealLocalControlAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()
	credential := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))
	instanceID := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x49}, 16))
	status := desktopcontrol.StatusResponse{
		Generation: instanceID, Ready: true, APIVersion: "v1", ProductBuild: "test-build",
		StatusKey: "runtime.state.initialized",
		Runtime: productruntime.RuntimeStatus{
			State: productruntime.RuntimeStateInitialized, InstanceID: instanceID,
			Host: hostcontract.KindDesktop, Storage: productruntime.StorageStateHealthy, StartedAt: now,
		},
	}
	var current atomic.Pointer[desktopcontrol.StatusResponse]
	current.Store(&status)
	var rootTrusted atomic.Bool
	rootTrusted.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet ||
			request.Header.Get("Authorization") != "Bearer "+credential {
			http.Error(writer, "unexpected request", http.StatusForbidden)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		if request.URL.Path == "/api/v1/platform/root-ca" {
			_ = json.NewEncoder(writer).Encode(desktopcontrol.RootCAResponse{
				Available: true, RootValid: rootTrusted.Load(), CertificatePresent: "present", TrustDecision: "trusted",
			})
			return
		}
		_ = json.NewEncoder(writer).Encode(current.Load())
	}))
	defer server.Close()
	layout, err := runtimepath.Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.RuntimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	session := localdiscovery.Session{
		Schema:            localdiscovery.Schema,
		InstanceID:        instanceID,
		ProcessID:         os.Getpid(),
		BaseURL:           server.URL,
		ControlCredential: credential,
		ExpiresAt:         now.Add(time.Hour),
	}
	payload, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.CLIControlRecord, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"status", "doctor"} {
		var stdout, stderr strings.Builder
		code, key := execute(
			[]string{command},
			[]string{"LANG=en_US.UTF-8"},
			strings.NewReader(""),
			&stdout,
			&stderr,
		)
		if code != 0 || key != "" || stderr.Len() != 0 {
			t.Fatalf(
				"%s code=%d key=%q stdout=%q stderr=%q",
				command,
				code,
				key,
				stdout.String(),
				stderr.String(),
			)
		}
		for _, expected := range []string{"ready", server.URL, "desktop", "healthy"} {
			if !strings.Contains(stdout.String(), expected) {
				t.Fatalf("%s output lacks %q: %s", command, expected, stdout.String())
			}
		}
		if command == "doctor" && !strings.Contains(stdout.String(), "vibermate run -- codex") {
			t.Fatalf("doctor output lacks next action: %s", stdout.String())
		}
		if command == "doctor" && runtime.GOOS == "darwin" && !strings.Contains(stdout.String(), "PASS Local root trust") {
			t.Fatalf("root trust outcome absent: %s", stdout.String())
		}
	}
	if runtime.GOOS == "darwin" {
		rootTrusted.Store(false)
		var stdout, stderr strings.Builder
		code, key := execute([]string{"doctor"}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &stdout, &stderr)
		if code != 1 || key != "cli.error.localRootUntrusted" || strings.Contains(stdout.String(), "PASS") {
			t.Fatalf("untrusted Root: code=%d key=%q stdout=%s", code, key, &stdout)
		}
		rootTrusted.Store(true)
	}
	for _, stopped := range []bool{false, true} {
		updated := status
		updated.Runtime.RecordingFailure = &productruntime.PersistenceFailure{
			Operation: "usage", Reason: "write_failed", At: now,
		}
		wantCode := 0
		if stopped {
			wantCode = 1
			updated.Ready = false
			updated.StatusKey = "runtime.state.degraded"
			updated.Runtime.State = productruntime.RuntimeStateDegraded
			updated.Runtime.Storage = productruntime.StorageStateUnavailable
			updated.Runtime.StorageFailure = &productruntime.PersistenceFailure{
				Operation: "egress_complete", Reason: "timeout", At: now,
			}
		}
		current.Store(&updated)
		for _, command := range []string{"status", "doctor"} {
			var stdout, stderr strings.Builder
			code, key := execute([]string{command}, []string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &stdout, &stderr)
			output := stdout.String()
			expectedCode := wantCode
			if command == "doctor" {
				expectedCode = 1
			}
			if code != expectedCode || key != "" || stderr.Len() != 0 ||
				!strings.Contains(output, "WARN") || !strings.Contains(output, "usage / write_failed") ||
				strings.Contains(output, "PASS") || strings.Contains(output, "vibermate run -- codex") {
				t.Fatalf("%s stopped=%t: code=%d key=%q output=%s stderr=%s", command, stopped, code, key, output, stderr.String())
			}
			if stopped && (!strings.Contains(output, "egress_complete / timeout") || !strings.Contains(output, now.Format(time.RFC3339))) {
				t.Fatalf("core audit first cause missing: %s", output)
			}
		}
	}
}

func TestDoctorRejectsBrokenCONNECTDespiteValidRemoteSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC().Truncate(time.Millisecond)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 32))
	machineID := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x36}, 32))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodConnect {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.Method != http.MethodGet ||
			request.URL.Path != servercontrol.RuntimeUserCurrentSessionPath ||
			request.Header.Get("Authorization") != "Bearer "+token {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(servercontrol.RuntimeUserCurrentSession{
			Schema: servercontrol.RuntimeUserCurrentSessionSchema, InstanceID: "instance.test",
			APIVersion: "v1", ProductBuild: "test-build",
			SessionID: "login.test", MachineID: machineID,
			DeviceName: "test-device",
			User:       servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"},
		})
	}))
	defer server.Close()
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stateDirectory, err := clientpath.DefaultRemoteStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := workspaceidentity.Open(context.Background(), filepath.Join(stateDirectory, "identity"), bytes.NewReader(bytes.Repeat([]byte{0x36}, 64)), now)
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Shutdown(context.Background())
	store, err := serverconnection.OpenLoginStore(filepath.Join(stateDirectory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{
		Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice",
		SessionID: "login.test", SessionToken: token, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credential); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"status", "doctor"} {
		var stdout, stderr strings.Builder
		code, key := execute(
			[]string{command, "--server", server.URL},
			[]string{"LANG=en_US.UTF-8"}, strings.NewReader(""), &stdout, &stderr,
		)
		wantCode := 0
		if command == "doctor" {
			wantCode = 1
		}
		if code != wantCode || key != "" || stderr.Len() != 0 {
			t.Fatalf(
				"%s code=%d key=%q stdout=%q stderr=%q",
				command, code, key, stdout.String(), stderr.String(),
			)
		}
		for _, expected := range []string{"alice", server.URL, "unencrypted HTTP", "instance.test"} {
			if !strings.Contains(stdout.String(), expected) {
				t.Fatalf("%s output lacks %q: %s", command, expected, stdout.String())
			}
		}
		if command == "doctor" && (!strings.Contains(stdout.String(), "FAIL Proxy entry") || !strings.Contains(stdout.String(), "CONNECT")) {
			t.Fatalf("doctor hides broken CONNECT: %s", stdout.String())
		}
	}
}

func TestExecuteRequiresOneExactRunSeparator(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		nil,
		{"run"},
		{"run", "claude"},
		{"run", "--"},
		{"run", "--env"},
		{"run", "--env", "work"},
		{"run", "--env", "bad ID", "--", "claude"},
		{"run", "--environment", "work", "--", "claude"},
		{"run", "--env", "work", "--env", "personal", "--", "claude"},
		{"status", "--server"},
		{"doctor", "--server", "invalid-host"},
	} {
		code, key := execute(
			arguments,
			[]string{"LANG=en_US.UTF-8"},
			&bytes.Buffer{},
			&bytes.Buffer{},
			&bytes.Buffer{},
		)
		if code != 2 || key != keyUsage {
			t.Fatalf("execute(%v) code=%d key=%q", arguments, code, key)
		}
	}
}

func TestLaunchFailureKeyDistinguishesEnvironmentSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want string
	}{
		{err: runlauncher.ErrManagedAuthEnvironmentRequired, want: "cli.error.managedAuth.environmentRequired"},
		{err: runlauncher.ErrManagedAuthClientRequired, want: "cli.error.managedAuth.clientRequired"},
		{err: runlauncher.ErrManagedAuthDestinationRequired, want: "cli.error.managedAuth.managedDestinationRequired"},
		{err: runlauncher.ErrManagedAuthConflict, want: "cli.error.managedAuth.conflict"},
		{err: runlauncher.ErrEnvironmentNotFound, want: keyEnvironmentMissing},
		{err: runlauncher.ErrEnvironmentUnavailable, want: keyEnvironmentDown},
		{err: runlauncher.ErrClientTargetNotConfigured, want: keyClientTargetNotConfigured},
		{err: runlauncher.ErrClientTargetInvalid, want: keyClientTargetInvalid},
		{err: runlauncher.ErrRuntimeUnavailable, want: keyRuntimeUnavailable},
		{err: runlauncher.ErrRemoteLoginRequired, want: keyRemoteLoginRequired},
		{err: runlauncher.ErrRemoteRuntimeUnavailable, want: keyRemoteRuntimeUnavailable},
		{err: runlauncher.ErrCapturePreparationTimedOut, want: keyCapturePreparationTimedOut},
		{err: runlauncher.ErrCodexIsolationUnsupported, want: "cli.error.codexIsolationUnsupported"},
		{err: errors.New("other launch failure"), want: keyLaunchFailed},
		{err: errors.Join(runlauncher.ErrCaptureSupervisionFailed, runlauncher.ErrRuntimeUnavailable), want: "cli.error.captureSupervisionFailed"},
		{err: errors.Join(runlauncher.ErrCaptureFinalizationFailed, runlauncher.ErrRuntimeUnavailable), want: "cli.error.captureFinalizationFailed"},
	}
	for _, test := range tests {
		if got := launchFailureKey(test.err); got != test.want {
			t.Fatalf("launchFailureKey(%v)=%q want %q", test.err, got, test.want)
		}
	}
}

func TestParseLoginRequiresOneExactServer(t *testing.T) {
	t.Parallel()

	parsed, err := parseLogin([]string{"login", "--server", "192.168.1.20:9666"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.server.Origin() != "http://192.168.1.20:9666" {
		t.Fatalf("server = %q", parsed.server.Origin())
	}
	secure, err := parseLogin([]string{"login", "--server", "https://vm.test:9666"})
	if err != nil {
		t.Fatal(err)
	}
	if secure.server.Origin() != "https://vm.test:9666" {
		t.Fatalf("secure server = %q", secure.server.Origin())
	}
	for _, arguments := range [][]string{
		nil,
		{"login"},
		{"login", "--server"},
		{"login", "--server", "host"},
		{"login", "--server", "host:9666", "extra"},
		{"login", "--server", "host:9666", "--server", "other:9666"},
		{"login", "--env", "work"},
	} {
		if _, err := parseLogin(arguments); err == nil {
			t.Fatalf("parseLogin(%v) succeeded", arguments)
		}
	}
}

func TestParseLoginAndRunNormalizeDefaultServerPorts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ input, origin string }{
		{"https://runtime.example.test", "https://runtime.example.test:443"},
		{"http://runtime.example.test", "http://runtime.example.test:80"},
	} {
		t.Run(test.input, func(t *testing.T) {
			t.Run("login", func(t *testing.T) {
				parsed, err := parseLogin([]string{"login", "--server", test.input})
				if err != nil {
					t.Fatal(err)
				}
				if parsed.server.Origin() != test.origin {
					t.Fatalf("server = %q; want %q", parsed.server.Origin(), test.origin)
				}
			})
			t.Run("run", func(t *testing.T) {
				parsed, err := parseRun([]string{"run", "--server", test.input, "--", "codex"})
				if err != nil {
					t.Fatal(err)
				}
				if parsed.server.Origin() != test.origin || !slices.Equal(parsed.command, []string{"codex"}) {
					t.Fatalf("parseRun() = %+v; want origin %q", parsed, test.origin)
				}
			})
		})
	}
}

func TestParseLogoutRequiresOneExactServer(t *testing.T) {
	t.Parallel()
	parsed, err := parseLogout([]string{"logout", "--server", "runtime.lan:9666"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.server.Origin() != "http://runtime.lan:9666" {
		t.Fatalf("server = %q", parsed.server.Origin())
	}
	for _, arguments := range [][]string{
		nil, {"logout"}, {"logout", "--server"},
		{"logout", "--server", "runtime.lan"},
		{"logout", "--server", "runtime.lan:9666", "extra"},
	} {
		if _, err := parseLogout(arguments); err == nil {
			t.Fatalf("parseLogout(%v) succeeded", arguments)
		}
	}
}

func TestParseTrustRequiresHTTPSAndExplicitSystemRoots(t *testing.T) {
	t.Parallel()
	parsed, err := parseTrust([]string{
		"trust", "--server", "https://runtime.example.test:9666", "--system-roots",
	})
	if err != nil || parsed.server.Origin() != "https://runtime.example.test:9666" {
		t.Fatalf("parseTrust() = %+v, %v", parsed, err)
	}
	for _, arguments := range [][]string{
		nil,
		{"trust"},
		{"trust", "--server", "runtime.example.test:9666", "--system-roots"},
		{"trust", "--server", "http://runtime.example.test:9666", "--system-roots"},
		{"trust", "--server", "https://runtime.example.test:9666"},
		{"trust", "--server", "https://runtime.example.test:9666", "--forget"},
	} {
		if _, err := parseTrust(arguments); err == nil {
			t.Fatalf("parseTrust(%v) succeeded", arguments)
		}
	}
}

func TestRemoteLoginCommandPromptsAndPersistsWithoutEchoingSecrets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 24, 18, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x52}, 32))
	var observed servercontrol.RuntimeUserLogin
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != servercontrol.RuntimeUserSessionPath {
			http.NotFound(writer, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&observed); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(servercontrol.RuntimeUserSession{
			Schema: servercontrol.RuntimeUserSessionSchema, InstanceID: "instance.test",
			APIVersion: "v1", ProductBuild: "test-build",
			SessionID: "login.test", SessionToken: token,
			User:      servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"},
			ExpiresAt: now.Add(24 * time.Hour),
		})
	}))
	defer server.Close()
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stateDirectory := filepath.Join(t.TempDir(), "remote-client")
	var stdout, stderr strings.Builder
	secret := "test-password-that-must-not-be-printed"
	code, key := executeRemoteLogin(
		context.Background(), loginConfig{server: target}, stateDirectory,
		"test-device", fixedCommandClock{now: now},
		bytes.NewReader(bytes.Repeat([]byte{0x35}, 512)),
		strings.NewReader("alice\n"+secret+"\n"), &stdout, &stderr,
	)
	if code != 0 || key != "" {
		t.Fatalf("executeRemoteLogin() code=%d key=%q stdout=%q stderr=%q", code, key, stdout.String(), stderr.String())
	}
	if observed.Username != "alice" || observed.Password != secret ||
		observed.DeviceName != "test-device" || observed.MachineID == "" {
		t.Fatalf("observed login = %#v", observed)
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, secret) || strings.Contains(combined, token) {
		t.Fatalf("login output leaked a secret: %q", combined)
	}
	if !strings.Contains(stderr.String(), "Username:") ||
		!strings.Contains(stderr.String(), "Password:") ||
		!strings.Contains(stderr.String(), "unencrypted HTTP") ||
		!strings.Contains(stdout.String(), "alice") ||
		!strings.Contains(stdout.String(), target.Origin()) {
		t.Fatalf("login output missing actionable facts: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	payload, err := os.ReadFile(filepath.Join(stateDirectory, "login", "login-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), secret) {
		t.Fatal("persisted Login Session contains the password")
	}
	info, err := os.Stat(filepath.Join(stateDirectory, "login", "login-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("login store mode = %o", info.Mode().Perm())
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(stateDirectory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Load(target, now)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Username() != "alice" || credential.SessionToken().Value() != token {
		t.Fatalf("stored credential = %#v", credential)
	}
}

func TestRemoteLoginCommandRejectsMissingCredentialsBeforeNetwork(t *testing.T) {
	t.Parallel()
	target, err := serverconnection.ParseTarget("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"\npassword123\n", "alice\n\n"} {
		code, key := executeRemoteLogin(
			context.Background(), loginConfig{server: target},
			filepath.Join(t.TempDir(), "remote-client"), "test-device",
			fixedCommandClock{now: time.Now().UTC()}, bytes.NewReader(bytes.Repeat([]byte{1}, 512)),
			strings.NewReader(input), io.Discard, io.Discard,
		)
		if code != 1 || key != keyLoginFailed {
			t.Fatalf("input %q: code=%d key=%q", input, code, key)
		}
	}
}

func TestRemoteLoginWarnsBeforeReadingCredentialsOverHTTP(t *testing.T) {
	t.Parallel()
	target, err := serverconnection.ParseTarget("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	input := &warningBeforeReadInput{
		input:   strings.NewReader("\n"),
		prompts: &stderr,
	}
	code, key := executeRemoteLogin(
		context.Background(), loginConfig{server: target},
		filepath.Join(t.TempDir(), "remote-client"), "test-device",
		fixedCommandClock{now: time.Now().UTC()},
		bytes.NewReader(bytes.Repeat([]byte{1}, 512)), input,
		io.Discard, &stderr,
	)
	if code != 1 || key != keyLoginFailed {
		t.Fatalf("code=%d key=%q", code, key)
	}
	if !input.warnedBeforeRead {
		t.Fatalf("credentials were read before HTTP warning: %q", stderr.String())
	}
}

func TestRemoteLoginHTTPWarningUsesSelectedCLILanguage(t *testing.T) {
	t.Parallel()
	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := catalogs.Render(
		locales.SimplifiedChinese,
		keyLoginHTTPWarning,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	code, key := execute(
		[]string{"login", "--server", "http://127.0.0.1:1"},
		[]string{"LC_ALL=zh_CN.UTF-8"},
		strings.NewReader("\n"), io.Discard, &stderr,
	)
	if code != 1 || key != keyLoginFailed {
		t.Fatalf("code=%d key=%q", code, key)
	}
	if !strings.Contains(stderr.String(), expected) ||
		strings.Contains(stderr.String(), "your username and password") {
		t.Fatalf("HTTP warning did not use selected CLI language: %q", stderr.String())
	}
}

type warningBeforeReadInput struct {
	input            io.Reader
	prompts          *strings.Builder
	warnedBeforeRead bool
}

func (input *warningBeforeReadInput) Read(destination []byte) (int, error) {
	input.warnedBeforeRead = strings.Contains(
		input.prompts.String(), "unencrypted HTTP",
	)
	return input.input.Read(destination)
}

type fixedCommandClock struct{ now time.Time }

func (clock fixedCommandClock) Now() time.Time { return clock.now }

func TestParseRunLeavesDefaultSelectionToCoreOrUsesExplicitEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		arguments   []string
		environment environment.EnvironmentID
		command     []string
	}{
		{
			name:        "Core-selected default",
			arguments:   []string{"run", "--", "claude", "--print", "hello"},
			environment: "",
			command:     []string{"claude", "--print", "hello"},
		},
		{
			name:        "explicit Environment",
			arguments:   []string{"run", "--env", "work", "--", "codex", "exec"},
			environment: environment.EnvironmentID("work"),
			command:     []string{"codex", "exec"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := parseRun(test.arguments)
			if err != nil {
				t.Fatalf("parseRun() error = %v", err)
			}
			if parsed.environmentID != test.environment ||
				!slices.Equal(parsed.command, test.command) {
				t.Fatalf("parseRun() = %+v", parsed)
			}
		})
	}
}

func TestParseRunSelectsAnExplicitServerAddress(t *testing.T) {
	t.Parallel()

	parsed, err := parseRun([]string{
		"run",
		"--server", "192.168.1.20:9666",
		"--env", "work",
		"--",
		"codex", "resume", "thread-id",
	})
	if err != nil {
		t.Fatalf("parseRun() error = %v", err)
	}
	if parsed.server.Origin() != "http://192.168.1.20:9666" ||
		parsed.environmentID != environment.EnvironmentID("work") ||
		!slices.Equal(parsed.command, []string{"codex", "resume", "thread-id"}) {
		t.Fatalf("parseRun() = %+v", parsed)
	}
}

func TestParseRunRejectsInvalidOrAmbiguousServerOptions(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"run", "--server", "", "--", "codex"},
		{"run", "--server", "192.168.1.20", "--", "codex"},
		{"run", "--server", "ftp://192.168.1.20:9666", "--", "codex"},
		{"run", "--server", "host:9666", "--server", "other:9666", "--", "codex"},
		// Run admission is Server-owned; the client cannot grant itself an
		// approval bypass.
		{"run", "--client-approval", "never", "--", "codex"},
		{"run", "--env", "work", "--unknown", "value", "--", "codex"},
	} {
		if _, err := parseRun(arguments); err == nil {
			t.Fatalf("parseRun(%v) succeeded", arguments)
		}
	}
}

func TestParseRunKeepsAnExplicitHTTPSServerWithoutFallingBack(t *testing.T) {
	t.Parallel()

	parsed, err := parseRun([]string{
		"run", "--server", "https://VIBERMATE.LAN:9666", "--", "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.server.Origin() != "https://vibermate.lan:9666" {
		t.Fatalf("server = %q", parsed.server.Origin())
	}
}

// A post-start timeout must keep the supervision summary and show a safe detail,
// without printing run IDs, capabilities, origins, or untrusted response content.
func TestCaptureSupervisionTimeoutDetail(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("requires a shell child")
	}
	t.Setenv("HOME", t.TempDir())
	directory := t.TempDir()
	executable := filepath.Join(directory, "agent")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nread line\n"), 0700); err != nil {
		t.Fatal(err)
	}
	input, send, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer send.Close()
	credential := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))
	const runID = "private-run-FAKE_CREDENTIAL"
	var attached atomic.Bool
	var finished atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/attach-process") {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		switch r.URL.Path {
		case "/api/v1/capture-runs":
			if r.Header.Get("Authorization") != "Bearer "+credential {
				t.Error("missing control credential")
			}
			now := time.Now().UTC()
			cwd, err := os.Getwd()
			if err != nil {
				t.Error(err)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(capturecontrol.LaunchGrant{
				Run:             capturecontrol.CaptureRunView{ID: runID, ExecutableLabel: "agent", CWD: cwd, CreatedAt: now, ExpiresAt: now.Add(time.Minute), ClientAdapterState: clientadapter.StatusGeneric, ClientRecognition: clientadapter.RecognitionUnknown, CatalogRevision: 7},
				CatalogRevision: 7, Recognition: clientadapter.RecognitionUnknown, LaunchRecipe: clientadapter.LaunchGeneric,
				ExecutablePath: executable, ProxyAddress: "http://" + r.Host, ProxyDelivery: capturecontrol.ProxyDeliveryLocalListener,
				ProxyToken: "proxy-FAKE_CREDENTIAL", RunCapability: "run-FAKE_CREDENTIAL", ProtectedAuthorities: []string{}, ManagedCredentialAuthorities: []string{},
			})
		case "/api/v1/capture-runs/" + runID + "/actions/attach-process":
			var input capturecontrol.AttachRequest
			// The launcher starts a real child before issuing this request.
			if r.Header.Get(capturecontrol.RunCapabilityHeader) != "run-FAKE_CREDENTIAL" {
				t.Error("missing run capability")
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.ProcessID <= 0 {
				t.Errorf("invalid child attachment: %+v %v", input, err)
			}
			attached.Store(true)
			<-r.Context().Done()
		case "/api/v1/capture-runs/" + runID + "/actions/finish":
			finished.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	layout, err := runtimepath.Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.RuntimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(localdiscovery.Session{Schema: localdiscovery.Schema, InstanceID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x49}, 16)), ProcessID: os.Getpid(), BaseURL: server.URL, ControlCredential: credential, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.CLIControlRecord, payload, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	env := []string{"LANG=en_US.UTF-8", "PATH=" + directory + ":/usr/bin:/bin"}
	code, key := executeContext(ctx, []string{"run", "--", executable}, env, input, &stdout, &stderr)
	if code != 1 || key != "cli.error.captureSupervisionFailed" || !attached.Load() || !finished.Load() {
		t.Fatalf("supervision lifecycle: code=%d key=%q attached=%t finished=%t stderr=%q", code, key, attached.Load(), finished.Load(), stderr.String())
	}
	// main renders the returned key; use the same localized renderer here.
	if err := renderCLIMessage(env, &stderr, key, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Capture control request failed: stage=process_attach reason=timeout.") || !strings.Contains(stderr.String(), "supervision") {
		t.Fatalf("missing safe supervision detail: %q", stderr.String())
	}
	for _, secret := range []string{"FAKE_CREDENTIAL", credential, server.URL, runID} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("unsafe diagnostic: %q", stderr.String())
		}
	}
}

func TestControlTimeoutDetailUsesFailureWording(t *testing.T) {
	for _, test := range []struct{ locale, want string }{
		{"en_US.UTF-8", "Capture control request failed: stage=process_attach reason=timeout.\n"},
		{"zh_CN.UTF-8", "\u6293\u5305\u63a7\u5236\u8bf7\u6c42\u5931\u8d25\uff1astage=process_attach reason=timeout\u3002\n"},
	} {
		t.Run(test.locale, func(t *testing.T) {
			var stderr bytes.Buffer
			err := renderCLIMessage([]string{"LANG=" + test.locale}, &stderr, "cli.error.controlFailureDetail", map[string]string{"details": "stage=process_attach reason=timeout"})
			if err != nil || stderr.String() != test.want {
				t.Fatalf("timeout classified as rejection: output=%q err=%v", stderr.String(), err)
			}
		})
	}
}

func TestParseRunManagedAuth(t *testing.T) {
	got, err := parseRun([]string{"run", "--env", "work", "--managed-auth", "--", "codex", "resume", "fixture-id"})
	if err != nil {
		t.Fatalf("opt-in rejected: %v", err)
	}
	if !got.managedAuth || got.environmentID.String() != "work" || !slices.Equal(got.command, []string{"codex", "resume", "fixture-id"}) {
		t.Fatalf("opt-in invocation changed: %+v", got)
	}
	for _, args := range [][]string{
		{"run", "--managed-auth", "--", "codex"},
		{"run", "--env", "work", "--managed-auth", "--managed-auth", "--", "codex"},
		{"run", "--env", "work", "--codex-provider", "--", "codex"},
		{"run", "--env", "work", "--managed-auth=true", "--", "codex"},
		{"run", "--env", "work", "--managed-auth", "true", "--", "codex"},
		{"run", "--env", "work", "--managed-auth", "--"},
	} {
		if _, err := parseRun(args); err == nil {
			t.Errorf("invalid opt-in accepted: %v", args)
		}
	}
}

func TestManagedAuthDiagnosticsAreLocalized(t *testing.T) {
	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []locales.Locale{locales.EnglishUS, locales.SimplifiedChinese} {
		for _, key := range []string{"cli.managedAuth.active", "cli.error.managedAuth.environmentRequired", "cli.error.managedAuth.clientRequired", "cli.error.managedAuth.managedDestinationRequired", "cli.error.managedAuth.conflict"} {
			if message, err := catalogs.Render(locale, key, nil); err != nil || message == "" {
				t.Fatalf("missing managed-auth diagnostic: %s %s %v", locale, key, err)
			}
		}
	}
	code, key := execute([]string{"run", "--managed-auth", "--", "codex"}, []string{"LANG=en_US.UTF-8"}, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 2 || key != "cli.error.managedAuth.environmentRequired" {
		t.Fatalf("missing environment diagnostic: %d %s", code, key)
	}
}
