package desktophost_test

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/launcherdiscovery"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const (
	liveOriginEnvironment = "VIBERMATE_LIVE_PROVIDER_ORIGIN"
	liveKeyEnvironment    = "VIBERMATE_LIVE_PROVIDER_KEY"
	liveModelEnvironment  = "VIBERMATE_LIVE_PROVIDER_MODEL"
	liveAgentEnvironment  = "VIBERMATE_LIVE_AGENT"
)

// A real agent client, launched the way the product launches one, reaching a
// real model through vibermate.
//
// Everything else proves a part. This proves the thing: an installed Claude
// Code binary that knows nothing about vibermate beyond the environment the
// launcher gave it, talking to a backend that speaks a different dialect,
// through a TLS connection vibermate terminated with its own root.
func TestARealAgentClientReachesAModelThroughVibermate(t *testing.T) {
	origin := os.Getenv(liveOriginEnvironment)
	key := os.Getenv(liveKeyEnvironment)
	model := os.Getenv(liveModelEnvironment)
	agent := os.Getenv(liveAgentEnvironment)
	if origin == "" || key == "" || model == "" || agent == "" {
		t.Skipf(
			"live agent run needs %s, %s, %s, and %s",
			liveOriginEnvironment,
			liveKeyEnvironment,
			liveModelEnvironment,
			liveAgentEnvironment,
		)
	}
	agentPath, err := exec.LookPath(agent)
	if err != nil {
		t.Skipf("%s is not on PATH: %v", agent, err)
	}

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := liveHostOptions(t, paths, filepath.Join(root, "data"))
	host := startHost(t, options)
	defer shutdownHost(t, host)
	runtime := host.Runtime()

	accessID, err := access.NewAccessID("access-live-agent")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := liveAgentAccess(t, accessID, origin, model)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	value, err := secretstore.NewValue([]byte(key))
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
		t.Fatalf("store the provider credential: %v", err)
	}
	rules := runtime.ConnectionRules()
	if _, err := rules.Replace(
		context.Background(),
		rules.Current().Revision,
		[]connectionpolicy.Rule{{
			ID:       "live.allow-agent-endpoint",
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
			"HOME=" + t.TempDir(),
			// The client's own credential is never what reaches the backend.
			"ANTHROPIC_API_KEY=vibermate-live-placeholder",
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
		4*time.Minute,
	)
	defer cancelRun()
	exitCode, err := launcher.Run(runContext, []string{
		agentPath,
		"--print",
		"--model",
		"claude-client-alias",
		"Reply with the single word: ready",
	})
	answered := output.String()
	if err != nil || exitCode != 0 {
		// A failed live run is worth explaining, so the records that would
		// otherwise be discarded with the runtime are printed first.
		records, listErr := runtime.Activities().List(
			context.Background(),
			activity.PageRequest{Limit: 20},
		)
		if listErr == nil {
			for _, record := range records.Items {
				t.Logf("activity: %+v", record)
			}
		}
		t.Fatalf(
			"a captured agent client failed: exit=%d err=%v output=%s",
			exitCode,
			err,
			answered,
		)
	}
	if strings.TrimSpace(answered) == "" {
		t.Fatal("the agent client printed nothing")
	}
	t.Logf("agent client output: %q", strings.TrimSpace(answered))

	// The connection was decrypted as an agent endpoint, and a provider
	// attempt reached the real backend.
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
		t.Fatalf("no decrypted connection was recorded: %+v", connections.Items)
	}
	attempts, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	for _, record := range attempts.Items {
		if record.Attempt.Purpose() == egressaudit.PurposeProviderAttempt &&
			strings.Contains(record.Attempt.TargetOrigin(), originHost(t, origin)) {
			reached = true
		}
	}
	if !reached {
		t.Fatalf("no provider attempt reached %q: %+v", origin, attempts.Items)
	}
}

func originHost(t *testing.T, origin string) string {
	t.Helper()

	parsed, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

func liveHostOptions(
	t *testing.T,
	paths desktophost.Paths,
	dataDirectory string,
) desktophost.Options {
	t.Helper()

	runtimePaths, err := productruntime.NewRuntimePaths(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	factory, err := hostsecret.NewDevelopmentFileFactory(
		filepath.Join(t.TempDir(), "secrets", "store.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	options := desktophost.DefaultOptions(paths, productruntime.Options{
		Paths:          runtimePaths,
		Host:           hostcontract.Desktop(),
		OfflineHold:    gate,
		Secrets:        secrets,
		Approvals:      toolapproval.DefaultConfig(),
		ExchangeHold:   exchange.DefaultHoldPolicy(),
		Clock:          productruntime.SystemClock{},
		InstanceIDs:    productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader,
		Lifecycle:      productruntime.DefaultLifecycleOptions(),
	})
	options.LauncherTTL = 10 * time.Minute
	options.AppSessionTTL = time.Hour
	options.CaptureRunLifetime = 5 * time.Minute
	options.ShutdownTimeout = 20 * time.Second
	return options
}
