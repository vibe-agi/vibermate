//go:build darwin || linux

package productruntime_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type acpChildActivity struct{ runs capturerun.Repository }

func (a acpChildActivity) Active(ctx context.Context, c captureidentity.Reference) (bool, error) {
	return a.runs.Active(ctx, c.ID, time.Now())
}

type isolatedACPDiscovery struct{ session localdiscovery.Session }

func (d isolatedACPDiscovery) Load() (localdiscovery.Session, error) { return d.session, nil }

type heldRealACPStore struct {
	owner            *runtimepersistence.ObservationStore
	entered, release chan struct{}
	once             sync.Once
	fail             bool
	reads            atomic.Int64
}

func (s *heldRealACPStore) PutACP(ctx context.Context, r acpobservation.Record) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	if s.fail {
		return errors.New("optional ACP unavailable")
	}
	return s.owner.PutACP(ctx, r)
}
func (s *heldRealACPStore) Read(context.Context, string, int64) (acpobservation.Record, error) {
	s.reads.Add(1)
	return acpobservation.Record{}, errors.New("optional ACP read unavailable")
}
func TestIsolatedACPChildFixture(t *testing.T) {
	if os.Getenv("VIBERMATE_TASK2_ACP_CHILD") != "1" {
		return
	}
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
func TestACPRealChildFinishesWhileIndependentSnapshotWriterIsHeld(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "failed"}[fail], func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: filepath.Join(directory, "control.db"), LayoutID: "acp-child", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer control.Shutdown(ctx)
			observations, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(directory, "observations.db"), LayoutID: "acp-child", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer observations.Shutdown(ctx)
			sink := &heldRealACPStore{owner: observations, entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
			queue := productruntime.NewACPRecordingTestRecorder(ctx, sink)
			defer queue.Shutdown(ctx)
			var once sync.Once
			release := func() { once.Do(func() { close(sink.release) }) }
			defer release()
			runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(control.CaptureRunRepository()))
			if err != nil {
				t.Fatal(err)
			}
			defer runs.Shutdown(ctx)
			grant, err := runs.Create(ctx, capturerun.CreateCommand{CWD: directory, CanonicalExecutablePath: executable, ExecutableLabel: "test-agent", CatalogRevision: 1, Lifetime: 10 * time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			compiler, err := productruntime.NewRecordingTestCompiler()
			if err != nil {
				t.Fatal(err)
			}
			environments, err := environment.NewManager(ctx, control.EnvironmentRepository(), compiler, environment.NewAtomicProjection(), nil)
			if err != nil {
				t.Fatal(err)
			}
			origin, _ := originidentity.ParseProviderOrigin("https://native.example")
			direct, _ := egressprofile.Explicit(egressprofile.Direct())
			env := environment.Environment{ID: "acp.child", Name: "ACP child", State: environment.StateActive, Revision: 1, ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, PolicySet: &environment.PolicySet{ToolMode: environment.ToolPolicyObserve}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "acp.child.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: direct}}}
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
			activity := acpChildActivity{control.CaptureRunRepository()}
			assignments, err := captureassignment.NewManager(captureassignment.DefaultOptions(control.CaptureAssignmentRepository(), environments, activity))
			if err != nil {
				t.Fatal(err)
			}
			defer assignments.Shutdown(ctx)
			capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: grant.Run.ID}
			if _, err := assignments.Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: env.ID, Source: captureassignment.SourceLaunch}); err != nil {
				t.Fatal(err)
			}
			manager, err := acpobservation.NewManager(control.ACPRegistrations(), runs, assignments, environments, productruntime.SystemClock{}, acpobservation.RecordingOptions{Recorder: queue, Scopes: queue, Reader: sink})
			if err != nil {
				t.Fatal(err)
			}
			view := func(v capturerun.View) capturecontrol.CaptureRunView {
				return capturecontrol.CaptureRunView{ID: v.ID, ExecutableLabel: v.ExecutableLabel, CWD: v.CWD, ProcessID: v.ProcessID, CreatedAt: v.CreatedAt, ExpiresAt: v.ExpiresAt, ClientAdapterState: clientadapter.StatusGeneric, ClientRecognition: clientadapter.RecognitionUnknown, CatalogRevision: 1}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var value any
				var err error
				status := http.StatusOK
				if r.URL.Path == "/api/v1/capture-runs" {
					status = http.StatusCreated
					value = capturecontrol.LaunchGrant{Run: view(grant.Run), CatalogRevision: 1, Recognition: clientadapter.RecognitionUnknown, LaunchRecipe: clientadapter.LaunchGeneric, ExecutablePath: executable, ProxyAddress: "http://" + r.Host, ProxyDelivery: capturecontrol.ProxyDeliveryLocalListener, ProxyToken: grant.ProxyCapability.Value(), RunCapability: grant.ControlCapability.Value(), ProtectedAuthorities: []string{}, ManagedCredentialAuthorities: []string{}}
				} else if r.Header.Get(capturecontrol.RunCapabilityHeader) != grant.ControlCapability.Value() {
					status = http.StatusForbidden
				} else {
					switch {
					case strings.HasSuffix(r.URL.Path, "/start-acp"):
						value, err = manager.Start(r.Context(), grant.Run.ID, grant.ControlCapability, false)
					case strings.HasSuffix(r.URL.Path, "/observe-acp"):
						var snapshot acpobservation.Snapshot
						err = json.NewDecoder(io.LimitReader(r.Body, acpobservation.MaxSnapshotBytes)).Decode(&snapshot)
						if err == nil {
							err = manager.Publish(r.Context(), grant.Run.ID, grant.ControlCapability, snapshot)
						}
						status = http.StatusNoContent
					case strings.HasSuffix(r.URL.Path, "/attach-process"):
						var input capturecontrol.AttachRequest
						err = json.NewDecoder(r.Body).Decode(&input)
						if err == nil {
							var v capturerun.View
							v, err = runs.Attach(r.Context(), grant.Run.ID, grant.ControlCapability, input.ProcessID)
							value = view(v)
						}
					case strings.HasSuffix(r.URL.Path, "/heartbeat"):
						var v capturerun.View
						v, err = runs.Heartbeat(r.Context(), grant.Run.ID, grant.ControlCapability, 10*time.Minute)
						value = view(v)
					case strings.HasSuffix(r.URL.Path, "/finish"):
						err = runs.Finish(r.Context(), grant.Run.ID, grant.ControlCapability)
						status = http.StatusNoContent
					default:
						status = http.StatusNotFound
					}
				}
				if err != nil {
					t.Error(err)
					status = http.StatusServiceUnavailable
				}
				w.WriteHeader(status)
				if value != nil {
					_ = json.NewEncoder(w).Encode(value)
				}
			}))
			defer func() { release(); server.Close() }()
			input, err := os.CreateTemp(directory, "input")
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			const exact = "unchanged ACP editor bytes\n"
			_, _ = input.WriteString(exact)
			_, _ = input.Seek(0, 0)
			output, err := os.CreateTemp(directory, "output")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			stderr, err := os.CreateTemp(directory, "stderr")
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			launcher, err := runlauncher.New(runlauncher.Config{Discovery: isolatedACPDiscovery{localdiscovery.Session{Schema: localdiscovery.Schema, ProcessID: os.Getpid(), BaseURL: server.URL, InstanceID: base64.RawURLEncoding.EncodeToString(make([]byte, 20)), ControlCredential: grant.ProxyCapability.Value(), ExpiresAt: time.Now().Add(time.Hour)}}, BaseEnvironment: []string{"VIBERMATE_TASK2_ACP_CHILD=1", "GORACE=atexit_sleep_ms=0"}, Stdin: input, Stdout: output, Stderr: stderr, Getwd: func() (string, error) { return directory, nil }, ControlTimeout: time.Second, TerminationTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			launchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			type outcome struct {
				code int
				err  error
			}
			done := make(chan outcome, 1)
			go func() {
				code, err := launcher.RunACP(launchCtx, runlauncher.ACPLaunchRequest{Command: []string{executable, "-test.run=^TestIsolatedACPChildFixture$"}})
				done <- outcome{code, err}
			}()
			select {
			case <-sink.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("optional snapshot writer was not held")
			}
			select {
			case result := <-done:
				if result.err != nil || result.code != 0 {
					t.Fatalf("snapshot changed child outcome: code=%d err=%v", result.code, result.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("actual child waited for optional snapshot")
			}
			bytes, err := os.ReadFile(output.Name())
			if err != nil || string(bytes) != exact {
				t.Fatalf("ACP bytes changed: %q %v", bytes, err)
			}
			current, err := runs.GetRun(ctx, grant.Run.ID)
			if err != nil || current.State != capturerun.StateFinished {
				t.Fatalf("required Capture finish blocked: %+v %v", current, err)
			}
			if sink.reads.Load() != 0 {
				t.Fatal("startup/listing consulted optional reader")
			}
			markers, err := manager.Markers(ctx, []string{grant.Run.ID})
			if err != nil || !markers[grant.Run.ID] {
				t.Fatal("control registration marker unavailable", err)
			}
			release()
			if err := queue.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			retained, err := observations.ACPSnapshots().Read(ctx, grant.Run.ID, time.Now().UnixMilli())
			if fail {
				if !errors.Is(err, acpobservation.ErrNotFound) {
					t.Fatalf("failed snapshot retained: %v", err)
				}
			} else if err != nil || !retained.Snapshot.Final || retained.Snapshot.ExitCode == nil || *retained.Snapshot.ExitCode != 0 {
				t.Fatalf("standalone final snapshot: %+v %v", retained, err)
			}
		})
	}
}
