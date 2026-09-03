//go:build !vibermate_native_secrets

package desktophost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

const (
	managedEnvironmentID = "managed-anthropic"
	managedEndpointID    = "target.managed.anthropic"
	managedAccountID     = "anthropic-test"
	managedSecret        = "managed-test-credential"
)

type managedProviderObservation struct {
	path          string
	authorization string
	xAPIKey       string
	apiKey        string
	cookie        string
	body          string
}

// This is the first test that drives a managed request through the real
// Desktop composition root: launcher -> authenticated proxy -> leaf TLS ->
// Environment request resolution -> Exchange -> ProviderAccount lease ->
// final AuthDriver -> provider transport. It repeats after a full Host reopen
// to prove that SQLite metadata and the private file SecretStore recover as
// one usable route, not merely as independent component records.
func TestManagedAnthropicRouteUsesOnlyFrozenAccountAcrossRestart(t *testing.T) {
	observed := make(chan managedProviderObservation, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, _ := io.ReadAll(request.Body)
		observed <- managedProviderObservation{
			path: request.URL.Path, authorization: request.Header.Get("Authorization"),
			xAPIKey: request.Header.Get("X-Api-Key"), apiKey: request.Header.Get("Api-Key"),
			cookie: request.Header.Get("Cookie"), body: string(body),
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"msg_managed","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"managed reached"}],
			"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":4,"output_tokens":2}
		}`)
	}))
	defer provider.Close()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	dataDirectory := filepath.Join(root, "data")
	secretPath := filepath.Join(root, "private", "provider-secrets.json")
	factory, err := hostsecret.NewDevelopmentFileFactory(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fixedSelfTestCatalog(t)

	first, _ := startManagedHost(t, paths, dataDirectory, factory, catalog)
	endpointID := createManagedEndpoint(t, first.Runtime().UpstreamEndpoints(), provider.URL)
	accountID := createManagedAccount(t, first.Runtime().ProviderAccounts(), endpointID)
	environmentID := publishManagedEnvironment(t, first, provider.URL, endpointID, accountID)
	firstDigest := resolveEnvironmentDigest(t, first, environmentID)
	runManagedChild(t, paths, environmentID, false)
	assertManagedObservation(t, <-observed)
	assertManagedEvidence(t, first, environmentID, accountID)
	shutdownHost(t, first)

	second, secondSecrets := startManagedHost(t, paths, dataDirectory, factory, catalog)
	defer shutdownHost(t, second)
	if got := resolveEnvironmentDigest(t, second, environmentID); got != firstDigest {
		t.Fatalf("recovered Environment digest = %q, want %q", got, firstDigest)
	}
	view, err := second.Runtime().ProviderAccounts().Get(context.Background(), accountID)
	if err != nil || view.Account.Revision != 1 ||
		view.Health.State != provideraccount.HealthReady ||
		view.Health.CredentialEpoch != 1 {
		t.Fatalf("recovered ProviderAccount = %+v, %v", view, err)
	}
	runManagedChild(t, paths, environmentID, false)
	assertManagedObservation(t, <-observed)
	assertManagedEvidence(t, second, environmentID, accountID)

	account, err := second.Runtime().ProviderAccounts().Get(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondSecrets.Delete(context.Background(), account.Account.SecretRef); err != nil {
		t.Fatal(err)
	}
	runManagedChild(t, paths, environmentID, true)
	select {
	case got := <-observed:
		t.Fatalf("missing managed credential reached provider: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	missing, err := second.Runtime().ProviderAccounts().Get(context.Background(), accountID)
	if err != nil || missing.Health.State != provideraccount.HealthMissing {
		t.Fatalf("missing managed account health = %+v, %v", missing, err)
	}
}

func TestManagedClaudeOAuthRouteUsesBearerCredential(t *testing.T) {
	observed := make(chan managedProviderObservation, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, _ := io.ReadAll(request.Body)
		observed <- managedProviderObservation{
			path: request.URL.Path, authorization: request.Header.Get("Authorization"),
			xAPIKey: request.Header.Get("X-Api-Key"), apiKey: request.Header.Get("Api-Key"),
			cookie: request.Header.Get("Cookie"), body: string(body),
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"msg_oauth","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"managed reached"}],
			"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":4,"output_tokens":2}
		}`)
	}))
	defer provider.Close()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	factory, err := hostsecret.NewDevelopmentFileFactory(
		filepath.Join(root, "private", "provider-secrets.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	host, _ := startManagedHost(
		t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t),
	)
	defer shutdownHost(t, host)
	endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
	accountID := createManagedAccountWithDriver(
		t, host.Runtime().ProviderAccounts(), endpointID, providerauth.StaticHeaderDriverRef(),
	)
	environmentID := publishManagedEnvironment(t, host, provider.URL, endpointID, accountID)
	runManagedChild(t, paths, environmentID, false)

	got := <-observed
	if got.path != "/v1/messages" || got.authorization != "Bearer "+managedSecret ||
		got.xAPIKey != "" || got.apiKey != "" || got.cookie != "" ||
		!strings.Contains(got.body, `"content":"managed route"`) {
		t.Fatalf("managed Claude OAuth provider observation = %+v", got)
	}
	assertManagedEvidence(t, host, environmentID, accountID)
}

