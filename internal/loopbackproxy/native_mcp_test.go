package loopbackproxy_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type testIDs struct{ value atomic.Uint64 }

func (ids *testIDs) NewNativeOperationID(context.Context) (nativetool.ID, error) {
	return nativetool.NewID(fmt.Sprintf("%032x", ids.value.Add(1)))
}
func (ids *testIDs) NewOwnerEpoch(context.Context) (uint64, error) { return ids.value.Add(1), nil }

type captureActivity struct {
	repository manualcapture.ActivityReader
	runs       capturerun.ActivityReader
}

func (a captureActivity) Active(ctx context.Context, c captureidentity.Reference) (bool, error) {
	if c.Kind == captureidentity.KindManagedRun {
		return a.runs.Active(ctx, c.ID, time.Now().UTC())
	}
	id, err := manualcapture.ParseID(c.ID)
	if err != nil {
		return false, err
	}
	return a.repository.Active(ctx, id, time.Now().UTC())
}

type nativeFixture struct {
	store        *runtimepersistence.Store
	db           *sql.DB
	assignments  *captureassignment.Manager
	admission    captureadmission.Admission
	capture      captureidentity.Reference
	executor     nativemcp.Executor
	transport    *countingTransport
	ids          *testIDs
	gate         *offlinehold.Gate
	environments *environment.Manager
	proxy        *proxyFixture
	credential   string
	manuals      *manualcapture.Manager
	manualGrant  manualcapture.Grant
	runs         *capturerun.Manager
	managedGrant capturerun.LaunchGrant
	modelIDs     *nativeModelIDCounter
}

type nativeModelIDCounter struct{ count atomic.Uint64 }

func (counter *nativeModelIDCounter) NewExchangeID(context.Context) (string, error) {
	return fmt.Sprintf("model-only-%d", counter.count.Add(1)), nil
}

type countingTransport struct {
	mu       sync.Mutex
	count    int
	requests []string
	handler  func(*http.Request, []byte) (*http.Response, error)
	before   func(*http.Request)
}

