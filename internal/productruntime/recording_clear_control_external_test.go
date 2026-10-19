package productruntime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestRuntimeArchiveClearHolderRefusalAndRequiredCommitIgnoreHeldHistory(t *testing.T) {
	productruntime.RunRecordingClearAuthorityFixture(t, func(r *productruntime.Runtime, authority *toolapproval.Authority, epoch func() uint64) {
		app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: contentBudgetReady{}, Status: r, Environments: r.Environments(), Assignments: r.CaptureAssignments(), Activities: r.Activities(), Contents: r.ExchangeContents(), Connections: r.ConnectionEvents(), Egress: r.EgressAttempts(), Approvals: r.ToolApprovals(), Endpoints: r.UpstreamEndpoints(), Accounts: r.ProviderAccounts(), Offline: r, Clock: desktopcontrol.SystemClock{}, CaptureRuns: r.CaptureRunReader(), ManualCaptures: r.ManualCaptures(), Archive: r.EvidenceArchive(), ArchiveBarrier: r.EvidenceArchiveBarrier()})
		if err != nil {
			t.Fatal(err)
		}
		create := func() capturerun.LaunchGrant {
			t.Helper()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			dir := t.TempDir()
			grant, err := r.CaptureRuns().Create(ctx, capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", CatalogRevision: 1, Lifetime: time.Minute})
			if err != nil {
				t.Fatal("required admission waited for held history", err)
			}
			return grant
		}
		grant := create()
		before := epoch()
		answers := make(chan toolapproval.NetworkAskOutcome, 1)
		failures := make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			v, err := authority.AskNetwork(ctx, toolapproval.NetworkAskRequest{IngressID: grant.Run.ID, Host: "example.test", Port: 443})
			answers <- v
			failures <- err
		}()
		deadline := time.Now().Add(2 * time.Second)
		for {
			page, err := authority.ListApprovals(t.Context(), toolapproval.PageRequest{State: toolapproval.StatePending, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("shared required question not published")
			}
			time.Sleep(time.Millisecond)
		}
		clear := func(key string) desktopcontrol.DeletionResponse {
			t.Helper()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/evidence/actions/clear", nil).WithContext(ctx)
			request.Header.Set("If-Match", "1")
			request.Header.Set("Idempotency-Key", key)
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("real clear route: %d %s", response.Code, response.Body.String())
			}
			var result desktopcontrol.DeletionResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			return result
		}
		refused := clear("clear-live-holder-0001")
		if refused.Deleted || refused.HolderCount != 1 || epoch() != before {
			t.Fatalf("holder refusal mutated clear boundary: %+v epoch=%d/%d", refused, epoch(), before)
		}
		select {
		case <-answers:
			t.Fatal("holder refusal canceled shared question")
		default:
		}
		if err = r.CaptureRuns().Finish(t.Context(), grant.Run.ID, grant.ControlCapability); err != nil {
			t.Fatal(err)
		}
		accepted := clear("clear-accepted-owned-0002")
		if !accepted.Deleted || accepted.Released == nil || accepted.Released.Captures != 1 || !accepted.Released.CleanupPending || !accepted.Released.CountsIncomplete || !accepted.Released.PrivacyMetadataRetained || epoch() != before+1 {
			t.Fatalf("required clear not committed independently: %+v epoch=%d/%d", accepted, epoch(), before)
		}
		select {
		case answer := <-answers:
			if answer.Allowed {
				t.Fatal("pre-clear pending question allowed")
			}
		case <-time.After(time.Second):
			t.Fatal("clear abandoned existing shared waiter")
		}
		if err = <-failures; err != nil {
			t.Fatal(err)
		}
		next := create()
		if err = r.CaptureRuns().Finish(context.Background(), next.Run.ID, next.ControlCapability); err != nil {
			t.Fatal(err)
		}
	})
}
