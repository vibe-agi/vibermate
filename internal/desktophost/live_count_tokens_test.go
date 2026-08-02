//go:build !vibermate_native_secrets

// This run stores a provider credential, so it is built only when the
// development file backend is the selected one. See live_agent_test.go.
package desktophost_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/launcherdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// catalogedClaudeVersion is the Claude Code release this build carries
// evidence for. A different build is a different client for these purposes.
const catalogedClaudeVersion = "2.1.220"

// countTokensSkillMarker is written into the fixture skill text. The client
// puts that text in the `count_tokens` request body, so it is a canary with a
// real client's real payload: M1.0-C0a says that body may never leave the
// machine with the client's own credentials.
const countTokensSkillMarker = "vibermate-c0a-count-tokens-canary-do-not-egress"

// M1.0-C0a step 1: what a fixed Claude Code does when `count_tokens` is
// rejected locally, observed in the product's own runtime rather than against
// a stand-in server.
//
// `claude plugin details` is used because it is the one non-interactive
// command that reliably requests `count_tokens`: it prices every skill in a
// plugin, and it sends the skill text to do it. `--print` prompts do not
// request it at all, which is why the first probe of this question could only
// record `not_observed`.
//
// The design fixes the order at 12-implementation-readiness §3.2: step 1
// observes the compatibility outcome and does not authorise anything. So this
// asserts what the client does, not that any particular thing is acceptable.
func TestAFixedClaudeCodeDegradesWhenCountTokensIsRejectedLocally(t *testing.T) {
	agent := os.Getenv(liveAgentEnvironment)
	if agent == "" {
		t.Skipf("this observation needs %s", liveAgentEnvironment)
	}
	agentPath, err := exec.LookPath(agent)
	if err != nil {
		t.Skipf("%s is not on PATH: %v", agent, err)
	}
	version, err := exec.Command(agentPath, "--version").Output()
	if err != nil {
		t.Skipf("could not read the client version: %v", err)
	}
	if !strings.Contains(string(version), catalogedClaudeVersion) {
		t.Skipf(
			"the installed client is %q; this observation is about %s",
			strings.TrimSpace(string(version)),
			catalogedClaudeVersion,
		)
	}

	clientHome := newCountTokensPluginFixture(t, agentPath)

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, liveHostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	runtime := host.Runtime()

	accessID, err := access.NewAccessID("access-count-tokens")
	if err != nil {
		t.Fatal(err)
	}
	// No provider is ever contacted on this path, so the backend named here
	// only has to be a well-formed origin.
	aggregate := liveAgentAccess(
		t,
		accessID,
		"https://backend.invalid:443",
		"model-that-is-never-called",
	)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	value, err := secretstore.NewValue([]byte("count-tokens-placeholder"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Credentials().ReplaceSecret(
		context.Background(),
		accesscredential.ReplaceCommand{
			AccessID:         accessID,
			ProfileID:        aggregate.Profiles[0].ID,
			CredentialID:     aggregate.AccountBindings[0].ID,
			ExpectedRevision: 0,
			Value:            value,
		},
	); err != nil {
		t.Fatalf("store the placeholder credential: %v", err)
	}
	rules := runtime.ConnectionRules()
	if _, err := rules.Replace(
		context.Background(),
		rules.Current().Revision,
		[]connectionpolicy.Rule{{
			ID:       "count-tokens.allow-agent-endpoint",
			Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHostPort("api.anthropic.com", 443),
		}},
		rules.Current().Default,
	); err != nil {
		t.Fatal(err)
	}

	sessionFile, err := launcherdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: sessionFile,
		BaseEnvironment: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + clientHome,
			"ANTHROPIC_API_KEY=vibermate-count-tokens-placeholder",
		},
		Stdin:              strings.NewReader(""),
		Stdout:             &output,
		Stderr:             os.Stderr,
		HeartbeatInterval:  100 * time.Millisecond,
		ControlTimeout:     10 * time.Second,
		TerminationTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithTimeout(
		context.Background(),
		2*time.Minute,
	)
	defer cancelRun()
	exitCode, err := launcher.Run(runContext, []string{
		agentPath,
		"plugin",
		"details",
		countTokensFixturePlugin,
	})
	printed := output.String()
	t.Logf("client exit=%d output:\n%s", exitCode, printed)

	// The observation itself. The design names three outcomes and does not
	// presuppose one, so each is reported in its own terms.
	if err != nil || exitCode != 0 {
		t.Fatalf(
			"observed outcome: the client BROKE on the local rejection "+
				"(exit=%d err=%v). M1.0-C0a step 2 stands regardless; step 5 "+
				"must record this client/version as blocked with this reason. "+
				"output=%s",
			exitCode,
			err,
			printed,
		)
	}
	if !strings.Contains(printed, "Projected token cost") ||
		!strings.Contains(printed, "Always-on:") {
		t.Fatalf(
			"observed outcome: the client survived but produced no token cost, "+
				"which is neither local estimation nor a clean break. output=%s",
			printed,
		)
	}

	// A connection was decrypted as the agent endpoint, so the client's HTTPS
	// requests reached this proxy rather than failing to connect.
	connections, err := runtime.ConnectionEvents().List(
		context.Background(),
		connectionevent.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	decrypted := false
	for _, record := range connections.Items {
		if record.RequestedHost == "api.anthropic.com" &&
			record.Decryption == connectionevent.DecryptionMITM {
			decrypted = true
		}
	}
	if !decrypted {
		t.Fatalf(
			"no decrypted agent-endpoint connection was recorded, so this run "+
				"does not show the client reaching the proxy at all: %+v",
			connections.Items,
		)
	}

	// What the outbound records must show. An attempt carries no body, so
	// searching them for the skill text would assert nothing; and the record
	// constructor already refuses to pair an original-origin purpose with a
	// client payload class, so asserting that pairing is absent would only
	// restate a constructor. Two things here are decided by this run rather
	// than by construction:
	//
	//   - `count_tokens` produced no outbound attempt of any kind, so the
	//     rejection happened before dispatch;
	//   - the bytes that did leave for the inbound origin are far too few to
	//     be the skill text the client put in that body.
	attempts, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 100},
	)
	if err != nil {
		t.Fatal(err)
	}
	var originalOriginBytes int64
	for _, record := range attempts.Items {
		attempt := record.Attempt
		switch attempt.Purpose() {
		case egressaudit.PurposeProfileOperation:
			t.Fatalf(
				"a profile operation reached the network; basic Preview "+
					"implements no ProfileOperationTarget: %+v",
				attempt,
			)
		case egressaudit.PurposeProviderAttempt:
			t.Fatalf(
				"this run contacts no provider, so a provider attempt means "+
					"count_tokens entered the model pipeline: %+v",
				attempt,
			)
		case egressaudit.PurposeOriginalOrigin, egressaudit.PurposeAgentProbe:
			originalOriginBytes += attempt.BytesOut()
		}
	}
	// The fixture skills are kilobytes each and the client sends their text.
	// The control probe that legitimately reaches the inbound origin is a
	// bodiless HEAD.
	//
	// This ceiling is load-bearing, and that was established rather than
	// assumed: registering `count_tokens` as `OperationPayloadControl` instead
	// of `OperationPayloadClientSemantic` makes this run send 6,488 bytes of
	// skill text to the inbound origin and fail here. That mutation is also
	// what shows the client really does issue `count_tokens` in this run, and
	// not merely that it survived.
	const controlPlaneByteCeiling = 1024
	if originalOriginBytes > controlPlaneByteCeiling {
		t.Fatalf(
			"%d bytes went to the inbound origin, more than a bodiless "+
				"control probe needs; the %q skill text may have escaped",
			originalOriginBytes,
			countTokensSkillMarker,
		)
	}
}
