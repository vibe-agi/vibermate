package productruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

var errAcquireNotExpected = errors.New("egress acquisition is not expected in M0 tests")

func TestProductionAccessCompilerFreezesExactResponsesOperation(t *testing.T) {
	t.Parallel()

	compiler, err := productionAccessPlanCompiler()
	if err != nil {
		t.Fatal(err)
	}
	accessID, err := access.NewAccessID("access-runtime-responses")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := runtimeAccessAggregate(t, accessID, 1, "Responses")
	clientOrigin, err := access.NewClientOrigin("https://api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.AgentEndpoint.ClientOrigin = clientOrigin
	aggregate.AgentEndpoint.ClientDialect = access.DialectOpenAIResponses
	plan, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatalf("compile Responses Access: %v", err)
	}
	codec := plan.CodecPlan()
	operations := codec.ClientOperations()
	if codec.ID().String() != "openai-responses-to-openai-chat" ||
		codec.ClientDialect() != access.DialectOpenAIResponses ||
		codec.ProviderDialect() != access.DialectOpenAIChat ||
		len(operations) != 1 ||
		operations[0].ID().String() != "openai-responses-create" ||
		operations[0].PathPattern() != "/v1/responses" {
		t.Fatalf("production Responses codec plan = %+v", codec)
	}
}

func TestProductRuntimeStartsAndShutsDownNormally(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{}
	options := testOptions(t, hostcontract.Desktop(), coordinator)

	runtime := startTestRuntime(t, options)
	status := runtime.Status()
	if status.State != RuntimeStateInitialized {
		t.Fatalf("runtime is not initialized: %+v", status)
	}
	if status.InstanceID == "" {
		t.Fatal("runtime instance ID is empty")
	}
	if status.Host != hostcontract.KindDesktop {
		t.Fatalf("runtime host = %q, want desktop", status.Host)
	}
	if status.SchemaRevision != 13 {
		t.Fatalf("schema revision = %d, want 13", status.SchemaRevision)
	}
	if status.AccessProjection.State != access.ProjectionStateHealthy ||
		status.AccessProjection.UnavailableAccessCount != 0 {
		t.Fatalf("initial Access projection health = %+v", status.AccessProjection)
	}
	if runtime.AccessProjectionHealth() != status.AccessProjection {
		t.Fatalf(
			"runtime Access projection health=%+v status=%+v",
			runtime.AccessProjectionHealth(),
			status.AccessProjection,
		)
	}
	if coordinator.boundInstanceID() != status.InstanceID {
		t.Fatalf(
			"coordinator instance ID = %q, runtime instance ID = %q",
			coordinator.boundInstanceID(),
			status.InstanceID,
		)
	}
	wire, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal runtime status: %v", err)
	}
	var wireStatus map[string]json.RawMessage
	if err := json.Unmarshal(wire, &wireStatus); err != nil {
		t.Fatalf("unmarshal runtime status: %v", err)
	}
	if _, exists := wireStatus["instanceId"]; !exists {
		t.Fatalf("runtime status does not use instanceId: %s", wire)
	}
	if _, exists := wireStatus["ready"]; exists {
		t.Fatalf("foundation status exposes product readiness: %s", wire)
	}
	if _, exists := wireStatus["accessProjection"]; !exists {
		t.Fatalf("runtime status omits Access projection health: %s", wire)
	}
	if _, exists := wireStatus["offlineHold"]; !exists {
		t.Fatalf("runtime status omits offline-hold state: %s", wire)
	}
	if status.OfflineHold.State != offlinehold.StateOnline {
		t.Fatalf("runtime offline-hold state = %+v", status.OfflineHold)
	}

	schemaState, err := runtime.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read runtime schema state: %v", err)
	}
	if schemaState.Revision != status.SchemaRevision {
		t.Fatalf(
			"schema state revision = %d, status revision = %d",
			schemaState.Revision,
			status.SchemaRevision,
		)
	}

	shutdownRuntime(t, runtime)
	stopped := runtime.Status()
	if stopped.State != RuntimeStateStopped {
		t.Fatalf("runtime did not stop: %+v", stopped)
	}
	if stopped.StoppedAt == nil {
		t.Fatal("runtime stopped timestamp is missing")
	}
	if stopped.StopReasonCode != "" {
		t.Fatalf("successful shutdown has a stop reason: %+v", stopped)
	}
	if _, err := runtime.SchemaStateReader().ReadSchemaState(context.Background()); !errors.Is(
		err,
		runtimepersistence.ErrStoreClosing,
	) {
		t.Fatalf("stopped runtime accepted a new schema read: %v", err)
	}
	if coordinator.beginShutdownCount() != 1 || coordinator.drainCount() != 1 {
		t.Fatalf(
			"offline shutdown counts = begin:%d drain:%d",
			coordinator.beginShutdownCount(),
			coordinator.drainCount(),
		)
	}
}

func TestProductRuntimeWiresAccessCASAndRestoresItAcrossRestart(t *testing.T) {
	t.Parallel()

	dataDirectory := filepath.Join(t.TempDir(), "runtime-data")
	paths, err := NewRuntimePaths(dataDirectory)
	if err != nil {
		t.Fatalf("create runtime paths: %v", err)
	}
	accessID, err := access.NewAccessID("access-runtime")
	if err != nil {
		t.Fatalf("create Access ID: %v", err)
	}

	first := startTestRuntime(t, testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	if _, err := first.SnapshotResolver().ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("empty runtime resolved an Access: %v", err)
	}
	write, err := first.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				1,
				"Runtime Access",
			),
		},
	)
	if err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write runtime Access result=%+v err=%v", write, err)
	}
	firstRootIdentity := first.LocalRootIdentity()
	firstRootCertificate := first.LocalRootCertificate().CertificatePEM()
	shutdownRuntime(t, first)
	if _, err := first.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 1,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				2,
				"Rejected after shutdown",
			),
		},
	); !errors.Is(err, access.ErrAccessRuntimeStopping) {
		t.Fatalf("stopped runtime accepted an Access write: %v", err)
	}

	second := startTestRuntime(t, testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	defer shutdownRuntime(t, second)
	if second.LocalRootIdentity() != firstRootIdentity ||
		!bytes.Equal(
			second.LocalRootCertificate().CertificatePEM(),
			firstRootCertificate,
		) {
		t.Fatal("ProductRuntime reopen changed local Root identity or certificate")
	}
	recovered, err := second.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve recovered runtime Access: %v", err)
	}
	if recovered.Revision() != 1 ||
		recovered.Binding().Name != "Runtime Access" {
		t.Fatalf("recovered runtime Access: revision=%d binding=%+v",
			recovered.Revision(), recovered.Binding())
	}
	if second.Status().State != RuntimeStateInitialized {
		t.Fatalf("runtime with recovered Access is not initialized: %+v", second.Status())
	}
}

