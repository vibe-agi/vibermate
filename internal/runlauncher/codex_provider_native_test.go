package runlauncher_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func TestInstalledCodexManagedAuthEmptyHomeAndCaptureIsolation(t *testing.T) {
	binary := codexProviderNativeBinary(t)
	home, config := codexProviderNativeHome(t, "")
	firstMarker, nextMarker := "fresh supplemental conversation marker", "supplemental continuation marker"
	first := newCodexProviderNativeFixture(t, binary, home, "work", "chatgpt.com:443", codexProviderNativeExec("", firstMarker), 0x71, true)
	id := codexProviderNativeRun(t, first, binary, home, true)
	first.assertRequest("vibermate-local-proxy", firstMarker)
	path, before := codexProviderNativeHistory(t, home, id, "vibermate")
	codexProviderNativeUnchanged(t, home, config, nil)

	second := newCodexProviderNativeFixture(t, binary, home, "personal", "api.openai.com:443", codexProviderNativeExec(id, nextMarker), 0x81, true)
	if got := codexProviderNativeRun(t, second, binary, home, true); got != id {
		t.Fatalf("supplemental resume changed thread ID: got %s want %s", got, id)
	}
	second.assertRequest("vibermate-local-proxy", firstMarker, "synthetic reply for work", nextMarker)
	resumedPath, after := codexProviderNativeHistory(t, home, id, "vibermate")
	if path != resumedPath || !bytes.HasPrefix(after, before) || !bytes.Contains(after, []byte(nextMarker)) {
		t.Fatal("supplemental resume did not append to the original history path")
	}
	if first.control.proxy == second.control.proxy || first.control.expectedEnvironment == second.control.expectedEnvironment || first.requests[0].authority == second.requests[0].authority {
		t.Fatal("independent Capture grants did not retain distinct capability and Environment routing")
	}
	// A capability from the first Capture must not admit traffic at the second.
	codexProviderNativeRejectForeignCapability(t, second, first.control.proxy)
	codexProviderNativeUnchanged(t, home, config, nil)
}

func TestInstalledCodexManagedAuthResumesOpenAIHistoryWithoutLogin(t *testing.T) {
	binary := codexProviderNativeBinary(t)
	home, config := codexProviderNativeHome(t, "")
	auth := []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-file-api-key"}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	seedMarker, nextMarker := "original openai conversation marker", "cross-provider continuation marker"
	seed := newCodexProviderNativeFixture(t, binary, home, environment.SystemTransparentID.String(), "api.openai.com:443", codexProviderNativeExec("", seedMarker), 0x91, false)
	id := codexProviderNativeRun(t, seed, binary, home, false)
	seed.assertRequest("synthetic-file-api-key", seedMarker)
	path, before := codexProviderNativeHistory(t, home, id, "openai")
	codexProviderNativeUnchanged(t, home, config, auth)
	if err := os.Remove(filepath.Join(home, "auth.json")); err != nil {
		t.Fatal(err)
	}

	// Releases before Codex bound a conversation to the provider it was saved
	// with resume it in place under ViberMate's provider; later ones refuse.
	// Both are Codex's call. What ViberMate owes either way: a refusal sends
	// nothing, changes nothing, and says how to continue.
	resume := newCodexProviderNativeFixture(t, binary, home, "work", "chatgpt.com:443", codexProviderNativeExec(id, nextMarker), 0xa1, true)
	run := codexProviderNativeTryRun(t, resume, binary, home, true)
	if run.ok {
		t.Log("Codex resumed the conversation in place under ViberMate's provider")
		if run.id != id {
			t.Fatalf("cross-provider resume changed thread ID: got %s want %s", run.id, id)
		}
		resume.assertRequest("vibermate-local-proxy", seedMarker, "synthetic reply for system_transparent", nextMarker)
		resumedPath, after := codexProviderNativeHistory(t, home, id, "openai")
		if resumedPath != path || !bytes.HasPrefix(after, before) || !bytes.Contains(after, []byte(nextMarker)) {
			t.Fatal("cross-provider resume replaced or migrated original history")
		}
		before = after
	} else {
		// Only Codex's own refusal to move the conversation is that behaviour;
		// a resume that fails for any other reason is a regression. ViberMate's
		// lines are left out, since its hint names the provider too.
		var codexOutput []string
		for _, line := range strings.Split(run.stderr, "\n") {
			if !strings.HasPrefix(line, "vibermate") {
				codexOutput = append(codexOutput, line)
			}
		}
		if !strings.Contains(strings.Join(codexOutput, "\n"), "model provider") {
			t.Fatalf("the resume failed for a reason other than the conversation's provider: code=%d err=%v stderr=%s", run.code, run.err, run.stderr)
		}
		t.Log("Codex refused to move the conversation to ViberMate's provider")
		if !strings.Contains(run.stderr, "`fork`") {
			t.Fatalf("a refused resume did not explain how to continue: %s", run.stderr)
		}
		if requests := resume.requestCount(); requests != 0 {
			t.Fatalf("a refused resume reached inference %d times", requests)
		}
		if _, after := codexProviderNativeHistory(t, home, id, "openai"); !bytes.Equal(before, after) {
			t.Fatal("a refused resume changed the original history")
		}
	}

	// Forking continues the conversation under managed auth on every release:
	// a new thread on ViberMate's provider, the original left as it was.
	forkMarker := "cross-provider fork marker"
	forkCommand := []string{"codex", "--no-daemon", "exec", "fork", id, "--skip-git-repo-check", "--json", "--model", "fixture", forkMarker}
	fork := newCodexProviderNativeFixture(t, binary, home, "work", "chatgpt.com:443", forkCommand, 0xe1, true)
	forked := codexProviderNativeRun(t, fork, binary, home, true)
	if forked == id {
		t.Fatalf("fork reused the thread ID %s", id)
	}
	fork.assertRequest("vibermate-local-proxy", seedMarker, forkMarker)
	if _, history := codexProviderNativeHistory(t, home, forked, "vibermate"); !bytes.Contains(history, []byte(forkMarker)) {
		t.Fatal("forked history lacks its continuation")
	}
	if _, after := codexProviderNativeHistory(t, home, id, "openai"); !bytes.Equal(before, after) {
		t.Fatal("forking changed the original history")
	}
	codexProviderNativeUnchanged(t, home, config, nil)
}

