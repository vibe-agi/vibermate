// Command vibermate-canary-proof judges a client canary run (ADR 0026). Each
// protection the recognized-client policy grants, and each client's identity
// proof on darwin, names the installed-client test that re-proves it against
// the newest published release. This command prints the go test pattern that
// selects exactly those tests, and reads the go test -json stream of a run to
// say whether every one of them ran and passed.
//
// A proof that was skipped or did not run is a failure: a -run pattern that
// matches nothing, or a test that skips because a job named no binary, would
// otherwise pass while proving nothing.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const (
	proofSchema = "vibermate-canary-proof/1"
	// modulePath prefixes a policy canary's repository-relative package to
	// give the package go test -json reports.
	modulePath = "github.com/vibe-agi/vibermate"
	// identityProtection names the identity canary in a proof. It proves no
	// Feature: it proves that the newest release is still the client the
	// policy describes, recognized by its publisher's signature.
	identityProtection = "signer_recognition"
)

// Outcomes of a canary run. Only pass exits 0.
const (
	outcomePass = "pass"
	// drift: only control arms failed, so a release may no longer need a
	// protection, while every protected arm still holds.
	outcomeDrift = "drift"
	// broken: anything else, including a proof that was skipped or missing.
	outcomeBroken = "broken"
)

// Results of one required test.
const (
	resultPass    = "pass"
	resultFail    = "fail"
	resultSkip    = "skip"
	resultMissing = "missing"
)

// host describes where the command runs and the run it judges.
type host struct {
	goos   string
	goarch string
	commit string
	run    string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, host{
		goos:   runtime.GOOS,
		goarch: runtime.GOARCH,
		commit: os.Getenv("GITHUB_SHA"),
		run:    os.Getenv("GITHUB_RUN_ID"),
	}))
}

func run(arguments []string, stdout, stderr io.Writer, machine host) int {
	if len(arguments) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch arguments[0] {
	case "tests":
		return runTests(arguments[1:], stdout, stderr)
	case "verify":
		return runVerify(arguments[1:], stdout, stderr, machine)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s\n", arguments[0], usage)
		return 2
	}
}

const usage = `usage:
  vibermate-canary-proof tests --client <id> --os <linux|darwin>
  vibermate-canary-proof verify --client <id> --os <linux|darwin> --version <v> --input <go-test.json> --proof <out.json> [--github-output <file>]`

// requiredProof is one test a canary run must pass, and what it proves.
type requiredProof struct {
	protection string
	canary     clientadapter.Canary
}

// requiredProofs is ProtectionCanaries(clientID, goos), each named by the
// protection it re-proves.
func requiredProofs(clientID, goos string) ([]requiredProof, error) {
	if goos != "linux" && goos != "darwin" {
		return nil, fmt.Errorf("--os must be linux or darwin, not %q", goos)
	}
	var policy *clientadapter.RecognizedClientPolicy
	policies := clientadapter.RecognizedClientPolicies()
	for index := range policies {
		if policies[index].ClientID == clientID {
			policy = &policies[index]
		}
	}
	if clientID == "" || policy == nil {
		return nil, fmt.Errorf("no recognized-client policy names client %q", clientID)
	}
	var proofs []requiredProof
	seen := map[clientadapter.Canary]bool{}
	for _, canary := range clientadapter.ProtectionCanaries(clientID, goos) {
		if seen[canary] {
			continue
		}
		seen[canary] = true
		var names []string
		for _, grant := range append(append([]clientadapter.ProtectionGrant(nil),
			policy.Grants...), policy.Unconditional...) {
			if grant.Canary == canary {
				names = append(names, grant.Feature.Name())
			}
		}
		if canary == policy.IdentityCanary {
			names = append(names, identityProtection)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("canary %s in %s proves nothing the policy for %s names",
				canary.Test, canary.Package, clientID)
		}
		for _, name := range names {
			if name == "" {
				return nil, fmt.Errorf("canary %s proves a protection without a name", canary.Test)
			}
			proofs = append(proofs, requiredProof{protection: name, canary: canary})
		}
	}
	if len(proofs) == 0 {
		return nil, fmt.Errorf("the policy for %s names no proof on %s", clientID, goos)
	}
	return proofs, nil
}

// testsPattern is the anchored -run pattern that selects exactly the required
// tests.
func testsPattern(proofs []requiredProof) string {
	var names []string
	seen := map[string]bool{}
	for _, proof := range proofs {
		if !seen[proof.canary.Test] {
			seen[proof.canary.Test] = true
			names = append(names, regexp.QuoteMeta(proof.canary.Test))
		}
	}
	return "^(" + strings.Join(names, "|") + ")$"
}

