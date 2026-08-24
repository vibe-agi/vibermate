package providertransport

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestFetchEndpointModelsHonorsHoldAuditsAndReleasesItsAction(t *testing.T) {
	t.Parallel()

	gate := newStartedGate(t)
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatalf("Enter() error = %v", err)
	}
	audit := &runtimeAuditRecorder{}
	transport := &runtimeTransportStub{
		audit: audit,
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"data":[{"id":"spark-model"}]}`,
			)),
		},
	}
	client := newRuntimeFetchClient(t, gate, transport, audit)
	endpoint := testDiscoveryEndpoint(t, "http://spark-2a59:8888")

	result := make(chan runtimeFetchResult, 1)
	go func() {
		response, err := client.FetchEndpointModels(
			context.Background(),
			endpoint,
			testRuntimeDiscoveryCredential(t, endpoint.RealmID),
		)
		result <- runtimeFetchResult{response: response, err: err}
	}()
	waitForGateQueue(t, gate, 1)
	if transport.callCount() != 0 || audit.appendCount() != 0 {
		t.Fatalf(
			"held discovery touched transport=%d audit=%d",
			transport.callCount(),
			audit.appendCount(),
		)
	}

	if _, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		offlinehold.ResumeRequest{Targets: gate.PendingProbeTargets()},
		proberSuccess{},
	); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	completed := <-result
	if completed.err != nil {
		t.Fatalf("FetchEndpointModels() error = %v", completed.err)
	}
	request := transport.lastRequest()
	if request.Method != http.MethodGet ||
		request.URL.String() != "http://spark-2a59:8888/v1/models" ||
		request.Host != "spark-2a59:8888" {
		t.Fatalf(
			"discovery request method=%q URL=%q Host=%q",
			request.Method,
			request.URL,
			request.Host,
		)
	}
	if authorization := request.Header.Get("Authorization"); authorization != "Bearer catalog-test-token" {
		t.Fatalf("discovery request authorization = %q", authorization)
	}
	if !transport.auditWasPresent() {
		t.Fatal("discovery reached transport before its audit attempt was durable")
	}
	if snapshot := gate.Snapshot(); snapshot.ActiveActions != 1 ||
		snapshot.ActiveEgress != 1 {
		t.Fatalf("discovery leases released before response terminal: %+v", snapshot)
	}
	body, err := io.ReadAll(completed.response.Body)
	if err != nil || string(body) != `{"data":[{"id":"spark-model"}]}` {
		t.Fatalf("ReadAll() body=%q error=%v", body, err)
	}
	waitForGateState(t, gate, offlinehold.StateOnline)
	if snapshot := gate.Snapshot(); snapshot.ActiveActions != 0 ||
		snapshot.ActiveEgress != 0 {
		t.Fatalf("discovery leases remain after response terminal: %+v", snapshot)
	}
	started, terminal := audit.attempts()
	if started.Purpose() != egressaudit.PurposeUpstreamModelDiscovery ||
		started.PayloadClass() != egressaudit.PayloadRuntime ||
		started.Parent().Kind != egressaudit.ParentRuntimeAction ||
		started.ConnectionID() != "" || started.Parent().ExchangeID != "" {
		t.Fatalf("discovery audit start = %+v", started)
	}
	if !terminal.Terminal() || terminal.Outcome() != egressaudit.OutcomeCompleted ||
		terminal.BytesOut() != 0 || terminal.BytesIn() != int64(len(body)) {
		t.Fatalf("discovery audit terminal = %+v", terminal)
	}
}

func TestFetchModelsDevUsesTheFixedMetadataOriginAndRuntimePurpose(t *testing.T) {
	t.Parallel()

	gate := newStartedGate(t)
	audit := &runtimeAuditRecorder{}
	transport := &runtimeTransportStub{
		audit: audit,
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"anthropic":{}}`)),
		},
	}
	client := newRuntimeFetchClient(t, gate, transport, audit)

	response, err := client.FetchModelsDev(context.Background())
	if err != nil {
		t.Fatalf("FetchModelsDev() error = %v", err)
	}
	request := transport.lastRequest()
	if request.Method != http.MethodGet ||
		request.URL.String() != "https://models.dev/models.json" ||
		request.Host != "models.dev" {
		t.Fatalf(
			"metadata request method=%q URL=%q Host=%q",
			request.Method,
			request.URL,
			request.Host,
		)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	started, terminal := audit.attempts()
	if started.Purpose() != egressaudit.PurposeModelMetadataDirectory ||
		started.Parent().Kind != egressaudit.ParentRuntimeAction ||
		terminal.Outcome() != egressaudit.OutcomeCompleted {
		t.Fatalf("metadata audit start=%+v terminal=%+v", started, terminal)
	}
}

