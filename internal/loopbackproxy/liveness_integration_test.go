package loopbackproxy_test

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// The executor double requests one liveness write; admission, frozen catalog
// routing, private TLS handler and downstream accounting are all real.
func TestResponsesKeepaliveUsesFrozenNativeClientContract(t *testing.T) {
	for _, test := range []struct {
		name, host, path, want string
		verified, manual       bool
		dialect                protocolspec.Dialect
		provider               string
	}{
		{name: "configured_native", host: "chatgpt.com", path: "/backend-api/codex/responses", want: "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"},
		{name: "manual_unknown_native", host: "chatgpt.com", path: "/backend-api/codex/responses", want: "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n", manual: true},
		{name: "generic_without_evidence", host: "api.openai.com", path: "/v1/responses", want: ": keepalive\n\n"},
		{name: "generic_manual", host: "api.openai.com", path: "/v1/responses", want: ": keepalive\n\n", manual: true},
		{name: "generic_exact160_evidence", host: "api.openai.com", path: "/v1/responses", want: "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n", verified: true},
		{name: "native_wrong_origin", host: "other.example", path: "/backend-api/codex/responses", want: ": keepalive\n\n", verified: true},
		{name: "native_wrong_port", host: "chatgpt.com:444", path: "/backend-api/codex/responses", want: ": keepalive\n\n", verified: true},
		{name: "native_wrong_path", host: "chatgpt.com", path: "/backend-api/codex/responses/"},
		{name: "native_raw_alias", host: "chatgpt.com", path: "/backend-api/codex/%72esponses"},
		{name: "native_query", host: "chatgpt.com", path: "/backend-api/codex/responses?extra=x"},
		{name: "generic_query", host: "api.openai.com", path: "/v1/responses?extra=x", verified: true},
		{name: "other_dialect", host: "api.anthropic.com", path: "/v1/messages", want: ": keepalive\n\n", dialect: protocolspec.DialectAnthropicMessages, verified: true},
		{name: "generic_client_chatgpt_provider", host: "api.openai.com", path: "/v1/responses", want: ": keepalive\n\n", provider: "https://chatgpt.com"},
		{name: "native_client_private_provider", host: "chatgpt.com", path: "/backend-api/codex/responses", want: "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n", provider: "https://private.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer := &rawObserver{}
			var adapter *clientadapter.Evidence
			if test.verified {
				catalog := clientadapter.BuiltInCatalog()
				evidence, ok := catalog.ExpectedEvidenceForPlatform("codex-cli", "0.160.0", "darwin", "arm64")
				if !ok {
					t.Fatal("actual closed tuple missing")
				}
				accepted, err := catalog.ValidateCompanionAttestation(clientadapter.Detection{Status: clientadapter.StatusVerified, Recognition: clientadapter.RecognitionVerified, CatalogRevision: catalog.Revision(), CanonicalPath: filepath.Join(t.TempDir(), "codex"), ExecutableLabel: "codex", Evidence: &evidence})
				if err != nil {
					t.Fatal(err)
				}
				adapter = accepted.Evidence
			}
			dialect := test.dialect
			if dialect == "" {
				dialect = protocolspec.DialectOpenAIResponses
			}
			origins := []string{"https://" + test.host}
			if test.provider != "" {
				origins = append(origins, test.provider)
			}
			fixture := newProxyFixtureForDialectWithPolicyAndRawEvidence(t, dialect, adapter, allowEverythingTestPolicy(t), observer, "", origins...)
			defer fixture.Close(t)
			credential := fixture.grant.ProxyCapability.Value()
			if test.manual {
				grant, err := fixture.manuals.Create(context.Background(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Unknown native159.3", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				capture, _ := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
				if _, err = fixture.assignments.Create(context.Background(), captureassignment.CreateCommand{Capture: capture, EnvironmentID: fixture.environment.ID, Source: captureassignment.SourceManualCreate}); err != nil {
					t.Fatal(err)
				}
				credential = grant.Credential.Value()
			}
			authorizer, err := captureadmission.NewAuthorizer(fixture.runs, fixture.manuals)
			if err != nil {
				t.Fatal(err)
			}
			proxyCredential, err := captureadmission.NewProxyCredential(credential)
			if err != nil {
				t.Fatal(err)
			}
			admission, err := authorizer.Authorize(context.Background(), proxyCredential)
			if err != nil {
				t.Fatal(err)
			}
			if !test.verified && admission.Supports(clientadapter.FeatureResponsesDataKeepalive) {
				t.Fatal("format selection fabricated adapter compatibility")
			}
			fixture.exchanges.keepalive = true
			origin, err := originidentity.ParseClientOrigin("https://" + test.host)
			if err != nil {
				t.Fatal(err)
			}
			secured := fixture.ConnectTLS(t, credential, origin.EndpointAuthority(), origin.Host())
			defer secured.Close()
			response := writeInnerRequest(t, secured, &http.Request{Method: "POST", URL: mustURL(t, test.path), Host: origin.EndpointAuthority(), Header: http.Header{"Content-Type": {"application/json"}, "User-Agent": {"codex/0.160.0"}, "Version": {"159.3"}}, Body: io.NopCloser(strings.NewReader(`{"model":"synthetic"}`)), ContentLength: int64(len(`{"model":"synthetic"}`))})
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if test.want == "" {
				if err != nil || response.StatusCode == 200 || len(fixture.exchanges.Requests()) != 0 {
					t.Fatal("invalid request bypassed frozen operation matching")
				}
				return
			}
			if err != nil || response.StatusCode != 200 {
				t.Fatalf("real request setup failed status=%d err=%v", response.StatusCode, err)
			}
			if string(body) != test.want {
				t.Errorf("frozen client contract keepalive=%q want=%q", body, test.want)
			}
			if test.provider != "" {
				route, exists := fixture.exchanges.Requests()[0].RequestPlan().UpstreamRoute()
				if !exists || route.ProviderTarget().Origin.String() != test.provider {
					t.Fatal("cross-route was not genuinely compiled")
				}
			}
			deadline := time.Now().Add(time.Second)
			for len(observer.snapshot()) < 2 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			observations := observer.snapshot()
			if len(observations) != 2 {
				t.Fatal("real raw downstream missing")
			}
			last := observations[1]
			if last.Layer != rawevidence.LayerClientDownstream || len(last.Frames) != 1 || last.Frames[0].Kind != rawevidence.FrameKeepalive || last.TotalBodyBytes != int64(len(body)) || last.BodySHA256 != sha256.Sum256(body) {
				t.Fatal("keepalive lost exact nonsemantic raw accounting")
			}
		})
	}
}

type livenessAccountCatalog struct{}

func (livenessAccountCatalog) LookupAccount(id, endpoint string) (environment.AccountDescriptor, bool) {
	return environment.AccountDescriptor{ID: "liveness.account", Revision: 1, DisplayName: "Liveness account", Active: true, UpstreamEndpointID: "liveness.provider", UpstreamEndpointRevision: 1, RealmID: "liveness.realm", BackendProtocols: []string{string(protocolspec.DialectOpenAIResponses)}}, id == "liveness.account" && endpoint == "liveness.provider"
}
func configureLivenessUpstream(t *testing.T, e *environment.Environment, provider string) {
	t.Helper()
	origin, err := originidentity.ParseProviderOrigin(provider)
	if err != nil {
		t.Fatal(err)
	}
	e.ClientEndpoints[0].ProtocolPlans[0].Destination = environment.DestinationPlan{Kind: environment.DestinationKindUpstream, Upstream: &environment.UpstreamPlan{
		DefaultRouteID: "liveness.route", RouteSet: environment.RouteSet{ID: "liveness.routes", Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{"liveness.route"}},
		Routes: []environment.UpstreamRoute{{ID: "liveness.route", Revision: 1, ProviderTarget: environment.ProviderTarget{ID: "liveness.provider", Revision: 1, Origin: origin, RealmID: "liveness.realm", Capabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}}, BackendProtocol: string(protocolspec.DialectOpenAIResponses),
			AccountPolicy: environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionFixed, FixedAccountID: "liveness.account", Accounts: []environment.RouteAccountReference{{ID: "liveness.account", Revision: 1, DisplayName: "Liveness account"}}}, ModelPolicy: environment.ModelPolicy{Revision: 1, Mode: environment.ModelModePassthrough}, WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue}},
	}}
}
