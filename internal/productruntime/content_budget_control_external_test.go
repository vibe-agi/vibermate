package productruntime_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

type contentBudgetReady struct{}

func (contentBudgetReady) Ready() bool { return true }

func TestRuntimeDefaultFourHTTPAndWaitingUnreadBody(t *testing.T) {
	productruntime.RunDefaultFourHTTPControlFixture(t, 4111, runtimeDefaultControls(t))
}

func TestRuntimeDefaultFourHTTPDrainSmall(t *testing.T) {
	productruntime.RunDefaultFourHTTPControlFixture(t, 32, runtimeDefaultControls(t))
}

func runtimeDefaultControls(t *testing.T) func(*productruntime.Runtime, exchange.ResourcePolicy) error {
	return func(runtime *productruntime.Runtime, policy exchange.ResourcePolicy) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		grant, err := runtime.CaptureRuns().Create(ctx, capturerun.CreateCommand{CWD: t.TempDir(), CanonicalExecutablePath: filepath.Join(t.TempDir(), "codex"), ExecutableLabel: "codex", Lifetime: 5 * time.Minute, CatalogRevision: 1})
		if err != nil {
			return err
		}
		if _, err := runtime.CaptureRuns().Attach(ctx, grant.Run.ID, grant.ControlCapability, 321); err != nil {
			return err
		}
		if _, err := runtime.CaptureRuns().Heartbeat(ctx, grant.Run.ID, grant.ControlCapability, 0); err != nil {
			return err
		}
		app, err := desktopcontrol.New(desktopcontrol.Options{ContentLimits: &policy.Content, Readiness: contentBudgetReady{}, Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}})
		if err != nil {
			return err
		}
		server := httptest.NewServer(app)
		defer server.Close()
		request, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/status", nil)
		if err != nil {
			return err
		}
		response, err := server.Client().Do(request)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if copyErr != nil {
			return copyErr
		}
		if response.StatusCode != 200 {
			return fmt.Errorf("actual status HTTP returned%d", response.StatusCode)
		}
		return nil
	}
}