func startManagedHost(
	t *testing.T,
	paths desktophost.Paths,
	dataDirectory string,
	factory hostsecret.DevelopmentFileFactory,
	catalog clientadapter.Catalog,
) (*desktophost.Host, secretstore.Store) {
	t.Helper()
	secrets, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	options := hostOptions(t, paths, dataDirectory)
	options.Runtime.Secrets = secrets
	options.ClientCatalog = catalog
	return startHost(t, options), secrets
}

func fixedSelfTestCatalog(t *testing.T) clientadapter.Catalog {
	t.Helper()
	executable, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	catalog, err := clientadapter.NewCatalog(1, []clientadapter.Release{{
		ID: "claude-code", Revision: 1, Version: "test",
		OperatingSystem: runtime.GOOS, Architecture: runtime.GOARCH,
		InstallShape:    clientadapter.InstallNativeSingleBinary,
		InvocationLabel: filepath.Base(executable), ArtifactRoot: ".",
		Artifacts: []clientadapter.Artifact{{
			Role: clientadapter.ArtifactEntrypoint, SHA256: hex.EncodeToString(digest[:]),
		}},
		LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
		Features:     clientadapter.FeatureCoreOwnedStreamingFallback,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func createManagedAccount(
	t *testing.T,
	accounts provideraccount.Controller,
	endpointID upstreamendpoint.ID,
) provideraccount.ID {
	t.Helper()
	return createManagedAccountWithDriver(
		t, accounts, endpointID, providerauth.AnthropicAPIKeyDriverRef(),
	)
}

func createManagedAccountWithDriver(
	t *testing.T,
	accounts provideraccount.Controller,
	endpointID upstreamendpoint.ID,
	driver providerauth.DriverRef,
) provideraccount.ID {
	t.Helper()
	id, err := provideraccount.NewID(managedAccountID)
	if err != nil {
		t.Fatal(err)
	}
	material, err := providerauth.NewMaterial(managedSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	secret, err := secretstore.NewValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	view, err := accounts.Create(context.Background(), provideraccount.CreateCommand{
		ID: id, DisplayName: "Anthropic test", UpstreamEndpointID: endpointID,
		Driver: driver, Secret: secret,
	})
	if err != nil || view.Account.Revision != 1 ||
		view.Health.CredentialEpoch != 1 {
		t.Fatalf("create managed account = %+v, %v", view, err)
	}
	return id
}

func createManagedEndpoint(
	t *testing.T,
	endpoints upstreamendpoint.Controller,
	providerURL string,
) upstreamendpoint.ID {
	t.Helper()
	id, err := upstreamendpoint.NewID(managedEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseProviderOrigin(providerURL)
	if err != nil {
		t.Fatal(err)
	}
	view, err := endpoints.Create(context.Background(), upstreamendpoint.CreateCommand{
		ID: id, DisplayName: "Anthropic test relay", Origin: origin,
		RealmID: "anthropic.official", BackendProtocols: []string{"anthropic_messages"},
		Capabilities: []protocolspec.ProviderCapability{
			protocolspec.ProviderCapabilityMessages,
			protocolspec.ProviderCapabilityStreaming,
			protocolspec.ProviderCapabilityToolCalls,
		},
		Drivers: []providerauth.DriverRef{
			providerauth.AnthropicAPIKeyDriverRef(), providerauth.StaticHeaderDriverRef(),
		},
	})
	if err != nil || view.Revision != 1 {
		t.Fatalf("create managed UpstreamEndpoint = %+v, %v", view, err)
	}
	return id
}

func publishManagedEnvironment(
	t *testing.T,
	host *desktophost.Host,
	providerURL string,
	upstreamEndpointID upstreamendpoint.ID,
	accountID provideraccount.ID,
) environment.EnvironmentID {
	t.Helper()
	id, err := environment.NewEnvironmentID(managedEnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	clientOrigin, err := originidentity.ParseClientOrigin("https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := originidentity.ParseProviderOrigin(providerURL)
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := environment.NewClientEndpointID("endpoint.anthropic")
	if err != nil {
		t.Fatal(err)
	}
	planID, err := environment.NewClientProtocolPlanID("plan.anthropic")
	if err != nil {
		t.Fatal(err)
	}
	routeID, err := environment.NewUpstreamRouteID("route.anthropic")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := environment.Environment{
		ID: id, Name: "Managed Anthropic", State: environment.StateActive, Revision: 1,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: endpointID, Revision: 1, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: planID, Revision: 1,
				ClientProtocol: environment.ClientProtocolAnthropicMessages,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{
					ID: "adapter.claude", Revision: 1,
				},
				EgressProfile: egressprofile.Direct(),
				Destination: environment.DestinationPlan{
					Kind: environment.DestinationKindUpstream,
					Upstream: &environment.UpstreamPlan{
						DefaultRouteID: routeID,
						RouteSet: environment.RouteSet{
							ID: "routes.anthropic", Revision: 1,
							CandidateRouteIDs: []environment.UpstreamRouteID{routeID},
						},
						Routes: []environment.UpstreamRoute{{
							ID: routeID, Revision: 1,
							ProviderTarget: environment.ProviderTarget{
								ID: upstreamEndpointID.String(), Revision: 1,
								Origin: providerOrigin, RealmID: "anthropic.official",
								Capabilities: []protocolspec.ProviderCapability{
									protocolspec.ProviderCapabilityMessages,
									protocolspec.ProviderCapabilityStreaming,
									protocolspec.ProviderCapabilityToolCalls,
								},
							},
							BackendProtocol: string(environment.ClientProtocolAnthropicMessages),
							AccountPolicy: environment.RouteAccountPolicy{
								Revision: 1, Mode: environment.AccountSelectionFixed,
								FixedAccountID: accountID.String(),
								Accounts: []environment.RouteAccountReference{{
									ID: accountID.String(), Revision: 1, DisplayName: "Anthropic test",
								}},
							},
							ModelPolicy:    environment.ModelPolicy{Revision: 1, Mode: "passthrough"},
							WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
						}},
					},
				},
			}},
		}},
	}
	draft, err := host.Runtime().Environments().SaveDraft(
		context.Background(),
		environment.DraftCommand{Candidate: aggregate},
	)
	if err != nil {
		t.Fatalf("save managed Environment: %v", err)
	}
	preview, err := host.Runtime().Environments().Preview(
		context.Background(), id, draft.Revision,
	)
	if err != nil {
		t.Fatalf("preview managed Environment: %v", err)
	}
	result, err := host.Runtime().Environments().Publish(context.Background(), preview)
	if err != nil || result.Outcome != environment.CommitOutcomeCommitted {
		t.Fatalf("publish managed Environment = %+v, %v", result, err)
	}
	return id
}

func resolveEnvironmentDigest(
	t *testing.T,
	host *desktophost.Host,
	id environment.EnvironmentID,
) string {
	t.Helper()
	snapshot, err := host.Runtime().EnvironmentResolver().Resolve(id)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Digest().String()
}

func runManagedChild(
	t *testing.T,
	paths desktophost.Paths,
	environmentID environment.EnvironmentID,
	expectFailure bool,
) {
	t.Helper()
	discovery, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		hostClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	baseEnvironment := []string{
		"PATH=/usr/bin:/bin",
		childManagedEnvironment + "=1",
		"ANTHROPIC_API_KEY=client-ambient-secret",
		"ANTHROPIC_AUTH_TOKEN=client-ambient-auth-token",
		"CLAUDE_CODE_OAUTH_TOKEN=client-oauth-secret",
	}
	if expectFailure {
		baseEnvironment = append(baseEnvironment, childManagedFailure+"=1")
	}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       discovery,
		BaseEnvironment: baseEnvironment,
		Stdin:           strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr,
		HeartbeatInterval: 10 * time.Millisecond,
		ControlTimeout:    2 * time.Second, CreateTimeout: 5 * time.Second,
		TerminationTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	exitCode, err := launcher.Run(ctx, runlauncher.LaunchRequest{
		EnvironmentID: environmentID,
		Command:       []string{os.Args[0]},
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("managed child exit=%d err=%v stderr=%s", exitCode, err, stderr.String())
	}
}

type hostClock struct{}

func (hostClock) Now() time.Time { return time.Now().UTC() }

func assertManagedObservation(t *testing.T, got managedProviderObservation) {
	t.Helper()
	if got.path != "/v1/messages" || got.xAPIKey != managedSecret ||
		got.authorization != "" || got.apiKey != "" || got.cookie != "" ||
		!strings.Contains(got.body, `"content":"managed route"`) {
		t.Fatalf("managed provider observation = %+v", got)
	}
}

func assertManagedEvidence(
	t *testing.T,
	host *desktophost.Host,
	environmentID environment.EnvironmentID,
	accountID provideraccount.ID,
) {
	t.Helper()
	page, err := host.Runtime().Activities().ListExchanges(
		context.Background(),
		activity.PageRequest{Limit: 20, EnvironmentID: environmentID.String()},
	)
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("managed Activity = %+v, %v", page, err)
	}
	record := page.Items[0]
	if record.Status != activity.StatusSucceeded ||
		record.AccountID != accountID.String() || record.AccountRevision != 1 ||
		record.CredentialEpoch != 1 || record.RouteID != "route.anthropic" {
		t.Fatalf("managed Activity record = %+v", record)
	}
	egress, err := host.Runtime().EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{
			Limit: 20, ExchangeID: record.SubjectID,
			Purpose: egressaudit.PurposeProviderAttempt,
		},
	)
	if err != nil || len(egress.Items) != 1 {
		t.Fatalf("managed EgressAttempt = %+v, %v", egress, err)
	}
	attempt := egress.Items[0].Attempt
	if !attempt.Terminal() || attempt.Outcome() != egressaudit.OutcomeCompleted ||
		attempt.TargetOrigin() == "https://api.anthropic.com" ||
		attempt.BytesOut() == 0 || attempt.BytesIn() == 0 {
		t.Fatalf("managed EgressAttempt = %+v", egressaudit.ViewOf(egress.Items[0]))
	}
}
