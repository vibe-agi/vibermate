package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const (
	runlauncherPackage   = modulePath + "/internal/runlauncher"
	loopbackproxyPackage = modulePath + "/internal/loopbackproxy"
	clientadapterPackage = modulePath + "/internal/clientadapter"

	claudeFallback = "TestInstalledClaudeLeavesStreamFallbackToCore"
	claudeTools    = "TestInstalledClaudeWorkspaceToolsMatchTheClassifier"
	claudeIdentity = "TestInstalledClaudeIsRecognizedByItsSigner"
	claudeTurn     = "TestInstalledClaudeCompletesATurnThroughLauncher"
	codexKeepalive = "TestInstalledCodexAcceptsTheDataKeepalive"
	codexFallback  = "TestInstalledCodexFallsBackToHTTPAfterProxy426"
	codexIdentity  = "TestInstalledCodexIsRecognizedByItsSigner"
)

// streamBuilder writes a go test -json stream in the shape test2json gives it:
// a run event, output, and a terminal action for each test, with a parent's
// terminal action after its subtests'.
type streamBuilder struct {
	lines []string
}

func (builder *streamBuilder) event(action, pkg, test, output string) *streamBuilder {
	event := map[string]any{"Action": action, "Package": pkg}
	if test != "" {
		event["Test"] = test
	}
	if output != "" {
		event["Output"] = output
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	builder.lines = append(builder.lines, string(encoded))
	return builder
}

func (builder *streamBuilder) raw(line string) *streamBuilder {
	builder.lines = append(builder.lines, line)
	return builder
}

type subtest struct {
	name   string
	action string
}

// test writes one top-level test and its subtests, in order.
func (builder *streamBuilder) test(pkg, name, action string, subtests ...subtest) *streamBuilder {
	builder.event("run", pkg, name, "")
	builder.event("output", pkg, name, "=== RUN   "+name+"\n")
	for _, sub := range subtests {
		full := name + "/" + sub.name
		builder.event("run", pkg, full, "")
		builder.event("output", pkg, full, "=== RUN   "+full+"\n")
		builder.event("output", pkg, full, "    arm line of "+full+"\n")
		builder.event(sub.action, pkg, full, "")
	}
	builder.event(action, pkg, name, "")
	return builder
}

// pkg writes a package's own result after its tests.
func (builder *streamBuilder) pkg(pkg, action string) *streamBuilder {
	builder.event("output", pkg, "", strings.ToUpper(action)+"\t"+pkg+"\n")
	return builder.event(action, pkg, "", "")
}

func (builder *streamBuilder) String() string {
	if len(builder.lines) == 0 {
		return ""
	}
	return strings.Join(builder.lines, "\n") + "\n"
}

func newStream() *streamBuilder {
	return &streamBuilder{}
}

func passingClaude(builder *streamBuilder) *streamBuilder {
	return builder.
		test(runlauncherPackage, claudeTurn, "pass", subtest{"recognized/own_credential", "pass"}).
		test(runlauncherPackage, claudeFallback, "pass",
			subtest{"protected/recognized_upstream/cut", "pass"},
			subtest{"control/recognized_original/cut", "pass"}).
		test(runlauncherPackage, claudeTools, "pass",
			subtest{"protected/effects", "pass"},
			subtest{"refuses/basis_none", "pass"})
}

type verifyRun struct {
	exit     int
	stdout   string
	stderr   string
	proof    proofDocument
	output   string
	hasProof bool
}

func verifyStream(t *testing.T, clientID, goos, input string, machine host) verifyRun {
	t.Helper()

	directory := t.TempDir()
	inputPath := filepath.Join(directory, "attempt-1.json")
	if err := os.WriteFile(inputPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(directory, "proof.json")
	outputPath := filepath.Join(directory, "github-output")
	var stdout, stderr bytes.Buffer
	result := verifyRun{exit: run([]string{
		"verify", "--client", clientID, "--os", goos, "--version", "9.9.9",
		"--input", inputPath, "--proof", proofPath, "--github-output", outputPath,
	}, &stdout, &stderr, machine)}
	result.stdout, result.stderr = stdout.String(), stderr.String()
	if encoded, err := os.ReadFile(proofPath); err == nil {
		result.hasProof = true
		if err := json.Unmarshal(encoded, &result.proof); err != nil {
			t.Fatalf("the proof file is not JSON: %v\n%s", err, encoded)
		}
	}
	if encoded, err := os.ReadFile(outputPath); err == nil {
		result.output = string(encoded)
	}
	return result
}

func linuxHost() host {
	return host{goos: "linux", goarch: "amd64", commit: "0123abcd", run: "42"}
}

func darwinHost() host {
	return host{goos: "darwin", goarch: "arm64", commit: "0123abcd", run: "42"}
}

func TestVerifyOutcomes(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		clientID    string
		goos        string
		stream      string
		outcome     string
		results     []string // per required proof, in policy order
		protections string   // the GitHub output
	}{
		{
			name:     "every proof passes",
			clientID: "claude-code", goos: "linux",
			stream:  passingClaude(newStream()).pkg(runlauncherPackage, "pass").String(),
			outcome: outcomePass, results: []string{"pass", "pass"},
		},
		{
			name:     "every proof passes on darwin, the identity proof included",
			clientID: "claude-code", goos: "darwin",
			stream: passingClaude(newStream()).pkg(runlauncherPackage, "pass").
				test(clientadapterPackage, claudeIdentity, "pass", subtest{"refuses/modified_copy", "pass"}).
				pkg(clientadapterPackage, "pass").String(),
			outcome: outcomePass, results: []string{"pass", "pass", "pass"},
		},
		{
			name:     "a required proof skipped",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "pass", subtest{"protected/recognized_upstream/cut", "pass"}).
				test(runlauncherPackage, claudeTools, "skip").
				pkg(runlauncherPackage, "pass").String(),
			outcome: outcomeBroken, results: []string{"pass", "skip"},
			protections: "structured_workspace_tools",
		},
		{
			name:     "a required proof missing",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "pass", subtest{"protected/recognized_upstream/cut", "pass"}).
				pkg(runlauncherPackage, "pass").String(),
			outcome: outcomeBroken, results: []string{"pass", "missing"},
			protections: "structured_workspace_tools",
		},
		{
			// The identity proof skips on Linux by design; on darwin a run
			// that did not select it proves nothing about the signature.
			name:     "the identity proof missing on darwin",
			clientID: "claude-code", goos: "darwin",
			stream:  passingClaude(newStream()).pkg(runlauncherPackage, "pass").String(),
			outcome: outcomeBroken, results: []string{"pass", "pass", "missing"},
			protections: identityProtection,
		},
		{
			name:     "the identity proof skipped on darwin",
			clientID: "codex-cli", goos: "darwin",
			stream: newStream().
				test(loopbackproxyPackage, codexKeepalive, "pass", subtest{"protected/data_frame", "pass"}).
				test(loopbackproxyPackage, codexFallback, "pass", subtest{"control/websocket_attempted", "pass"}).
				pkg(loopbackproxyPackage, "pass").
				test(clientadapterPackage, codexIdentity, "skip").
				pkg(clientadapterPackage, "pass").String(),
			outcome: outcomeBroken, results: []string{"pass", "pass", "skip"},
			protections: identityProtection,
		},
		{
			name:     "only a control arm failed",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "fail",
					subtest{"protected/recognized_upstream/cut", "pass"},
					subtest{"control/recognized_original/cut", "fail"}).
				test(runlauncherPackage, claudeTools, "pass").
				pkg(runlauncherPackage, "fail").String(),
			outcome: outcomeDrift, results: []string{"fail", "pass"},
			protections: "core_owned_streaming_fallback",
		},
		{
			name:     "control arms of two Codex proofs failed",
			clientID: "codex-cli", goos: "linux",
			stream: newStream().
				test(loopbackproxyPackage, codexKeepalive, "fail",
					subtest{"protected/data_frame", "pass"},
					subtest{"control/comment_only", "fail"}).
				test(loopbackproxyPackage, codexFallback, "fail",
					subtest{"protected/production_shape", "pass"},
					subtest{"control/websocket_attempted", "fail"}).
				pkg(loopbackproxyPackage, "fail").String(),
			outcome: outcomeDrift, results: []string{"fail", "fail"},
			protections: "responses_data_keepalive,responses_websocket_http_fallback",
		},
		{
			name:     "a control and a protected arm failed",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "fail",
					subtest{"protected/recognized_upstream/cut", "fail"},
					subtest{"control/recognized_original/cut", "fail"}).
				test(runlauncherPackage, claudeTools, "pass").
				pkg(runlauncherPackage, "fail").String(),
			outcome: outcomeBroken, results: []string{"fail", "pass"},
			protections: "core_owned_streaming_fallback",
		},
		{
			name:     "a refuses arm failed",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "pass").
				test(runlauncherPackage, claudeTools, "fail",
					subtest{"protected/effects", "pass"},
					subtest{"refuses/extra_property", "fail"}).
				pkg(runlauncherPackage, "fail").String(),
			outcome: outcomeBroken, results: []string{"pass", "fail"},
			protections: "structured_workspace_tools",
		},
		{
			// A parent that fails on its own, after its subtests passed, is a
			// leaf with no control segment.
			name:     "a top-level proof failed outside its arms",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "pass").
				test(runlauncherPackage, claudeTools, "fail",
					subtest{"control/never", "pass"}).
				pkg(runlauncherPackage, "fail").String(),
			outcome: outcomeBroken, results: []string{"pass", "fail"},
			protections: "structured_workspace_tools",
		},
		{
			name:     "a control arm of a test no policy row cites failed",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeTurn, "fail", subtest{"control/recognized", "fail"}).
				test(runlauncherPackage, claudeFallback, "pass").
				test(runlauncherPackage, claudeTools, "pass").
				pkg(runlauncherPackage, "fail").String(),
			outcome: outcomeBroken, results: []string{"pass", "pass"},
		},
		{
			name:     "a drift beside a skipped proof",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "fail",
					subtest{"control/recognized_original/cut", "fail"}).
				test(runlauncherPackage, claudeTools, "skip").
				pkg(runlauncherPackage, "fail").String(),
			outcome: outcomeBroken, results: []string{"fail", "skip"},
			protections: "core_owned_streaming_fallback,structured_workspace_tools",
		},
		{
			name:     "a package failed outside any test",
			clientID: "claude-code", goos: "linux",
			stream:  passingClaude(newStream()).pkg(runlauncherPackage, "fail").String(),
			outcome: outcomeBroken, results: []string{"pass", "pass"},
		},
		{
			name:     "another package did not build",
			clientID: "claude-code", goos: "linux",
			stream: passingClaude(newStream()).pkg(runlauncherPackage, "pass").
				raw(`{"ImportPath":"` + clientadapterPackage + ` [` + clientadapterPackage + `.test]","Action":"build-output","Output":"undefined: x\n"}`).
				raw(`{"ImportPath":"` + clientadapterPackage + ` [` + clientadapterPackage + `.test]","Action":"build-fail"}`).
				raw(`{"Action":"fail","Package":"` + clientadapterPackage + `","FailedBuild":"` + clientadapterPackage + `"}`).String(),
			outcome: outcomeBroken, results: []string{"pass", "pass"},
		},
		{
			// A panic in another test or a timeout ends the binary while a
			// proof is still running.
			name:     "a proof that never finished",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "pass").
				event("run", runlauncherPackage, claudeTools, "").
				event("run", runlauncherPackage, claudeTools+"/protected/effects", "").
				pkg(runlauncherPackage, "fail").String(),
			outcome: outcomeBroken, results: []string{"pass", "fail"},
			protections: "structured_workspace_tools",
		},
		{
			name:     "a line that is not a test event",
			clientID: "claude-code", goos: "linux",
			stream: passingClaude(newStream()).pkg(runlauncherPackage, "pass").
				raw("go: downloading example.com/m v1.0.0").String(),
			outcome: outcomeBroken, results: []string{"pass", "pass"},
		},
		{
			name:     "an empty stream",
			clientID: "claude-code", goos: "linux",
			stream:  "",
			outcome: outcomeBroken, results: []string{"missing", "missing"},
			protections: "core_owned_streaming_fallback,structured_workspace_tools",
		},
		{
			// Results are keyed by package as well as name.
			name:     "a proof that passed in another package",
			clientID: "claude-code", goos: "linux",
			stream: newStream().
				test(runlauncherPackage, claudeFallback, "pass").
				test(clientadapterPackage, claudeTools, "pass").
				pkg(runlauncherPackage, "pass").
				pkg(clientadapterPackage, "pass").String(),
			outcome: outcomeBroken, results: []string{"pass", "missing"},
			protections: "structured_workspace_tools",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			machine := linuxHost()
			if testCase.goos == "darwin" {
				machine = darwinHost()
			}
			got := verifyStream(t, testCase.clientID, testCase.goos, testCase.stream, machine)
			wantExit := 1
			if testCase.outcome == outcomePass {
				wantExit = 0
			}
			if got.exit != wantExit {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", got.exit, wantExit, got.stdout, got.stderr)
			}
			if !got.hasProof {
				t.Fatalf("no proof file was written; stderr: %s", got.stderr)
			}
			proofs, err := requiredProofs(testCase.clientID, testCase.goos)
			if err != nil {
				t.Fatal(err)
			}
			want := proofDocument{
				Schema:  proofSchema,
				Client:  testCase.clientID,
				Version: "9.9.9",
				OS:      testCase.goos,
				Arch:    machine.goarch,
				Commit:  "0123abcd",
				Run:     "42",
				Outcome: testCase.outcome,
			}
			if len(testCase.results) != len(proofs) {
				t.Fatalf("the case names %d results for %d required proofs", len(testCase.results), len(proofs))
			}
			for index, proof := range proofs {
				want.Protections = append(want.Protections, protectionResult{
					Feature: proof.protection,
					Test:    proof.canary.Test,
					Result:  testCase.results[index],
				})
			}
			if !reflect.DeepEqual(got.proof, want) {
				t.Fatalf("proof = %+v\nwant  %+v\nstdout:\n%s", got.proof, want, got.stdout)
			}
			wantOutput := "outcome=" + testCase.outcome + "\nprotections=" + testCase.protections + "\n"
			if got.output != wantOutput {
				t.Fatalf("GitHub output = %q, want %q", got.output, wantOutput)
			}
			if !strings.Contains(got.stdout, testCase.clientID+" 9.9.9 on "+testCase.goos+"/"+machine.goarch+": "+testCase.outcome) {
				t.Fatalf("stdout does not state the outcome:\n%s", got.stdout)
			}
		})
	}
}

