package localca

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type environmentFixture struct {
	environmentID environment.EnvironmentID
	origin        originidentity.ClientOrigin
	aggregate     environment.Environment
}

type environmentProjection struct {
	authority   *Authority
	compiler    environment.Compiler
	projection  *environment.AtomicProjection
	manager     *captureassignment.Manager
	repository  *localAssignmentRepository
	captures    map[environment.EnvironmentID]captureidentity.Reference
	connections map[environment.EnvironmentID]*captureassignment.ConnectionLease
}

func newEnvironmentProjection(
	t *testing.T,
	authority *Authority,
	fixtures ...environmentFixture,
) *environmentProjection {
	t.Helper()
	compiler := newEnvironmentCompiler(t)
	projection := environment.NewAtomicProjection()
	snapshots := make([]environment.EnvironmentSnapshot, 0, len(fixtures))
	for _, fixture := range fixtures {
		snapshot, err := compiler.Compile(fixture.aggregate)
		if err != nil {
			t.Fatalf("compile Environment fixture: %v", err)
		}
		snapshots = append(snapshots, snapshot)
	}
	system, err := compiler.CompileSystemTransparent()
	if err != nil {
		t.Fatalf("compile system_transparent fixture: %v", err)
	}
	if err := projection.Restore(system, snapshots); err != nil {
		t.Fatalf("restore Environment projection: %v", err)
	}
	repository := newLocalAssignmentRepository()
	manager, err := captureassignment.NewManager(captureassignment.Options{
		Repository: repository, Environments: projection,
		Activity: localCaptureActivity{},
		Clock:    authority.clock,
	})
	if err != nil {
		t.Fatalf("construct Capture assignment manager: %v", err)
	}
	harness := &environmentProjection{
		authority: authority, compiler: compiler, projection: projection,
		manager: manager, repository: repository,
		captures:    make(map[environment.EnvironmentID]captureidentity.Reference),
		connections: make(map[environment.EnvironmentID]*captureassignment.ConnectionLease),
	}
	for index, fixture := range fixtures {
		capture, err := captureidentity.New(captureidentity.KindManagedRun, "localca."+fixture.environmentID.String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Create(context.Background(), captureassignment.CreateCommand{
			Capture: capture, EnvironmentID: fixture.environmentID, Source: captureassignment.SourceLaunch,
		}); err != nil {
			t.Fatalf("create Capture assignment %d: %v", index, err)
		}
		connection, err := manager.RegisterConnection(
			context.Background(), capture, "connection."+fixture.environmentID.String(),
			fixture.origin,
		)
		if err != nil {
			t.Fatalf("register Capture connection %d: %v", index, err)
		}
		harness.captures[fixture.environmentID] = capture
		harness.connections[fixture.environmentID] = connection
		t.Cleanup(connection.Close)
	}
	return harness
}

type localCaptureActivity struct{}

func (localCaptureActivity) Active(
	context.Context,
	captureidentity.Reference,
) (bool, error) {
	return true, nil
}

func (harness *environmentProjection) Publish(t *testing.T, fixture environmentFixture) {
	t.Helper()
	candidate, err := harness.compiler.Compile(fixture.aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.projection.Publish(candidate); err != nil {
		t.Fatalf("publish Environment replacement: %v", err)
	}
}

func (harness *environmentProjection) MarkUnavailable(id environment.EnvironmentID) {
	harness.projection.MarkUnavailable(id)
}

func newEnvironmentFixture(
	t *testing.T,
	suffix string,
	revision environment.Revision,
) environmentFixture {
	t.Helper()
	environmentID, err := environment.NewEnvironmentID("environment-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseClientOrigin("https://" + suffix + ".example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := environment.Environment{
		ID: environmentID, Name: "Local CA test Environment", State: environment.StateActive, Revision: revision,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: "endpoint-" + environment.ClientEndpointID(suffix), Revision: revision, ClientOrigin: origin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: "protocol-" + environment.ClientProtocolPlanID(suffix), Revision: revision,
				ClientProtocol:      environment.ClientProtocolAnthropicMessages,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter.localca", Revision: 1},
				Destination:         environment.DestinationPlan{Kind: environment.DestinationKindOriginal},
				EgressProfile:       egressprofile.Direct(),
			}},
		}},
	}
	return environmentFixture{environmentID: environmentID, origin: origin, aggregate: aggregate}
}

