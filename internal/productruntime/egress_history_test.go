package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
	"testing"
	"time"
)

func TestActualRuntimeRestoresExactBuiltinV1AssignmentWhileCurrentV2Inherits(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	capture, _ := captureidentity.New(captureidentity.KindManagedRun, "historical-system-v1")
	var historical captureassignment.Assignment
	first, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{environment: func(ctx context.Context, request environmentBuildRequest) (environmentBuildResult, error) {
		compiler, err := productionEnvironmentCompiler(request.accounts, request.endpoints)
		if err != nil {
			return environmentBuildResult{}, err
		}
		old, err := compiler.CompileSystemTransparentRevision(1)
		if err != nil {
			return environmentBuildResult{}, err
		}
		boundary, err := environment.NewLaunchAuthorityBoundary(old)
		if err != nil {
			return environmentBuildResult{}, err
		}
		historical = captureassignment.Assignment{Capture: capture, EnvironmentID: old.ID(), EnvironmentRevision: 1, EnvironmentDigest: old.Digest(), Revision: 1, Source: captureassignment.SourceSystemTransparent, LaunchAuthority: boundary, UpdatedAt: time.Now().UTC().Truncate(time.Millisecond)}
		if result, err := request.assignmentRepository.Write(ctx, 0, historical); err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted {
			t.Fatalf("historical repository fixture: %+v %v", result, err)
		}
		return buildEnvironment(ctx, request)
	}})
	if err != nil {
		t.Fatal(err)
	}
	shutdownRuntime(t, first)
	second := startTestRuntime(t, options)
	defer shutdownRuntime(t, second)
	restored, err := second.CaptureAssignments().Resolve(t.Context(), capture)
	if err != nil || restored != historical {
		t.Fatal("historical assignment relabeled", err)
	}
	origin, _ := originidentity.ParseClientOrigin("https://chatgpt.com")
	manager := second.assignments.(*captureassignment.Manager)
	connection, err := manager.RegisterConnection(t.Context(), capture, "historical-v1-connection", origin)
	if err != nil {
		t.Fatal("actual historical resolver rejected V1 assignment", err)
	}
	defer connection.Close()
	if connection.Environment().Digest().String() != "e67a3f983b3521732f79fdb6de38415b67d5091ac5a943c5a7cca0af7b5e6e6b" {
		t.Fatal("historical builtin digest changed")
	}
	lease, err := manager.BeginRequest(t.Context(), capture, "historical-v1-connection", environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: "/backend-api/codex/responses", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	fact, err := lease.Plan().ResolvedEgress()
	if err != nil || fact.Source() != egressprofile.SelectionFlow || fact.DefaultRevision() != 0 || !fact.Profile().Equal(egressprofile.Direct()) {
		t.Fatal("old explicit assignment began inheriting", err)
	}
	current, err := second.Environments().Get(t.Context(), environment.SystemTransparentID)
	if err != nil || current.Revision() != 2 {
		t.Fatal("current builtin not V2", err)
	}
	if current.Aggregate().ClientEndpoints[0].ProtocolPlans[0].EgressProfile != egressprofile.Inherit() {
		t.Fatal("current builtin remains explicit")
	}
}