func TestProductRuntimeWiresExchangePipelineToActiveAccessPlan(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-exchange-runtime")
	if err != nil {
		t.Fatal(err)
	}
	provider := &pipelineProviderRuntime{
		responseBody: []byte(`{
			"id":"chatcmpl-runtime",
			"object":"chat.completion",
			"created":1,
			"model":"gpt-4.1-mini",
			"choices":[{
				"index":0,
				"message":{"role":"assistant","content":"Runtime path.","refusal":null},
				"finish_reason":"stop",
				"logprobs":null
			}],
			"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}
		}`),
	}
	options := testOptions(
		t,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	builders := productionBuilders()
	builders.provider = fixedProviderBuilder{component: provider}
	runtime, err := startWithBuilders(context.Background(), options, builders)
	if err != nil {
		t.Fatalf("start ProductRuntime with provider fixture: %v", err)
	}
	defer shutdownRuntime(t, runtime)

	write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				1,
				"Exchange Runtime Access",
			),
		},
	)
	if err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve active Access plan: %v", err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-runtime-wiring",
		activePlan.IngressBinding(),
		runtimeAnthropicOperationEvidence(t),
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":32,
			"messages":[{"role":"user","content":"hello"}]
		}`),
		exchange.ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	downstream := &runtimeDownstream{}
	result, err := runtime.ExchangeExecutor().Execute(
		context.Background(),
		request,
		downstream,
	)
	if err != nil {
		t.Fatalf("execute ProductRuntime Exchange: %v", err)
	}
	if result.AccessRevision != 1 ||
		result.Outcome != exchange.AttemptSucceeded ||
		!result.Ledger.DownstreamTerminal {
		t.Fatalf("Exchange result = %+v", result)
	}
	providerRequest := provider.requestSnapshot()
	var providerWire struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(providerRequest.Body(), &providerWire); err != nil {
		t.Fatal(err)
	}
	if providerWire.Model != "gpt-4.1-mini" ||
		providerRequest.RelativePath() != "chat/completions" {
		t.Fatalf(
			"provider request model=%q path=%q",
			providerWire.Model,
			providerRequest.RelativePath(),
		)
	}
	if downstream.mode != exchange.ResponseModeJSON ||
		!bytes.Contains(downstream.body.Bytes(), []byte("Runtime path.")) {
		t.Fatalf(
			"downstream mode=%q body=%s",
			downstream.mode,
			downstream.body.Bytes(),
		)
	}
	records, err := runtime.Activities().List(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatalf("list runtime Activity: %v", err)
	}
	if len(records.Items) != 1 ||
		records.Items[0].Kind != activity.KindExchangeCompleted ||
		records.Items[0].SubjectID != "exchange-runtime-wiring" ||
		records.Items[0].Status != activity.StatusSucceeded {
		t.Fatalf("runtime Activity = %+v", records.Items)
	}
}

func TestProductRuntimeWiresResponsesThroughTheSameExchangeAndProvider(
	t *testing.T,
) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-responses-runtime")
	if err != nil {
		t.Fatal(err)
	}
	provider := &pipelineProviderRuntime{
		responseBody: []byte(`{
			"id":"chatcmpl-responses-runtime",
			"object":"chat.completion",
			"created":1,
			"model":"gpt-4.1-mini",
			"choices":[{
				"index":0,
				"message":{"role":"assistant","content":"Responses runtime path.","refusal":null},
				"finish_reason":"stop",
				"logprobs":null
			}],
			"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}
		}`),
	}
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	builders := productionBuilders()
	builders.provider = fixedProviderBuilder{component: provider}
	runtime, err := startWithBuilders(context.Background(), options, builders)
	if err != nil {
		t.Fatalf("start ProductRuntime with provider fixture: %v", err)
	}
	defer shutdownRuntime(t, runtime)

	aggregate := runtimeAccessAggregate(
		t,
		accessID,
		1,
		"Responses Runtime Access",
	)
	clientOrigin, err := access.NewClientOrigin("https://api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.AgentEndpoint.ClientOrigin = clientOrigin
	aggregate.AgentEndpoint.ClientDialect = access.DialectOpenAIResponses
	write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate:        aggregate,
		},
	)
	if err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Responses Access result=%+v err=%v", write, err)
	}
	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve Responses Access: %v", err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-responses-runtime-wiring",
		activePlan.IngressBinding(),
		runtimeResponsesOperationEvidence(t),
		[]byte(`{
			"model":"codex-client-alias",
			"input":[{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"hello"}]
			}],
			"store":false,
			"stream":false
		}`),
		exchange.ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	downstream := &runtimeDownstream{}
	result, err := runtime.ExchangeExecutor().Execute(
		context.Background(),
		request,
		downstream,
	)
	if err != nil {
		t.Fatalf("execute Responses Exchange: %v", err)
	}
	if result.AccessRevision != 1 ||
		result.PlanHash != activePlan.PlanHash().String() ||
		result.Outcome != exchange.AttemptSucceeded ||
		!result.Ledger.DownstreamTerminal {
		t.Fatalf("Responses Exchange result = %+v", result)
	}
	var providerWire struct {
		Model string `json:"model"`
	}
	providerRequest := provider.requestSnapshot()
	if err := json.Unmarshal(providerRequest.Body(), &providerWire); err != nil {
		t.Fatal(err)
	}
	var clientWire struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(downstream.body.Bytes(), &clientWire); err != nil {
		t.Fatal(err)
	}
	if providerWire.Model != "gpt-4.1-mini" ||
		providerRequest.RelativePath() != "chat/completions" ||
		downstream.mode != exchange.ResponseModeJSON ||
		clientWire.Object != "response" ||
		clientWire.Status != "completed" ||
		len(clientWire.Output) != 1 ||
		len(clientWire.Output[0].Content) != 1 ||
		clientWire.Output[0].Content[0].Text != "Responses runtime path." {
		t.Fatalf(
			"provider model=%q path=%q downstream=%s",
			providerWire.Model,
			providerRequest.RelativePath(),
			downstream.body.Bytes(),
		)
	}
}

func TestProductRuntimeComposesCaptureIngressAndConnectionAudit(t *testing.T) {
	t.Parallel()

	runtime := startTestRuntime(
		t,
		testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}),
	)
	defer shutdownRuntime(t, runtime)

	accessID, err := access.NewAccessID("access-proxy-runtime")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				1,
				"Proxy Runtime Access",
			),
		},
	)
	if err != nil || result.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v error=%v", result, err)
	}
	authorities, err := runtime.ActiveClientAuthorities()
	if err != nil ||
		!reflect.DeepEqual(authorities, []string{"api.anthropic.com:443"}) {
		t.Fatalf("active client authorities=%v error=%v", authorities, err)
	}

	grant, err := runtime.CaptureRuns().Create(
		context.Background(),
		capturerun.CreateCommand{
			CWD:             t.TempDir(),
			ExecutablePath:  "/usr/bin/true",
			Lifetime:        time.Minute,
			CatalogRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create CaptureRun: %v", err)
	}
	request := httptest.NewRequest(http.MethodConnect, "http://127.0.0.1", nil)
	request.Host = "unregistered.example.test:443"
	request.Header.Set(
		"Proxy-Authorization",
		"Basic "+base64.StdEncoding.EncodeToString(
			[]byte("capture:"+grant.ProxyCapability.Value()),
		),
	)
	recorder := httptest.NewRecorder()
	runtime.ProxyHandler().ServeHTTP(recorder, request)
	// An unregistered authority is forwarded blind rather than refused, so it
	// reaches the gated dialer. The host does not resolve, so the dial fails;
	// what this test guards is that the composition wired that path at all and
	// that it left connection evidence.
	if recorder.Code != http.StatusBadGateway ||
		!bytes.Contains(
			recorder.Body.Bytes(),
			[]byte(`"reasonCode":"blind_tunnel_failed"`),
		) {
		t.Fatalf(
			"proxy rejection status=%d body=%s",
			recorder.Code,
			recorder.Body.Bytes(),
		)
	}
	page, err := runtime.ConnectionEvents().List(
		context.Background(),
		connectionevent.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatalf("list ConnectionEvents: %v", err)
	}
	if len(page.Items) < 2 ||
		page.Items[len(page.Items)-1].Phase !=
			connectionevent.PhaseAttempted {
		t.Fatalf("runtime proxy ConnectionEvents = %+v", page.Items)
	}
	allowedBlind := false
	for _, record := range page.Items {
		if record.Decision == connectionevent.DecisionAllow &&
			record.Decryption == connectionevent.DecryptionBlind {
			allowedBlind = true
		}
	}
	if !allowedBlind {
		t.Fatalf("no blind decision was recorded: %+v", page.Items)
	}
	identity := runtime.LocalRootIdentity()
	certificate := runtime.LocalRootCertificate()
	if !identity.Valid() || !certificate.Valid() ||
		len(certificate.CertificatePEM()) == 0 {
		t.Fatalf(
			"runtime local Root evidence is incomplete: identity=%+v certificate=%+v",
			identity,
			certificate,
		)
	}
}

func TestProductRuntimeStatusDegradesWhenAccessProjectionIsUnavailable(t *testing.T) {
	t.Parallel()

	options := testOptions(
		t,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	builders := productionBuilders()
	builders.access = failingPublicationAccessBuilder{
		failRevision: 2,
	}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if err != nil {
		t.Fatalf("start runtime with publication failure fixture: %v", err)
	}
	defer shutdownRuntime(t, runtime)

	accessID, err := access.NewAccessID("access-runtime-health")
	if err != nil {
		t.Fatalf("construct Access ID: %v", err)
	}
	if _, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				1,
				"Revision one",
			),
		},
	); err != nil {
		t.Fatalf("create Access before publication failure: %v", err)
	}
	result, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 1,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				2,
				"Revision two",
			),
		},
	)
	if result.Outcome != access.WriteOutcomeCommitted ||
		!errors.Is(err, access.ErrProjectionUnavailable) {
		t.Fatalf("runtime publication failure result=%+v err=%v", result, err)
	}

	health := runtime.AccessProjectionHealth()
	if health.State != access.ProjectionStateUnavailable ||
		health.UnavailableAccessCount != 1 {
		t.Fatalf("runtime Access projection health = %+v", health)
	}
	status := runtime.Status()
	if status.State != RuntimeStateDegraded ||
		status.AccessProjection != health {
		t.Fatalf("runtime did not observe Access projection health: %+v", status)
	}
}

func TestProductRuntimeShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{}
	runtime := startTestRuntime(
		t,
		testOptions(t, hostcontract.Desktop(), coordinator),
	)

	const callers = 12
	results := make(chan error, callers)
	var callersWaitGroup sync.WaitGroup
	for range callers {
		callersWaitGroup.Add(1)
		go func() {
			defer callersWaitGroup.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			results <- runtime.Shutdown(ctx)
		}()
	}
	callersWaitGroup.Wait()
	close(results)
	for result := range results {
		if result != nil {
			t.Fatalf("concurrent shutdown returned an error: %v", result)
		}
	}
	if coordinator.beginShutdownCount() != 1 || coordinator.drainCount() != 1 {
		t.Fatalf(
			"offline cleanup ran more than once: begin=%d drain=%d",
			coordinator.beginShutdownCount(),
			coordinator.drainCount(),
		)
	}
}

func TestProductRuntimeAccessRecoveryFailureRollsBackSQLite(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("Access recovery failed")
	var events eventLog
	options := testOptions(
		t,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	builders := productionBuilders()
	builders.storage = tracingStorageBuilder{
		delegate: builders.storage,
		events:   &events,
	}
	builders.access = failingAccessBuilder{err: startupCause}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed Access recovery returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("Access recovery cause was not preserved: %v", err)
	}
	if got, want := events.snapshot(), []string{"sqlite.shutdown"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Access recovery rollback order = %v, want %v", got, want)
	}

	reopened, openErr := runtimepersistence.Open(
		context.Background(),
		runtimepersistence.Options{
			DatabasePath:           options.Paths.DatabasePath(),
			BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
			CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
		},
	)
	if openErr != nil {
		t.Fatalf("reopen SQLite after Access recovery rollback: %v", openErr)
	}
	if closeErr := reopened.Shutdown(context.Background()); closeErr != nil {
		t.Fatalf("close reopened SQLite store: %v", closeErr)
	}
}

func TestProductRuntimeCreatesDistinctIncarnationsAcrossStarts(t *testing.T) {
	t.Parallel()

	dataDirectory := filepath.Join(t.TempDir(), "runtime-data")
	paths, err := NewRuntimePaths(dataDirectory)
	if err != nil {
		t.Fatalf("create runtime paths: %v", err)
	}

	firstOptions := testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	first := startTestRuntime(t, firstOptions)
	firstStatus := first.Status()
	firstState, err := first.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read first schema state: %v", err)
	}
	shutdownRuntime(t, first)

	secondOptions := testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	second := startTestRuntime(t, secondOptions)
	secondStatus := second.Status()
	secondState, err := second.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read second schema state: %v", err)
	}
	shutdownRuntime(t, second)

	if firstStatus.InstanceID == secondStatus.InstanceID {
		t.Fatalf("runtime incarnation was reused: %q", firstStatus.InstanceID)
	}
	if firstState != secondState {
		t.Fatalf(
			"schema state did not remain continuous: first=%+v second=%+v",
			firstState,
			secondState,
		)
	}
}

func TestProductRuntimeCurrentSliceAcceptsBothHostContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host hostcontract.Contract
		kind hostcontract.Kind
	}{
		{name: "Desktop", host: hostcontract.Desktop(), kind: hostcontract.KindDesktop},
		{name: "Server", host: hostcontract.Server(), kind: hostcontract.KindServer},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime := startTestRuntime(
				t,
				testOptions(t, test.host, &coordinatorDouble{}),
			)
			defer shutdownRuntime(t, runtime)
			if got := runtime.Status().Host; got != test.kind {
				t.Fatalf("runtime host = %q, want %q", got, test.kind)
			}
		})
	}
}

func TestProductRuntimeRequiresOfflineHoldCoordinatorBeforeOpeningResources(t *testing.T) {
	t.Parallel()

	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	options.OfflineHold = nil

	runtime, err := Start(context.Background(), options)
	if runtime != nil {
		t.Fatal("invalid options returned a runtime")
	}
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("expected ErrInvalidOptions, got %v", err)
	}
	if _, statErr := os.Stat(options.Paths.DatabasePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid options created a database: %v", statErr)
	}
}

func TestProductRuntimeMiddleStageFailureRollsBackInReverseOrder(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("offline binding failed")
	var events eventLog
	coordinator := &coordinatorDouble{startErr: startupCause}
	options := testOptions(t, hostcontract.Desktop(), coordinator)

	builders := productionBuilders()
	builders.storage = tracingStorageBuilder{
		delegate: builders.storage,
		events:   &events,
	}
	builders.access = tracingAccessBuilder{
		delegate: builders.access,
		events:   &events,
	}
	builders.monitor = fixedMonitorBuilder{
		component: &ownedComponentDouble{
			events: &events,
			event:  "monitor.shutdown",
		},
	}
	builders.provider = fixedProviderBuilder{
		component: &egressRuntimeDouble{
			events: &events,
			event:  "provider.shutdown",
		},
	}
	builders.exchange = tracingExchangeBuilder{
		delegate: builders.exchange,
		events:   &events,
	}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed start returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("startup cause was not preserved: %v", err)
	}
	if got, want := events.snapshot(), []string{
		"exchange.begin-shutdown",
		"exchange.drain",
		"provider.shutdown",
		"monitor.shutdown",
		"access.shutdown",
		"sqlite.shutdown",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback order = %v, want %v", got, want)
	}
	if coordinator.beginShutdownCount() != 0 || coordinator.drainCount() != 0 {
		t.Fatal("cleanup was registered for the failed offline-hold stage")
	}

	reopened, openErr := runtimepersistence.Open(context.Background(), runtimepersistence.Options{
		DatabasePath:           options.Paths.DatabasePath(),
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if openErr != nil {
		t.Fatalf("reopen rolled-back SQLite store: %v", openErr)
	}
	if closeErr := reopened.Shutdown(context.Background()); closeErr != nil {
		t.Fatalf("close reopened SQLite store: %v", closeErr)
	}
}

func TestProductRuntimeRollbackErrorDoesNotOverrideStartupCause(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("foundation state verification failed")
	rollbackCause := errors.New("monitor drain failed")
	var events eventLog
	coordinator := &coordinatorDouble{events: &events}
	options := testOptions(t, hostcontract.Desktop(), coordinator)

	builders := productionBuilders()
	builders.storage = tracingStorageBuilder{
		delegate: builders.storage,
		events:   &events,
		readErr:  startupCause,
	}
	builders.monitor = fixedMonitorBuilder{
		component: &ownedComponentDouble{
			events: &events,
			event:  "monitor.shutdown",
			err:    rollbackCause,
		},
	}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed start returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("startup cause was not preserved: %v", err)
	}
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("rollback cause was not joined: %v", err)
	}
	wantEvents := []string{
		"offline.begin-shutdown",
		"offline.drain",
		"monitor.shutdown",
		"sqlite.shutdown",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("rollback order = %v, want %v", got, wantEvents)
	}
}

func TestProductRuntimeShutdownClosesIngressAndExchangeBeforeTransportDrain(
	t *testing.T,
) {
	t.Parallel()

	var events eventLog
	coordinator := &coordinatorDouble{events: &events}
	builders := productionBuilders()
	builders.provider = fixedProviderBuilder{
		component: &egressRuntimeDouble{
			events: &events,
			event:  "provider.shutdown",
		},
	}
	builders.original = fixedOriginalBuilder{
		component: &originalRuntimeDouble{
			events: &events,
			event:  "original.shutdown",
		},
	}
	builders.exchange = tracingExchangeBuilder{
		delegate: builders.exchange,
		events:   &events,
	}
	builders.capture = tracingCaptureBuilder{
		delegate: builders.capture,
		events:   &events,
	}
	builders.proxy = tracingProxyBuilder{
		delegate: builders.proxy,
		events:   &events,
	}
	runtime, err := startWithBuilders(
		context.Background(),
		testOptions(t, hostcontract.Desktop(), coordinator),
		builders,
	)
	if err != nil {
		t.Fatalf("start ProductRuntime: %v", err)
	}
	shutdownRuntime(t, runtime)

	want := []string{
		"proxy.begin-shutdown",
		"capture.begin-shutdown",
		"offline.begin-shutdown",
		"exchange.begin-shutdown",
		"capture.shutdown",
		"original.shutdown",
		"provider.shutdown",
		"proxy.drain",
		"exchange.drain",
		"offline.drain",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Exchange shutdown order = %v, want %v", got, want)
	}
}

func TestProductRuntimeProviderClosureUnblocksExchangeDrain(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-blocked-provider-body")
	if err != nil {
		t.Fatal(err)
	}
	var events eventLog
	body := newBlockingResponseBody()
	provider := &blockingBodyProviderRuntime{
		body:   body,
		events: &events,
	}
	coordinator := &coordinatorDouble{events: &events}
	options := testOptions(t, hostcontract.Desktop(), coordinator)
	builders := productionBuilders()
	builders.provider = fixedProviderBuilder{component: provider}
	builders.exchange = tracingExchangeBuilder{
		delegate: builders.exchange,
		events:   &events,
	}
	runtime, err := startWithBuilders(context.Background(), options, builders)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				1,
				"Blocked Provider Body",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve active Access plan: %v", err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-blocked-provider-body",
		activePlan.IngressBinding(),
		runtimeAnthropicOperationEvidence(t),
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":32,
			"stream":true,
			"messages":[{"role":"user","content":"hello"}]
		}`),
		exchange.ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	executeResult := make(chan error, 1)
	go func() {
		_, executeErr := runtime.ExchangeExecutor().Execute(
			context.Background(),
			request,
			&runtimeDownstream{},
		)
		executeResult <- executeErr
	}()
	select {
	case <-body.readStarted:
	case <-time.After(time.Second):
		t.Fatal("provider response body read did not start")
	}

	shutdownContext, cancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancel()
	if err := runtime.Shutdown(shutdownContext); err != nil {
		t.Fatalf("shutdown ProductRuntime: %v", err)
	}
	select {
	case executeErr := <-executeResult:
		if exchange.ReasonOf(executeErr) != exchange.ReasonExchangeCanceled {
			t.Fatalf("Exchange error = %v", executeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Exchange did not drain after provider body closure")
	}
	if got, want := events.snapshot(), []string{
		"offline.begin-shutdown",
		"exchange.begin-shutdown",
		"provider.shutdown",
		"exchange.drain",
		"offline.drain",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Exchange shutdown events = %v, want %v", got, want)
	}
}

func runtimeAnthropicOperationEvidence(
	t *testing.T,
) exchange.ClientOperationEvidence {
	t.Helper()
	operationID, err := access.NewClientOperationID(
		operationcatalog.AnthropicMessagesCreateID,
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := exchange.NewClientOperationEvidence(
		operationID,
		1,
		http.MethodPost,
		"/v1/messages",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func runtimeResponsesOperationEvidence(
	t *testing.T,
) exchange.ClientOperationEvidence {
	t.Helper()
	operationID, err := access.NewClientOperationID(
		operationcatalog.OpenAIResponsesCreateID,
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := exchange.NewClientOperationEvidence(
		operationID,
		1,
		http.MethodPost,
		"/v1/responses",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestProductRuntimeRejectsCorruptSQLiteOnRestart(t *testing.T) {
	t.Parallel()

	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatalf("create runtime paths: %v", err)
	}
	first := startTestRuntime(t, testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	shutdownRuntime(t, first)
	for _, artifact := range []string{
		paths.DatabasePath() + "-wal",
		paths.DatabasePath() + "-shm",
	} {
		if err := os.Remove(artifact); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove SQLite corruption fixture artifact: %v", err)
		}
	}
	if err := os.WriteFile(
		paths.DatabasePath(),
		bytes.Repeat([]byte{0xa5}, 4096),
		0o600,
	); err != nil {
		t.Fatalf("create corrupt SQLite fixture: %v", err)
	}

	second, err := Start(context.Background(), testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	if second != nil {
		t.Fatal("corrupt SQLite returned a runtime")
	}
	if err == nil {
		t.Fatal("corrupt SQLite startup returned no error")
	}
}

func TestProductRuntimeStartupRollbackUsesInternalDeadline(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("foundation state verification failed")
	coordinator := &coordinatorDouble{blockDrainUntilCancellation: true}
	options := testOptions(t, hostcontract.Desktop(), coordinator)
	options.Lifecycle.RollbackTimeout = 40 * time.Millisecond
	options.Lifecycle.HealthPollInterval = time.Hour
	failureObserved := make(chan time.Time, 1)
	builders := productionBuilders()
	builders.storage = tracingStorageBuilder{
		delegate: builders.storage,
		readErr:  startupCause,
		readObserved: func() {
			select {
			case failureObserved <- time.Now():
			default:
			}
		},
	}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed start returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("startup cause was not preserved: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rollback deadline was not reported: %v", err)
	}
	select {
	case started := <-failureObserved:
		if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
			t.Fatalf("bounded startup rollback took too long: %v", elapsed)
		}
	default:
		t.Fatal("startup failure observation is missing")
	}
}

func TestProductRuntimeShutdownDrainsOwnedGoroutineWithinDeadline(t *testing.T) {
	t.Parallel()

	options := testOptions(
		t,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	options.Lifecycle.HealthPollInterval = time.Millisecond
	runtime := startTestRuntime(t, options)
	monitor, ok := runtime.monitor.(*storageHealthMonitor)
	if !ok {
		t.Fatalf("runtime monitor has unexpected type %T", runtime.monitor)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown runtime: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("shutdown exceeded caller deadline: %v", elapsed)
	}
	select {
	case <-monitor.done:
	default:
		t.Fatal("shutdown returned before the owned monitor goroutine drained")
	}
}

func TestProductRuntimeAppliesInternalShutdownDeadline(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{blockDrainUntilCancellation: true}
	options := testOptions(
		t,
		hostcontract.Desktop(),
		coordinator,
	)
	options.Lifecycle.ShutdownTimeout = 40 * time.Millisecond
	runtime := startTestRuntime(t, options)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	err := runtime.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected internal shutdown deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("bounded shutdown took too long: %v", elapsed)
	}
	if runtime.Status().State != RuntimeStateStopFailed {
		t.Fatalf("runtime status after bounded shutdown: %+v", runtime.Status())
	}
	if runtime.Status().StopReasonCode != StopReasonShutdownFailed {
		t.Fatalf("runtime stop reason after bounded shutdown: %+v", runtime.Status())
	}
	if runtime.Status().StoppedAt != nil {
		t.Fatalf("failed shutdown reported a stopped timestamp: %+v", runtime.Status())
	}
}

func testOptions(
	t *testing.T,
	host hostcontract.Contract,
	coordinator offlinehold.RuntimeCoordinator,
) Options {
	t.Helper()
	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatalf("create runtime paths: %v", err)
	}
	return testOptionsWithPaths(t, paths, host, coordinator)
}

func testOptionsWithPaths(
	t *testing.T,
	paths RuntimePaths,
	host hostcontract.Contract,
	coordinator offlinehold.RuntimeCoordinator,
) Options {
	t.Helper()
	return Options{
		Paths:          paths,
		Host:           host,
		OfflineHold:    coordinator,
		Secrets:        unavailableSecretStore{},
		Approvals:      toolapproval.DefaultConfig(),
		ExchangeHold:   exchange.DefaultHoldPolicy(),
		Clock:          SystemClock{},
		InstanceIDs:    NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader,
		Lifecycle: LifecycleOptions{
			RollbackTimeout:    time.Second,
			ShutdownTimeout:    time.Second,
			HealthPollInterval: 10 * time.Millisecond,
		},
	}
}

func startTestRuntime(t *testing.T, options Options) *Runtime {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime, err := Start(ctx, options)
	if err != nil {
		t.Fatalf("start ProductRuntime: %v", err)
	}
	return runtime
}

func shutdownRuntime(t *testing.T, runtime *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown ProductRuntime: %v", err)
	}
}

type coordinatorDouble struct {
	mu                          sync.Mutex
	instanceID                  string
	startErr                    error
	drainErr                    error
	beginCount                  int
	drains                      int
	events                      *eventLog
	blockDrainUntilCancellation bool
	state                       offlinehold.State
	revision                    uint64
}

func (c *coordinatorDouble) Start(_ context.Context, binding offlinehold.RuntimeBinding) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instanceID = binding.InstanceID
	c.state = offlinehold.StateOnline
	c.revision++
	return c.startErr
}

func (c *coordinatorDouble) Acquire(
	context.Context,
	offlinehold.AcquireRequest,
) (offlinehold.Lease, error) {
	return nil, errAcquireNotExpected
}

func (c *coordinatorDouble) BeginAction(
	context.Context,
	offlinehold.ActionRequest,
) (*offlinehold.ActionLease, error) {
	return &offlinehold.ActionLease{}, nil
}

func (c *coordinatorDouble) BeginShutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginCount++
	c.state = offlinehold.StateStopping
	c.revision++
	if c.events != nil {
		c.events.add("offline.begin-shutdown")
	}
}

func (c *coordinatorDouble) Snapshot() offlinehold.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return offlinehold.Snapshot{
		State:        c.state,
		Revision:     c.revision,
		ActiveByKind: map[offlinehold.EgressKind]int{},
		QueuedByKind: map[offlinehold.EgressKind]int{},
	}
}

