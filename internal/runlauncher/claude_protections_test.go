package runlauncher_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"github.com/vibe-agi/vibermate/locales"
)

// C1 (ADR 0026): when the launcher leaves stream recovery to ViberMate, an
// installed Claude Code whose stream breaks after ViberMate has answered ends
// the turn rather than sending the same turn again as a second, separately
// billed non-streaming request. The protected arms hold for the protection to
// work; the controls show that the release still falls back on its own when
// it is not told, so the instruction still matters.
//
// Each arm launches `claude -p` through the real launcher with
// CLAUDE_CODE_MAX_RETRIES=0, so the only second request a release may send
// is its own non-streaming fallback. The fixture fails the first streaming
// request that defines tools: it cuts the connection after a delta, or ends
// the stream with the proxy's own in-band error.
func TestInstalledClaudeLeavesStreamFallbackToCore(t *testing.T) {
	binary := claudeNativeBinary(t)
	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	notice, err := catalogs.Render(locales.EnglishUS, "cli.protections.streamingFallback", nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, test := range []struct {
		name      string
		shape     claudeGrantShape
		managed   bool // the client's target is an Upstream-only authority
		failure   claudeStreamFailure
		inherited []string
		protected bool
	}{
		{name: "protected/recognized_upstream/cut", shape: grantRecognized, managed: true, failure: streamCut, protected: true},
		{name: "protected/recognized_upstream/in_band_error", shape: grantRecognized, managed: true, failure: streamInBandError, protected: true},
		// An inherited 0 keeps the release's own fallback; the launcher must
		// replace it.
		{name: "protected/recognized_replaces_inherited_zero/cut", shape: grantRecognized, managed: true, failure: streamCut,
			inherited: []string{"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0"}, protected: true},
		{name: "protected/verified_core_owned/cut", shape: grantVerifiedCoreOwned, managed: true, failure: streamCut, protected: true},
		// The launcher must not set the variable for a recognized client on
		// Original Destination, so the client keeps its own fallback.
		{name: "control/recognized_original/cut", shape: grantRecognized, failure: streamCut},
		{name: "control/verified_client_default/cut", shape: grantVerifiedClientDefault, managed: true, failure: streamCut},
		{name: "control/verified_client_default/in_band_error", shape: grantVerifiedClientDefault, managed: true, failure: streamInBandError},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			command := []string{"claude", "-p", "Reply with the fixture text", "--output-format", "json"}
			f := newClaudeNativeFixture(t, binary, home, command, byte(0x11+index*4), test.managed, test.shape)
			f.failStream(test.failure)
			f.environment = append(f.environment, "CLAUDE_CODE_MAX_RETRIES=0")
			f.environment = append(f.environment, test.inherited...)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			var stdout, stderr strings.Builder
			code, err := f.launcher(binary, home, &stdout, &stderr).Run(ctx, runlauncher.LaunchRequest{EnvironmentID: "work", ManagedAuth: test.managed, Command: command})
			result, parsed := claudeNativeResult(stdout.String(), false)
			fallbacks := f.fallbackRequests(t)
			t.Logf("exit=%d parsed=%v is_error=%v fallback requests=%d", code, parsed, result.IsError, fallbacks)
			if err != nil {
				t.Fatalf("the launch itself failed: code=%d err=%v stderr=%s", code, err, stderr.String())
			}
			// The command says it left recovery to ViberMate exactly when it did.
			if announced := strings.Contains(stderr.String(), notice); announced != test.protected {
				t.Fatalf("the fallback notice was printed=%v, want %v: stderr=%s", announced, test.protected, stderr.String())
			}
			if test.protected {
				if fallbacks != 0 {
					t.Fatalf("the client sent the broken turn again without streaming (%d request(s)) although the launcher left recovery to ViberMate; stdout=%s stderr=%s",
						fallbacks, stdout.String(), stderr.String())
				}
				if code == 0 && (!parsed || !result.IsError) {
					t.Fatalf("the client reported success after its stream broke: stdout=%s stderr=%s", stdout.String(), stderr.String())
				}
			} else {
				if fallbacks != 1 {
					t.Fatalf("the client sent %d non-streaming fallback request(s), want exactly 1: the release may no longer fall back on its own; stdout=%s stderr=%s",
						fallbacks, stdout.String(), stderr.String())
				}
				if code != 0 || !parsed || result.IsError || !strings.Contains(result.Result, f.reply) {
					t.Fatalf("the client's own fallback did not complete the turn: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
				}
			}
			f.assertWire(t)
		})
	}
}