func TestInstalledCodexManagedAuthOverridesInheritedStaticBearer(t *testing.T) {
	binary := codexProviderNativeBinary(t)
	provider := "model_provider=\"vibermate\"\n[model_providers.vibermate]\nname=\"Inherited synthetic provider\"\nbase_url=\"https://chatgpt.com/backend-api/codex\"\nwire_api=\"responses\"\nrequires_openai_auth=false\nexperimental_bearer_token=\"synthetic-inherited-static-token\"\n"
	home, config := codexProviderNativeHome(t, provider)
	auth := []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-file-api-key"}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	for index, managed := range []bool{false, true} {
		name, bearer, env := "ordinary", "synthetic-inherited-static-token", environment.SystemTransparentID.String()
		if managed {
			name, bearer, env = "managed", "vibermate-local-proxy", "work"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCodexProviderNativeFixture(t, binary, home, env, "chatgpt.com:443", codexProviderNativeExec("", "inherited bearer fixture"), byte(0xb1+index*4), managed)
			codexProviderNativeRun(t, fixture, binary, home, managed)
			fixture.assertRequest(bearer, "inherited bearer fixture")
			codexProviderNativeUnchanged(t, home, config, auth)
		})
	}
}

func TestInstalledCodexManagedAuthRejectsInheritedCommandAuthBeforeExecution(t *testing.T) {
	binary := codexProviderNativeBinary(t)
	home, _ := codexProviderNativeHome(t, "")
	marker := filepath.Join(home, "command-was-executed")
	commandPath := filepath.Join(home, "synthetic-auth-command")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\ntouch "+strconv.Quote(marker)+"\nprintf synthetic-command-token\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config := codexProviderNativeConfig() + "\n[model_providers.vibermate]\nname=\"Synthetic command provider\"\n[model_providers.vibermate.auth]\ncommand=" + strconv.Quote(commandPath) + "\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := newCodexProviderNativeFixture(t, binary, home, "work", "chatgpt.com:443", codexProviderNativeExec("", "must not reach inference"), 0xc1, true)
	var stdout, stderr bytes.Buffer
	code, err := fixture.launcher(binary, home, strings.NewReader(""), &stdout, &stderr).Run(codexProviderNativeContext(t), runlauncher.LaunchRequest{EnvironmentID: "work", ManagedAuth: true, Command: fixture.control.expectedCommand})
	if err == nil && code == 0 {
		t.Fatal("native provider command-auth/env_key conflict was accepted")
	}
	if !strings.Contains(stderr.String(), "provider auth cannot be combined with env_key") {
		t.Fatalf("missing native fail-closed diagnostic: code=%d err=%v stderr=%s", code, err, &stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("inherited auth command executed: %v", err)
	}
	if len(fixture.requests) != 0 || fixture.control.finishCalls != 1 {
		t.Fatal("conflicting native provider used inference or left Capture unfinished")
	}
	codexProviderNativeUnchanged(t, home, []byte(config), nil)
}

func codexProviderNativeBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("set VIBERMATE_CODEX_ACCEPTANCE to an absolute native Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native Codex binary must be absolute")
	}
	binary, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(codexProviderNativeContext(t), binary, "--version")
	command.Env = codexProviderNativeEnvironment(t.TempDir())
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(strings.TrimSpace(string(output)))
	return binary
}