func (c *coordinatorDouble) PendingProbeTargets() []offlinehold.ProbeTarget {
	return nil
}

func (c *coordinatorDouble) Enter(
	context.Context,
	uint64,
) (offlinehold.Snapshot, error) {
	return c.Snapshot(), errors.New("offline control is not expected in this test")
}

func (c *coordinatorDouble) Resume(
	context.Context,
	uint64,
	offlinehold.ResumeRequest,
	offlinehold.Prober,
) (offlinehold.Snapshot, error) {
	return c.Snapshot(), errors.New("offline control is not expected in this test")
}

func (c *coordinatorDouble) Drain(ctx context.Context) error {
	c.mu.Lock()
	c.drains++
	block := c.blockDrainUntilCancellation
	drainErr := c.drainErr
	if c.events != nil {
		c.events.add("offline.drain")
	}
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return drainErr
}

func (c *coordinatorDouble) boundInstanceID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instanceID
}

func (c *coordinatorDouble) beginShutdownCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.beginCount
}

func (c *coordinatorDouble) drainCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.drains
}

type unavailableSecretStore struct{}

func (unavailableSecretStore) Read(
	context.Context,
	secretstore.Reference,
) (*secretstore.Value, error) {
	return nil, secretstore.ErrNotFound
}

func (unavailableSecretStore) Inspect(
	context.Context,
	secretstore.Reference,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{State: secretstore.StateMissing}, nil
}

