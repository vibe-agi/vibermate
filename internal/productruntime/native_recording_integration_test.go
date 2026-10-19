//go:build vibermate_acceptance_synthetic

package productruntime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
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
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type heldNativeContentStore struct {
	owner            *runtimepersistence.ObservationStore
	entered, release chan struct{}
	once             sync.Once
	fail             bool
}

func (s *heldNativeContentStore) PutNativeContent(ctx context.Context, v nativetool.ContentObservation) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	if s.fail {
		return errors.New("optional native write failed")
	}
	return s.owner.PutNativeContent(ctx, v)
}

type recordingLocalTransport struct {
	url    string
	client *http.Client
	closed *atomic.Int64
}
type recordingResponseBody struct {
	io.ReadCloser
	closed *atomic.Int64
}

func (b recordingResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.closed.Add(1)
	return err
}
func (t recordingLocalTransport) RoundTrip(r *http.Request, _ egressnetwork.Policy) (*http.Response, error) {
	copy := r.Clone(r.Context())
	url := *r.URL
	target := strings.TrimPrefix(t.url, "http://")
	url.Scheme = "http"
	url.Host = target
	copy.URL = &url
	response, err := t.client.Do(copy)
	if err == nil {
		response.Body = recordingResponseBody{response.Body, t.closed}
	}
	return response, err
}
func TestNativeJSONAndSSEFinishBeforeOptionalBodyWriterRelease(t *testing.T) {
	for _, media := range []string{"json", "sse"} {
		for _, fault := range []string{"blocked", "failed", "saturated"} {
			t.Run(media+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				directory := t.TempDir()
				control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: filepath.Join(directory, "control.db"), LayoutID: "native-wire", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				defer control.Shutdown(ctx)
				observations, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(directory, "observations.db"), LayoutID: "native-wire", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				defer observations.Shutdown(ctx)
				budget, _ := recording.NewBudget(recording.Limits{Records: 2048, Bytes: 64 << 20})
				queue := newObservationRecorder(ctx, budget, nil, nil)
				defer queue.Shutdown(ctx)
				writer := &heldNativeContentStore{owner: observations, entered: make(chan struct{}), release: make(chan struct{}), fail: fault == "failed"}
				queue.nativeContent = writer
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(writer.release) }) }
				defer release()
				compiler, err := productionEnvironmentCompiler(nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				environments, err := environment.NewManager(ctx, control.EnvironmentRepository(), compiler, environment.NewAtomicProjection(), nil)
				if err != nil {
					t.Fatal(err)
				}
				origin, _ := originidentity.ParseProviderOrigin("https://native.example")
				env := environment.Environment{ID: "native.recording", Name: "Native recording", State: environment.StateActive, Revision: 1, ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, PolicySet: &environment.PolicySet{ToolMode: environment.ToolPolicyObserve}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.recording.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: authoredEgress(t, egressprofile.Direct())}}}
				draft, err := environments.SaveDraft(ctx, environment.DraftCommand{Candidate: env})
				if err != nil {
					t.Fatal(err)
				}
				preview, err := environments.Preview(ctx, env.ID, draft.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := environments.Publish(ctx, preview); err != nil {
					t.Fatal(err)
				}
				runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(control.CaptureRunRepository()))
				if err != nil {
					t.Fatal(err)
				}
				defer runs.Shutdown(ctx)
				manuals, err := manualcapture.NewManager(ctx, manualcapture.DefaultOptions(control.ManualCaptureRepository()))
				if err != nil {
					t.Fatal(err)
				}
				defer manuals.Shutdown(ctx)
				grant, err := manuals.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Native recording", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
				if err != nil {
					t.Fatal(err)
				}
				authorizer, _ := captureadmission.NewAuthorizer(runs, manuals)
				credential, _ := captureadmission.NewProxyCredential(grant.Credential.Value())
				admission, err := authorizer.Authorize(ctx, credential)
				if err != nil {
					t.Fatal(err)
				}
				capture, _ := admission.CaptureReference()
				activity, _ := newCaptureActivityResolver(control.CaptureRunRepository(), control.ManualCaptureRepository(), SystemClock{})
				assignments, err := captureassignment.NewManager(captureassignment.DefaultOptions(control.CaptureAssignmentRepository(), environments, activity))
				if err != nil {
					t.Fatal(err)
				}
				defer assignments.Shutdown(ctx)
				if _, err := assignments.Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: env.ID, Source: captureassignment.SourceManualCreate}); err != nil {
					t.Fatal(err)
				}
				connection, err := assignments.RegisterProviderConnection(ctx, capture, "native.recording.connection", origin)
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Close()
				bridge := nativetool.TransitionalRepository{AuthorityRepository: control.NativeToolRepository()}
				approvalOptions := toolapproval.DefaultOptions(control.ToolApprovalRepository())
				approvalOptions.NativeRepository = bridge
				authority, err := toolapproval.New(ctx, approvalOptions)
				if err != nil {
					t.Fatal(err)
				}
				decisions, _ := toolpolicy.New(authority)
				offline, _ := offlinehold.New(offlinehold.DefaultConfig())
				if err := offline.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "recording-native"}); err != nil {
					t.Fatal(err)
				}
				const rawResult = `{"jsonrpc":"2.0", "id":3,"result":{"_meta":{"private":"wire-only"},"content":[{"type":"text","text":"public result","_meta":{"private":"wire-only"}}]}}`
				expected := rawResult
				if media == "sse" {
					expected = "event: message\nid: final-native\ndata: " + rawResult + "\n\n"
				}
				var toolSends, closed atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if strings.Contains(string(body), `"method":"tools/call"`) {
						toolSends.Add(1)
						if r.Header.Get("Authorization") != "Bearer synthetic-original-login" {
							t.Error("credential changed")
						}
						if media == "sse" {
							w.Header().Set("Content-Type", "text/event-stream")
						} else {
							w.Header().Set("Content-Type", "application/json")
						}
						io.WriteString(w, expected)
						return
					}
					// Fixed controlled handshake, with exact synthetic catalog evidence.
					w.Header().Set("Content-Type", "application/json")
					switch {
					case strings.Contains(string(body), `"method":"initialize"`):
						w.Header().Set("Mcp-Session-Id", "profile-session")
						io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}}`)
					case strings.Contains(string(body), `"method":"tools/list"`):
						io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"synthetic_read","description":"Synthetic public tool","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}}]}}`)
					default:
						w.WriteHeader(http.StatusAccepted)
					}
				}))
				defer server.Close()
				executor, err := nativemcp.New(nativemcp.Dependencies{OwnerContext: ctx, Transport: recordingLocalTransport{server.URL, server.Client(), &closed}, Store: bridge, ToolDecisions: decisions, Offline: offline, Clock: time.Now, IDs: nativeIDs{rand.Reader}, ContentProfiles: nativemcp.AcceptanceSyntheticContentProfiles(), ContentRecorder: queue, RecordingScopes: queue}, nativemcp.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { release(); _ = executor.Close(ctx) }()
				serve := func(ordinal int, body string) (nativemcp.Terminal, *httptest.ResponseRecorder) {
					facts := environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: environment.ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}
					lease, err := assignments.BeginRequest(ctx, capture, "native.recording.connection", facts)
					if err != nil {
						t.Error(err)
						return nativemcp.Terminal{}, nil
					}
					request := httptest.NewRequest("POST", "https://native.example"+environment.ClientToolMCPPath, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Accept", "application/json, text/event-stream")
					request.Header.Set("Authorization", "Bearer synthetic-original-login")
					if ordinal > 0 {
						request.Header.Set("Mcp-Session-Id", "profile-session")
						request.Header.Set("Mcp-Protocol-Version", "2025-06-18")
					}
					id, _ := nativetool.NewID(fmt.Sprintf("%032x", ordinal+1))
					captured, err := nativemcp.NewCapturedRequest(lease, admission, request, id)
					if err != nil {
						lease.Release()
						t.Error(err)
						return nativemcp.Terminal{}, nil
					}
					response := httptest.NewRecorder()
					terminal := executor.Serve(ctx, captured, response)
					return terminal, response
				}
				for i, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
					if terminal, _ := serve(i, body); terminal.Outcome != nativemcp.OutcomeCompleted {
						t.Fatalf("handshake: %+v", terminal)
					}
				}
				if fault == "saturated" {
					reservation, ok := budget.TryReserve(recording.Raw, 64<<20)
					if !ok {
						t.Fatal("cannot saturate shared budget")
					}
					defer reservation.Release()
				}
				if fault != "saturated" {
					stamp, input := nativeQueueInput(queue)
					if queue.ObserveNativeContent(stamp, input) != recording.Accepted {
						t.Fatal("native body prime refused")
					}
					select {
					case <-writer.entered:
					case <-time.After(time.Second):
						t.Fatal("native body writer fault not active before measured call")
					}
				}
				const call = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"synthetic_read","arguments":{"query":"synthetic"}}}`
				type outcome struct {
					terminal nativemcp.Terminal
					response *httptest.ResponseRecorder
				}
				done := make(chan outcome, 1)
				go func() { terminal, response := serve(3, call); done <- outcome{terminal, response} }()
				if fault != "saturated" {
					select {
					case <-writer.entered:
					case <-time.After(3 * time.Second):
						t.Fatal("actual body writer was never entered")
					}
				}
				var got outcome
				select {
				case got = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("native execution waited for optional writer release")
				}
				if got.terminal.Outcome != nativemcp.OutcomeCompleted || got.response.Body.String() != expected {
					t.Fatalf("result bytes/outcome: %+v %q", got.terminal, got.response.Body.String())
				}
				if closed.Load() != 4 {
					t.Fatalf("real response owners held by recording: closed=%d", closed.Load())
				}
				operation, err := control.NativeToolRepository().Get(ctx, got.terminal.OperationID)
				if err != nil || operation.State != "completed" {
					t.Fatalf("required completion waited: %+v %v", operation, err)
				}
				if terminal, _ := serve(4, call); terminal.Outcome == nativemcp.OutcomeCompleted || toolSends.Load() != 1 {
					t.Fatalf("duplicate executed again: %+v sends=%d", terminal, toolSends.Load())
				}
				connection.Close()
				drain, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				if err := assignments.Drain(drain); err != nil {
					t.Fatalf("execution lease survived optional recording: %v", err)
				}
				if fault != "saturated" && budget.Snapshot().ReservedRecords < 1 {
					t.Fatal("worker released live ownership")
				}
				release()
				if err := queue.Flush(ctx); err != nil {
					t.Fatal(err)
				}
				value, err := observations.GetNativeContent(ctx, got.terminal.OperationID, nativetool.ContentToolResult)
				if fault == "blocked" {
					if err != nil || strings.Contains(string(value.Body), "wire-only") || !strings.Contains(string(value.Body), "public result") || value.Identity.Scope != operation.RecordingScope || !value.Identity.CreatedAt.Equal(operation.CreatedAt) {
						t.Fatalf("independent retained body: %+v %v", value, err)
					}
				} else if !errors.Is(err, nativetool.ErrNotFound) {
					t.Fatalf("failed/saturated recording produced content: %v", err)
				}
			})
		}
	}
}