func newEnvironmentCompiler(t *testing.T) environment.Compiler {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	pairs := make([]protocolspec.CodecPairDefinition, 0, 2)
	for _, dialect := range []protocolspec.Dialect{
		protocolspec.DialectAnthropicMessages,
		protocolspec.DialectOpenAIResponses,
	} {
		pairID, err := protocolspec.NewCodecPairID("localca." + string(dialect) + ".passthrough")
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, protocolspec.CodecPairDefinition{
			ID: pairID, Revision: 1,
			ClientDialect:      dialect,
			ProviderDialect:    dialect,
			ClientOperationIDs: operations.SemanticOperationIDs(dialect),
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		})
	}
	protocols, err := protocolspec.NewCatalog(
		operations.Definitions(),
		pairs,
	)
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(nil, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

func leafAdmission(
	t *testing.T,
	harness *environmentProjection,
	authority *Authority,
	fixture environmentFixture,
) captureassignment.LeafIssuanceAdmission {
	t.Helper()
	connection := harness.connections[fixture.environmentID]
	if connection == nil {
		t.Fatalf("missing connection for Environment %q", fixture.environmentID)
	}
	san, err := certidentity.NewDNSName(fixture.origin.Host())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := connection.AdmitLeaf(
		context.Background(), authority.Identity().Revision(), san,
		certidentity.LeafKeyAlgorithmECDSAP256,
	)
	if err != nil {
		t.Fatalf("admit leaf issuance: %v", err)
	}
	return admission
}

func mustDNSName(t *testing.T, value string) certidentity.SubjectAlternativeName {
	t.Helper()
	san, err := certidentity.NewDNSName(value)
	if err != nil {
		t.Fatal(err)
	}
	return san
}

type localAssignmentRepository struct {
	mu          sync.Mutex
	assignments map[captureidentity.Reference]captureassignment.Assignment
}

func newLocalAssignmentRepository() *localAssignmentRepository {
	return &localAssignmentRepository{assignments: make(map[captureidentity.Reference]captureassignment.Assignment)}
}

func (repository *localAssignmentRepository) Load(
	_ context.Context,
	reference captureidentity.Reference,
) (captureassignment.Assignment, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	assignment, exists := repository.assignments[reference]
	return assignment, exists, nil
}

func (repository *localAssignmentRepository) ListByEnvironment(
	_ context.Context,
	id environment.EnvironmentID,
	limit int,
) ([]captureassignment.Assignment, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result := make([]captureassignment.Assignment, 0)
	for _, assignment := range repository.assignments {
		if assignment.EnvironmentID == id {
			result = append(result, assignment)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Capture.Key() < result[right].Capture.Key() })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (repository *localAssignmentRepository) Write(
	_ context.Context,
	expected captureassignment.Revision,
	candidate captureassignment.Assignment,
) (captureassignment.CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	current, exists := repository.assignments[candidate.Capture]
	if (!exists && expected != 0) || (exists && current.Revision != expected) {
		actual := captureassignment.Revision(0)
		if exists {
			actual = current.Revision
		}
		return captureassignment.CommitResult{
			Outcome: captureassignment.CommitOutcomeConflict, Assignment: current, Actual: actual,
		}, errors.New("assignment revision conflict")
	}
	repository.assignments[candidate.Capture] = candidate
	return captureassignment.CommitResult{
		Outcome:    captureassignment.CommitOutcomeCommitted,
		Assignment: candidate, Actual: candidate.Revision,
	}, nil
}
