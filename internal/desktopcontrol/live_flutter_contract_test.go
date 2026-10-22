//go:build darwin || linux

package desktopcontrol_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimecontrol"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// The mandatory enabled CI lane uses current production HTTP, never saved JSON.
// One Go owner retains the producer until the real Flutter consumer has exited.
func TestLiveRuntimeFlutterContentContract(t *testing.T) {
	if os.Getenv("VIBERMATE_RUN_FLUTTER_CONTRACT") != "1" {
		t.Skip("enabled by the dedicated Flutter contract lane")
	}
	flutter, err := exec.LookPath("flutter")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := productruntime.NewRuntimePaths(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	startup, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	r, err := productruntime.Start(startup, productruntime.Options{
		Paths: paths, Host: hostcontract.Server(), OfflineHold: gate, Secrets: newCredentialStoreFixture(),
		Approvals: toolapproval.DefaultConfig(), ExchangeHold: exchange.DefaultHoldPolicy(),
		Clock: productruntime.SystemClock{}, InstanceIDs: productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader, Lifecycle: productruntime.DefaultLifecycleOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	capture, scope := createScopedFixtureCapture(t, r)
	const id = "live-content-contract"
	question, err := protocolcore.NewTextBlock("synthetic question")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := protocolcore.NewTextBlock("synthetic response")
	if err != nil {
		t.Fatal(err)
	}
	req := protocolcore.Request{RequestedModel: "synthetic-model", EffectiveModel: "synthetic-model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{question}}}}
	resp := protocolcore.Response{ID: "synthetic-response", RequestedModel: "synthetic-model", EffectiveModel: "synthetic-model", ReportedModel: "synthetic-model", StopReason: protocolcore.StopReasonEndTurn, Blocks: []protocolcore.ContentBlock{answer}}
	conversation, err := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: capture, ExchangeID: id, SourceDisplayName: "fixture-agent", Request: &req})
	if err != nil {
		t.Fatal(err)
	}
	frozen := exchangecontent.FrozenRef{EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1}
	appendScopedFixtureActivity(t, r, scope, activity.Event{Kind: activity.KindExchangeCompleted, SubjectID: id, Status: activity.StatusSucceeded, EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: frozen.EnvironmentDigest, ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "fixture-agent", SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: capture, ConnectionID: "synthetic-connection", Conversation: conversation})
	record, err := exchangecontent.NewRecord(id, frozen, environment.DefaultContentRecordingPolicy(), time.Now().UTC(), req, &resp, exchangecontent.WithParentRef(exchangecontent.ParentRef{CaptureRunID: capture}))
	if err != nil {
		t.Fatal(err)
	}
	appendScopedFixtureRecord(t, r, scope, record)
	app, err := runtimecontrol.New(runtimecontrol.Options{Runtime: r, Readiness: readyState(true), Clock: desktopcontrol.SystemClock{}, ResolveLocalIdentities: false})
	if err != nil {
		t.Fatal(err)
	}
	read, write := capability(0x35), capability(0x36)
	auth, err := desktopcontrol.NewAuthenticator(desktopcontrol.CapabilityGrant{ReadToken: read, WriteToken: write, ExpiresAt: time.Now().Add(10 * time.Minute)}, desktopcontrol.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{Authority: server.Listener.Addr().String(), AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: app, Bootstrap: emptyBootstrap(), CLIControl: http.NotFoundHandler(), ManualCaptures: rejectingManualCaptureHandler{}, DesktopPrincipal: desktopManualPrincipal(t)})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	var continued, rejected atomic.Int32
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := &liveContractWriter{ResponseWriter: w, status: http.StatusOK}
		router.ServeHTTP(tracked, r)
		if r.URL.Query().Get("contentCursor") != "" {
			if tracked.status == http.StatusOK {
				continued.Add(1)
			} else if tracked.status == http.StatusUnprocessableEntity {
				rejected.Add(1)
			}
		}
	})
	server.Start()
	defer server.Close()
	defer server.CloseClientConnections()
	// A real detail read establishes readiness without consuming a continuation.
	httpClient := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/exchanges/"+id+"?contentView=reading&contentMode=paged", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+read)
	request.Header.Set("Origin", "vibermate://desktop")
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var detail desktopcontrol.ExchangeDetail
	decodeErr := json.NewDecoder(response.Body).Decode(&detail)
	response.Body.Close()
	if response.StatusCode != 200 || decodeErr != nil || detail.Content.Page == nil || !strings.Contains(detail.Content.Page.RequestNextCursor, ".") {
		t.Fatalf("production readiness/contract: HTTP %d, %v", response.StatusCode, decodeErr)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	childCtx, stop := context.WithTimeout(t.Context(), 2*time.Minute)
	defer stop()
	args := []string{"test", "--no-pub", "--concurrency=1", "--reporter=expanded", "--dart-define=VIBERMATE_LIVE_CONTENT_REQUIRED=true", "--dart-define=VIBERMATE_LIVE_CONTENT_ENDPOINT=" + server.URL, "--dart-define=VIBERMATE_LIVE_CONTENT_READ_TOKEN=" + read, "--dart-define=VIBERMATE_LIVE_CONTENT_WRITE_TOKEN=" + write, "test/live_content_contract_test.dart"}
	cmd := exec.CommandContext(childCtx, flutter, args...)
	cmd.Dir = filepath.Join(filepath.Dir(file), "..", "..", "ui", "flutter_app")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal("live Flutter consumer failed", err)
	}
	if continued.Load() < 1 || rejected.Load() < 1 {
		t.Fatalf("consumer did not prove both signed continuation and tamper rejection: %d/%d", continued.Load(), rejected.Load())
	}
}

type liveContractWriter struct {
	http.ResponseWriter
	status int
}

func (w *liveContractWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