// fallbackRequests counts the later /v1/messages requests that resend the
// failed turn without streaming: the same messages, and stream false or
// absent. It logs every request so a resend that changed its messages is
// visible rather than silently uncounted.
func (f *claudeNativeFixture) fallbackRequests(t *testing.T) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	failed := -1
	for index, request := range f.messages {
		marker := ""
		if request.failed {
			marker = " (failed here)"
		}
		t.Logf("request %d: stream=%v tools=%d messages=%s%s", index, request.stream, request.tools, request.messagesDigest[:12], marker)
		if request.failed && failed < 0 {
			failed = index
		}
	}
	if failed < 0 {
		t.Fatal("no streaming request that defines tools reached the provider, so no stream was failed")
	}
	fallbacks := 0
	for _, request := range f.messages[failed+1:] {
		if !request.stream && request.messagesDigest == f.messages[failed].messagesDigest {
			fallbacks++
		}
	}
	return fallbacks
}

// claudeWorkspaceTools are the built-in tools whose definitions C2 logs.
// Read, Edit, Write and NotebookEdit may be approved on the recognized basis;
// Glob and Grep stay exact-release only until they meet their entry rule.
var claudeWorkspaceTools = []string{"Read", "Edit", "Write", "NotebookEdit", "Glob", "Grep"}

// claudeRecognizedFileTools are the tools whose calls C2 asserts the gate
// approves without a question.
var claudeRecognizedFileTools = map[string]bool{"Read": true, "Edit": true, "Write": true, "NotebookEdit": true}

