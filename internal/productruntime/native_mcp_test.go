package productruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runtimeProfileProbe struct{ calls int }

func (p *runtimeProfileProbe) Resolve(environment.ClientToolContract, nativetool.CatalogDefinition) (nativemcp.ContentProfile, bool) {
	p.calls++
	return nativemcp.ContentProfile{}, false
}

type runtimeProfileWire struct{}

func (*runtimeProfileWire) RoundTrip(r *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	if policy != egressnetwork.DefaultPolicy() || r.Header.Get("Authorization") != "Bearer synthetic-original-login" {
		return nil, errors.New("unexpected profile fixture transport")
	}
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return nil, err
	}
	header := http.Header{"Content-Type": []string{"application/json"}}
	var result string
	switch request.Method {
	case "initialize":
		header.Set("Mcp-Session-Id", "profile-session")
		result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}`
	case "tools/list":
		result = `{"tools":[{"name":"synthetic_read","description":"Synthetic public tool","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}}]}`
	case "tools/call":
		result = `{"content":[{"type":"text","text":"synthetic result"}]}`
	default:
		return &http.Response{StatusCode: 202, Header: header, Body: http.NoBody}, nil
	}
	return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":` + result + `}`))}, nil
}

func TestNativeRuntimeContentProfileComposition(t *testing.T) {
	for _, inject := range []bool{false, true} {
		t.Run(fmt.Sprint(inject), func(t *testing.T) {
			ctx := context.Background()
			gate, err := offlinehold.New(offlinehold.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			options := testOptions(t, hostcontract.Desktop(), gate)
			probe := &runtimeProfileProbe{}
			if inject {
				options.ContentProfiles = probe
			}
			options.OriginalTransport = &runtimeProfileWire{}
			observations, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "profile", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer observations.Shutdown(ctx)
			var adapter *nativeRevocation
			runtime, err := startWithBuilders(ctx, options, runtimeBuildOverrides{recorder: func(owner context.Context, b *recording.Budget, c connectionevent.ScopedStore, e egressaudit.ScopedAttemptStore) *observationRecorder {
				q := newObservationRecorder(owner, b, c, e)
				q.nativeContent = observations
				return q
			}, environment: func(ctx context.Context, r environmentBuildRequest) (environmentBuildResult, error) {
				adapter = r.nativeRevocation.(*nativeRevocation)
				return buildEnvironment(ctx, r)
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownRuntime(t, runtime)
			origin, _ := originidentity.ParseProviderOrigin("https://native.example")
			env := environment.Environment{ID: "native.profile", Name: "Native profile", State: environment.StateActive, Revision: 1, ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, PolicySet: &environment.PolicySet{ToolMode: environment.ToolPolicyObserve}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.profile.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: authoredEgress(t, egressprofile.Direct())}}}
			draft, err := runtime.Environments().SaveDraft(ctx, environment.DraftCommand{Candidate: env})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := runtime.Environments().Preview(ctx, env.ID, draft.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = runtime.Environments().Publish(ctx, preview); err != nil {
				t.Fatal(err)
			}
			grant, err := runtime.manualCaptures.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic profile", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			authorizer, err := captureadmission.NewAuthorizer(runtime.captureRuns, runtime.manualCaptures)
			if err != nil {
				t.Fatal(err)
			}
			credential, _ := captureadmission.NewProxyCredential(grant.Credential.Value())
			admission, err := authorizer.Authorize(ctx, credential)
			if err != nil {
				t.Fatal(err)
			}
			capture, _ := admission.CaptureReference()
			if _, err = runtime.assignments.Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: env.ID, Source: captureassignment.SourceManualCreate}); err != nil {
				t.Fatal(err)
			}
			connection, err := runtime.assignments.RegisterProviderConnection(ctx, capture, "native.profile.connection", origin)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			for i, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"synthetic_read","arguments":{"query":"synthetic"}}}`} {
				facts := environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: environment.ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}
				lease, err := runtime.assignments.BeginRequest(ctx, capture, "native.profile.connection", facts)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", "https://native.example"+environment.ClientToolMCPPath, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer synthetic-original-login")
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Accept", "application/json, text/event-stream")
				if i > 0 {
					r.Header.Set("Mcp-Session-Id", "profile-session")
					r.Header.Set("Mcp-Protocol-Version", "2025-06-18")
				}
				id, _ := nativetool.NewID(fmt.Sprintf("%032x", i+1))
				captured, err := nativemcp.NewCapturedRequest(lease, admission, r, id)
				if err != nil {
					t.Fatal(err)
				}
				terminal := adapter.executor.Serve(ctx, captured, httptest.NewRecorder())
				if terminal.Outcome != nativemcp.OutcomeCompleted {
					t.Fatalf("runtime call %d=%+v", i, terminal)
				}
				if i == 3 {
					op, err := runtime.NativeToolRepository().Get(ctx, id)
					if err != nil || op.Arguments != nil || op.Result != nil {
						t.Fatal("unknown composition retained content")
					}
					for _, part := range []nativetool.ContentPart{nativetool.ContentToolArguments, nativetool.ContentToolResult} {
						if _, err := observations.GetNativeContent(ctx, id, part); !errors.Is(err, nativetool.ErrNotFound) {
							t.Fatalf("unknown profile retained optional body: %v", err)
						}
					}
				}
			}
			if (probe.calls > 0) != inject || (inject && probe.calls != 1) {
				t.Fatalf("Runtime resolver calls=%d injected=%t", probe.calls, inject)
			}
		})
	}
}

type nativeDrainProbe struct {
	captureAssignmentRuntime
	before func(context.Context) error
}

func (probe *nativeDrainProbe) Drain(ctx context.Context) error {
	if probe.before != nil {
		if err := probe.before(ctx); err != nil {
			return err
		}
	}
	return probe.captureAssignmentRuntime.Drain(ctx)
}

func TestNativeRuntimeRecoversSameStoreAndClosesBeforeAssignmentDrain(t *testing.T) {
	ctx := context.Background()
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	var adapter *nativeRevocation
	var probe *nativeDrainProbe
	runtime, err := startWithBuilders(ctx, options, runtimeBuildOverrides{environment: func(ctx context.Context, request environmentBuildRequest) (environmentBuildResult, error) {
		adapter = request.nativeRevocation.(*nativeRevocation)
		if adapter.executor != nil {
			t.Fatal("native module published before recovery/build")
		}
		result, err := buildEnvironment(ctx, request)
		if err != nil {
			return result, err
		}
		probe = &nativeDrainProbe{captureAssignmentRuntime: result.assignments}
		result.assignments = probe
		return result, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if adapter == nil || adapter.executor == nil || runtime.ProxyHandler() == nil {
		t.Fatal("runtime omitted actual native module")
	}
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	aggregate := environment.Environment{ID: "native.runtime", Name: "Native runtime", State: environment.StateActive, Revision: 1, ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.runtime.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: authoredEgress(t, egressprofile.Direct())}}}
	draft, err := runtime.Environments().SaveDraft(ctx, environment.DraftCommand{Candidate: aggregate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := runtime.Environments().Preview(ctx, aggregate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Environments().Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	grant, err := runtime.manualCaptures.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic runtime", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := captureadmission.NewAuthorizer(runtime.captureRuns, runtime.manualCaptures)
	if err != nil {
		t.Fatal(err)
	}
	credential, _ := captureadmission.NewProxyCredential(grant.Credential.Value())
	admission, err := authorizer.Authorize(ctx, credential)
	if err != nil {
		t.Fatal(err)
	}
	capture, _ := admission.CaptureReference()
	if _, err = runtime.assignments.Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: aggregate.ID, Source: captureassignment.SourceManualCreate}); err != nil {
		t.Fatal(err)
	}
	connection, err := runtime.assignments.RegisterProviderConnection(ctx, capture, "native.runtime.connection", origin)
	if err != nil {
		t.Fatal(err)
	}
	facts := environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: environment.ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}
	captured := func(id string, body string) nativemcp.CapturedRequest {
		lease, err := runtime.assignments.BeginRequest(ctx, capture, "native.runtime.connection", facts)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "https://native.example"+environment.ClientToolMCPPath, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer synthetic-original-login")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		nativeID, _ := nativetool.NewID(id)
		result, err := nativemcp.NewCapturedRequest(lease, admission, request, nativeID)
		if err != nil {
			lease.Release()
			t.Fatal(err)
		}
		return result
	}
	first := captured("11111111111111111111111111111111", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"0.160.0"}}}`)
	terminal := adapter.executor.Serve(ctx, first, httptest.NewRecorder())
	if terminal.Outcome == nativemcp.OutcomeCompleted {
		t.Fatal("offline boundary double unexpectedly permitted outbound")
	}
	if _, err := runtime.storage.NativeToolRepository().Get(ctx, terminal.OperationID); err != nil {
		t.Fatalf("module did not use actual runtime Store: %v", err)
	}
	second := captured("22222222222222222222222222222222", "{")
	checked := false
	probe.before = func(ctx context.Context) error {
		terminal := adapter.executor.Serve(ctx, second, httptest.NewRecorder())
		if terminal.Reason != nativemcp.ReasonRevoked {
			return errors.New("native module still accepted parsing before assignment drain")
		}
		scope, err := runtime.storage.NativeToolRepository().GetScope(ctx, capture)
		if err != nil || scope.State != "revoked" || !scope.ReplayBarrier {
			return errors.New("module did not close authority while Store was alive")
		}
		checked = true
		connection.Close()
		return nil
	}
	shutdownRuntime(t, runtime)
	if !checked {
		t.Fatal("assignment drain probe did not run")
	}
	db, err := sql.Open("sqlite", options.Paths.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	var revision uint64
	if err = db.QueryRow(`SELECT revision FROM native_tool_scopes WHERE capture_kind=? AND capture_id=?`, capture.Kind, capture.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	recovered := false
	options = testOptionsWithPaths(t, options.Paths, hostcontract.Desktop(), &coordinatorDouble{})
	runtime, err = startWithBuilders(ctx, options, runtimeBuildOverrides{environment: func(ctx context.Context, request environmentBuildRequest) (environmentBuildResult, error) {
		// Read through the same actual Store before any proxy/host can be published.
		result, err := buildEnvironment(ctx, request)
		if err != nil {
			return result, err
		}
		db, err := sql.Open("sqlite", options.Paths.DatabasePath())
		if err != nil {
			return result, err
		}
		defer db.Close()
		var next uint64
		if err = db.QueryRow(`SELECT revision FROM native_tool_scopes WHERE capture_kind=? AND capture_id=?`, captureidentity.KindManualCapture, capture.ID).Scan(&next); err != nil {
			return result, err
		}
		if next <= revision {
			return result, errors.New("native Recover did not fence old scope before Environment build")
		}
		recovered = true
		return result, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	shutdownRuntime(t, runtime)
	if !recovered {
		t.Fatal("runtime recovery proof missing")
	}
}