func (unavailableSecretStore) Replace(
	context.Context,
	secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrReadOnly
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type fixedMonitorBuilder struct {
	component ownedComponent
	err       error
}

func (b fixedMonitorBuilder) Build(monitorBuildRequest) (ownedComponent, error) {
	return b.component, b.err
}

type egressRuntimeDouble struct {
	events *eventLog
	event  string
}

func (*egressRuntimeDouble) Do(
	context.Context,
	providertransport.Request,
) (*http.Response, providertransport.Evidence, error) {
	return nil, providertransport.Evidence{},
		errors.New("provider request is not expected in this test")
}

func (runtime *egressRuntimeDouble) Shutdown(context.Context) error {
	runtime.events.add(runtime.event)
	return nil
}

type originalRuntimeDouble struct {
	events *eventLog
	event  string
}

func (*originalRuntimeDouble) Do(
	context.Context,
	originaltransport.Request,
) (*http.Response, error) {
	return nil, errors.New("original-origin request is not expected in this test")
}

func (runtime *originalRuntimeDouble) Shutdown(context.Context) error {
	runtime.events.add(runtime.event)
	return nil
}

type pipelineProviderRuntime struct {
	mu           sync.Mutex
	request      providertransport.Request
	responseBody []byte
	closed       bool
}

type blockingBodyProviderRuntime struct {
	body   *blockingResponseBody
	events *eventLog
}

func (runtime *blockingBodyProviderRuntime) Do(
	context.Context,
	providertransport.Request,
) (*http.Response, providertransport.Evidence, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: runtime.body,
	}, providertransport.Evidence{}, nil
}