// C2 (ADR 0026): the built-in file tools an installed Claude Code defines are
// the ones the Core workspace classifier understands. The fixture scripts a
// Read, a Write, an Edit and a NotebookEdit into a fresh workspace and checks
// each file effect. Every call is then decided on the tool gate, over the
// definitions of the request it answered, with the basis this build receives
// in production: the exact release's when the binary is catalogued, and
// otherwise the recognized client's, which also proves each definition's
// closed shape and each call's arguments. A failure means recognized runs
// would ask for this release; C2 has no drift control. The refusals prove
// that the approval can fail.
//
// The fixture is a CONNECT relay, not the Exchange. The wiring from the
// Exchange to the gate is proven by the pipeline and Desktop composition
// tests.
func TestInstalledClaudeWorkspaceToolsMatchTheClassifier(t *testing.T) {
	binary := claudeNativeBinary(t)
	basis := claudeWorkspaceToolsBasis(t, binary)
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	script := &claudeToolScript{
		workspace: workspace, basis: basis, marker: "marker-A-" + hex.EncodeToString(random),
		seen: make(map[string]bool),
	}
	if err := os.WriteFile(script.path("notes.txt"), []byte(script.marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script.path("book.ipynb"), []byte(claudeToolNotebook), 0o600); err != nil {
		t.Fatal(err)
	}
	command := []string{"claude", "-p", "Follow the tool instructions", "--output-format", "json", "--permission-mode", "bypassPermissions"}
	f := newClaudeNativeFixture(t, binary, home, command, 0x61, true, grantRecognized)
	f.control.workspace = workspace
	f.setScript(script.respond)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	var stdout, stderr strings.Builder
	code, runErr := f.launcher(binary, workspace, &stdout, &stderr).Run(ctx, runlauncher.LaunchRequest{EnvironmentID: "work", ManagedAuth: true, Command: command})
	result, parsed := claudeNativeResult(stdout.String(), false)
	t.Logf("exit=%d parsed=%v is_error=%v", code, parsed, result.IsError)
	requests := script.logRun(t, f)
	if runErr != nil {
		t.Fatalf("the launch itself failed: code=%d err=%v stderr=%s", code, runErr, stderr.String())
	}
	shapes := claudeCapturedToolShapes(t)

	t.Run("protected/effects", func(t *testing.T) {
		script.mu.Lock()
		defer script.mu.Unlock()
		for _, failure := range script.failures {
			t.Error(failure)
		}
		if !script.completed {
			t.Errorf("the client did not finish the tool script (stopped before step %d)", script.step)
		}
		if read := script.first("Read"); read == nil || !read.answered || read.isError || !strings.Contains(read.result, script.marker) {
			t.Errorf("no Read result holds %s", script.marker)
		}
		if content, err := os.ReadFile(script.path("created.txt")); err != nil || string(content) != "marker-C" {
			t.Errorf("created.txt = %q (%v), want %q after the Write and the Edit", content, err, "marker-C")
		}
		if script.last("NotebookEdit") != nil {
			if source, err := claudeNotebookCellSource(script.path("book.ipynb"), "c1"); err != nil || source != "marker-N2" {
				t.Errorf("book.ipynb cell c1 = %q (%v), want %q after the NotebookEdit", source, err, "marker-N2")
			}
		} else if script.notebookSeen {
			t.Error("a request defined NotebookEdit, but not the one the script would have called it from")
		}
		if code != 0 || !parsed || result.IsError || !strings.Contains(result.Result, f.reply) {
			t.Errorf("the turn did not complete: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
		// Every request must decode for the Anthropic route, the only one on
		// which the gate sees Claude Code's own definitions. Once ToolSearch
		// has loaded a tool, each later request carries its tool_reference
		// result, which the translated route refuses; that limit is the turn
		// test's concern, so it is logged per request above, not failed here.
		f.checkWire(t, false)
	})

	t.Run("protected/gate", func(t *testing.T) {
		script.mu.Lock()
		calls := append([]*claudeScriptedCall(nil), script.calls...)
		definitions := append([]claudeLoggedDefinition(nil), script.definitions...)
		script.mu.Unlock()
		decided := 0
		for _, call := range calls {
			if !claudeRecognizedFileTools[call.name] {
				t.Logf("%s %s from request %d: approved without a question=%v (not asserted)", call.name, call.arguments, call.request, call.decision.approved)
				continue
			}
			decided++
			if answered := requests[call.request]; answered.sameDialectErr != "" || len(answered.decoded.Tools) == 0 {
				t.Errorf("%s from request %d: the Anthropic route did not decode that request, so the gate had no definitions: %q", call.name, call.request, answered.sameDialectErr)
				continue
			}
			if !call.decision.approved {
				t.Errorf("%s %s from request %d was not approved without a question on the %s basis (review calls %d, err %v): %s",
					call.name, call.arguments, call.request, basis, call.decision.reviews, call.decision.err,
					explainClaudeToolCall(shapes, requests[call.request].decoded.Tools, call.name, call.arguments))
			}
		}
		if decided == 0 {
			t.Error("the fixture issued no file tool call to decide")
		}
		// One in-workspace call for every definition the release sent, also
		// those the script did not call, decided before it may run.
		for _, definition := range definitions {
			if !claudeRecognizedFileTools[definition.name] {
				continue
			}
			arguments := script.syntheticCall(t, definition.name)
			decision := decideClaudeToolCall(workspace, basis, requests[definition.request].decoded.Tools, requests[definition.request].decoded.ToolNamespaces, definition.name, arguments)
			if !decision.approved {
				t.Errorf("a synthetic %s %s over request %d's definitions was not approved without a question on the %s basis (review calls %d, err %v): %s",
					definition.name, arguments, definition.request, basis, decision.reviews, decision.err,
					explainClaudeToolCall(shapes, requests[definition.request].decoded.Tools, definition.name, arguments))
			}
		}
	})

	// The refusals use the release's own definitions from the request the
	// Read call answered. Except basis_none, they are decided on the
	// recognized basis whatever this build's production basis is: an exact
	// release keeps the containment-only rule, so only the recognized checks
	// can refuse an extra property, an undeclared argument or an
	// execution-bearing path. Each first requires its unchanged twin to be
	// approved, so the refusal shows the change alone made the gate ask.
	script.mu.Lock()
	read := script.first("Read")
	script.mu.Unlock()
	var released []protocolcore.ToolDefinition
	var namespaces []protocolcore.ToolNamespace
	if read != nil {
		released, namespaces = requests[read.request].decoded.Tools, requests[read.request].decoded.ToolNamespaces
	}
	notes := script.path("notes.txt")
	outside := filepath.Join(home, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readNotes := claudeToolArguments(t, map[string]any{"file_path": notes})
	for _, refusal := range []struct {
		name            string
		basis           clientadapter.ProtectionBasis
		tool            string
		twin, arguments string
		definitions     func(*testing.T) []protocolcore.ToolDefinition
	}{
		{name: "refuses/basis_none", basis: clientadapter.ProtectionNone, tool: "Read", twin: readNotes, arguments: readNotes},
		{name: "refuses/extra_property", basis: clientadapter.ProtectionRecognizedClient, tool: "Read", twin: readNotes, arguments: readNotes,
			definitions: func(t *testing.T) []protocolcore.ToolDefinition {
				return claudeDefinitionsWithProperty(t, released, "Read", "extra", map[string]any{"type": "boolean"})
			}},
		{name: "refuses/undeclared_argument", basis: clientadapter.ProtectionRecognizedClient, tool: "Read", twin: readNotes,
			arguments: claudeToolArguments(t, map[string]any{"file_path": notes, "url": "x"})},
		{name: "refuses/outside_workspace", basis: clientadapter.ProtectionRecognizedClient, tool: "Read", twin: readNotes,
			arguments: claudeToolArguments(t, map[string]any{"file_path": outside})},
		{name: "refuses/execution_bearing", basis: clientadapter.ProtectionRecognizedClient, tool: "Write",
			twin:      claudeToolArguments(t, map[string]any{"file_path": script.path("synthetic.txt"), "content": "synthetic"}),
			arguments: claudeToolArguments(t, map[string]any{"file_path": script.path(".git", "hooks", "pre-commit"), "content": "synthetic"})},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			if read == nil {
				t.Fatal("no Read call was issued, so the release's definitions are unknown")
			}
			twinBasis := refusal.basis
			if twinBasis == clientadapter.ProtectionNone {
				twinBasis = basis
			}
			twin := decideClaudeToolCall(workspace, twinBasis, released, namespaces, refusal.tool, refusal.twin)
			if !twin.approved {
				t.Fatalf("the unchanged %s %s was not approved on the %s basis (review calls %d, err %v), so this refusal proves nothing: %s",
					refusal.tool, refusal.twin, twinBasis, twin.reviews, twin.err, explainClaudeToolCall(shapes, released, refusal.tool, refusal.twin))
			}
			definitions := released
			if refusal.definitions != nil {
				definitions = refusal.definitions(t)
			}
			decision := decideClaudeToolCall(workspace, refusal.basis, definitions, namespaces, refusal.tool, refusal.arguments)
			if decision.err != nil || decision.approved || decision.reviews != 1 {
				t.Fatalf("%s %s on the %s basis: approved without a question=%v, review calls %d, err %v; want exactly one review",
					refusal.tool, refusal.arguments, refusal.basis, decision.approved, decision.reviews, decision.err)
			}
		})
	}
}

// claudeWorkspaceToolsBasis is the structured-tools basis this binary's
// connections receive in production. A catalogued release has the exact
// basis on every platform. Anything else is decided on the recognized basis:
// a build signed by its publisher receives it on Apple silicon, and C5 proves
// the newest release is still recognized there.
func claudeWorkspaceToolsBasis(t *testing.T, binary string) clientadapter.ProtectionBasis {
	t.Helper()
	verifier, err := clientadapter.NewReleaseVerifier(clientadapter.BuiltInCatalog())
	if err != nil {
		t.Fatal(err)
	}
	detection, err := verifier.Verify(context.Background(), clientadapter.Request{Command: []string{"claude"}, CWD: "/", ExecutablePath: binary})
	if err != nil {
		t.Fatalf("verify the installed claude: %v", err)
	}
	if detection.Recognition == clientadapter.RecognitionVerified {
		if detection.Evidence == nil || !detection.Evidence.Supports(clientadapter.FeatureStructuredWorkspaceTools) {
			t.Fatalf("the installed claude is a catalogued release without the workspace tools: %+v", detection.Evidence)
		}
		t.Logf("the installed claude is catalogued release %s %s; its calls are decided on the %s basis",
			detection.Evidence.ID, detection.Evidence.Version, clientadapter.ProtectionExactRelease)
		return clientadapter.ProtectionExactRelease
	}
	signer := "no signer"
	if detection.Signer != nil {
		signer = fmt.Sprintf("signer %s revision %d", detection.Signer.ID, detection.Signer.Revision)
	}
	t.Logf("the installed claude is %s (%s); its calls are decided on the %s basis",
		detection.Recognition, signer, clientadapter.ProtectionRecognizedClient)
	return clientadapter.ProtectionRecognizedClient
}

// claudeToolNotebook is a minimal nbformat 4.5 notebook with one code cell.
const claudeToolNotebook = `{"cells":[{"cell_type":"code","execution_count":null,"id":"c1","metadata":{},"outputs":[],"source":"marker-N1"}],` +
	`"metadata":{"language_info":{"name":"python"}},"nbformat":4,"nbformat_minor":5}` + "\n"

// claudeToolScript scripts the model's side of C2. It advances one step per
// streaming request that defines tools, once that request carries the
// previous step's tool_result, and checks each step's effect before the next
// call:
//
//  0. require Read, Edit and Write; if ToolSearch is defined, load
//     NotebookEdit, Glob and Grep with it;
//  1. Read notes.txt;
//  2. require marker A in the result, then Write created.txt;
//  3. require marker-B, then Edit it to marker-C;
//  4. require marker-C, then, when NotebookEdit is defined, Read book.ipynb,
//     which NotebookEdit requires first;
//  5. require marker-N1 in that result, then NotebookEdit cell c1;
//  6. require cell c1 to be marker-N2;
//  7. call Glob and Grep when defined (logged, not asserted), then end the
//     turn with text.
//
// Each call is decided on the gate when it is issued, over the definitions
// of the request it answers, as the Exchange decides before it releases a
// call to the client.
type claudeToolScript struct {
	workspace, marker string
	basis             clientadapter.ProtectionBasis

	mu           sync.Mutex
	step         int
	pending      *claudeScriptedCall
	calls        []*claudeScriptedCall
	failures     []string
	lines        []string
	definitions  []claudeLoggedDefinition
	seen         map[string]bool
	notebookSeen bool
	finished     bool
	completed    bool
}

type claudeScriptedCall struct {
	id, name, arguments string
	request             int // the recorded request this call answered
	decision            claudeGateDecision
	result              string
	isError, answered   bool
}

// claudeLoggedDefinition is one distinct definition of a workspace tool and
// the first request that carried it.
type claudeLoggedDefinition struct {
	name    string
	request int
	summary string
	schema  string
}

func (s *claudeToolScript) path(elements ...string) string {
	return filepath.Join(append([]string{s.workspace}, elements...)...)
}

func (s *claudeToolScript) first(name string) *claudeScriptedCall {
	for _, call := range s.calls {
		if call.name == name {
			return call
		}
	}
	return nil
}

func (s *claudeToolScript) last(name string) *claudeScriptedCall {
	for index := len(s.calls) - 1; index >= 0; index-- {
		if s.calls[index].name == name {
			return s.calls[index]
		}
	}
	return nil
}

func (s *claudeToolScript) fail(format string, arguments ...any) {
	s.failures = append(s.failures, fmt.Sprintf(format, arguments...))
	s.finished = true
}

func (s *claudeToolScript) respond(index int, request claudeNativeRequest, model string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := s.observe(index, request)
	if s.finished {
		return "", false
	}
	if s.pending != nil {
		result, isError, found := claudeToolResult(request.body, s.pending.id)
		if !found {
			s.lines = append(s.lines, fmt.Sprintf("request %d carries no result for %s %s; answered with text", index, s.pending.name, s.pending.id))
			return "", false
		}
		s.pending.result, s.pending.isError, s.pending.answered = result, isError, true
		s.pending = nil
	}
	for {
		switch s.step {
		case 0:
			for _, required := range []string{"Read", "Edit", "Write"} {
				if !names[required] {
					s.fail("the release no longer defines %s; recognized runs would ask for it", required)
				}
			}
			if s.finished {
				return "", false
			}
			s.step = 1
			if names["ToolSearch"] {
				return s.issue(index, request, model, "ToolSearch", map[string]any{"query": "select:NotebookEdit,Glob,Grep", "max_results": 5}), true
			}
		case 1:
			s.step = 2
			return s.issue(index, request, model, "Read", map[string]any{"file_path": s.path("notes.txt")}), true
		case 2:
			if read := s.last("Read"); read.isError || !strings.Contains(read.result, s.marker) {
				s.fail("the Read result does not hold %s: is_error=%v result=%q", s.marker, read.isError, read.result)
				return "", false
			}
			s.step = 3
			return s.issue(index, request, model, "Write", map[string]any{"file_path": s.path("created.txt"), "content": "marker-B"}), true
		case 3:
			if !s.holds("Write", "marker-B") {
				return "", false
			}
			s.step = 4
			return s.issue(index, request, model, "Edit", map[string]any{
				"file_path": s.path("created.txt"), "old_string": "marker-B", "new_string": "marker-C",
			}), true
		case 4:
			if !s.holds("Edit", "marker-C") {
				return "", false
			}
			s.step = 7
			if names["NotebookEdit"] {
				// NotebookEdit refuses a notebook the session has not read.
				s.step = 5
				return s.issue(index, request, model, "Read", map[string]any{"file_path": s.path("book.ipynb")}), true
			}
		case 5:
			if read := s.last("Read"); read.isError || !strings.Contains(read.result, "marker-N1") {
				s.fail("the Read of book.ipynb does not hold marker-N1: is_error=%v result=%q", read.isError, read.result)
				return "", false
			}
			s.step = 6
			return s.issue(index, request, model, "NotebookEdit", map[string]any{
				"notebook_path": s.path("book.ipynb"), "cell_id": "c1", "new_source": "marker-N2",
			}), true
		case 6:
			if source, err := claudeNotebookCellSource(s.path("book.ipynb"), "c1"); err != nil || source != "marker-N2" {
				edit := s.last("NotebookEdit")
				s.fail("book.ipynb cell c1 = %q (%v) after the NotebookEdit, want %q: is_error=%v result=%q", source, err, "marker-N2", edit.isError, edit.result)
				return "", false
			}
			s.step = 7
		case 7:
			if names["Glob"] && s.last("Glob") == nil {
				return s.issue(index, request, model, "Glob", map[string]any{"pattern": "*.txt", "path": s.workspace}), true
			}
			if names["Grep"] && s.last("Grep") == nil {
				return s.issue(index, request, model, "Grep", map[string]any{"pattern": "marker-C", "path": s.workspace}), true
			}
			s.finished, s.completed = true, true
			return "", false
		default:
			return "", false
		}
	}
}

// holds requires created.txt to hold want after the named call.
func (s *claudeToolScript) holds(name, want string) bool {
	content, err := os.ReadFile(s.path("created.txt"))
	if err == nil && string(content) == want {
		return true
	}
	call := s.last(name)
	s.fail("created.txt = %q (%v) after the %s, want %q: is_error=%v result=%q", content, err, name, want, call.isError, call.result)
	return false
}

// issue answers the request with one tool_use and decides that call on the
// gate first, over the request's own definitions.
func (s *claudeToolScript) issue(index int, request claudeNativeRequest, model, name string, input map[string]any) string {
	arguments, err := json.Marshal(input)
	if err != nil {
		s.fail("encode the %s input: %v", name, err)
		return ""
	}
	call := &claudeScriptedCall{id: fmt.Sprintf("toolu_c2_%02d_%s", len(s.calls), strings.ToLower(name)), name: name, arguments: string(arguments), request: index}
	call.decision = decideClaudeToolCall(s.workspace, s.basis, request.decoded.Tools, request.decoded.ToolNamespaces, name, call.arguments)
	s.calls = append(s.calls, call)
	s.pending = call
	return claudeToolUseEvents(model, call.id, name, call.arguments)
}

// observe records the request's tool names and every new definition of a
// workspace tool, and returns the names it defines.
func (s *claudeToolScript) observe(index int, request claudeNativeRequest) map[string]bool {
	var wire struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(request.body, &wire)
	names := make(map[string]bool, len(wire.Tools))
	listed := make([]string, 0, len(wire.Tools))
	for _, tool := range wire.Tools {
		var name string
		_ = json.Unmarshal(tool["name"], &name)
		names[name] = true
		listed = append(listed, name)
		if !claudeWorkspaceToolListed(name) {
			continue
		}
		if name == "NotebookEdit" {
			s.notebookSeen = true
		}
		digest := sha256.Sum256(append([]byte(name+"\x00"), tool["input_schema"]...))
		key := hex.EncodeToString(digest[:])
		if s.seen[key] {
			continue
		}
		s.seen[key] = true
		s.definitions = append(s.definitions, claudeLoggedDefinition{
			name: name, request: index,
			summary: claudeDefinitionSummary(tool), schema: claudeSchemaWithoutDescriptions(tool["input_schema"]),
		})
	}
	sort.Strings(listed)
	s.lines = append(s.lines, fmt.Sprintf("request %d (step %d): %d tools %v", index, s.step, len(listed), listed))
	return names
}

func claudeWorkspaceToolListed(name string) bool {
	for _, candidate := range claudeWorkspaceTools {
		if candidate == name {
			return true
		}
	}
	return false
}

// logRun logs the run and returns a copy of the recorded requests.
func (s *claudeToolScript) logRun(t *testing.T, f *claudeNativeFixture) []claudeNativeRequest {
	t.Helper()
	f.mu.Lock()
	requests := append([]claudeNativeRequest(nil), f.messages...)
	f.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, request := range requests {
		t.Logf("request %d: stream=%v tools=%d codec=%q", index, request.stream, request.tools, request.codecErr)
	}
	for _, line := range s.lines {
		t.Log(line)
	}
	for _, definition := range s.definitions {
		t.Logf("definition %s (request %d): %s", definition.name, definition.request, definition.summary)
		t.Logf("definition %s (request %d) schema: %s", definition.name, definition.request, definition.schema)
	}
	for _, call := range s.calls {
		result := call.result
		if len(result) > 400 {
			result = result[:400] + "..."
		}
		t.Logf("call %s %s from request %d: answered=%v is_error=%v approved without a question=%v (review calls %d, err %v) result=%q",
			call.name, call.arguments, call.request, call.answered, call.isError, call.decision.approved, call.decision.reviews, call.decision.err, result)
	}
	return requests
}

// syntheticCall is an in-workspace call of the named tool with its required
// arguments only.
func (s *claudeToolScript) syntheticCall(t *testing.T, name string) string {
	t.Helper()
	switch name {
	case "Read":
		return claudeToolArguments(t, map[string]any{"file_path": s.path("notes.txt")})
	case "Edit":
		return claudeToolArguments(t, map[string]any{"file_path": s.path("notes.txt"), "old_string": s.marker, "new_string": "synthetic"})
	case "Write":
		return claudeToolArguments(t, map[string]any{"file_path": s.path("synthetic.txt"), "content": "synthetic"})
	case "NotebookEdit":
		return claudeToolArguments(t, map[string]any{"notebook_path": s.path("book.ipynb"), "new_source": "synthetic"})
	default:
		t.Fatalf("no synthetic call for %s", name)
		return ""
	}
}

func claudeToolArguments(t *testing.T, input map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// claudeToolUseEvents is a streamed assistant message that calls one tool.
func claudeToolUseEvents(model, id, name, arguments string) string {
	partial, _ := json.Marshal(arguments)
	var events strings.Builder
	for _, event := range [][2]string{
		{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":1}}}`, model)},
		{"content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, id, name)},
		{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, partial)},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":20}}`},
		{"message_stop", `{"type":"message_stop"}`},
	} {
		fmt.Fprintf(&events, "event: %s\ndata: %s\n\n", event[0], event[1])
	}
	return events.String()
}

// claudeToolResult finds the tool_result for id among the request's messages
// and flattens its content to text.
func claudeToolResult(body []byte, id string) (string, bool, bool) {
	var wire struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &wire) != nil {
		return "", false, false
	}
	for index := len(wire.Messages) - 1; index >= 0; index-- {
		var blocks []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(wire.Messages[index].Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type == "tool_result" && block.ToolUseID == id {
				return claudeFlattenContent(block.Content), block.IsError, true
			}
		}
	}
	return "", false, false
}

func claudeFlattenContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return string(raw)
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		var typed struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(block, &typed) == nil && typed.Type == "text" {
			parts = append(parts, typed.Text)
			continue
		}
		parts = append(parts, string(block))
	}
	return strings.Join(parts, "\n")
}

func claudeNotebookCellSource(path, id string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var notebook struct {
		Cells []struct {
			ID     string          `json:"id"`
			Source json.RawMessage `json:"source"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(raw, &notebook); err != nil {
		return "", err
	}
	for _, cell := range notebook.Cells {
		if cell.ID != id {
			continue
		}
		var source string
		if json.Unmarshal(cell.Source, &source) == nil {
			return source, nil
		}
		var lines []string
		if err := json.Unmarshal(cell.Source, &lines); err != nil {
			return "", err
		}
		return strings.Join(lines, ""), nil
	}
	return "", fmt.Errorf("no cell %q", id)
}

// claudeDefinitionSummary is what review reads about one definition: its
// tool-level fields, the schema's top-level keys, each property's name and
// type, the required set and additionalProperties.
func claudeDefinitionSummary(tool map[string]json.RawMessage) string {
	var extra []string
	for key, value := range tool {
		if key != "name" && key != "description" && key != "input_schema" {
			extra = append(extra, key+"="+string(value))
		}
	}
	sort.Strings(extra)
	var schema map[string]json.RawMessage
	_ = json.Unmarshal(tool["input_schema"], &schema)
	keys := make([]string, 0, len(schema))
	for key := range schema {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var properties map[string]map[string]json.RawMessage
	_ = json.Unmarshal(schema["properties"], &properties)
	typed := make([]string, 0, len(properties))
	for name, property := range properties {
		propertyType := string(property["type"])
		if propertyType == "" {
			propertyType = "<no type>"
		}
		typed = append(typed, name+":"+propertyType)
	}
	sort.Strings(typed)
	additional := string(schema["additionalProperties"])
	if additional == "" {
		additional = "<absent>"
	}
	return fmt.Sprintf("tool fields %v; keys %v; properties %v; required %s; additionalProperties %s",
		extra, keys, typed, schema["required"], additional)
}

// claudeSchemaWithoutDescriptions is the schema with every description
// keyword removed, in the form of internal/toolpolicy/testdata, so that a
// testdata file for a new release can be built from the log.
func claudeSchemaWithoutDescriptions(raw json.RawMessage) string {
	var schema any
	if json.Unmarshal(raw, &schema) != nil {
		return string(raw)
	}
	var strip func(node any, properties bool)
	strip = func(node any, properties bool) {
		switch value := node.(type) {
		case map[string]any:
			if !properties {
				delete(value, "description")
			}
			for key, child := range value {
				strip(child, !properties && key == "properties")
			}
		case []any:
			for _, child := range value {
				strip(child, false)
			}
		}
	}
	strip(schema, false)
	encoded, err := json.Marshal(schema)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

type claudeGateDecision struct {
	approved bool // approved with no review call
	reviews  int
	err      error
}

type countingToolReview struct{ calls int }

func (review *countingToolReview) Decide(context.Context, exchange.ToolDecisionRequest) (exchange.ToolDecision, error) {
	review.calls++
	return exchange.ToolDecision{Outcome: exchange.ToolDecisionRejected, ReasonCode: "asked_by_review"}, nil
}

// decideClaudeToolCall decides one call in Review, as the Exchange asks the
// gate, over the given definitions and the local workspace.
func decideClaudeToolCall(
	workspace string,
	basis clientadapter.ProtectionBasis,
	tools []protocolcore.ToolDefinition,
	namespaces []protocolcore.ToolNamespace,
	name, arguments string,
) claudeGateDecision {
	decisionContext, err := exchange.NewToolDecisionContextForBasis(
		environment.PolicySet{ToolMode: environment.ToolPolicyReview}, workspace, basis, tools, namespaces)
	if err != nil {
		return claudeGateDecision{err: err}
	}
	input, err := protocolcore.NewJSONObject([]byte(arguments), protocolcore.MaxToolJSONBytes)
	if err != nil {
		return claudeGateDecision{err: err}
	}
	key, err := protocolcore.NewCallKey("anthropic-messages", "toolu_c2_decision")
	if err != nil {
		return claudeGateDecision{err: err}
	}
	environmentID, err := environment.NewEnvironmentID("environment-c2")
	if err != nil {
		return claudeGateDecision{err: err}
	}
	routeID, err := environment.NewUpstreamRouteID("route-c2")
	if err != nil {
		return claudeGateDecision{err: err}
	}
	request, err := exchange.NewToolDecisionRequest("exchange-c2", environmentID, 1, environment.CandidateDigest{1}, routeID, 1, decisionContext,
		[]protocolcore.ToolIntent{{ResponseID: "msg_fixture", Call: protocolcore.ToolCall{Key: key, Name: name, Arguments: input}}})
	if err != nil {
		return claudeGateDecision{err: err}
	}
	review := &countingToolReview{}
	gate, err := toolpolicy.New(review)
	if err != nil {
		return claudeGateDecision{err: err}
	}
	decision, err := gate.Decide(context.Background(), request)
	return claudeGateDecision{
		approved: err == nil && decision.Outcome == exchange.ToolDecisionApproved && review.calls == 0,
		reviews:  review.calls, err: err,
	}
}

// claudeDefinitionsWithProperty copies the definitions and adds one property
// to the named tool's schema.
func claudeDefinitionsWithProperty(t *testing.T, definitions []protocolcore.ToolDefinition, tool, property string, value any) []protocolcore.ToolDefinition {
	t.Helper()
	changed := make([]protocolcore.ToolDefinition, len(definitions))
	found := false
	for index, definition := range definitions {
		changed[index] = definition.Clone()
		if definition.Name != tool {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.InputSchema.Bytes(), &schema); err != nil {
			t.Fatal(err)
		}
		properties, _ := schema["properties"].(map[string]any)
		if properties == nil {
			t.Fatalf("%s declares no properties", tool)
		}
		properties[property] = value
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		changed[index].InputSchema, err = protocolcore.NewJSONObject(encoded, protocolcore.MaxToolJSONBytes)
		if err != nil {
			t.Fatal(err)
		}
		found = true
	}
	if !found {
		t.Fatalf("the release does not define %s", tool)
	}
	return changed
}

// claudeCapturedShape is one tool as the committed wire captures declare it.
// TestRecognizedToolShapesMatchTheCapturedDefinitions holds the shape table
// to those captures, so their union names what the table accepts.
type claudeCapturedShape struct {
	properties map[string]string
	required   []string
}

func claudeCapturedToolShapes(t *testing.T) map[string]claudeCapturedShape {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "toolpolicy", "testdata", "claude-code-*-file-tools.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no captured file tools under internal/toolpolicy/testdata: %v", err)
	}
	shapes := make(map[string]claudeCapturedShape)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var entries []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"input_schema"`
		}
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, entry := range entries {
			shape, known := shapes[entry.Name]
			if !known {
				shape = claudeCapturedShape{properties: make(map[string]string), required: entry.InputSchema.Required}
			}
			for name, property := range entry.InputSchema.Properties {
				shape.properties[name] = property.Type
			}
			shapes[entry.Name] = shape
		}
	}
	return shapes
}

// explainClaudeToolCall names what the shape table lacks for one call: the
// definition's keys, properties and required set, and the call's arguments,
// compared with the committed captures. It explains a failure; the gate alone
// decides.
func explainClaudeToolCall(shapes map[string]claudeCapturedShape, tools []protocolcore.ToolDefinition, name, arguments string) string {
	var problems []string
	shape, known := shapes[name]
	if !known {
		problems = append(problems, "the shape table has no "+name)
	}
	var definition *protocolcore.ToolDefinition
	for index := range tools {
		if tools[index].Name == name {
			definition = &tools[index]
		}
	}
	declared := make(map[string]bool)
	if definition == nil {
		problems = append(problems, "the request does not define "+name)
	} else {
		var schema map[string]json.RawMessage
		_ = json.Unmarshal(definition.InputSchema.Bytes(), &schema)
		for key := range schema {
			switch key {
			case "$schema", "type", "properties", "required", "additionalProperties", "description", "title":
			default:
				problems = append(problems, "top-level key "+key)
			}
		}
		if !bytes.Equal(bytes.TrimSpace(schema["additionalProperties"]), []byte("false")) {
			problems = append(problems, fmt.Sprintf("additionalProperties is %q, not false", schema["additionalProperties"]))
		}
		var properties map[string]map[string]json.RawMessage
		_ = json.Unmarshal(schema["properties"], &properties)
		for property, body := range properties {
			declared[property] = true
			var propertyType string
			if json.Unmarshal(body["type"], &propertyType) != nil {
				problems = append(problems, fmt.Sprintf("property %s has no single type (%s)", property, body["type"]))
				continue
			}
			if captured, listed := shape.properties[property]; known && !listed {
				problems = append(problems, fmt.Sprintf("property %s (%s) is not in the shape table", property, propertyType))
			} else if known && captured != propertyType {
				problems = append(problems, fmt.Sprintf("property %s is %s, captured as %s", property, propertyType, captured))
			}
		}
		var required []string
		_ = json.Unmarshal(schema["required"], &required)
		requiredSet := make(map[string]bool, len(required))
		for _, property := range required {
			requiredSet[property] = true
			if !declared[property] {
				problems = append(problems, "required names the undeclared "+property)
			}
		}
		for _, property := range shape.required {
			if !requiredSet[property] {
				problems = append(problems, "required lacks "+property)
			}
		}
	}
	var input map[string]json.RawMessage
	_ = json.Unmarshal([]byte(arguments), &input)
	for argument := range input {
		if _, listed := shape.properties[argument]; known && !listed {
			problems = append(problems, "argument "+argument+" is not in the shape table")
		}
		if definition != nil && !declared[argument] {
			problems = append(problems, "argument "+argument+" is not declared by this definition")
		}
	}
	sort.Strings(problems)
	if len(problems) == 0 {
		return "the definition and arguments match the captured shape, so a path or workspace check asked"
	}
	return strings.Join(problems, "; ")
}
