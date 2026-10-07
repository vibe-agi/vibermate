package desktopcontrol_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestCandidateRealControlHTTPBypassesFourOccupiedModelSlots(t *testing.T) {
	l := exchangecontent.SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}}, CanonicalBytes: 256 << 20, RetainedBytes: 64 << 20, StructureBytes: 32 << 20}
	reserve, scratch, err := exchange.RequiredResponseReservation(l.Semantic)
	if err != nil {
		t.Fatal(err)
	}
	l.CanonicalBytes += reserve.CanonicalBytes
	l.RetainedBytes += reserve.RetainedBytes
	l.StructureBytes += reserve.StructureBytes
	l.Scratch = scratch
	requestBytes, responseBytes, err := exchange.RequiredExecutionEnvelope(l)
	if err != nil {
		t.Fatal(err)
	}
	slot := requestBytes + responseBytes
	runtime := startRuntimeWithSecrets(t, newCredentialStoreFixture(), exchange.ResourcePolicy{Content: l, RequestBytes: requestBytes, ResponseBytes: responseBytes, SlotBytes: slot, ActiveBytes: 4 * slot})
	defer shutdownRuntime(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	grant, err := runtime.CaptureRuns().Create(ctx, capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "codex"), ExecutableLabel: "codex", Lifetime: 5 * time.Minute, CatalogRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.CaptureRuns().Attach(ctx, grant.Run.ID, grant.ControlCapability, 321); err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.CaptureAssignments().Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: environment.SystemTransparentID, Source: captureassignment.SourceLaunch}); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(runtime.ProxyHandler())
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	proxyURL.User = url.UserPassword("capture", grant.ProxyCapability.Value())
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(runtime.LocalRootCertificate().CertificatePEM())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ExpectContinueTimeout: time.Hour}
	defer transport.CloseIdleConnections()
	entered := make(chan struct{}, 4)
	done := make(chan struct{}, 4)
	for range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			body := &controlHeldModelBody{ctx: ctx, entered: entered}
			request, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/responses", body)
			request.ContentLength = 100
			request.Header.Set("Expect", "100-continue")
			response, _ := (&http.Client{Transport: transport}).Do(request)
			if response != nil {
				response.Body.Close()
			}
		}()
	}
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("model bodies never occupied all four slots")
		}
	}
	app, err := desktopcontrol.New(desktopcontrol.Options{ContentLimits: &l, Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(app)
	defer control.Close()
	requestHTTP, _ := http.NewRequestWithContext(ctx, "GET", control.URL+"/api/v1/status", nil)
	response, err := control.Client().Do(requestHTTP)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("control HTTP blocked or failed: %v status=%d", err, response.StatusCode)
	}
	if _, err := runtime.CaptureRuns().Heartbeat(ctx, grant.Run.ID, grant.ControlCapability, 0); err != nil {
		t.Fatal(err)
	}
	cancel()
	for range 4 {
		<-done
	}
}

type controlHeldModelBody struct {
	ctx     context.Context
	entered chan<- struct{}
	once    sync.Once
}

func (b *controlHeldModelBody) Read([]byte) (int, error) {
	b.once.Do(func() { b.entered <- struct{}{} })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
