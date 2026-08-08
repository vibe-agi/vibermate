package capturegrant

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type recordingManualCaptures struct {
	command manualcapture.CreateCommand
	grant   manualcapture.Grant
	err     error
	calls   int
	revokes int
}

func (controller *recordingManualCaptures) Create(
	_ context.Context,
	command manualcapture.CreateCommand,
) (manualcapture.Grant, error) {
	controller.calls++
	controller.command = command
	return controller.grant, controller.err
}

func (*recordingManualCaptures) Rotate(
	context.Context,
	manualcapture.RotateCommand,
) (manualcapture.Grant, error) {
	return manualcapture.Grant{}, errors.New("unexpected rotate")
}

func (controller *recordingManualCaptures) Revoke(
	_ context.Context,
	_ manualcapture.RevokeCommand,
) (manualcapture.View, error) {
	controller.revokes++
	return controller.grant.Capture, nil
}

func (*recordingManualCaptures) Get(
	context.Context,
	manualcapture.OwnerScope,
	manualcapture.ID,
) (manualcapture.View, error) {
	return manualcapture.View{}, errors.New("unexpected get")
}

func (*recordingManualCaptures) List(
	context.Context,
	manualcapture.PageRequest,
) (manualcapture.Page, error) {
	return manualcapture.Page{}, errors.New("unexpected list")
}