func parseClientFlags(name string, arguments []string, stderr io.Writer,
	extra func(*flag.FlagSet)) (*flag.FlagSet, string, string, error) {
	flags := flag.NewFlagSet("vibermate-canary-proof "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	clientID := flags.String("client", "", "recognized client ID, for example claude-code")
	goos := flags.String("os", "", "operating system the tests ran on: linux or darwin")
	if extra != nil {
		extra(flags)
	}
	if err := flags.Parse(arguments); err != nil {
		return nil, "", "", err
	}
	if flags.NArg() != 0 {
		return nil, "", "", fmt.Errorf("vibermate-canary-proof %s takes no positional arguments", name)
	}
	return flags, *clientID, *goos, nil
}

func runTests(arguments []string, stdout, stderr io.Writer) int {
	_, clientID, goos, err := parseClientFlags("tests", arguments, stderr, nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	proofs, err := requiredProofs(clientID, goos)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stdout, testsPattern(proofs))
	return 0
}

type verifyConfig struct {
	clientID     string
	goos         string
	version      string
	input        string
	proof        string
	githubOutput string
}

func runVerify(arguments []string, stdout, stderr io.Writer, machine host) int {
	var config verifyConfig
	_, clientID, goos, err := parseClientFlags("verify", arguments, stderr, func(flags *flag.FlagSet) {
		flags.StringVar(&config.version, "version", "", "client release under test")
		flags.StringVar(&config.input, "input", "", "go test -json stream of the run")
		flags.StringVar(&config.proof, "proof", "", "proof file to write")
		flags.StringVar(&config.githubOutput, "github-output", "", "GitHub step output file to append to")
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	config.clientID, config.goos = clientID, goos
	if config.version == "" || config.input == "" || config.proof == "" {
		fmt.Fprintln(stderr, "--version, --input and --proof are required")
		return 2
	}
	if strings.ContainsFunc(config.version, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		fmt.Fprintf(stderr, "--version %q holds a space or a character outside printable ASCII\n", config.version)
		return 2
	}
	proofs, err := requiredProofs(config.clientID, config.goos)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	// The proof records where the tests ran, so it must be judged there.
	if config.goos != machine.goos {
		fmt.Fprintf(stderr, "--os %s names another system than this host's %s\n", config.goos, machine.goos)
		return 2
	}
	input, err := os.Open(config.input)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	stream, err := readStream(input, stdout)
	closeErr := input.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		fmt.Fprintf(stderr, "read %s: %v\n", config.input, err)
		return 2
	}
	verdict := judge(stream, proofs)

	document := proofDocument{
		Schema:      proofSchema,
		Client:      config.clientID,
		Version:     config.version,
		OS:          config.goos,
		Arch:        machine.goarch,
		Commit:      machine.commit,
		Run:         machine.run,
		Outcome:     verdict.outcome,
		Protections: verdict.protections,
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := os.WriteFile(config.proof, append(encoded, '\n'), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if config.githubOutput != "" {
		if err := appendGitHubOutput(config.githubOutput, verdict); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	printVerdict(stdout, document, verdict)
	if verdict.outcome != outcomePass {
		return 1
	}
	return 0
}

// testEvent is the part of a test2json event the verdict reads.
type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
	// ImportPath names the package of a build event, which has no Package.
	ImportPath string
}

type testKey struct {
	pkg  string
	test string
}

// stream is what a go test -json stream says about each test.
type stream struct {
	// final is each test's last terminal action: pass, fail or skip.
	final map[testKey]string
	// started holds every test that ran, finished or not.
	started map[testKey]bool
	// packageFailed holds every package whose own result is fail.
	packageFailed map[string]bool
	buildFailed   []string
	malformed     int
}

// readStream reads a go test -json stream and copies each output event to
// log, so the job log reads as go test -v would have printed it.
func readStream(input io.Reader, log io.Writer) (stream, error) {
	parsed := stream{
		final:         map[testKey]string{},
		started:       map[testKey]bool{},
		packageFailed: map[string]bool{},
	}
	reader := bufio.NewReader(input)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) != 0 {
			var event testEvent
			if json.Unmarshal(trimmed, &event) != nil || event.Action == "" {
				parsed.malformed++
			} else {
				parsed.record(event, log)
			}
		}
		if errors.Is(err, io.EOF) {
			return parsed, nil
		}
		if err != nil {
			return stream{}, err
		}
	}
}

func (parsed *stream) record(event testEvent, log io.Writer) {
	switch event.Action {
	case "output", "build-output":
		_, _ = io.WriteString(log, event.Output)
	case "build-fail":
		parsed.buildFailed = append(parsed.buildFailed, event.ImportPath)
	case "run":
		if event.Test != "" {
			parsed.started[testKey{event.Package, event.Test}] = true
		}
	case "pass", "fail", "skip":
		if event.Test == "" {
			if event.Action == "fail" {
				parsed.packageFailed[event.Package] = true
			}
			return
		}
		key := testKey{event.Package, event.Test}
		parsed.started[key] = true
		parsed.final[key] = event.Action
	}
}

type protectionResult struct {
	Feature string `json:"feature"`
	Test    string `json:"test"`
	Result  string `json:"result"`
}

type proofDocument struct {
	Schema      string             `json:"schema"`
	Client      string             `json:"client"`
	Version     string             `json:"version"`
	OS          string             `json:"os"`
	Arch        string             `json:"arch"`
	Commit      string             `json:"commit"`
	Run         string             `json:"run"`
	Outcome     string             `json:"outcome"`
	Protections []protectionResult `json:"protections"`
}

type verdict struct {
	outcome     string
	protections []protectionResult
	// failedLeaves are the failing tests no failing subtest explains.
	failedLeaves []testKey
	// unexplained says why the stream failed outside any test result.
	unexplained []string
}

func judge(parsed stream, proofs []requiredProof) verdict {
	var result verdict
	required := map[testKey]bool{}
	allRequiredPass := true
	requiredDidNotRun := false
	for _, proof := range proofs {
		key := testKey{modulePath + "/" + proof.canary.Package, proof.canary.Test}
		required[key] = true
		outcome, finished := parsed.final[key]
		switch {
		case finished:
		case parsed.started[key]:
			outcome = resultFail
		default:
			outcome = resultMissing
		}
		if outcome != resultPass {
			allRequiredPass = false
		}
		if outcome == resultSkip || outcome == resultMissing {
			requiredDidNotRun = true
		}
		result.protections = append(result.protections, protectionResult{
			Feature: proof.protection,
			Test:    proof.canary.Test,
			Result:  outcome,
		})
	}

	failed := map[testKey]bool{}
	failedPackages := map[string]bool{}
	for key, action := range parsed.final {
		if action == resultFail {
			failed[key] = true
			failedPackages[key.pkg] = true
		}
	}
	for key := range parsed.started {
		if _, finished := parsed.final[key]; !finished {
			result.unexplained = append(result.unexplained,
				fmt.Sprintf("%s in %s started and never finished", key.test, key.pkg))
			failed[key] = true
			failedPackages[key.pkg] = true
		}
	}
	for pkg := range parsed.packageFailed {
		if !failedPackages[pkg] {
			result.unexplained = append(result.unexplained,
				fmt.Sprintf("package %s failed outside any test", pkg))
		}
	}
	for _, importPath := range parsed.buildFailed {
		result.unexplained = append(result.unexplained,
			fmt.Sprintf("build of %s failed", importPath))
	}
	if parsed.malformed != 0 {
		result.unexplained = append(result.unexplained,
			fmt.Sprintf("%d line(s) of the stream are not test events", parsed.malformed))
	}
	sort.Strings(result.unexplained)

	for key := range failed {
		leaf := true
		for other := range failed {
			if other.pkg == key.pkg && strings.HasPrefix(other.test, key.test+"/") {
				leaf = false
				break
			}
		}
		if leaf {
			result.failedLeaves = append(result.failedLeaves, key)
		}
	}
	sort.Slice(result.failedLeaves, func(i, j int) bool {
		if result.failedLeaves[i].pkg != result.failedLeaves[j].pkg {
			return result.failedLeaves[i].pkg < result.failedLeaves[j].pkg
		}
		return result.failedLeaves[i].test < result.failedLeaves[j].test
	})

	switch {
	case len(failed) == 0 && len(result.unexplained) == 0 && allRequiredPass:
		result.outcome = outcomePass
	case len(result.unexplained) == 0 && !requiredDidNotRun &&
		len(result.failedLeaves) != 0 && allUnderRequiredControls(result.failedLeaves, required):
		result.outcome = outcomeDrift
	default:
		result.outcome = outcomeBroken
	}
	return result
}

// allUnderRequiredControls reports whether every leaf sits under a control
// subtest of a required test: the first name segment below the top-level test
// starts with "control".
func allUnderRequiredControls(leaves []testKey, required map[testKey]bool) bool {
	for _, leaf := range leaves {
		segments := strings.Split(leaf.test, "/")
		if len(segments) < 2 || !strings.HasPrefix(segments[1], "control") ||
			!required[testKey{leaf.pkg, segments[0]}] {
			return false
		}
	}
	return true
}

// notProven lists, once each and in policy order, the protections whose test
// did not pass.
func (result verdict) notProven() []string {
	var names []string
	seen := map[string]bool{}
	for _, protection := range result.protections {
		if protection.Result != resultPass && !seen[protection.Feature] {
			seen[protection.Feature] = true
			names = append(names, protection.Feature)
		}
	}
	return names
}

func appendGitHubOutput(path string, result verdict) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(file, "outcome=%s\nprotections=%s\n",
		result.outcome, strings.Join(result.notProven(), ","))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func printVerdict(stdout io.Writer, document proofDocument, result verdict) {
	fmt.Fprintf(stdout, "\n%s %s on %s/%s: %s\n",
		document.Client, document.Version, document.OS, document.Arch, document.Outcome)
	for _, protection := range document.Protections {
		fmt.Fprintf(stdout, "  %s: %s %s\n", protection.Feature, protection.Test, protection.Result)
	}
	for _, leaf := range result.failedLeaves {
		fmt.Fprintf(stdout, "  failed: %s in %s\n", leaf.test, leaf.pkg)
	}
	for _, reason := range result.unexplained {
		fmt.Fprintf(stdout, "  %s\n", reason)
	}
}
