package nativemcp

import (
	"context"
	"database/sql"
	"fmt"
	recordingbudget "github.com/vibe-agi/vibermate/internal/recording"
	"io"
	"net/http"
	"net/http/httptest"
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

type captureActivity struct{ repository manualcapture.ActivityReader }

func (a captureActivity) Active(ctx context.Context, c captureidentity.Reference) (bool, error) {
	id, err := manualcapture.ParseID(c.ID)
	if err != nil {
		return false, err
	}
	return a.repository.Active(ctx, id, time.Now().UTC())
}

// The public-backup fixture supplies the real current control owner. Other
// protocol fixtures retain their private staging adapter.
type nativeFixtureAuthority interface {
	nativetool.LifecycleAuthority
	nativetool.ExecutionAuthority
}
type nativeFixturePorts interface {
	Shutdown(context.Context) error
	EnvironmentRepository() environment.Repository
	ManualCaptureRepository() manualcapture.Repository
	CaptureRunRepository() capturerun.Repository
	CaptureAssignmentRepository() captureassignment.Repository
	ToolApprovalRepository() toolapproval.Repository
}

type nativeFixture struct {
	content      *fixtureContent
	contentDB    *sql.DB
	store        *runtimepersistence.Store
	authority    nativeFixtureAuthority
	db           *sql.DB
	assignments  *captureassignment.Manager
	admission    captureadmission.Admission
	capture      captureidentity.Reference
	executor     Executor
	transport    *countingTransport
	ids          *testIDs
	gate         *offlinehold.Gate
	environments *environment.Manager
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

func newNativeFixture(t *testing.T, recording environment.ContentRecordingPolicy, custom ...Limits) *nativeFixture {
	return newNativeFixtureMode(t, environment.ToolPolicyReview, recording, custom...)
}
func newNativeFixtureMode(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, custom ...Limits) *nativeFixture {
	return newNativeFixtureStoreLifetime(t, mode, recording, 0, custom...)
}
func newNativeFixtureStoreLifetime(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, lifetime time.Duration, custom ...Limits) *nativeFixture {
	return newNativeFixtureWithChoice(t, mode, recording, lifetime, authoredEgress(t, egressprofile.Direct()), custom...)
}

func newNativeFixtureWithChoice(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, lifetime time.Duration, choice egressprofile.Choice, custom ...Limits) *nativeFixture {
	t.Helper()
	return newNativeFixtureWithApprovalConfig(t, mode, recording, lifetime, choice, toolapproval.Config{DecisionTimeout: 3 * time.Second}, custom...)
}

func newNativeFixtureWithApprovalConfig(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, lifetime time.Duration, choice egressprofile.Choice, approvalConfig toolapproval.Config, custom ...Limits) *nativeFixture {
	t.Helper()
	return newNativeFixtureWithApprovalObservation(t, mode, recording, lifetime, choice, approvalConfig, nil, custom...)
}

func newNativeFixtureWithApprovalObservation(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, lifetime time.Duration, choice egressprofile.Choice, approvalConfig toolapproval.Config, observe func(nativetool.Approval), custom ...Limits) *nativeFixture {
	t.Helper()
	return newNativeFixtureWithStore(t, mode, recording, lifetime, choice, approvalConfig, observe, nil, "", custom...)
}

func newNativeFixtureWithStore(t *testing.T, mode environment.ToolPolicyMode, recording environment.ContentRecordingPolicy, lifetime time.Duration, choice egressprofile.Choice, approvalConfig toolapproval.Config, observe func(nativetool.Approval), control *runtimepersistence.ControlStore, path string, custom ...Limits) *nativeFixture {
	t.Helper()
	ctx := context.Background()
	var store *runtimepersistence.Store
	var ports nativeFixturePorts
	var native nativeFixtureAuthority
	if control == nil {
		path = filepath.Join(t.TempDir(), "runtime.db")
		var err error
		store, err = runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: path, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout, NativeStreamLifetime: lifetime})
		if err != nil {
			t.Fatal(err)
		}
		ports, native = store, store.NativeToolRepository()
	} else {
		ports, native = control, control.NativeToolRepository()
	}
	t.Cleanup(func() { _ = ports.Shutdown(ctx) })
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
	envs, err := environment.NewManager(ctx, ports.EnvironmentRepository(), compiler, environment.NewAtomicProjection(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	e := environment.Environment{ID: "native.synthetic", Name: "Synthetic native", State: environment.StateActive, Revision: 1, ContentRecording: recording, PolicySet: &environment.PolicySet{ToolMode: mode}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.synthetic.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: choice}}}
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
	manuals, err := manualcapture.NewManager(ctx, manualcapture.DefaultOptions(ports.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manuals.Shutdown(ctx) })
	grant, err := manuals.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic native", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(ports.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Shutdown(ctx) })
	auth, err := captureadmission.NewAuthorizer(runs, manuals)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := captureadmission.NewProxyCredential(grant.Credential.Value())
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
	assignments, err := captureassignment.NewManager(captureassignment.Options{Repository: ports.CaptureAssignmentRepository(), Environments: envs, Activity: captureActivity{ports.ManualCaptureRepository()}, Clock: manualcapture.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assignments.Shutdown(ctx) })
	if _, err = assignments.Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: e.ID, Source: captureassignment.SourceManualCreate}); err != nil {
		t.Fatal(err)
	}
	connection, err := assignments.RegisterProviderConnection(ctx, capture, "synthetic.connection", origin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Close)
	options := toolapproval.DefaultOptions(ports.ToolApprovalRepository())
	options.NativeRepository = native
	if observe != nil {
		options.NativeRepository = approvalPublicationObserver{LifecycleAuthority: options.NativeRepository, observe: observe}
	}
	options.Config = approvalConfig
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
	limits := DefaultLimits()
	if len(custom) > 0 {
		limits = custom[0]
	}
	observationsPath := filepath.Join(filepath.Dir(path), "observations.db")
	if control != nil {
		observationsPath = filepath.Join(t.TempDir(), "observations.db")
	}
	observations, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: observationsPath, LayoutID: "native-fixture", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observations.Shutdown(ctx) })
	contentDB, err := sql.Open("sqlite", observationsPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = contentDB.Close() })
	epoch, _ := recordingbudget.NewEpoch(1)
	budget, _ := recordingbudget.NewBudget(recordingbudget.Limits{Records: 2048, Bytes: 64 << 20})
	content := &fixtureContent{epoch: epoch, budget: budget, store: observations}
	t.Cleanup(func() {
		content.mu.Lock()
		defer content.mu.Unlock()
		for _, item := range content.items {
			item.reservation.Release()
		}
	})
	executor, err := New(Dependencies{OwnerContext: ctx, Transport: transport, Store: native, ToolDecisions: gate, Offline: offline, Clock: time.Now, IDs: ids, ContentRecorder: content, RecordingScopes: content}, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close(ctx) })
	return &nativeFixture{content: content, contentDB: contentDB, store: store, authority: native, db: db, assignments: assignments, admission: admission, capture: capture, executor: executor, transport: transport, ids: ids, gate: offline, environments: envs}
}
func (f *nativeFixture) captured(t *testing.T, method, body, session string) CapturedRequest {
	t.Helper()
	ctx := context.Background()
	facts := environment.RequestFacts{Target: protocolspec.RequestTarget{Method: method, Path: environment.ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}
	lease, err := f.assignments.BeginRequest(ctx, f.capture, "synthetic.connection", facts)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.ids.NewNativeOperationID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "https://native.example"+environment.ClientToolMCPPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-original-login")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		r.Header.Set("Mcp-Session-Id", session)
		r.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	}
	captured, err := NewCapturedRequest(lease, f.admission, r, id)
	if err != nil {
		lease.Release()
		t.Fatal(err)
	}
	return captured
}
func (f *nativeFixture) serve(t *testing.T, body, session string) (Terminal, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	terminal := f.executor.Serve(context.Background(), f.captured(t, "POST", body, session), w)
	return terminal, w
}
func (f *nativeFixture) handshake(t *testing.T) {
	t.Helper()
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{"form":{},"url":{}},"extensions":{"openai/form":{},"openai/elicitation":{"form":{},"userVerification":{}}}},"clientInfo":{"name":"codex-mcp-client","version":"0.160.0","title":"Codex"}}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
		session := "synthetic-session"
		if strings.Contains(body, `"method":"initialize"`) {
			session = ""
		}
		terminal, w := f.serve(t, body, session)
		if terminal.Outcome != OutcomeCompleted {
			op, storeErr := f.authority.Get(context.Background(), terminal.OperationID)
			t.Fatalf("handshake terminal %+v status%d body%s op%+v storeErr%v sends%d", terminal, w.Code, w.Body.String(), op, storeErr, f.transport.sends())
		}
	}
}
func (f *nativeFixture) approve(t *testing.T, id nativetool.ID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, err := f.authority.Get(context.Background(), id)
		if err == nil && op.ApprovalID != nil {
			approval, err := f.authority.GetApproval(context.Background(), *op.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.authority.DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "synthetic-allow-" + id.String(), ObservedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	op, err := f.authority.Get(context.Background(), id)
	t.Fatalf("real Review question absent: operation %+v error%v", op, err)
}
func TestNativeExecutorApprovedCallHasDedicatedStartReceipt(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"synthetic-call-1"}}}`, "synthetic-session")
	w := httptest.NewRecorder()
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(context.Background(), request, w) }()
	f.approve(t, request.id)
	terminal := <-results
	if terminal.Outcome != OutcomeCompleted || f.transport.sends() != 4 {
		t.Fatalf("call %+v sends%d", terminal, f.transport.sends())
	}
	requireNativeClaimAfterDrain(t, f.executor.(*executor), request.id, false)
	op, err := f.authority.Get(context.Background(), request.id)
	if err != nil || op.Capture != f.capture || op.Kind != nativetool.RequestToolsCall || op.ToolName != "read_file" || op.CredentialOwner != "original_client" || op.ApprovalID == nil {
		t.Fatalf("real native projection %+v %v", op, err)
	}
	for _, table := range []string{"native_tool_once", "native_tool_deliveries"} {
		var count int
		query := `SELECT COUNT(*) FROM ` + table
		if table == "native_tool_once" {
			query += ` WHERE operation_id='` + request.id.String() + `'`
		}
		if err := f.db.QueryRow(query).Scan(&count); err != nil || count == 0 {
			t.Fatalf("committed %s count%d err%v", table, count, err)
		}
	}
	for _, table := range []string{"runtime_activities", "runtime_exchange_contents", "native_tool_contents"} {
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("native leaked into %s", table)
		}
	}
}