func TestIssueManualCaptureDerivesOwnerFromPrincipal(t *testing.T) {
	t.Parallel()
	credential, err := capturecredential.New(
		capturecredential.KindManualCapture,
		make([]byte, capturecredential.EntropyBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	proxyCredential, err := manualcapture.NewProxyCredential(credential.Value())
	if err != nil {
		t.Fatal(err)
	}
	manuals := &recordingManualCaptures{grant: manualcapture.Grant{
		Capture:    manualcapture.View{ID: "manual-one"},
		Credential: proxyCredential,
	}}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(
			filepath.Join(t.TempDir(), "ca"),
			context.Background(),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := authority.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown local CA: %v", err)
		}
	})
	generation := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	issuer := &Issuer{
		manuals:     manuals,
		authorities: fixedManualAuthorities(t, []string{"api.anthropic.com:443"}),
		proxyOrigin: "http://127.0.0.1:41080",
		generation:  generation,
		rootID:      authority.Identity(),
		root:        authority.Certificate(),
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                    "enrolled:one",
		Kind:                  controlprincipal.KindEnrolledClient,
		ProxyClientBindingID:  "binding-one",
		MachineRegistrationID: "machine-one",
		CredentialRevision:    1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantManualCapture,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	captureContext, err := issuer.GetManualCaptureContext(
		context.Background(),
		principal,
		"work",
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := issuer.IssueManualCapture(
		context.Background(),
		principal,
		ManualCaptureRequest{
			EnvironmentID:     "work",
			DisplayName:       "Remote desktop",
			ClientClass:       manualcapture.ClientDesktopApp,
			Lifetime:          manualcapture.LifetimeUntilRevoked,
			ConfirmationToken: captureContext.ConfirmationToken,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	bindingID, ok := manuals.command.Owner.ProxyClientBindingID()
	if manuals.calls != 1 || !ok || bindingID != "binding-one" ||
		grant.Context.ProxyAddress != issuer.proxyOrigin ||
		grant.Context.ConfirmationToken != captureContext.ConfirmationToken ||
		grant.Context.RootIdentity != authority.Identity() ||
		grant.Context.RootCertificate.Path() != authority.Certificate().Path() {
		t.Fatalf(
			"command=%+v grant=%+v controller calls=%d",
			manuals.command,
			grant,
			manuals.calls,
		)
	}
}

type manualAuthorities struct {
	assignment captureassignment.Assignment
}

func (authority manualAuthorities) Review(
	context.Context,
	environment.EnvironmentID,
) (CaptureAuthorityReview, error) {
	set, err := NewCaptureAuthoritySet(authority.assignment)
	if err != nil {
		return CaptureAuthorityReview{}, err
	}
	return set.Review(), nil
}

func (authority manualAuthorities) AssignAndResolve(
	_ context.Context,
	capture captureidentity.Reference,
	environmentID environment.EnvironmentID,
) (CaptureAuthoritySet, error) {
	assignment := authority.assignment
	assignment.Capture = capture
	assignment.EnvironmentID = environmentID
	assignment.Source = captureassignment.SourceManualCreate
	return NewCaptureAuthoritySet(assignment)
}

func (authority manualAuthorities) Resolve(
	context.Context,
	captureidentity.Reference,
) (CaptureAuthoritySet, error) {
	return NewCaptureAuthoritySet(authority.assignment)
}

func fixedManualAuthorities(t *testing.T, protected []string) CaptureAuthorityResolver {
	t.Helper()
	return manualAuthorities{assignment: captureAuthorityAssignment(t, protected, nil)}
}

func captureAuthorityAssignmentForEnvironment(
	t *testing.T,
	environmentID environment.EnvironmentID,
	protected []string,
	managed []string,
) captureassignment.Assignment {
	t.Helper()
	assignment := captureAuthorityAssignment(t, protected, managed)
	digest := assignment.LaunchAuthority.InitialEnvironmentDigest()
	boundary, err := environment.NewLaunchAuthorityBoundaryFromScopes(
		environmentID, 1, digest, protected, managed,
	)
	if err != nil {
		t.Fatal(err)
	}
	assignment.EnvironmentID = environmentID
	assignment.LaunchAuthority = boundary
	return assignment
}

func TestIssueManualCaptureRejectsUnauthorizedPrincipalBeforeDependencies(
	t *testing.T,
) {
	t.Parallel()
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "local-cli:run-only",
		Kind:               controlprincipal.KindLocalCLI,
		CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Issuer{}).IssueManualCapture(
		context.Background(),
		principal,
		ManualCaptureRequest{},
	); !errors.Is(err, ErrPrincipalUnauthorized) {
		t.Fatalf("IssueManualCapture() error = %v", err)
	}
}

func TestManualCaptureContextDoesNotDeliverRootForTransparentEnvironment(t *testing.T) {
	t.Parallel()
	issuer, authority := manualCaptureTestIssuer(
		t,
		&recordingManualCaptures{},
		manualAuthorities{assignment: captureAuthorityAssignmentForEnvironment(
			t, "system_transparent", nil, nil,
		)},
	)
	context, err := issuer.GetManualCaptureContext(
		context.Background(), manualCapturePrincipal(t), "system_transparent",
	)
	if err != nil {
		t.Fatal(err)
	}
	if context.DeliverRoot || context.RootIdentity.Valid() || context.RootCertificate.Valid() ||
		len(context.ProtectedAuthorities) != 0 || context.EnvironmentID != "system_transparent" {
		t.Fatalf("transparent context exposed Root authority: %+v", context)
	}
	_ = authority
}

func TestManualCaptureUsesEnvironmentAssignmentAuthorityForTransparentAndSemantic(t *testing.T) {
	t.Parallel()

	semantic := semanticEnvironmentSnapshot(t)
	resolver := &manualSnapshotResolver{snapshots: map[environment.EnvironmentID]environment.EnvironmentSnapshot{
		semantic.ID(): semantic,
	}}
	for _, test := range []struct {
		name           string
		environmentID  environment.EnvironmentID
		manualID       string
		wantRoot       bool
		wantProtection []string
	}{
		{name: "system transparent", environmentID: environment.SystemTransparentID, manualID: "manual-transparent"},
		{name: "semantic", environmentID: semantic.ID(), manualID: "manual-semantic", wantRoot: true, wantProtection: []string{"api.example:443"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &manualAssignmentRepository{}
			assignments, err := captureassignment.NewManager(captureassignment.DefaultOptions(repository, resolver))
			if err != nil {
				t.Fatal(err)
			}
			authorities, err := NewEnvironmentAuthorityResolver(assignments, resolver)
			if err != nil {
				t.Fatal(err)
			}
			credential, err := capturecredential.New(
				capturecredential.KindManualCapture,
				make([]byte, capturecredential.EntropyBytes),
			)
			if err != nil {
				t.Fatal(err)
			}
			proxyCredential, err := manualcapture.NewProxyCredential(credential.Value())
			if err != nil {
				t.Fatal(err)
			}
			manuals := &recordingManualCaptures{grant: manualcapture.Grant{
				Capture:    manualcapture.View{ID: test.manualID, CredentialRevision: 1},
				Credential: proxyCredential,
			}}
			issuer, _ := manualCaptureTestIssuer(t, manuals, authorities)
			principal := manualCapturePrincipal(t)
			review, err := issuer.GetManualCaptureContext(context.Background(), principal, test.environmentID)
			if err != nil {
				t.Fatal(err)
			}
			if review.DeliverRoot != test.wantRoot ||
				!slices.Equal(review.ProtectedAuthorities, test.wantProtection) {
				t.Fatalf("review=%+v", review)
			}
			grant, err := issuer.IssueManualCapture(context.Background(), principal, ManualCaptureRequest{
				EnvironmentID: test.environmentID, DisplayName: test.name,
				ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked,
				ConfirmationToken: review.ConfirmationToken,
			})
			if err != nil {
				t.Fatal(err)
			}
			if grant.Authority.AssignmentRevision() != 1 ||
				grant.Authority.EnvironmentID() != test.environmentID ||
				grant.Context.DeliverRoot != test.wantRoot ||
				!slices.Equal(grant.Authority.ProtectedAuthorities(), test.wantProtection) {
				t.Fatalf("grant=%+v", grant)
			}
		})
	}
}

func TestManualCaptureReviewFailsClosedWhenEnvironmentChangesBeforeCreate(t *testing.T) {
	t.Parallel()
	oldAssignment := captureAuthorityAssignment(t, []string{"api.anthropic.com:443"}, nil)
	newAssignment := assignmentWithBoundaryRevision(t, oldAssignment, 2, []string{"api.anthropic.com:443"})
	authorities := &sequencedManualAuthorities{reviews: []captureassignment.Assignment{oldAssignment, newAssignment}}
	manuals := &recordingManualCaptures{}
	issuer, _ := manualCaptureTestIssuer(t, manuals, authorities)
	principal := manualCapturePrincipal(t)
	review, err := issuer.GetManualCaptureContext(context.Background(), principal, "work")
	if err != nil {
		t.Fatal(err)
	}
	_, err = issuer.IssueManualCapture(context.Background(), principal, ManualCaptureRequest{
		EnvironmentID: "work", DisplayName: "Terminal", ClientClass: manualcapture.ClientCLI,
		Lifetime: manualcapture.LifetimeUntilRevoked, ConfirmationToken: review.ConfirmationToken,
	})
	if !errors.Is(err, ErrManualCaptureConflict) || manuals.calls != 0 || manuals.revokes != 0 {
		t.Fatalf("stale review error=%v creates=%d revokes=%d", err, manuals.calls, manuals.revokes)
	}
}

func TestManualCaptureRevokesCredentialWhenAssignmentBoundaryChangesAfterCreate(t *testing.T) {
	t.Parallel()
	credential, err := capturecredential.New(
		capturecredential.KindManualCapture,
		make([]byte, capturecredential.EntropyBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	proxyCredential, err := manualcapture.NewProxyCredential(credential.Value())
	if err != nil {
		t.Fatal(err)
	}
	oldAssignment := captureAuthorityAssignment(t, []string{"api.anthropic.com:443"}, nil)
	newAssignment := assignmentWithBoundaryRevision(t, oldAssignment, 2, []string{"api.anthropic.com:443"})
	authorities := &sequencedManualAuthorities{
		reviews:  []captureassignment.Assignment{oldAssignment, oldAssignment},
		assigned: newAssignment,
	}
	manuals := &recordingManualCaptures{grant: manualcapture.Grant{
		Capture:    manualcapture.View{ID: "manual-one", CredentialRevision: 1},
		Credential: proxyCredential,
	}}
	issuer, _ := manualCaptureTestIssuer(t, manuals, authorities)
	principal := manualCapturePrincipal(t)
	review, err := issuer.GetManualCaptureContext(context.Background(), principal, "work")
	if err != nil {
		t.Fatal(err)
	}
	_, err = issuer.IssueManualCapture(context.Background(), principal, ManualCaptureRequest{
		EnvironmentID: "work", DisplayName: "Terminal", ClientClass: manualcapture.ClientCLI,
		Lifetime: manualcapture.LifetimeUntilRevoked, ConfirmationToken: review.ConfirmationToken,
	})
	if !errors.Is(err, ErrManualCaptureConflict) || manuals.calls != 1 || manuals.revokes != 1 {
		t.Fatalf("changed assignment error=%v creates=%d revokes=%d", err, manuals.calls, manuals.revokes)
	}
}

type sequencedManualAuthorities struct {
	mu       sync.Mutex
	reviews  []captureassignment.Assignment
	assigned captureassignment.Assignment
}

func (authority *sequencedManualAuthorities) Review(
	context.Context,
	environment.EnvironmentID,
) (CaptureAuthorityReview, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if len(authority.reviews) == 0 {
		return CaptureAuthorityReview{}, errors.New("no review fixture")
	}
	assignment := authority.reviews[0]
	if len(authority.reviews) > 1 {
		authority.reviews = authority.reviews[1:]
	}
	set, err := NewCaptureAuthoritySet(assignment)
	return set.Review(), err
}

func (authority *sequencedManualAuthorities) AssignAndResolve(
	_ context.Context,
	capture captureidentity.Reference,
	environmentID environment.EnvironmentID,
) (CaptureAuthoritySet, error) {
	assignment := authority.assigned
	assignment.Capture = capture
	assignment.EnvironmentID = environmentID
	assignment.Source = captureassignment.SourceManualCreate
	return NewCaptureAuthoritySet(assignment)
}

func (authority *sequencedManualAuthorities) Resolve(
	context.Context,
	captureidentity.Reference,
) (CaptureAuthoritySet, error) {
	return CaptureAuthoritySet{}, errors.New("unexpected resolve")
}

func assignmentWithBoundaryRevision(
	t *testing.T,
	assignment captureassignment.Assignment,
	revision environment.Revision,
	protected []string,
) captureassignment.Assignment {
	t.Helper()
	digest := assignment.LaunchAuthority.InitialEnvironmentDigest()
	digest[0]++
	boundary, err := environment.NewLaunchAuthorityBoundaryFromScopes(
		assignment.EnvironmentID, revision, digest, protected, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	assignment.LaunchAuthority = boundary
	return assignment
}

func manualCapturePrincipal(t *testing.T) controlprincipal.Principal {
	t.Helper()
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID: "local-cli:manual-test", Kind: controlprincipal.KindLocalCLI,
		CredentialRevision: 1,
		AllowedGrantKinds:  []controlprincipal.GrantKind{controlprincipal.GrantManualCapture},
	})
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func manualCaptureTestIssuer(
	t *testing.T,
	manuals manualcapture.Controller,
	authorities CaptureAuthorityResolver,
) (*Issuer, *localca.Authority) {
	t.Helper()
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(filepath.Join(t.TempDir(), "ca"), context.Background()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := authority.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown local CA: %v", err)
		}
	})
	return &Issuer{
		manuals: manuals, authorities: authorities,
		proxyOrigin: "http://127.0.0.1:41080",
		generation:  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)),
		rootID:      authority.Identity(), root: authority.Certificate(),
	}, authority
}

type manualSnapshotResolver struct {
	snapshots map[environment.EnvironmentID]environment.EnvironmentSnapshot
}

func (resolver *manualSnapshotResolver) Resolve(
	id environment.EnvironmentID,
) (environment.EnvironmentSnapshot, error) {
	if id == environment.SystemTransparentID {
		return environment.SystemTransparentSnapshot(), nil
	}
	snapshot, exists := resolver.snapshots[id]
	if !exists {
		return environment.EnvironmentSnapshot{}, environment.ErrEnvironmentNotFound
	}
	return snapshot, nil
}

func (resolver *manualSnapshotResolver) ResolveClientOrigin(
	id environment.EnvironmentID,
	origin originidentity.ClientOrigin,
) (environment.ClientEndpointSnapshot, error) {
	snapshot, err := resolver.Resolve(id)
	if err != nil {
		return environment.ClientEndpointSnapshot{}, err
	}
	endpoint, exists := snapshot.LookupClientOrigin(origin)
	if !exists {
		return environment.ClientEndpointSnapshot{}, environment.ErrEnvironmentNotFound
	}
	return endpoint, nil
}

type manualAssignmentRepository struct {
	mu         sync.Mutex
	assignment captureassignment.Assignment
	exists     bool
}

func (repository *manualAssignmentRepository) Load(
	_ context.Context,
	reference captureidentity.Reference,
) (captureassignment.Assignment, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.exists || repository.assignment.Capture != reference {
		return captureassignment.Assignment{}, false, nil
	}
	return repository.assignment, true, nil
}

func (repository *manualAssignmentRepository) ListByEnvironment(
	context.Context,
	environment.EnvironmentID,
	int,
) ([]captureassignment.Assignment, error) {
	return nil, nil
}

func (repository *manualAssignmentRepository) Write(
	_ context.Context,
	expected captureassignment.Revision,
	candidate captureassignment.Assignment,
) (captureassignment.CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	actual := captureassignment.Revision(0)
	if repository.exists {
		actual = repository.assignment.Revision
	}
	if actual != expected {
		return captureassignment.CommitResult{
			Outcome:    captureassignment.CommitOutcomeConflict,
			Assignment: repository.assignment, Actual: actual,
		}, nil
	}
	repository.assignment = candidate
	repository.exists = true
	return captureassignment.CommitResult{
		Outcome:    captureassignment.CommitOutcomeCommitted,
		Assignment: candidate, Actual: candidate.Revision,
	}, nil
}

func semanticEnvironmentSnapshot(t *testing.T) environment.EnvironmentSnapshot {
	t.Helper()
	clientOrigin, err := originidentity.ParseClientOrigin("https://api.example")
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := originidentity.ParseProviderOrigin(clientOrigin.String())
	if err != nil {
		t.Fatal(err)
	}
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	pairID, err := protocolspec.NewCodecPairID("test.manual.anthropic")
	if err != nil {
		t.Fatal(err)
	}
	protocols, err := protocolspec.NewCatalog(
		operations.Definitions(),
		[]protocolspec.CodecPairDefinition{{
			ID: pairID, Revision: 1,
			ClientDialect:      protocolspec.DialectAnthropicMessages,
			ProviderDialect:    protocolspec.DialectAnthropicMessages,
			ClientOperationIDs: operations.SemanticOperationIDs(protocolspec.DialectAnthropicMessages),
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	returnValue, err := compiler.Compile(environment.Environment{
		ID: "work", Name: "Work", State: environment.StateActive, Revision: 1,
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: "endpoint.api", Revision: 1, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: "plan.messages", Revision: 1,
				ClientProtocol:      environment.ClientProtocolAnthropicMessages,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter.claude", Revision: 1},
				Mode:                environment.PlanModeManaged,
				UpstreamPlan: environment.UpstreamPlan{
					DefaultRouteID: "route.original",
					RouteSet:       environment.RouteSet{ID: "routes.original", Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{"route.original"}},
					Routes: []environment.UpstreamRoute{{
						ID: "route.original", Revision: 1,
						ProviderTarget: environment.ProviderTarget{
							ID: "target.original", Revision: 1, Origin: providerOrigin,
							RealmID: "realm.anthropic",
							Capabilities: []protocolspec.ProviderCapability{
								protocolspec.ProviderCapabilityMessages,
								protocolspec.ProviderCapabilityStreaming,
								protocolspec.ProviderCapabilityToolCalls,
							},
						},
						BackendProtocol: "anthropic_messages",
						AccountPolicy: environment.RouteAccountPolicy{
							Revision: 1, Mode: environment.AccountModeClientPassthrough,
							AllowedRealmIDs: []string{"realm.anthropic"}, FailoverPolicy: environment.FailoverOff,
						},
						ModelPolicy:    environment.ModelPolicy{Revision: 1, Mode: "passthrough"},
						WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
					}},
				},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return returnValue
}