type runtimeFetchResult struct {
	response *http.Response
	err      error
}

type sequentialInstanceIDs struct {
	mu   sync.Mutex
	next int
}

func (source *sequentialInstanceIDs) NewInstanceID(context.Context) (string, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.next++
	return []string{
		"runtime-action",
		"runtime-request",
		"runtime-attempt",
	}[source.next-1], nil
}

type runtimeTransportStub struct {
	mu       sync.Mutex
	audit    *runtimeAuditRecorder
	response *http.Response
	request  *http.Request
	calls    int
	audited  bool
}

func (transport *runtimeTransportStub) RoundTrip(
	request *http.Request,
	_ TransportDispatch,
) (*http.Response, transportprofile.Evidence, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.calls++
	transport.audited = transport.audit.appendCount() == 1
	transport.request = request.Clone(context.Background())
	transport.request.Header = request.Header.Clone()
	transport.request.Host = request.Host
	return transport.response, transportprofile.Evidence{}, nil
}

func (transport *runtimeTransportStub) callCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.calls
}

func (transport *runtimeTransportStub) auditWasPresent() bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.audited
}

func (transport *runtimeTransportStub) lastRequest() *http.Request {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.request.Clone(context.Background())
}

type runtimeAuditRecorder struct {
	mu       sync.Mutex
	started  egressaudit.Attempt
	terminal egressaudit.Attempt
}

func (recorder *runtimeAuditRecorder) Append(
	_ context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.started = attempt
	return egressaudit.Record{}, nil
}

func (recorder *runtimeAuditRecorder) Complete(
	_ context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.terminal = attempt
	return egressaudit.Record{}, nil
}

func (recorder *runtimeAuditRecorder) appendCount() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.started.ID() == "" {
		return 0
	}
	return 1
}

func (recorder *runtimeAuditRecorder) attempts() (
	egressaudit.Attempt,
	egressaudit.Attempt,
) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.started, recorder.terminal
}

func newRuntimeFetchClient(
	t *testing.T,
	gate *offlinehold.Gate,
	transport Transport,
	audit egressaudit.Writer,
) *Client {
	t.Helper()
	authenticator, err := NewStaticBearerAuthenticator(
		&secretReaderStub{value: []byte("catalog-test-token")},
	)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
		Audit:         audit,
		InstanceIDs:   &sequentialInstanceIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownClient(t, client) })
	return client
}

type runtimeDiscoveryCredential struct {
	secret  secretstore.Reference
	account providerauth.AccountRef
}

func (credential *runtimeDiscoveryCredential) Mode() providerauth.CredentialMode {
	return providerauth.CredentialManaged
}

func (credential *runtimeDiscoveryCredential) Driver() providerauth.DriverRef {
	return providerauth.StaticHeaderDriverRef()
}

func (credential *runtimeDiscoveryCredential) Secret() secretstore.Reference {
	return credential.secret
}

func (credential *runtimeDiscoveryCredential) Account() (providerauth.AccountRef, bool) {
	return credential.account, true
}

func (*runtimeDiscoveryCredential) Release() {}

func testRuntimeDiscoveryCredential(
	t *testing.T,
	realmID string,
) providerauth.Lease {
	t.Helper()
	reference, err := secretstore.ParseReference("secret://provider/catalog-test")
	if err != nil {
		t.Fatal(err)
	}
	return &runtimeDiscoveryCredential{
		secret: reference,
		account: providerauth.AccountRef{
			ID:              "account.catalog-test",
			Revision:        2,
			CredentialEpoch: 3,
			RealmID:         realmID,
		},
	}
}

func testDiscoveryEndpoint(t *testing.T, rawOrigin string) upstreamendpoint.Endpoint {
	t.Helper()
	origin, err := originidentity.ParseProviderOrigin(rawOrigin)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	endpoint := upstreamendpoint.Endpoint{
		ID:               "target.spark",
		DisplayName:      "DGX Spark",
		Origin:           origin,
		RealmID:          "spark.local",
		BackendProtocols: []string{"openai_responses"},
		Capabilities: []protocolspec.ProviderCapability{
			protocolspec.ProviderCapabilityMessages,
		},
		Drivers:   []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()},
		State:     upstreamendpoint.StateActive,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := endpoint.Validate(); err != nil {
		t.Fatal(err)
	}
	return endpoint
}