func codexProviderNativeHome(t *testing.T, extra string) (string, []byte) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Seed only native first-run UX preferences so the TUI does not persist
	// screen-reader detection and the installed release's model announcement.
	config := []byte(codexProviderNativeConfig() + extra + "\n[tui]\nscreen_reader_detection_done=true\n[tui.model_availability_nux]\n\"gpt-6.1-sol\"=4\n[projects." + strconv.Quote(home) + "]\ntrust_level=\"trusted\"\n")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	return home, config
}

func codexProviderNativeConfig() string {
	return "cli_auth_credentials_store=\"file\"\nanalytics.enabled=false\nfeedback.enabled=false\ncheck_for_update_on_startup=false\nfeatures.apps=false\nfeatures.remote_plugin=false\n"
}

func codexProviderNativeExec(id, prompt string) []string {
	args := []string{"codex", "--no-daemon", "exec"}
	if id == "" {
		args = append(args, "--skip-git-repo-check")
	} else {
		args = append(args, "resume", id, "--skip-git-repo-check")
	}
	return append(args, "--json", "--model", "fixture", prompt)
}

// codexProviderNativeTurn is one launched turn's outcome. ok means it
// completed with the fixture's reply and exposed its thread ID.
type codexProviderNativeTurn struct {
	ok             bool
	id             string
	code           int
	err            error
	stdout, stderr string
}

func codexProviderNativeTryRun(t *testing.T, f *codexProviderNativeFixture, binary, home string, managed bool) codexProviderNativeTurn {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code, err := f.launcher(binary, home, strings.NewReader(""), &stdout, &stderr).Run(codexProviderNativeContext(t), runlauncher.LaunchRequest{EnvironmentID: environment.EnvironmentID(f.control.expectedEnvironment), ManagedAuth: managed, Command: f.control.expectedCommand})
	turn := codexProviderNativeTurn{code: code, err: err, stdout: stdout.String(), stderr: stderr.String()}
	if err != nil || code != 0 || !strings.Contains(turn.stdout, `"type":"turn.completed"`) || !strings.Contains(turn.stdout, "synthetic reply for "+f.control.expectedEnvironment) {
		return turn
	}
	for _, line := range strings.Split(turn.stdout, "\n") {
		var event struct {
			Type string `json:"type"`
			ID   string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "thread.started" && event.ID != "" {
			turn.ok, turn.id = true, event.ID
			return turn
		}
	}
	return turn
}

func codexProviderNativeRun(t *testing.T, f *codexProviderNativeFixture, binary, home string, managed bool) string {
	t.Helper()
	turn := codexProviderNativeTryRun(t, f, binary, home, managed)
	if !turn.ok {
		t.Fatalf("native turn failed: code=%d err=%v stdout=%s stderr=%s", turn.code, turn.err, turn.stdout, turn.stderr)
	}
	return turn.id
}

func codexProviderNativeHistory(t *testing.T, home, id, provider string) (string, []byte) {
	t.Helper()
	var found string
	if err := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, "-"+id+".jsonl") {
			if found != "" {
				t.Fatal("duplicate native history for thread")
			}
			found = path
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			ID       string `json:"id"`
			Provider string `json:"model_provider"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(bytes.SplitN(body, []byte("\n"), 2)[0], &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Type != "session_meta" || meta.Payload.ID != id || meta.Payload.Provider != provider {
		t.Fatalf("native session metadata changed: %+v", meta)
	}
	return found, body
}

func codexProviderNativeUnchanged(t *testing.T, home string, config, auth []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || !bytes.Equal(got, config) {
		t.Fatalf("persistent config changed: %v\nbefore=%s\nafter=%s", err, config, got)
	}
	got, err = os.ReadFile(filepath.Join(home, "auth.json"))
	if auth == nil {
		if !os.IsNotExist(err) {
			t.Fatalf("empty login home acquired auth.json: %v", err)
		}
	} else if err != nil || !bytes.Equal(got, auth) {
		t.Fatalf("synthetic auth changed: %v", err)
	}
}