func TestVerifyReplaysTheTestOutput(t *testing.T) {
	t.Parallel()

	stream := newStream().
		test(runlauncherPackage, claudeFallback, "fail",
			subtest{"control/recognized_original/cut", "fail"}).
		test(runlauncherPackage, claudeTools, "pass").
		pkg(runlauncherPackage, "fail").String()
	got := verifyStream(t, "claude-code", "linux", stream, linuxHost())
	for _, line := range []string{
		"=== RUN   " + claudeFallback + "/control/recognized_original/cut\n",
		"    arm line of " + claudeFallback + "/control/recognized_original/cut\n",
		"FAIL\t" + runlauncherPackage + "\n",
		"  failed: " + claudeFallback + "/control/recognized_original/cut in " + runlauncherPackage + "\n",
		"  core_owned_streaming_fallback: " + claudeFallback + " fail\n",
	} {
		if !strings.Contains(got.stdout, line) {
			t.Fatalf("stdout lacks %q:\n%s", line, got.stdout)
		}
	}
}

func TestVerifyAppendsOneVerdictPerAttempt(t *testing.T) {
	t.Parallel()

	// The workflow verifies a second attempt into the same GitHub output
	// file; the later line of a key is the one GitHub keeps.
	directory := t.TempDir()
	outputPath := filepath.Join(directory, "github-output")
	if err := os.WriteFile(outputPath, []byte("version=9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt, stream := range []string{
		"",
		passingClaude(newStream()).pkg(runlauncherPackage, "pass").String(),
	} {
		inputPath := filepath.Join(directory, "attempt.json")
		if err := os.WriteFile(inputPath, []byte(stream), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		exit := run([]string{
			"verify", "--client", "claude-code", "--os", "linux", "--version", "9.9.9",
			"--input", inputPath, "--proof", filepath.Join(directory, "proof.json"),
			"--github-output", outputPath,
		}, &stdout, &stderr, linuxHost())
		if want := []int{1, 0}[attempt]; exit != want {
			t.Fatalf("attempt %d: exit = %d, want %d; stderr %s", attempt+1, exit, want, stderr.String())
		}
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "version=9.9.9\n" +
		"outcome=broken\nprotections=core_owned_streaming_fallback,structured_workspace_tools\n" +
		"outcome=pass\nprotections=\n"
	if string(encoded) != want {
		t.Fatalf("GitHub output = %q, want %q", encoded, want)
	}
}

func TestRequiredProofsNameEveryProtection(t *testing.T) {
	t.Parallel()

	type named struct {
		protection string
		canary     clientadapter.Canary
	}
	fallback := clientadapter.Canary{Package: "internal/runlauncher", Test: claudeFallback}
	tools := clientadapter.Canary{Package: "internal/runlauncher", Test: claudeTools}
	claudeSigner := clientadapter.Canary{Package: "internal/clientadapter", Test: claudeIdentity}
	keepalive := clientadapter.Canary{Package: "internal/loopbackproxy", Test: codexKeepalive}
	websocket := clientadapter.Canary{Package: "internal/loopbackproxy", Test: codexFallback}
	codexSigner := clientadapter.Canary{Package: "internal/clientadapter", Test: codexIdentity}
	for _, testCase := range []struct {
		clientID string
		goos     string
		want     []named
	}{
		{"claude-code", "linux", []named{
			{"core_owned_streaming_fallback", fallback},
			{"structured_workspace_tools", tools},
		}},
		{"claude-code", "darwin", []named{
			{"core_owned_streaming_fallback", fallback},
			{"structured_workspace_tools", tools},
			{identityProtection, claudeSigner},
		}},
		{"codex-cli", "linux", []named{
			{"responses_data_keepalive", keepalive},
			{"responses_websocket_http_fallback", websocket},
		}},
		{"codex-cli", "darwin", []named{
			{"responses_data_keepalive", keepalive},
			{"responses_websocket_http_fallback", websocket},
			{identityProtection, codexSigner},
		}},
	} {
		proofs, err := requiredProofs(testCase.clientID, testCase.goos)
		if err != nil {
			t.Fatalf("requiredProofs(%q, %q): %v", testCase.clientID, testCase.goos, err)
		}
		var got []named
		for _, proof := range proofs {
			got = append(got, named{proof.protection, proof.canary})
		}
		if !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("requiredProofs(%q, %q) = %+v, want %+v", testCase.clientID, testCase.goos, got, testCase.want)
		}
		// The required set is ProtectionCanaries, nothing more or less.
		var canaries []clientadapter.Canary
		for _, proof := range proofs {
			canaries = append(canaries, proof.canary)
		}
		if want := clientadapter.ProtectionCanaries(testCase.clientID, testCase.goos); !reflect.DeepEqual(canaries, want) {
			t.Fatalf("required canaries = %+v, ProtectionCanaries = %+v", canaries, want)
		}
	}
}

func TestTestsPatternMatchesTheRequiredSet(t *testing.T) {
	t.Parallel()

	identities := map[string]string{"claude-code": claudeIdentity, "codex-cli": codexIdentity}
	others := map[string][]string{
		"claude-code": {claudeTurn, "TestInstalledClaude", codexKeepalive},
		"codex-cli": {
			"TestInstalledCodexProgressSurvivesNativeStream", "TestInstalledCodex", claudeFallback,
		},
	}
	for _, policy := range clientadapter.RecognizedClientPolicies() {
		for _, goos := range []string{"linux", "darwin"} {
			var stdout, stderr bytes.Buffer
			if exit := run([]string{"tests", "--client", policy.ClientID, "--os", goos},
				&stdout, &stderr, linuxHost()); exit != 0 {
				t.Fatalf("tests %s %s: exit %d, stderr %s", policy.ClientID, goos, exit, stderr.String())
			}
			printed := stdout.String()
			if !strings.HasSuffix(printed, "\n") || strings.Count(printed, "\n") != 1 {
				t.Fatalf("tests %s %s printed %q, want one line", policy.ClientID, goos, printed)
			}
			pattern := strings.TrimSuffix(printed, "\n")
			// go test splits -run on slashes into per-level patterns.
			if !strings.HasPrefix(pattern, "^(") || !strings.HasSuffix(pattern, ")$") ||
				strings.Contains(pattern, "/") {
				t.Fatalf("pattern %q is not one anchored top-level alternation", pattern)
			}
			compiled := regexp.MustCompile(pattern)
			required := map[string]bool{}
			for _, canary := range clientadapter.ProtectionCanaries(policy.ClientID, goos) {
				required[canary.Test] = true
				if !compiled.MatchString(canary.Test) {
					t.Fatalf("pattern %q does not select %s", pattern, canary.Test)
				}
				for _, nearMiss := range []string{canary.Test + "X", "X" + canary.Test, canary.Test[:len(canary.Test)-1]} {
					if compiled.MatchString(nearMiss) {
						t.Fatalf("pattern %q selects %s", pattern, nearMiss)
					}
				}
			}
			if len(required) == 0 {
				t.Fatalf("%s on %s requires no test", policy.ClientID, goos)
			}
			for _, other := range others[policy.ClientID] {
				if compiled.MatchString(other) {
					t.Fatalf("pattern %q selects %s, which no row cites", pattern, other)
				}
			}
			identity := identities[policy.ClientID]
			if identity == "" {
				t.Fatalf("no identity test is known for %s", policy.ClientID)
			}
			if selected := compiled.MatchString(identity); selected != (goos == "darwin") {
				t.Fatalf("pattern %q selects %s: %t, want %t", pattern, identity, selected, goos == "darwin")
			}
		}
	}
}

func TestUsageErrorsWriteNothing(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := filepath.Join(directory, "attempt-1.json")
	if err := os.WriteFile(input, []byte(passingClaude(newStream()).String()), 0o600); err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(directory, "proof.json")
	output := filepath.Join(directory, "github-output")
	verify := func(overrides ...string) []string {
		values := map[string]string{
			"--client": "claude-code", "--os": "linux", "--version": "2.1.330",
			"--input": input, "--proof": proof, "--github-output": output,
		}
		for index := 0; index+1 < len(overrides); index += 2 {
			values[overrides[index]] = overrides[index+1]
		}
		arguments := []string{"verify"}
		for _, name := range []string{"--client", "--os", "--version", "--input", "--proof", "--github-output"} {
			if values[name] != "" {
				arguments = append(arguments, name, values[name])
			}
		}
		return arguments
	}
	for _, testCase := range []struct {
		name      string
		arguments []string
		machine   host
	}{
		{"no command", nil, linuxHost()},
		{"an unknown command", []string{"judge"}, linuxHost()},
		{"tests for an unknown client", []string{"tests", "--client", "gemini-cli", "--os", "linux"}, linuxHost()},
		{"tests without a client", []string{"tests", "--os", "linux"}, linuxHost()},
		{"tests for macos", []string{"tests", "--client", "claude-code", "--os", "macos"}, linuxHost()},
		{"tests without an os", []string{"tests", "--client", "claude-code"}, linuxHost()},
		{"tests with a positional argument", []string{"tests", "--client", "claude-code", "--os", "linux", "x"}, linuxHost()},
		{"verify for an unknown client", verify("--client", "gemini-cli"), linuxHost()},
		{"verify for macos", verify("--os", "macos"), linuxHost()},
		{"verify for darwin on a linux host", verify("--os", "darwin"), linuxHost()},
		{"verify for linux on a darwin host", verify(), darwinHost()},
		{"verify without a version", verify("--version", ""), linuxHost()},
		{"verify with a version holding a space", verify("--version", "2.1.330 x"), linuxHost()},
		{"verify with a version holding a newline", verify("--version", "2.1.330\noutcome=pass"), linuxHost()},
		{"verify without an input", verify("--input", ""), linuxHost()},
		{"verify without a proof path", verify("--proof", ""), linuxHost()},
		{"verify of a missing input", verify("--input", filepath.Join(directory, "absent.json")), linuxHost()},
		{"verify with an unknown flag", append(verify(), "--outcome", "pass"), linuxHost()},
	} {
		var stdout, stderr bytes.Buffer
		if exit := run(testCase.arguments, &stdout, &stderr, testCase.machine); exit != 2 {
			t.Fatalf("%s: exit = %d, want 2; stdout %q", testCase.name, exit, stdout.String())
		}
		if stderr.Len() == 0 {
			t.Fatalf("%s: nothing on standard error", testCase.name)
		}
		for _, path := range []string{proof, output} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("%s: %s was written", testCase.name, path)
			}
		}
	}
}

func TestModulePathIsThisModule(t *testing.T) {
	t.Parallel()

	encoded, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(encoded), "\n")
	if first != "module "+modulePath {
		t.Fatalf("go.mod begins %q, want module %s", first, modulePath)
	}
}
