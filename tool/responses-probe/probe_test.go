package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var fixedNow = func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) }

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := findRepository(".")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

type invocation struct {
	code           int
	stdout, stderr string
	out            string
}

func invoke(t *testing.T, env map[string]string, args ...string) invocation {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	args = append(args, "--repo", repositoryRoot(t), "--out", out)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, func(name string) string { return env[name] }, &stdout, &stderr, fixedNow)
	return invocation{code: code, stdout: stdout.String(), stderr: stderr.String(), out: out}
}

func writeKeyFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openai.key")
	if err := os.WriteFile(path, []byte(canaryKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertNoKey scans every byte the tool printed or wrote for the key and
// its tail.
func assertNoKey(t *testing.T, result invocation) {
	t.Helper()
	needles := []string{canaryKey, canaryKey[len(canaryKey)-8:], strings.TrimPrefix(canaryKey, "sk-")}
	check := func(where, content string) {
		for _, needle := range needles {
			if strings.Contains(content, needle) {
				t.Errorf("%s carries the key (%q)", where, needle)
			}
		}
	}
	check("stdout", result.stdout)
	check("stderr", result.stderr)
	// The digits of the key are not read as numbers of a message either.
	for _, name := range []string{"report.json", "report.md"} {
		if content, err := os.ReadFile(filepath.Join(result.out, name)); err == nil && strings.Contains(string(content), "123456789") {
			t.Errorf("%s carries the digits of the key", name)
		}
	}
	files := 0
	_ = filepath.WalkDir(result.out, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		check(path, string(content))
		files++
		return nil
	})
	if files == 0 {
		t.Fatal("no output file was written")
	}
}

func readReport(t *testing.T, out string) runReport {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report runReport
	if err := json.Unmarshal(content, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func verdictOf(t *testing.T, report runReport, id string) verdict {
	t.Helper()
	for _, row := range report.Questions {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no verdict for %s", id)
	return verdict{}
}

func probeOf(t *testing.T, report runReport, id string) *probeResult {
	t.Helper()
	for _, result := range report.Probes {
		if result.ID == id {
			return result
		}
	}
	t.Fatalf("no probe %s", id)
	return nil
}

// The tool encodes exactly what the registered path sends: with the golden
// tests' mapping, session and binding, every corpus body is byte for byte
// the committed Responses golden.
func TestBodiesAreTheRegisteredPathsGoldens(t *testing.T) {
	loaded, err := loadCorpus(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	golden := map[string]string{"claude-opus-5-5": "gpt-6.2-astra", "claude-sonnet-5-5": "gpt-6.2-sol", "claude-haiku-5-5": "gpt-6-luna"}
	b := newBuilder("golden-session", "target.openai.official", "golden-account")
	for _, fixture := range corpusFixtures {
		raw := loaded.body(fixture)
		result, err := b.build(raw, golden[requestedModel(raw)], profileState{noCyber: true})
		if err != nil {
			t.Fatalf("%s: %v", fixture, err)
		}
		want, err := os.ReadFile(filepath.Join(repositoryRoot(t), "internal/anthropicresponses/testdata/claude-2.1.329", fixture+".responses.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(result.body, want) {
			t.Fatalf("%s differs from the registered path's golden", fixture)
		}
		// The preset mapping of the tool agrees with the goldens' mapping.
		r := &runner{cfg: config{models: seedModels()}}
		if got := r.mapModel(requestedModel(raw)); got != golden[requestedModel(raw)] {
			t.Fatalf("%s maps to %s, the goldens to %s", fixture, got, golden[requestedModel(raw)])
		}
	}
}

func TestPlanPrintsAndWritesEveryProbeWithoutNetwork(t *testing.T) {
	fake := newFakeUpstream(t, fakeScenario{listed: seedModels()})
	result := invoke(t, map[string]string{"OPENAI_API_KEY": canaryKey}, "--base-url", fake.server.URL)
	if result.code != 0 {
		t.Fatalf("plan exited %d: %s", result.code, result.stderr)
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Fatalf("plan sent %d requests", len(seen))
	}
	for _, want := range []string{
		"no network",
		"models: gpt-6.2-astra, gpt-6-astra, gpt-6.2-sol, gpt-6.1-sol, gpt-6-sol, gpt-6-luna, gpt-5.6-luna",
		"preset mapping: opus -> gpt-6.2-astra, sonnet -> gpt-6.2-sol, haiku -> gpt-6-luna",
		"Q11.models", "Q1.gpt-5.6-luna", "Q4.turn-2", "Q10.tokens.claude-models.007", "Q10.tampered-reasoning",
		"requests: 29 always, 4 conditional (only when a default is refused), 33 at most; budget --max-requests 40",
		"Q8 and Q9 are not planned: add --include-slow",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("plan output lacks %q:\n%s", want, result.stdout)
		}
	}
	if !strings.Contains(result.stdout, "Q6.full") || !strings.Contains(result.stdout, "--non-reasoning-model") {
		t.Errorf("plan does not explain the skipped Q6:\n%s", result.stdout)
	}
	entries, err := os.ReadDir(filepath.Join(result.out, "bodies"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2*32 {
		t.Fatalf("wrote %d body files, want a body and notes for the 32 POST probes", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(result.out, "bodies", "02-Q7.cyber.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"access_programs":{"cyber":"standard"}`)) || !bytes.Contains(body, []byte(`"max_output_tokens":64`)) {
		t.Fatalf("Q7.cyber body lacks the finalizer or the cap: %.200s", body)
	}
	if _, err := os.Stat(filepath.Join(result.out, "plan.json")); err != nil {
		t.Fatal(err)
	}
	slow := invoke(t, nil, "plan", "--include-slow", "--non-reasoning-model", "gpt-4.1")
	if slow.code != 0 || !strings.Contains(slow.stdout, "requests: 45 always, 6 conditional (only when a default is refused), 51 at most") ||
		!strings.Contains(slow.stdout, "live mode will refuse to start until --max-requests is at least 51") {
		t.Fatalf("slow plan:\n%s%s", slow.stdout, slow.stderr)
	}
}

func TestLiveNeedsTheFlagAndAKey(t *testing.T) {
	fake := newFakeUpstream(t, fakeScenario{listed: seedModels()})
	keyFile := writeKeyFile(t, 0o600)
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"live mode without --live", map[string]string{"OPENAI_API_KEY": canaryKey}, []string{"live", "--key-file", keyFile}, "add --live to confirm"},
		{"--live without a key", nil, []string{"--live"}, "live mode needs an API key"},
		{"plan with --live", nil, []string{"plan", "--live"}, "plan mode never sends"},
		{"group-readable key file", nil, []string{"--live", "--key-file", writeKeyFile(t, 0o640)}, "has mode 0640; it must be 0600 or stricter"},
		{"world-readable key file", nil, []string{"--live", "--key-file", writeKeyFile(t, 0o604)}, "has mode 0604"},
		{"missing key file", nil, []string{"--live", "--key-file", filepath.Join(t.TempDir(), "absent")}, "cannot be read"},
		{"plain http to a remote host", nil, []string{"--live", "--key-file", keyFile, "--base-url", "http://api.openai.com"}, "plain http only for a loopback"},
		{"budget below the plan", nil, []string{"--live", "--key-file", keyFile, "--max-requests", "5", "--overflow-tokens", "300000"}, "the plan needs up to 33 requests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{}, tc.args...)
			if !slices.Contains(args, "--base-url") {
				args = append(args, "--base-url", fake.server.URL)
			}
			result := invoke(t, tc.env, args...)
			if result.code == 0 || !strings.Contains(result.stderr, tc.want) {
				t.Fatalf("exit %d, stderr %q; want a refusal naming %q", result.code, result.stderr, tc.want)
			}
			if strings.Contains(result.stderr+result.stdout, canaryKey) {
				t.Fatal("a refusal printed the key")
			}
		})
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Fatalf("a refused run sent %d requests", len(seen))
	}
	// 0600 and 0400 are accepted, and so is the environment variable.
	for _, mode := range []os.FileMode{0o600, 0o400} {
		key, err := loadKey(writeKeyFile(t, mode), func(string) string { return "" })
		if err != nil || key.value != canaryKey {
			t.Fatalf("mode %04o: %v", mode, err)
		}
	}
	if key, err := loadKey("", func(string) string { return " " + canaryKey + "\n" }); err != nil || key.value != canaryKey {
		t.Fatalf("OPENAI_API_KEY: %v", err)
	}
	if _, err := loadKey("", func(string) string { return "sk-with space" }); err == nil || strings.Contains(err.Error(), "sk-with") {
		t.Fatalf("a key with a space: %v", err)
	}
}

// Every probe end to end against an upstream that accepts what the API is
// expected to accept, with the key echoed everywhere it could leak.
func TestLiveRunAgainstAnAcceptingUpstream(t *testing.T) {
	listed := append(seedModels(), "fake-non-reasoning")
	fake := newFakeUpstream(t, fakeScenario{
		listed:  listed,
		windows: map[string]int{"gpt-6.2-astra": 400000},
		gap:     30 * time.Millisecond,
		echo:    true,
	})
	result := invoke(t, nil, "--live", "--key-file", writeKeyFile(t, 0o600), "--base-url", fake.server.URL,
		"--include-slow", "--max-requests", "60", "--non-reasoning-model", "fake-non-reasoning", "--overflow-tokens", "300000", "--verbose")
	if result.code != 0 {
		t.Fatalf("live run exited %d:\n%s\n%s", result.code, result.stdout, result.stderr)
	}
	assertNoKey(t, result)
	seen := fake.seen()
	for _, request := range seen {
		if request.authorization != "Bearer "+canaryKey || !strings.HasPrefix(request.userAgent, "ViberMate/") {
			t.Fatalf("request %s %s has authorization/user agent %q", request.method, request.path, request.userAgent)
		}
		if request.method == "POST" && request.accept != "text/event-stream" {
			t.Fatalf("POST Accept = %q", request.accept)
		}
	}
	if !strings.Contains(result.stderr, "Authorization: [redacted]") {
		t.Fatalf("the verbose log does not show the redaction:\n%s", result.stderr)
	}
	report := readReport(t, result.out)
	// Every default holds, so no conditional probe ran.
	if report.Budget.Sent != len(seen) || len(seen) != 45 {
		t.Fatalf("sent %d (report %d), want the 45 unconditional requests", len(seen), report.Budget.Sent)
	}
	for _, row := range report.Questions {
		if row.Verdict != verdictPass {
			t.Errorf("%s = %s: %s", row.ID, row.Verdict, row.Summary)
		}
	}
	if report.FinalProfile != (profileState{}).String() {
		t.Fatalf("a switch flipped: %s", report.FinalProfile)
	}

	// Q3 timing and Q8 gaps are measured from the wire.
	q8 := probeOf(t, report, "Q8.max")
	if q8.Timing == nil || q8.Timing.LongestGapMS < 30 || q8.Timing.FirstEventMS == nil || q8.Timing.Events < 15 {
		t.Fatalf("Q8.max timing = %+v", q8.Timing)
	}
	q3 := probeOf(t, report, "Q3.max")
	if q3.Timing.FirstEvent != "response.created" || q3.Translation == nil || !q3.Translation.OK {
		t.Fatalf("Q3.max = %+v %+v", q3.Timing, q3.Translation)
	}
	// Q4 replayed the upstream's own reasoning item.
	turnTwo, err := os.ReadFile(filepath.Join(result.out, probeOf(t, report, "Q4.turn-2").BodyFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(turnTwo, []byte(`"type":"reasoning","id":"rs_resp_fake_`)) || !bytes.Contains(turnTwo, []byte(`"encrypted_content":"gAAAAABfake-`)) {
		t.Fatalf("turn two does not replay the reasoning item: %.300s", turnTwo)
	}
	// Q10: both error forms classified as design 8 expects, and the
	// calibration fixture for every corpus body.
	if q10 := report.Q10; q10 == nil || len(q10.ContextLengthExceeded) != 2 || q10.InvalidEncryptedContent == nil ||
		q10.ContextLengthExceeded[0].Classification.Class != "context_window" || q10.ContextLengthExceeded[0].Classification.LimitTokens != 400000 ||
		!q10.ContextLengthExceeded[0].MessageHasNumbers || q10.ContextLengthExceeded[0].Status != 400 ||
		q10.InvalidEncryptedContent.Classification.Class != "reasoning_state" || q10.InvalidEncryptedContent.InBand {
		t.Fatalf("Q10 = %+v", report.Q10)
	}
	overflow := probeOf(t, report, "Q10.overflow")
	if overflow.Classification.ClientStatus != 400 || !strings.HasPrefix(overflow.Classification.ClientMessage, "prompt is too long: ") {
		t.Fatalf("Claude Code would see %+v", overflow.Classification)
	}
	if strings.Contains(overflow.Edits[0], "300000") || !strings.Contains(overflow.Edits[0], "440000") {
		t.Fatalf("the filler was not sized from the listed window: %v", overflow.Edits)
	}
	var fixture calibration
	content, err := os.ReadFile(filepath.Join(result.out, "tokenestimate-calibration.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &fixture); err != nil || len(fixture.Bodies) != len(corpusFixtures) {
		t.Fatalf("calibration fixture: %v %+v", err, fixture)
	}
	for index, entry := range fixture.Bodies {
		if entry.Fixture != corpusFixtures[index] || entry.InputTokens <= 0 || entry.Estimate <= 0 || entry.Model == "" || entry.RequestedModel == "" {
			t.Fatalf("calibration entry %+v", entry)
		}
	}
	// Q9 sees the cache warm up on its key.
	if report.Q9 == nil || len(report.Q9.Turns) != 10 || report.Q9.Turns[0].CachedTokens != 0 || report.Q9.Turns[1].Share < 0.85 || len(report.Q9.BreakpointTurns) != 3 {
		t.Fatalf("Q9 = %+v", report.Q9)
	}
	// Q11 records the seeds and the listed window, never a fine-tuned id.
	if report.Q11 == nil || report.Q11.Listed != len(listed)+1 || !report.Q11.ContextWindowsExposed || report.Q11.windowOf("gpt-6.2-astra") != 400000 {
		t.Fatalf("Q11 = %+v", report.Q11)
	}
	markdown, err := os.ReadFile(filepath.Join(result.out, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secret-org", "ft:"} {
		if bytes.Contains(markdown, []byte(leaked)) || strings.Contains(result.stdout, leaked) {
			t.Fatalf("the report names %q", leaked)
		}
	}
	reportJSON, _ := os.ReadFile(filepath.Join(result.out, "report.json"))
	if bytes.Contains(reportJSON, []byte("secret-org")) {
		t.Fatal("report.json names the organization")
	}
}

// Every refusal 18.1 anticipates: each fails its question, names the switch,
// and the switch is in force for the probes after it.
func TestLiveRunAgainstARefusingUpstream(t *testing.T) {
	inputHas := func(body map[string]any, test func(map[string]any) bool) bool {
		input, _ := body["input"].([]any)
		for _, raw := range input {
			if item, ok := raw.(map[string]any); ok && test(item) {
				return true
			}
		}
		return false
	}
	refuse := func(body map[string]any) (int, string) {
		model, _ := body["model"].(string)
		reasoning, _ := body["reasoning"].(map[string]any)
		switch {
		case model == "gpt-6.1-sol":
			return 404, errorBody("The model `gpt-6.1-sol` does not exist or you do not have access to it."+" (request key "+canaryKey+")", "invalid_request_error", "", "model_not_found")
		case body["access_programs"] != nil:
			return 400, errorBody("Unknown parameter: 'access_programs'. key="+canaryKey, "invalid_request_error", "access_programs", "unknown_parameter")
		case inputHas(body, func(item map[string]any) bool { return item["phase"] != nil }):
			return 400, errorBody("Unknown parameter: 'input[3].phase'.", "invalid_request_error", "input[3].phase", "unknown_parameter")
		case inputHas(body, func(item map[string]any) bool {
			output, _ := item["output"].([]any)
			for _, part := range output {
				if part, ok := part.(map[string]any); ok && part["type"] == "input_file" {
					return true
				}
			}
			return false
		}):
			return 400, errorBody("Invalid value: 'input_file'.", "invalid_request_error", "input[9].output[1]", "invalid_value")
		case reasoning != nil && reasoning["effort"] == "max":
			return 400, errorBody("Unsupported value: 'max' is not supported with this model.", "invalid_request_error", "reasoning.effort", "unsupported_value")
		case model == "fake-non-reasoning" && reasoning != nil:
			return 400, errorBody("Unsupported parameter: 'reasoning.effort' is not supported with this model.", "invalid_request_error", "reasoning.effort", "unsupported_parameter")
		case model == "fake-non-reasoning" && body["include"] != nil:
			return 400, errorBody("Encrypted content is not supported with this model.", "invalid_request_error", "include", "unsupported_parameter")
		case inputHas(body, func(item map[string]any) bool {
			summary, _ := item["summary"].([]any)
			return item["type"] == "reasoning" && len(summary) != 0
		}):
			return 400, errorBody("Invalid 'input[3].summary'.", "invalid_request_error", "input[3].summary", "invalid_value")
		}
		return 0, ""
	}
	listed := []string{"gpt-6.2-astra", "gpt-6-astra", "gpt-6.2-sol", "gpt-6.1-sol", "gpt-6-luna", "gpt-5.6-luna", "fake-non-reasoning"}
	fake := newFakeUpstream(t, fakeScenario{listed: listed, refuse: refuse, tamperInBand: true, echo: true})
	result := invoke(t, map[string]string{"OPENAI_API_KEY": canaryKey}, "--live", "--base-url", fake.server.URL,
		"--non-reasoning-model", "fake-non-reasoning", "--overflow-tokens", "300000", "--verbose")
	if result.code != 0 {
		t.Fatalf("live run exited %d:\n%s\n%s", result.code, result.stdout, result.stderr)
	}
	assertNoKey(t, result)
	report := readReport(t, result.out)
	want := map[string]struct{ verdict, flip string }{
		"Q1":  {verdictPass, ""},
		"Q2":  {verdictFail, "AssistantPhase: omit"},
		"Q3":  {verdictPass, ""},
		"Q4":  {verdictFail, "ReplaySummary: empty"},
		"Q5":  {verdictFail, "ToolOutputDocuments: text_note"},
		"Q6":  {verdictFail, "IncludeEncryptedReasoning: reasoning_models"},
		"Q7":  {verdictFail, "gate the cyber finalizer"},
		"Q8":  {verdictSkipped, ""},
		"Q9":  {verdictSkipped, ""},
		"Q10": {verdictPass, ""},
		"Q11": {verdictFail, "claude_model_defaults.dart"},
	}
	for id, expected := range want {
		row := verdictOf(t, report, id)
		if row.Verdict != expected.verdict || !strings.HasPrefix(row.Switch, expected.flip) {
			t.Errorf("%s = %s, switch %q (%s); want %s, %q", id, row.Verdict, row.Switch, row.Summary, expected.verdict, expected.flip)
		}
	}
	if !strings.Contains(verdictOf(t, report, "Q3").Summary, "measured at xhigh") {
		t.Errorf("Q3 = %s", verdictOf(t, report, "Q3").Summary)
	}
	if q1 := verdictOf(t, report, "Q1"); !strings.Contains(q1.Summary, "gpt-6.1-sol (refused") && !strings.Contains(q1.Summary, "not judged: gpt-6.1-sol") {
		t.Errorf("Q1 = %s", q1.Summary)
	}
	if probeOf(t, report, "Q1.gpt-6-sol").Skipped == "" {
		t.Error("Q1 sent a model the list does not hold")
	}
	// The flipped switches are in force for every later probe.
	if report.FinalProfile != "AssistantPhase=omit ReplaySummary=empty ReplayReasoning=sealed ToolOutputDocuments=text_note cyber=omitted" {
		t.Fatalf("final profile %s", report.FinalProfile)
	}
	q1 := probeOf(t, report, "Q1.gpt-6.2-astra")
	body, err := os.ReadFile(filepath.Join(result.out, q1.BodyFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"access_programs"`)) || bytes.Contains(body, []byte(`"phase"`)) {
		t.Fatalf("Q1 after the flips still carries a refused field: %.300s", body)
	}
	// The in-band invalid_encrypted_content is classified for the
	// rejected-seal memory.
	tampered := probeOf(t, report, "Q10.tampered-reasoning")
	if tampered.Error == nil || !tampered.Error.InBand || tampered.Error.Code != "invalid_encrypted_content" ||
		tampered.Classification == nil || tampered.Classification.Class != "reasoning_state" {
		t.Fatalf("tampered = %+v %+v", tampered.Error, tampered.Classification)
	}
	// 404 is not a refusal of the body.
	notFound := probeOf(t, report, "Q1.gpt-6.1-sol")
	if notFound.Status != 404 || notFound.Classification == nil || notFound.Classification.Class != "not_found" || !strings.Contains(notFound.Error.Message, "[redacted]") {
		t.Fatalf("404 = %+v %+v", notFound.Error, notFound.Classification)
	}
	effort := probeOf(t, report, "Q3.max")
	if effort.Classification == nil || effort.Classification.Class != "effort" || effort.Classification.ClientMessage != "capability_rejected: effort_unsupported" {
		t.Fatalf("effort = %+v", effort.Classification)
	}
}

func TestAKeyRefusedByTheModelListStopsTheRun(t *testing.T) {
	fake := newFakeUpstream(t, fakeScenario{listed: seedModels()})
	keyFile := filepath.Join(t.TempDir(), "wrong.key")
	if err := os.WriteFile(keyFile, []byte("sk-wrong-0000000000000000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := invoke(t, nil, "--live", "--key-file", keyFile, "--base-url", fake.server.URL, "--overflow-tokens", "300000")
	if result.code != 1 || !strings.Contains(result.stderr, "the key was refused") {
		t.Fatalf("exit %d: %s", result.code, result.stderr)
	}
	if seen := fake.seen(); len(seen) != 1 || seen[0].path != "/v1/models" {
		t.Fatalf("sent %d requests after the refusal", len(seen))
	}
	report := readReport(t, result.out)
	listing := probeOf(t, report, "Q11.models")
	if listing.Status != 401 || listing.Error == nil || listing.Error.Message != "" || listing.Error.MessageOmitted == "" {
		t.Fatalf("a 401 message was kept: %+v", listing.Error)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(result.out, "report.json"))), "MNOP") {
		t.Fatal("the masked key of the 401 message reached the report")
	}
}

func TestAKeyWithoutASeedModelStopsAfterTheModelList(t *testing.T) {
	fake := newFakeUpstream(t, fakeScenario{listed: []string{"gpt-other-1", "gpt-other-2"}})
	result := invoke(t, map[string]string{"OPENAI_API_KEY": canaryKey}, "--live", "--base-url", fake.server.URL, "--overflow-tokens", "300000")
	if result.code != 1 || !strings.Contains(result.stderr, "lists none of --models") {
		t.Fatalf("exit %d: %s", result.code, result.stderr)
	}
	if seen := fake.seen(); len(seen) != 1 {
		t.Fatalf("sent %d requests to models the key cannot use", len(seen))
	}
	if report := readReport(t, result.out); !slices.Equal(report.Q11.PublicGPT, []string{"gpt-other-1", "gpt-other-2"}) {
		t.Fatalf("Q11 does not offer the listed ids: %+v", report.Q11)
	}
}

// The last fallback of Q4, and Q10's tampered seal when no real reasoning
// item exists (Q4 did not run).
func TestReplayOffAndASyntheticSeal(t *testing.T) {
	refuseReplay := func(body map[string]any) (int, string) {
		input, _ := body["input"].([]any)
		for _, raw := range input {
			if item, ok := raw.(map[string]any); ok && item["type"] == "reasoning" {
				return 400, errorBody("Reasoning items cannot be replayed.", "invalid_request_error", "input[3]", "invalid_value")
			}
		}
		return 0, ""
	}
	fake := newFakeUpstream(t, fakeScenario{listed: seedModels(), refuse: refuseReplay})
	result := invoke(t, map[string]string{"OPENAI_API_KEY": canaryKey}, "--live", "--base-url", fake.server.URL, "--only", "Q4")
	if result.code != 0 {
		t.Fatalf("exit %d: %s", result.code, result.stderr)
	}
	report := readReport(t, result.out)
	if row := verdictOf(t, report, "Q4"); row.Verdict != verdictFail || row.Switch != "ReplayReasoning: off" {
		t.Fatalf("Q4 = %+v", row)
	}
	if !accepted(probeOf(t, report, "Q4.turn-2-no-reasoning")) || !strings.Contains(report.FinalProfile, "ReplayReasoning=off") {
		t.Fatalf("the replay-off fallback was not accepted: %s", report.FinalProfile)
	}

	fake = newFakeUpstream(t, fakeScenario{listed: seedModels()})
	result = invoke(t, map[string]string{"OPENAI_API_KEY": canaryKey}, "--live", "--base-url", fake.server.URL, "--only", "Q10", "--overflow-tokens", "300000")
	if result.code != 0 {
		t.Fatalf("exit %d: %s", result.code, result.stderr)
	}
	report = readReport(t, result.out)
	tampered := probeOf(t, report, "Q10.tampered-reasoning")
	if tampered.Skipped != "" || !strings.Contains(tampered.Title, "synthetic") || tampered.Classification == nil || tampered.Classification.Class != "reasoning_state" {
		t.Fatalf("tampered = %+v", tampered)
	}
	sent := false
	for _, request := range fake.seen() {
		input, _ := request.body["input"].([]any)
		for _, raw := range input {
			if item, ok := raw.(map[string]any); ok && item["type"] == "reasoning" && strings.HasPrefix(item["encrypted_content"].(string), "gAAAAABprobe") {
				sent = true
			}
		}
	}
	if !sent {
		t.Fatal("the synthetic seal was not replayed as a reasoning item")
	}
	if row := verdictOf(t, report, "Q10"); row.Verdict != verdictPass {
		t.Fatalf("Q10 = %+v", row)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestRequestBudgetIsEnforcedWhileSending(t *testing.T) {
	fake := newFakeUpstream(t, fakeScenario{listed: seedModels()})
	loaded, err := loadCorpus(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig([]string{"--live", "--base-url", fake.server.URL, "--max-requests", "3", "--overflow-tokens", "300000"}, &bytes.Buffer{}, fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	key := apiKey{value: canaryKey}
	scrub := newScrubber(key)
	r := newRunner(cfg, loaded, newLiveSender(cfg.baseURL, key, scrub, nil), scrub, false, "budget", nil)
	r.run(context.Background())
	if seen := fake.seen(); len(seen) != 3 || r.sent != 3 {
		t.Fatalf("sent %d (counted %d), want the budget of 3", len(seen), r.sent)
	}
	skipped := 0
	for _, result := range r.results {
		if strings.HasPrefix(result.Skipped, "request budget reached") {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatal("no probe was skipped for the budget")
	}
}

func TestVerdictThresholds(t *testing.T) {
	gapProbe := func(id string, gap time.Duration) *probeResult {
		first := int64(100)
		return &probeResult{ID: id, Question: "Q8", Status: 200, Terminal: "response.completed",
			Timing: &timing{FirstHeaderMS: 50, FirstEventMS: &first, FirstEvent: "response.created", LongestGapMS: gap.Milliseconds(), TotalMS: 600000}}
	}
	ran := func(string) bool { return true }
	for _, tc := range []struct {
		gap  time.Duration
		want string
	}{{3 * time.Minute, verdictPass}, {4 * time.Minute, verdictPass}, {4*time.Minute + time.Second, verdictFail}} {
		rows := judge([]*probeResult{gapProbe("Q8.xhigh", time.Second), gapProbe("Q8.max", tc.gap)}, nil, ran)
		if row := rows[7]; row.ID != "Q8" || row.Verdict != tc.want || (tc.want == verdictFail) != strings.HasPrefix(row.Switch, "ProviderIdleAllowance") {
			t.Fatalf("gap %s: %+v", tc.gap, row)
		}
	}
	slowHead := func(head int64) *probeResult {
		first := head + 10
		return &probeResult{ID: "Q3.max", Question: "Q3", Status: 200, Terminal: "response.incomplete",
			Timing: &timing{FirstHeaderMS: head, FirstEventMS: &first, FirstEvent: "response.created"}}
	}
	if row := judge([]*probeResult{slowHead(1500)}, nil, ran)[2]; row.Verdict != verdictPass {
		t.Fatalf("1.5 s head: %+v", row)
	}
	if row := judge([]*probeResult{slowHead(2500)}, nil, ran)[2]; row.Verdict != verdictFail || !strings.Contains(row.Switch, "T6") {
		t.Fatalf("2.5 s head: %+v", row)
	}
	// A 429 says nothing about the body.
	limited := &probeResult{ID: "Q2.phase", Question: "Q2", Status: 429, Error: &upstreamError{Code: "rate_limit_exceeded"}}
	control := &probeResult{ID: "Q2.no-phase", Question: "Q2", Status: 200, Terminal: "response.completed"}
	if row := judge([]*probeResult{limited, control}, nil, ran)[1]; row.Verdict != verdictInconclusive {
		t.Fatalf("429: %+v", row)
	}
}

func TestHelpers(t *testing.T) {
	const opaque = "gAAAAABabcdefghijklmnopqrstuvwxyz0123456789=="
	if got := tamperOpaque(opaque); len(got) != len(opaque) || got == opaque || got[:15] != opaque[:15] || got[39:] != opaque[39:] {
		t.Fatalf("tamperOpaque = %q", got)
	}
	if filler := overflowText(1000); len(filler) != 4000 || filler != overflowText(1000) {
		t.Fatalf("overflowText is %d bytes or not deterministic", len(filler))
	}
	for model, row := range map[string]string{"claude-opus-5-5-20261101": "opus", "claude-fable-1": "opus", "claude-sonnet-5-5": "sonnet", "claude-haiku-5-5": "haiku", "claude-next": "other claude"} {
		if got := presetRowFor(model); got == nil || got.name != row {
			t.Errorf("%s maps to row %+v, want %s", model, got, row)
		}
	}
	if presetRowFor("gpt-6.1-terra") != nil {
		t.Error("a non-Claude id matched a preset row")
	}
	scrub := newScrubber(apiKey{value: canaryKey})
	if got := scrub.upstream("Incorrect API key provided: "+canaryKey+" and sk-proj-****abcd\x00", 300); strings.Contains(got, "canary") || strings.Contains(got, "abcd") || strings.ContainsRune(got, 0) {
		t.Fatalf("upstream scrub = %q", got)
	}
	if got := scrub.upstream(strings.Repeat("x", 400), 300); len(got) != 303 {
		t.Fatalf("truncation = %d", len(got))
	}
	for _, raw := range []string{"https://api.openai.com", "http://127.0.0.1:8080", "http://localhost:1"} {
		if _, err := validBaseURL(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://example.com", "ftp://api.openai.com", "https://user:pass@api.openai.com", "api.openai.com"} {
		if _, err := validBaseURL(raw); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}
