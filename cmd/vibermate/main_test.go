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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

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
		{"status"},
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
		{err: runlauncher.ErrEnvironmentNotFound, want: keyEnvironmentMissing},
		{err: runlauncher.ErrEnvironmentUnavailable, want: keyEnvironmentDown},
		{err: runlauncher.ErrRuntimeUnavailable, want: keyRuntimeUnavailable},
		{err: runlauncher.ErrRemoteLoginRequired, want: keyRemoteLoginRequired},
		{err: runlauncher.ErrCapturePreparationTimedOut, want: keyCapturePreparationTimedOut},
		{err: errors.New("other launch failure"), want: keyLaunchFailed},
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
			APIVersion: "v1", SessionID: "login.test", SessionToken: token,
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