func (runtime *blockingBodyProviderRuntime) Shutdown(context.Context) error {
	if runtime.events != nil {
		runtime.events.add("provider.shutdown")
	}
	return runtime.body.Close()
}

type blockingResponseBody struct {
	readStarted chan struct{}
	closed      chan struct{}
	startOnce   sync.Once
	closeOnce   sync.Once
}

func newBlockingResponseBody() *blockingResponseBody {
	return &blockingResponseBody{
		readStarted: make(chan struct{}),
		closed:      make(chan struct{}),
	}
}

func (body *blockingResponseBody) Read([]byte) (int, error) {
	body.startOnce.Do(func() { close(body.readStarted) })
	<-body.closed
	return 0, errors.New("provider response body was closed")
}

func (body *blockingResponseBody) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}

func (runtime *pipelineProviderRuntime) Do(
	_ context.Context,
	request providertransport.Request,
) (*http.Response, providertransport.Evidence, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return nil, providertransport.Evidence{},
			errors.New("pipeline provider is closed")
	}
	runtime.request = request
	return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(bytes.NewReader(runtime.responseBody)),
		}, providertransport.Evidence{
			Credential: providertransport.CredentialEvidence{
				DriverRef:  access.AuthDriverStaticHeaderValue,
				HeaderName: "authorization",
				SecretRead: true,
			},
		}, nil
}