func (t *countingTransport) RoundTrip(r *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	if policy != egressnetwork.DefaultPolicy() {
		return nil, fmt.Errorf("wrong frozen egress policy")
	}
	body, _ := io.ReadAll(r.Body)
	if t.before != nil {
		t.before(r)
	}
	t.mu.Lock()
	t.count++
	t.requests = append(t.requests, string(body))
	handler := t.handler
	t.mu.Unlock()
	if handler != nil {
		return handler(r, body)
	}
	var response string
	switch {
	case strings.Contains(string(body), `"method":"initialize"`):
		response = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}}`
	case strings.Contains(string(body), `"method":"tools/list"`):
		response = `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read_file","description":"Synthetic public tool","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}]}}`
	case strings.Contains(string(body), `"method":"tools/call"`):
		response = `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"synthetic result"}]}}`
	default:
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	h := http.Header{"Content-Type": []string{"application/json"}}
	if strings.Contains(string(body), `"method":"initialize"`) {
		h.Set("Mcp-Session-Id", "synthetic-session")
	}
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(response))}, nil
}
func (t *countingTransport) CloseIdleConnections() {}
func (t *countingTransport) sends() int            { t.mu.Lock(); defer t.mu.Unlock(); return t.count }

func newNativeFixture(t *testing.T, recording environment.ContentRecordingPolicy, custom ...nativemcp.Limits) *nativeFixture {
	return newNativeFixtureMode(t, environment.ToolPolicyReview, recording, nil, custom...)
}

type nativeHandlerSettings struct {
	origin                    string
	configure                 func(*loopbackproxy.Options)
	shared, off, raw, managed bool
	decisionTimeout           time.Duration
	revocation                captureassignment.NativeRevocation
}

func newNativeFixtureMode(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, settings *nativeHandlerSettings, custom ...nativemcp.Limits) *nativeFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: path, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(ctx) })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	operations, _ := operationcatalog.BuiltIn()
	var pairs []protocolspec.CodecPairDefinition
	for _, dialect := range []protocolspec.Dialect{protocolspec.DialectOpenAIResponses, protocolspec.DialectAnthropicMessages} {
		pairID, _ := protocolspec.NewCodecPairID("native.synthetic." + string(dialect))
		pairs = append(pairs, protocolspec.CodecPairDefinition{ID: pairID, Revision: 1, ClientDialect: dialect, ProviderDialect: dialect, ClientOperationIDs: operations.SemanticOperationIDs(dialect), RequiredCapabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}})
	}
	catalog, err := protocolspec.NewCatalog(operations.Definitions(), pairs)
	if err != nil {
		t.Fatal(err)
	}
	wires, _ := wireprofile.BuiltInCatalog()
	compiler, err := environment.NewCompiler(nil, nil, catalog, wires)
	if err != nil {
		t.Fatal(err)
	}
	envs, err := environment.NewManager(ctx, store.EnvironmentRepository(), compiler, environment.NewAtomicProjection(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	if settings != nil && settings.origin != "" {
		origin, err = originidentity.ParseProviderOrigin(settings.origin)
		if err != nil {
			t.Fatal(err)
		}
	}
	e := environment.Environment{ID: "native.synthetic", Name: "Synthetic native", State: environment.StateActive, Revision: 1, ContentRecording: recording, PolicySet: &environment.PolicySet{ToolMode: mode}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.synthetic.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: egressprofile.Direct()}}}
	if settings != nil {
		if settings.shared {
			e.ClientEndpoints = testEnvironmentForDialectRevision(t, protocolspec.DialectOpenAIResponses, "synthetic-model", 1, 1).ClientEndpoints
			for i := range e.ClientEndpoints {
				e.ClientEndpoints[i].ClientOrigin, _ = originidentity.ParseClientOrigin("https://native.example")
			}
		}
		if settings.off {
			e.ClientToolAccess = nil
		}
	}
	draft, err := envs.SaveDraft(ctx, environment.DraftCommand{Candidate: e})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := envs.Preview(ctx, e.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = envs.Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	manualOptions := manualcapture.DefaultOptions(store.ManualCaptureRepository())
	if settings != nil {
		manualOptions.NativeRevocation = settings.revocation
	}
	var raw *rawevidence.Manager
	if settings != nil && settings.raw {
		raw, err = rawevidence.Open(ctx, rawevidence.Options{Repository: store.RawEvidenceRepository(), Random: rand.Reader, Clock: rawevidence.SystemClock{}, Config: rawevidence.DefaultConfig()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = raw.Shutdown(ctx) })
		manualOptions.EvidenceBarrier = nativeRawTerminalBarrier{raw}
	}
	manuals, err := manualcapture.NewManager(ctx, manualOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manuals.Shutdown(ctx) })
	grant, err := manuals.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic native", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runOptions := capturerun.DefaultOptions(store.CaptureRunRepository())
	if settings != nil {
		runOptions.NativeRevocation = settings.revocation
	}
	if raw != nil {
		runOptions.EvidenceBarrier = nativeRunRawTerminalBarrier{raw}
	}
	runs, err := capturerun.NewManager(ctx, runOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Shutdown(ctx) })
	auth, err := captureadmission.NewAuthorizer(runs, manuals)
	if err != nil {
		t.Fatal(err)
	}
	credentialValue := grant.Credential.Value()
	var managedGrant capturerun.LaunchGrant
	if settings != nil && settings.managed {
		managedGrant, err = runs.Create(ctx, capturerun.CreateCommand{CWD: t.TempDir(), CanonicalExecutablePath: "/synthetic/codex", ExecutableLabel: "Synthetic", CatalogRevision: 1, Lifetime: 5 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		credentialValue = managedGrant.ProxyCapability.Value()
	}
	credential, err := captureadmission.NewProxyCredential(credentialValue)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := auth.Authorize(ctx, credential)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := admission.CaptureReference()
	if err != nil {
		t.Fatal(err)
	}
	assignmentOptions := captureassignment.Options{Repository: store.CaptureAssignmentRepository(), Environments: envs, Activity: captureActivity{repository: store.ManualCaptureRepository(), runs: store.CaptureRunRepository()}, Clock: manualcapture.SystemClock{}}
	if settings != nil {
		assignmentOptions.NativeRevocation = settings.revocation
	}
	assignments, err := captureassignment.NewManager(assignmentOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assignments.Shutdown(ctx) })
	source := captureassignment.SourceManualCreate
	if capture.Kind == captureidentity.KindManagedRun {
		source = captureassignment.SourceLaunch
	}
	if _, err = assignments.Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: e.ID, Source: source}); err != nil {
		t.Fatal(err)
	}
	connection, err := assignments.RegisterProviderConnection(ctx, capture, "synthetic.connection", origin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Close)
	options := toolapproval.DefaultOptions(store.ToolApprovalRepository())
	options.NativeRepository = store.NativeToolRepository()
	options.Config.DecisionTimeout = 3 * time.Second
	if settings != nil && settings.decisionTimeout > 0 {
		options.Config.DecisionTimeout = settings.decisionTimeout
	}
	authority, err := toolapproval.New(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := toolpolicy.New(authority)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = offline.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "synthetic.native"}); err != nil {
		t.Fatal(err)
	}
	transport := &countingTransport{}
	ids := &testIDs{}
	limits := nativemcp.DefaultLimits()
	if len(custom) > 0 {
		limits = custom[0]
	}
	executor, err := nativemcp.New(nativemcp.Dependencies{OwnerContext: ctx, Transport: transport, Store: store.NativeToolRepository(), ToolDecisions: gate, Offline: offline, Clock: time.Now, IDs: ids}, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close(ctx) })

	ca, err := localca.Open(ctx, localca.DefaultOptions(filepath.Join(t.TempDir(), "ca"), ctx))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ca.Shutdown(ctx) })
	connections, err := connectionevent.New(ctx, connectionevent.DefaultOptions(store.ConnectionEventRepository()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connections.Shutdown(ctx) })
	rules, err := connectionpolicy.NewManager(ctx, connectionpolicy.ManagerOptions{Repository: store.ConnectionRuleRepository(), Clock: toolapproval.SystemClock{}, Shipped: allowEverythingTestPolicy(t)})
	if err != nil {
		t.Fatal(err)
	}
	modelIDs := &nativeModelIDCounter{}
	handlerOptions := loopbackproxy.Options{OwnerContext: ctx, Admissions: auth, Assignments: assignments, Exchanges: &exchangeRecorder{}, Original: &originalRecorder{}, Certificates: ca, Connections: connections, Policy: rules.Source(), ExchangeIDs: modelIDs, NativeTools: executor, NativeOperationIDs: ids}
	if raw != nil {
		handlerOptions.RawEvidence = raw
	}
	if settings != nil && settings.configure != nil {
		settings.configure(&handlerOptions)
	}
	handler, err := loopbackproxy.New(handlerOptions)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close(); _ = handler.Shutdown(ctx) })
	proxy := &proxyFixture{listener: listener, authority: ca}
	f := &nativeFixture{store: store, db: db, assignments: assignments, admission: admission, capture: capture, executor: executor, transport: transport, ids: ids, gate: offline, environments: envs, proxy: proxy}
	f.credential = credentialValue
	f.manuals, f.manualGrant = manuals, grant
	f.runs, f.managedGrant = runs, managedGrant
	f.modelIDs = modelIDs
	t.Cleanup(func() { _ = authority.Shutdown(ctx); offline.BeginShutdown(); _ = offline.Drain(ctx) })
	return f
}

// A real native-only connection must reach initialize, not demand a model endpoint.
func TestNativeHandlerUsesFrozenAuthorityAndOneApprovedCall(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	secured := f.proxy.ConnectTLS(t, f.credential, "native.example:443", "native.example")
	defer secured.Close()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"0.160.0"}}}`
	response := writeInnerRequest(t, secured, &http.Request{Method: "POST", URL: mustURL(t, environment.ClientToolMCPPath), Host: "native.example:443", Header: http.Header{"Authorization": {"Bearer synthetic-original-login"}, "Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))})
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("native initialize status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	for _, wire := range []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
		response := nativeRequest(t, secured, wire, "synthetic-session")
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 202 {
			t.Fatalf("handshake status%d body%s", response.StatusCode, data)
		}
	}
	done := make(chan *http.Response, 1)
	go func() {
		done <- nativeRequest(t, secured, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"synthetic-native-handler-call"}}}`, "synthetic-session")
	}()
	deadline := time.Now().Add(2 * time.Second)
	var call nativetool.Operation
	for time.Now().Before(deadline) {
		page, err := f.store.NativeToolRepository().Page(context.Background(), nativetool.PageRequest{Capture: f.capture, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range page.Items {
			if op.Kind == nativetool.RequestToolsCall && op.ApprovalID != nil {
				call = op
			}
		}
		if call.ApprovalID != nil {
			break
		}
		time.Sleep(time.Millisecond * 5)
	}
	if call.ApprovalID == nil {
		t.Fatal("actual Review question missing")
	}
	approval, err := f.store.NativeToolRepository().GetApproval(context.Background(), *call.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.NativeToolRepository().DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "synthetic-native-handler-allow", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	response = <-done
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(data), "synthetic result") || f.transport.sends() != 4 {
		t.Fatalf("approved call status%d body%s sends%d", response.StatusCode, data, f.transport.sends())
	}
	if f.modelIDs.count.Load() != 0 {
		t.Fatal("native dispatch allocated a model Exchange ID")
	}
	completed, err := f.store.NativeToolRepository().Get(context.Background(), call.ID)
	if err != nil || completed.Capture != f.capture || completed.EnvironmentID != "native.synthetic" || completed.CredentialOwner != "original_client" || completed.State != "completed" {
		t.Fatalf("frozen native operation %+v %v", completed, err)
	}
	for _, table := range []string{"runtime_activities", "runtime_exchange_contents", "native_tool_contents"} {
		var count int
		if err := f.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("native content/model leak %s count%d %v", table, count, err)
		}
	}
}

func nativeRequest(t *testing.T, secured net.Conn, body, session string) *http.Response {
	t.Helper()
	return writeInnerRequest(t, secured, &http.Request{Method: "POST", URL: mustURL(t, environment.ClientToolMCPPath), Host: "native.example:443", Header: http.Header{"Authorization": {"Bearer synthetic-original-login"}, "Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}, "Mcp-Session-Id": {session}, "Mcp-Protocol-Version": {"2025-06-18"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))})
}

func TestNativeOnlyTLSAfterUnchangedRouteApplyLatest(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	old, err := f.assignments.Resolve(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.environments.Resolve(old.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	next := snapshot.Aggregate()
	next.Name = "Applied native-only"
	next.Revision++
	draft, err := f.environments.SaveDraft(context.Background(), environment.DraftCommand{Candidate: next, ExpectedBaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.environments.Preview(context.Background(), next.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.environments.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	if _, err = f.assignments.ApplyLatest(context.Background(), f.capture, 1); err != nil {
		t.Fatal(err)
	}
	resolver, err := capturegrant.NewEnvironmentAuthorityResolver(f.assignments, f.environments)
	if err != nil {
		t.Fatal(err)
	}
	authorities, err := resolver.Resolve(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	view := authorities.NativeToolLogin()
	if view == nil || view.Origin != "https://native.example" || view.ServicePath != "/backend-api/ps/mcp" || view.Contract != "codex_legacy_2025_06_18" {
		t.Fatalf("native launch metadata %+v", view)
	}
	view.Origin = "https://substituted.example"
	if authorities.NativeToolLogin().Origin != "https://native.example" {
		t.Fatal("native login view shares mutable storage")
	}
	secured := f.proxy.ConnectTLS(t, f.credential, "native.example:443", "native.example")
	defer secured.Close()
	response := nativeRequest(t, secured, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"0.160.0"}}}`, "")
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("native TLS after ApplyLatest status%d body%s", response.StatusCode, data)
	}
}