func (runtime *pipelineProviderRuntime) Shutdown(context.Context) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.closed = true
	return nil
}

func (runtime *pipelineProviderRuntime) requestSnapshot() providertransport.Request {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.request
}

type runtimeDownstream struct {
	mode exchange.ResponseMode
	body bytes.Buffer
}

func (downstream *runtimeDownstream) Begin(
	_ context.Context,
	mode exchange.ResponseMode,
) error {
	downstream.mode = mode
	return nil
}

func (downstream *runtimeDownstream) Write(
	_ context.Context,
	body []byte,
) (int, error) {
	return downstream.body.Write(body)
}

func (*runtimeDownstream) Keepalive(context.Context) error {
	return nil
}

func (*runtimeDownstream) Abort(
	context.Context,
	exchange.FailureNotice,
) error {
	return errors.New("stream abort is not expected")
}

type fixedProviderBuilder struct {
	component providerRuntime
	err       error
}

func (builder fixedProviderBuilder) Build(
	providerBuildRequest,
) (providerRuntime, error) {
	return builder.component, builder.err
}

type fixedOriginalBuilder struct {
	component originalRuntime
	err       error
}

func (builder fixedOriginalBuilder) Build(
	originalBuildRequest,
) (originalRuntime, error) {
	return builder.component, builder.err
}

type tracingExchangeBuilder struct {
	delegate exchangeBuilder
	events   *eventLog
}

func (builder tracingExchangeBuilder) Build(
	request exchangeBuildRequest,
) (exchangeRuntime, error) {
	runtime, err := builder.delegate.Build(request)
	if err != nil {
		return nil, err
	}
	return &tracingExchangeRuntime{
		exchangeRuntime: runtime,
		events:          builder.events,
	}, nil
}

type tracingExchangeRuntime struct {
	exchangeRuntime
	events *eventLog
}

type tracingCaptureBuilder struct {
	delegate captureBuilder
	events   *eventLog
}

func (builder tracingCaptureBuilder) Build(
	ctx context.Context,
	request captureBuildRequest,
) (captureRuntime, error) {
	runtime, err := builder.delegate.Build(ctx, request)
	if err != nil {
		return nil, err
	}
	return &tracingCaptureRuntime{
		captureRuntime: runtime,
		events:         builder.events,
	}, nil
}

type tracingCaptureRuntime struct {
	captureRuntime
	events *eventLog
}

func (runtime *tracingCaptureRuntime) BeginShutdown() {
	runtime.events.add("capture.begin-shutdown")
	runtime.captureRuntime.BeginShutdown()
}

func (runtime *tracingCaptureRuntime) Shutdown(ctx context.Context) error {
	runtime.events.add("capture.shutdown")
	return runtime.captureRuntime.Shutdown(ctx)
}

type tracingProxyBuilder struct {
	delegate proxyBuilder
	events   *eventLog
}

func (builder tracingProxyBuilder) Build(
	request proxyBuildRequest,
) (proxyRuntime, error) {
	runtime, err := builder.delegate.Build(request)
	if err != nil {
		return nil, err
	}
	return &tracingProxyRuntime{
		proxyRuntime: runtime,
		events:       builder.events,
	}, nil
}

type tracingProxyRuntime struct {
	proxyRuntime
	events *eventLog
}

func (runtime *tracingProxyRuntime) BeginShutdown() {
	runtime.events.add("proxy.begin-shutdown")
	runtime.proxyRuntime.BeginShutdown()
}

func (runtime *tracingProxyRuntime) Drain(ctx context.Context) error {
	runtime.events.add("proxy.drain")
	return runtime.proxyRuntime.Drain(ctx)
}

func (runtime *tracingExchangeRuntime) BeginShutdown() {
	if runtime.events != nil {
		runtime.events.add("exchange.begin-shutdown")
	}
	runtime.exchangeRuntime.BeginShutdown()
}

func (runtime *tracingExchangeRuntime) Drain(ctx context.Context) error {
	if runtime.events != nil {
		runtime.events.add("exchange.drain")
	}
	return runtime.exchangeRuntime.Drain(ctx)
}

func (runtime *tracingExchangeRuntime) Shutdown(ctx context.Context) error {
	runtime.BeginShutdown()
	return runtime.Drain(ctx)
}

type failingAccessBuilder struct {
	err error
}

func (b failingAccessBuilder) Build(
	context.Context,
	accessBuildRequest,
) (accessRuntime, error) {
	return nil, b.err
}

type tracingAccessBuilder struct {
	delegate accessBuilder
	events   *eventLog
}

func (b tracingAccessBuilder) Build(
	ctx context.Context,
	request accessBuildRequest,
) (accessRuntime, error) {
	component, err := b.delegate.Build(ctx, request)
	if err != nil {
		return nil, err
	}
	return &tracingAccessRuntime{
		accessRuntime: component,
		events:        b.events,
	}, nil
}

type tracingAccessRuntime struct {
	accessRuntime
	events *eventLog
}

func (r *tracingAccessRuntime) Shutdown(ctx context.Context) error {
	if r.events != nil {
		r.events.add("access.shutdown")
	}
	return r.accessRuntime.Shutdown(ctx)
}

type failingPublicationAccessBuilder struct {
	failRevision access.Revision
}

func (b failingPublicationAccessBuilder) Build(
	ctx context.Context,
	request accessBuildRequest,
) (accessRuntime, error) {
	compiler, err := productionAccessPlanCompiler()
	if err != nil {
		return nil, err
	}
	baseProjection, err := access.NewSnapshotProjection(
		request.rootRevision,
		request.leafCache,
	)
	if err != nil {
		return nil, err
	}
	projection := &failingPublicationProjection{
		SnapshotProjection: baseProjection,
		failRevision:       b.failRevision,
	}
	return access.NewManager(ctx, request.repository, compiler, projection)
}

type failingPublicationProjection struct {
	access.SnapshotProjection
	failRevision access.Revision
}

func (p *failingPublicationProjection) Publish(
	snapshot access.AccessPlanSnapshot,
) error {
	if snapshot.Revision() == p.failRevision {
		return errors.New("injected ProductRuntime Access publication failure")
	}
	return p.SnapshotProjection.Publish(snapshot)
}

func runtimeAccessAggregate(
	t *testing.T,
	accessID access.AccessID,
	revision access.Revision,
	name string,
) access.Aggregate {
	t.Helper()
	endpointID, err := access.NewAgentEndpointID(accessID.String() + "-endpoint")
	if err != nil {
		t.Fatalf("construct AgentEndpoint ID: %v", err)
	}
	profileID, err := access.NewEndpointProfileID(accessID.String() + "-profile")
	if err != nil {
		t.Fatalf("construct EndpointProfile ID: %v", err)
	}
	targetID, err := access.NewProviderTargetID(accessID.String() + "-target")
	if err != nil {
		t.Fatalf("construct ProviderTarget ID: %v", err)
	}
	accountID, err := access.NewAccountBindingID(accessID.String() + "-account")
	if err != nil {
		t.Fatalf("construct account binding ID: %v", err)
	}
	routeSetID, err := access.NewRouteSetID(accessID.String() + "-routes")
	if err != nil {
		t.Fatalf("construct RouteSet ID: %v", err)
	}
	egressID, err := access.NewEgressPolicyID(accessID.String() + "-egress")
	if err != nil {
		t.Fatalf("construct egress policy ID: %v", err)
	}
	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatalf("construct ClientOrigin: %v", err)
	}
	providerOrigin, err := access.NewProviderOrigin("https://api.openai.com:443/v1")
	if err != nil {
		t.Fatalf("construct ProviderOrigin: %v", err)
	}
	model, err := access.NewModelName("gpt-4.1-mini")
	if err != nil {
		t.Fatalf("construct model: %v", err)
	}
	secretRef, err := access.NewSecretRef("secret://provider/" + accessID.String())
	if err != nil {
		t.Fatalf("construct SecretRef: %v", err)
	}
	return access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          revision,
			Name:              name,
			Description:       "ProductRuntime executable Access",
			Status:            access.AccessStatusEnabled,
			AgentEndpointID:   endpointID,
			DefaultRouteSetID: routeSetID,
			ProfileIDs:        []access.EndpointProfileID{profileID},
			EgressPolicyID:    egressID,
		},
		AgentEndpoint: access.AgentEndpoint{
			ID:            endpointID,
			Revision:      revision,
			AccessID:      accessID,
			ClientOrigin:  clientOrigin,
			ClientDialect: access.DialectAnthropicMessages,
		},
		Profiles: []access.EndpointProfile{{
			ID:                  profileID,
			Revision:            revision,
			AccessID:            accessID,
			Name:                "OpenAI Chat",
			Description:         "Fixed M0 profile",
			BackendDialect:      access.DialectOpenAIChat,
			TargetID:            targetID,
			TransportProfileRef: access.ObservedClientH1TransportProfileRef(),
			AccountBindingIDs: []access.AccountBindingID{
				accountID,
			},
			DefaultAccountBindingID: accountID,
			DefaultModelPolicy: access.ModelPolicy{
				Revision:   revision,
				Mode:       access.ModelPolicyModeFixed,
				FixedModel: model,
			},
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:        targetID,
			Revision:  revision,
			AccessID:  accessID,
			ProfileID: profileID,
			Origin:    providerOrigin,
			Protocol:  access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            accountID,
			Revision:      revision,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Primary",
			SecretRef:     secretRef,
			AuthDriverRef: access.StaticHeaderAuthDriverRef(),
			Enabled:       true,
		}},
		RouteSets: []access.RouteSet{{
			ID:                  routeSetID,
			Revision:            revision,
			AccessID:            accessID,
			CandidateProfileIDs: []access.EndpointProfileID{profileID},
		}},
		EgressPolicy: access.AccessEgressPolicy{
			ID:       egressID,
			Revision: revision,
			AccessID: accessID,
			Mode:     access.EgressModeDirect,
		},
		PluginPlan: access.PluginPlan{
			Revision: revision,
			AccessID: accessID,
			Mode:     access.PluginPlanModePassThrough,
		},
	}
}

type ownedComponentDouble struct {
	events *eventLog
	event  string
	err    error
}

func (c *ownedComponentDouble) Shutdown(context.Context) error {
	if c.events != nil {
		c.events.add(c.event)
	}
	return c.err
}

type tracingStorageBuilder struct {
	delegate     storageBuilder
	events       *eventLog
	readErr      error
	readObserved func()
}

func (b tracingStorageBuilder) Build(
	ctx context.Context,
	request storageBuildRequest,
) (storageBuildResult, error) {
	result, err := b.delegate.Build(ctx, request)
	if err != nil {
		return storageBuildResult{}, err
	}
	result.store = &tracingStore{
		RuntimeStore: result.store,
		events:       b.events,
		readErr:      b.readErr,
		readObserved: b.readObserved,
	}
	return result, nil
}

type tracingStore struct {
	runtimepersistence.RuntimeStore
	events       *eventLog
	readErr      error
	readObserved func()
}

func (s *tracingStore) SchemaStateReader() runtimepersistence.SchemaStateReader {
	if s.readErr == nil {
		return s.RuntimeStore.SchemaStateReader()
	}
	return failingSchemaStateReader{
		err:      s.readErr,
		observed: s.readObserved,
	}
}

func (s *tracingStore) Shutdown(ctx context.Context) error {
	if s.events != nil {
		s.events.add("sqlite.shutdown")
	}
	return s.RuntimeStore.Shutdown(ctx)
}

type failingSchemaStateReader struct {
	err      error
	observed func()
}

func (r failingSchemaStateReader) ReadSchemaState(
	context.Context,
) (runtimepersistence.SchemaState, error) {
	if r.observed != nil {
		r.observed()
	}
	return runtimepersistence.SchemaState{}, r.err
}
