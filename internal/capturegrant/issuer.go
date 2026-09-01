// Package capturegrant owns control-authorized creation of data-plane capture
// grants. HTTP handlers authenticate and decode; this package decides whether
// a typed principal may create a grant and performs the Core issuance work.
package capturegrant

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const (
	maxArguments     = 256
	maxArgumentBytes = 32 << 10
)

var (
	ErrPrincipalUnauthorized    = errors.New("control principal is not authorized for the grant")
	ErrInvalidCaptureRun        = errors.New("CaptureRun request is invalid")
	ErrAdapterVerification      = errors.New("client adapter verification failed")
	ErrEnvironmentNotFound      = errors.New("selected Environment is not configured")
	ErrEnvironmentUnavailable   = errors.New("selected Environment is unavailable")
	ErrProjectionUnavailable    = errors.New("Environment projection is unavailable")
	ErrWorkspaceUnavailable     = errors.New("workspace identity is unavailable")
	ErrCaptureRunCreate         = errors.New("CaptureRun creation failed")
	ErrInvalidManualCapture     = errors.New("ManualCapture request is invalid")
	ErrManualCaptureCreate      = errors.New("ManualCapture creation failed")
	ErrManualCaptureNotFound    = errors.New("ManualCapture was not found")
	ErrManualCaptureConflict    = errors.New("ManualCapture state conflicts with the request")
	ErrManualCaptureUnavailable = errors.New(
		"ManualCapture authority is unavailable",
	)
)

type ClientRootApprovals interface {
	AskClientRoot(
		context.Context,
		toolapproval.ClientRootAskRequest,
	) (toolapproval.ClientRootAskOutcome, error)
}

// WorkspaceResolver is deliberately principal-aware. Desktop resolves a local
// cwd; a future enrolled companion must validate its registered machine scope
// instead of accidentally deriving a workspace on the Server filesystem.
type WorkspaceResolver interface {
	ResolveCaptureRun(
		context.Context,
		controlprincipal.Principal,
		string,
		workspaceidentity.Scope,
	) (workspaceidentity.Scope, error)
}

type localWorkspaceResolver struct {
	resolver workspaceidentity.LocalResolver
}

func NewLocalWorkspaceResolver(
	resolver workspaceidentity.LocalResolver,
) (WorkspaceResolver, error) {
	if resolver == nil {
		return nil, errors.New("local workspace resolver is unavailable")
	}
	return localWorkspaceResolver{resolver: resolver}, nil
}

func (resolver localWorkspaceResolver) ResolveCaptureRun(
	ctx context.Context,
	principal controlprincipal.Principal,
	cwd string,
	companion workspaceidentity.Scope,
) (workspaceidentity.Scope, error) {
	if !principal.Valid() || principal.Kind() != controlprincipal.KindLocalCLI ||
		companion != (workspaceidentity.Scope{}) {
		return workspaceidentity.Scope{}, ErrWorkspaceUnavailable
	}
	return resolver.resolver.ResolveLocal(ctx, cwd)
}

type companionWorkspaceResolver struct{}

func NewCompanionWorkspaceResolver() WorkspaceResolver {
	return companionWorkspaceResolver{}
}

func (companionWorkspaceResolver) ResolveCaptureRun(
	ctx context.Context,
	principal controlprincipal.Principal,
	_ string,
	scope workspaceidentity.Scope,
) (workspaceidentity.Scope, error) {
	if ctx == nil || !principal.Valid() ||
		(principal.Kind() != controlprincipal.KindEnrolledClient &&
			principal.Kind() != controlprincipal.KindRuntimeUser) ||
		scope.Validate() != nil ||
		scope.Evidence() != workspaceidentity.EvidenceRegisteredCompanion {
		return workspaceidentity.Scope{}, ErrWorkspaceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return workspaceidentity.Scope{}, err
	}
	if principal.Kind() == controlprincipal.KindRuntimeUser {
		machineID, ok := principal.MachineID()
		if !ok || machineID != scope.MachineID().String() {
			return workspaceidentity.Scope{}, ErrWorkspaceUnavailable
		}
	} else {
		machineRegistrationID, ok := principal.MachineRegistrationID()
		if !ok || machineRegistrationID != "machine."+scope.MachineID().String() {
			return workspaceidentity.Scope{}, ErrWorkspaceUnavailable
		}
	}
	return scope, nil
}

type Options struct {
	Runs                capturerun.Controller
	ManualCaptures      manualcapture.Controller
	Verifier            clientadapter.Verifier
	Authorities         CaptureAuthorityResolver
	ProxyOrigin         string
	Generation          string
	RootIdentity        localca.RootIdentity
	Root                localca.RootCertificate
	RunLifetime         time.Duration
	Workspaces          WorkspaceResolver
	ClientRootApprovals ClientRootApprovals
	CompanionCatalog    clientadapter.Catalog
	ProxyDelivery       ProxyDelivery
}

// ProxyDelivery says where the launcher obtains its process-local HTTP proxy.
// A Desktop launch receives the already-listening loopback origin. A remote
// companion creates its own loopback listener and relays that byte stream over
// the authenticated TLS connection to the Server.
type ProxyDelivery string

const (
	ProxyDeliveryLocalListener ProxyDelivery = "local_listener"
	ProxyDeliveryClientRelay   ProxyDelivery = "client_tls_relay"
)

func (delivery ProxyDelivery) Valid() bool {
	return delivery == ProxyDeliveryLocalListener ||
		delivery == ProxyDeliveryClientRelay
}

// Issuer is the single Core owner of CaptureRun and ManualCapture grant
// creation. Transport handlers authenticate and decode; neither may recreate
// ownership, Root delivery, or proxy grant policy.
type Issuer struct {
	runs        capturerun.Controller
	manuals     manualcapture.Controller
	verifier    clientadapter.Verifier
	authorities CaptureAuthorityResolver
	proxyOrigin string
	generation  string
	rootID      localca.RootIdentity
	root        localca.RootCertificate
	runLifetime time.Duration
	workspaces  WorkspaceResolver
	rootAsk     ClientRootApprovals
	companions  clientadapter.Catalog
	proxy       ProxyDelivery
}

func New(options Options) (*Issuer, error) {
	if options.Runs == nil ||
		options.ManualCaptures == nil ||
		options.Verifier == nil ||
		options.Authorities == nil ||
		options.RunLifetime <= 0 ||
		options.Workspaces == nil {
		return nil, errors.New("capture grant issuer dependencies are incomplete")
	}
	if !options.ProxyDelivery.Valid() {
		return nil, errors.New("capture grant proxy delivery is invalid")
	}
	switch options.ProxyDelivery {
	case ProxyDeliveryLocalListener:
		if err := validateProxyOrigin(options.ProxyOrigin); err != nil {
			return nil, err
		}
	case ProxyDeliveryClientRelay:
		if options.ProxyOrigin != "" {
			return nil, errors.New("remote capture grant carries a Server-local proxy origin")
		}
	}
	if !validGeneration(options.Generation) ||
		!options.RootIdentity.Valid() ||
		!options.Root.Valid() ||
		!rootDeliveryMatches(options.RootIdentity, options.Root) {
		return nil, errors.New("capture grant public Root delivery is incomplete")
	}
	return &Issuer{
		runs:        options.Runs,
		manuals:     options.ManualCaptures,
		verifier:    options.Verifier,
		authorities: options.Authorities,
		proxyOrigin: options.ProxyOrigin,
		generation:  options.Generation,
		rootID:      options.RootIdentity,
		root:        options.Root,
		runLifetime: options.RunLifetime,
		workspaces:  options.Workspaces,
		rootAsk:     options.ClientRootApprovals,
		companions:  options.CompanionCatalog,
		proxy:       options.ProxyDelivery,
	}, nil
}

type ManualCaptureRequest struct {
	EnvironmentID     environment.EnvironmentID
	DisplayName       string
	ClientClass       manualcapture.ClientClass
	Lifetime          manualcapture.Lifetime
	ExpiresIn         time.Duration
	ConfirmationToken string
}

type ManualCaptureContext struct {
	ConfirmationToken        string
	ProxyAddress             string
	EnvironmentID            environment.EnvironmentID
	EnvironmentRevision      environment.Revision
	EnvironmentDigest        environment.CandidateDigest
	LaunchAuthorityDigest    environment.LaunchAuthorityDigest
	ProtectedAuthorities     []string
	ManagedAuthorities       []string
	DeliverRoot              bool
	RootIdentity             localca.RootIdentity
	RootCertificate          localca.RootCertificate
	DefaultTemporaryLifetime time.Duration
	MaximumTemporaryLifetime time.Duration
}

type ManualCaptureGrant struct {
	Capture   manualcapture.Grant
	Authority CaptureAuthoritySet
	Context   ManualCaptureContext
}

type ManualCaptureRotateRequest struct {
	ID                         manualcapture.ID
	ExpectedCredentialRevision manualcapture.CredentialRevision
}

type ManualCaptureRevokeRequest struct {
	ID                         manualcapture.ID
	ExpectedCredentialRevision manualcapture.CredentialRevision
}

func (issuer *Issuer) GetManualCaptureContext(
	ctx context.Context,
	principal controlprincipal.Principal,
	environmentID environment.EnvironmentID,
) (ManualCaptureContext, error) {
	owner, err := issuer.manualCaptureOwner(ctx, principal)
	if err != nil {
		return ManualCaptureContext{}, err
	}
	review, err := issuer.authorities.Review(ctx, environmentID)
	if err != nil {
		return ManualCaptureContext{}, ErrProjectionUnavailable
	}
	return issuer.manualCaptureContext(owner, review), nil
}

func (issuer *Issuer) IssueManualCapture(
	ctx context.Context,
	principal controlprincipal.Principal,
	request ManualCaptureRequest,
) (ManualCaptureGrant, error) {
	owner, err := issuer.manualCaptureOwner(ctx, principal)
	if err != nil {
		return ManualCaptureGrant{}, err
	}
	if request.DisplayName == "" ||
		!request.ClientClass.Valid() || !request.Lifetime.Valid() ||
		request.ConfirmationToken == "" {
		return ManualCaptureGrant{}, ErrInvalidManualCapture
	}
	review, err := issuer.authorities.Review(ctx, request.EnvironmentID)
	if err != nil {
		return ManualCaptureGrant{}, ErrProjectionUnavailable
	}
	if !sameManualCaptureConfirmation(
		request.ConfirmationToken,
		issuer.manualCaptureConfirmation(owner, review),
	) {
		return ManualCaptureGrant{}, ErrManualCaptureConflict
	}
	grant, err := issuer.manuals.Create(ctx, manualcapture.CreateCommand{
		Owner:       owner,
		DisplayName: request.DisplayName,
		ClientClass: request.ClientClass,
		Lifetime:    request.Lifetime,
		ExpiresIn:   request.ExpiresIn,
	})
	if err != nil {
		return ManualCaptureGrant{}, classifyManualCaptureError(
			err,
			ErrManualCaptureCreate,
		)
	}
	capture, err := captureidentity.New(
		captureidentity.KindManualCapture,
		grant.Capture.ID,
	)
	if err != nil {
		return ManualCaptureGrant{}, issuer.revokeUnusedManualCapture(
			ctx, owner, grant, ErrManualCaptureCreate,
		)
	}
	authority, err := issuer.authorities.AssignAndResolve(
		ctx, capture, request.EnvironmentID, captureassignment.SourceManualCreate,
		clienttarget.Profile{},
	)
	if err != nil {
		return ManualCaptureGrant{}, issuer.revokeUnusedManualCapture(
			ctx, owner, grant, errors.Join(ErrManualCaptureCreate, err),
		)
	}
	if authority.AuthorityDigest() != review.AuthorityDigest() ||
		authority.InitialEnvironmentID() != review.EnvironmentID() ||
		authority.InitialEnvironmentRevision() != review.EnvironmentRevision() ||
		authority.InitialEnvironmentDigest() != review.EnvironmentDigest() {
		return ManualCaptureGrant{}, issuer.revokeUnusedManualCapture(
			ctx, owner, grant, ErrManualCaptureConflict,
		)
	}
	return ManualCaptureGrant{
		Capture: grant, Authority: authority,
		Context: issuer.manualCaptureContext(owner, authority.Review()),
	}, nil
}

func (issuer *Issuer) ListManualCaptures(
	ctx context.Context,
	principal controlprincipal.Principal,
	limit int,
) (manualcapture.Page, error) {
	owner, err := issuer.manualCaptureOwner(ctx, principal)
	if err != nil {
		return manualcapture.Page{}, err
	}
	page, err := issuer.manuals.List(ctx, manualcapture.PageRequest{
		Owner: owner,
		Limit: limit,
	})
	if err != nil {
		return manualcapture.Page{}, classifyManualCaptureError(
			err,
			ErrManualCaptureUnavailable,
		)
	}
	return page, nil
}

func (issuer *Issuer) GetManualCapture(
	ctx context.Context,
	principal controlprincipal.Principal,
	id manualcapture.ID,
) (manualcapture.View, error) {
	owner, err := issuer.manualCaptureOwner(ctx, principal)
	if err != nil {
		return manualcapture.View{}, err
	}
	if !id.Valid() {
		return manualcapture.View{}, ErrInvalidManualCapture
	}
	view, err := issuer.manuals.Get(ctx, owner, id)
	if err != nil {
		return manualcapture.View{}, classifyManualCaptureError(
			err,
			ErrManualCaptureUnavailable,
		)
	}
	return view, nil
}

func (issuer *Issuer) RotateManualCapture(
	ctx context.Context,
	principal controlprincipal.Principal,
	request ManualCaptureRotateRequest,
) (ManualCaptureGrant, error) {
	owner, err := issuer.manualCaptureOwner(ctx, principal)
	if err != nil {
		return ManualCaptureGrant{}, err
	}
	if !request.ID.Valid() ||
		!request.ExpectedCredentialRevision.Valid() {
		return ManualCaptureGrant{}, ErrInvalidManualCapture
	}
	grant, err := issuer.manuals.Rotate(ctx, manualcapture.RotateCommand{
		Owner:                      owner,
		ID:                         request.ID,
		ExpectedCredentialRevision: request.ExpectedCredentialRevision,
	})
	if err != nil {
		return ManualCaptureGrant{}, classifyManualCaptureError(
			err,
			ErrManualCaptureUnavailable,
		)
	}
	capture, err := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	if err != nil {
		return ManualCaptureGrant{}, ErrManualCaptureUnavailable
	}
	authority, err := issuer.authorities.Resolve(ctx, capture)
	if err != nil {
		return ManualCaptureGrant{}, ErrManualCaptureUnavailable
	}
	return ManualCaptureGrant{
		Capture: grant, Authority: authority,
		Context: issuer.manualCaptureContext(owner, authority.Review()),
	}, nil
}

func (issuer *Issuer) RevokeManualCapture(
	ctx context.Context,
	principal controlprincipal.Principal,
	request ManualCaptureRevokeRequest,
) (manualcapture.View, error) {
	owner, err := issuer.manualCaptureOwner(ctx, principal)
	if err != nil {
		return manualcapture.View{}, err
	}
	if !request.ID.Valid() ||
		!request.ExpectedCredentialRevision.Valid() {
		return manualcapture.View{}, ErrInvalidManualCapture
	}
	view, err := issuer.manuals.Revoke(ctx, manualcapture.RevokeCommand{
		Owner:                      owner,
		ID:                         request.ID,
		ExpectedCredentialRevision: request.ExpectedCredentialRevision,
	})
	if err != nil {
		return manualcapture.View{}, classifyManualCaptureError(
			err,
			ErrManualCaptureUnavailable,
		)
	}
	return view, nil
}

func (issuer *Issuer) manualCaptureOwner(
	ctx context.Context,
	principal controlprincipal.Principal,
) (manualcapture.OwnerScope, error) {
	if issuer == nil || issuer.manuals == nil || ctx == nil ||
		!principal.Valid() ||
		!principal.Allows(controlprincipal.GrantManualCapture) {
		return manualcapture.OwnerScope{}, ErrPrincipalUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return manualcapture.OwnerScope{}, err
	}
	switch principal.Kind() {
	case controlprincipal.KindDesktopApp, controlprincipal.KindLocalCLI:
		return manualcapture.NewLocalOwnerScope(), nil
	case controlprincipal.KindEnrolledClient:
		bindingID, ok := principal.ProxyClientBindingID()
		if !ok {
			return manualcapture.OwnerScope{}, ErrPrincipalUnauthorized
		}
		owner, err := manualcapture.NewProxyClientOwnerScope(bindingID)
		if err != nil {
			return manualcapture.OwnerScope{}, ErrPrincipalUnauthorized
		}
		return owner, nil
	default:
		return manualcapture.OwnerScope{}, ErrPrincipalUnauthorized
	}
}

func (issuer *Issuer) manualCaptureContext(
	owner manualcapture.OwnerScope,
	review CaptureAuthorityReview,
) ManualCaptureContext {
	context := ManualCaptureContext{
		ConfirmationToken:        issuer.manualCaptureConfirmation(owner, review),
		ProxyAddress:             issuer.proxyOrigin,
		EnvironmentID:            review.EnvironmentID(),
		EnvironmentRevision:      review.EnvironmentRevision(),
		EnvironmentDigest:        review.EnvironmentDigest(),
		LaunchAuthorityDigest:    review.AuthorityDigest(),
		ProtectedAuthorities:     review.ProtectedAuthorities(),
		ManagedAuthorities:       review.ManagedCredentialAuthorities(),
		DefaultTemporaryLifetime: manualcapture.DefaultTemporaryLifetime,
		MaximumTemporaryLifetime: manualcapture.MaximumTemporaryLifetime,
	}
	if len(context.ProtectedAuthorities) != 0 {
		context.DeliverRoot = true
		context.RootIdentity = issuer.rootID
		context.RootCertificate = issuer.root
	}
	return context
}

// manualCaptureConfirmation is an opaque review token, not an authorization
// credential. It collapses the runtime instance, Root identity, listener, and
// owner scope into one value so a create cannot silently consume a context
// different from the one the caller reviewed.
func (issuer *Issuer) manualCaptureConfirmation(
	owner manualcapture.OwnerScope,
	review CaptureAuthorityReview,
) string {
	hash := sha256.New()
	for _, value := range []string{
		"vibermate:manual-capture-confirmation",
		issuer.generation,
		issuer.proxyOrigin,
		issuer.rootID.Digest().String(),
		issuer.root.Path(),
		string(owner.Kind()),
		review.EnvironmentID().String(),
		strconv.FormatUint(uint64(review.EnvironmentRevision()), 10),
		review.EnvironmentDigest().String(),
		review.AuthorityDigest().String(),
		strconv.FormatInt(int64(manualcapture.DefaultTemporaryLifetime/time.Second), 10),
		strconv.FormatInt(int64(manualcapture.MaximumTemporaryLifetime/time.Second), 10),
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	if bindingID, ok := owner.ProxyClientBindingID(); ok {
		_, _ = hash.Write([]byte(bindingID))
	}
	return "ctx_" + base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func (issuer *Issuer) revokeUnusedManualCapture(
	ctx context.Context,
	owner manualcapture.OwnerScope,
	grant manualcapture.Grant,
	cause error,
) error {
	id, err := manualcapture.ParseID(grant.Capture.ID)
	if err != nil {
		return errors.Join(cause, err)
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, revokeErr := issuer.manuals.Revoke(cleanupContext, manualcapture.RevokeCommand{
		Owner: owner, ID: id,
		ExpectedCredentialRevision: grant.Capture.CredentialRevision,
	})
	if revokeErr != nil {
		return errors.Join(cause, fmt.Errorf("revoke unused ManualCapture: %w", revokeErr))
	}
	return cause
}

func sameManualCaptureConfirmation(left, right string) bool {
	return len(left) == len(right) &&
		subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func classifyManualCaptureError(err error, fallback error) error {
	switch {
	case errors.Is(err, manualcapture.ErrInvalidCommand):
		return errors.Join(ErrInvalidManualCapture, err)
	case errors.Is(err, manualcapture.ErrNotFound):
		return errors.Join(ErrManualCaptureNotFound, err)
	case errors.Is(err, manualcapture.ErrRevisionConflict),
		errors.Is(err, manualcapture.ErrNotActive),
		errors.Is(err, manualcapture.ErrStateConflict):
		return errors.Join(ErrManualCaptureConflict, err)
	case errors.Is(err, manualcapture.ErrRuntimeStopping):
		return errors.Join(ErrManualCaptureUnavailable, err)
	default:
		return errors.Join(fallback, err)
	}
}

type CaptureRunRequest struct {
	EnvironmentID     environment.EnvironmentID
	CWD               string
	Command           []string
	ExecutablePath    string
	RuntimeMetadata   capturerun.RuntimeMetadata
	ClientEnvironment clienttarget.EnvironmentFacts
	Companion         *CompanionAttestation
}

// CompanionAttestation is verification and opaque workspace evidence produced
// on the machine that will launch the client. The authenticated Server
// principal and exact catalog validation are separate checks performed before
// any grant is issued.
type CompanionAttestation struct {
	Detection clientadapter.Detection
	Workspace workspaceidentity.Scope
}

type CaptureRunGrant struct {
	Run                          capturerun.LaunchGrant
	CatalogRevision              clientadapter.CatalogRevision
	Recognition                  clientadapter.Recognition
	Adapter                      *clientadapter.Evidence
	Signer                       *clientadapter.SignerEvidence
	LaunchRecipe                 clientadapter.LaunchRecipe
	ExecutablePath               string
	ProxyAddress                 string
	ProxyDelivery                ProxyDelivery
	RootPEMPath                  string
	RootPEM                      string
	Capture                      captureidentity.Reference
	AssignmentRevision           captureassignment.Revision
	EnvironmentID                environment.EnvironmentID
	InitialEnvironmentID         environment.EnvironmentID
	InitialEnvironmentRevision   environment.Revision
	InitialEnvironmentDigest     environment.CandidateDigest
	LaunchAuthorityDigest        environment.LaunchAuthorityDigest
	ProtectedAuthorities         []string
	ManagedCredentialAuthorities []string
	LaunchEnvironment            environment.LaunchEnvironmentPolicy
}

func (issuer *Issuer) IssueCaptureRun(
	ctx context.Context,
	principal controlprincipal.Principal,
	request CaptureRunRequest,
) (CaptureRunGrant, error) {
	if issuer == nil || ctx == nil || !principal.Valid() ||
		!principal.Allows(controlprincipal.GrantCaptureRun) {
		return CaptureRunGrant{}, ErrPrincipalUnauthorized
	}
	if (issuer.proxy == ProxyDeliveryLocalListener &&
		principal.Kind() != controlprincipal.KindLocalCLI) ||
		(issuer.proxy == ProxyDeliveryClientRelay &&
			principal.Kind() != controlprincipal.KindEnrolledClient &&
			principal.Kind() != controlprincipal.KindRuntimeUser) {
		return CaptureRunGrant{}, ErrPrincipalUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return CaptureRunGrant{}, err
	}
	if err := validateCaptureRunRequest(request); err != nil {
		return CaptureRunGrant{}, fmt.Errorf("%w: %v", ErrInvalidCaptureRun, err)
	}
	detection, err := issuer.resolveDetection(ctx, principal, request)
	if err != nil || validateDetection(detection) != nil {
		return CaptureRunGrant{}, ErrAdapterVerification
	}
	var runAdapter *clientadapter.Evidence
	if detection.Status == clientadapter.StatusVerified &&
		detection.Evidence != nil {
		evidence := *detection.Evidence
		runAdapter = &evidence
	}
	profile, err := clienttarget.NewProfile(
		detectedClientID(detection),
		request.ClientEnvironment,
	)
	if err != nil {
		return CaptureRunGrant{}, ErrInvalidCaptureRun
	}
	workspace, err := issuer.workspaces.ResolveCaptureRun(
		ctx,
		principal,
		request.CWD,
		companionWorkspace(request.Companion),
	)
	if err != nil && !errors.Is(err, workspaceidentity.ErrInvalidIdentity) {
		return CaptureRunGrant{}, ErrWorkspaceUnavailable
	}
	selectedEnvironment := request.EnvironmentID
	assignmentSource := captureassignment.SourceLaunch
	if selectedEnvironment == "" {
		selectedEnvironment = environment.SystemTransparentID
		assignmentSource = captureassignment.SourceSystemTransparent
	}
	// Reject a missing or disabled explicit Environment before creating
	// a durable CaptureRun. This review is intentionally not authorization: the
	// later AssignAndResolve call remains the sole linearization point and must
	// re-check the same Environment while freezing the launch boundary.
	if _, reviewErr := issuer.authorities.Review(ctx, selectedEnvironment); reviewErr != nil {
		return CaptureRunGrant{}, classifyEnvironmentSelectionError(reviewErr)
	}
	var runtimeUserID runtimeuser.UserID
	var runtimeUsername string
	var loginSessionID runtimeuser.LoginSessionID
	var deviceName string
	if principal.Kind() == controlprincipal.KindRuntimeUser {
		userValue, userOK := principal.RuntimeUserID()
		usernameValue, usernameOK := principal.RuntimeUsername()
		sessionValue, sessionOK := principal.LoginSessionID()
		deviceValue, deviceOK := principal.DeviceName()
		runtimeUserID = runtimeuser.UserID(userValue)
		runtimeUsername = usernameValue
		loginSessionID = runtimeuser.LoginSessionID(sessionValue)
		deviceName = deviceValue
		if !userOK || !usernameOK || !sessionOK || !deviceOK || !runtimeUserID.Valid() ||
			!runtimeuser.ValidUsername(runtimeUsername) ||
			!loginSessionID.Valid() || !runtimeuser.ValidDeviceName(deviceName) {
			return CaptureRunGrant{}, ErrPrincipalUnauthorized
		}
	}
	grant, err := issuer.runs.Create(ctx, capturerun.CreateCommand{
		CWD:                     request.CWD,
		CanonicalExecutablePath: detection.CanonicalPath,
		ExecutableLabel:         detection.ExecutableLabel,
		Lifetime:                issuer.runLifetime,
		CatalogRevision:         detection.CatalogRevision,
		Adapter:                 runAdapter,
		Recognition:             detection.Recognition,
		Workspace:               workspace,
		Runtime:                 request.RuntimeMetadata,
		RuntimeUserID:           runtimeUserID,
		RuntimeUsername:         runtimeUsername,
		LoginSessionID:          loginSessionID,
		DeviceName:              deviceName,
	})
	if err != nil {
		return CaptureRunGrant{}, ErrCaptureRunCreate
	}
	// Persist the active run before creating its Environment assignment. The
	// assignment manager freezes launch authority while holding the selected
	// Environment's publication fence. If assignment creation is uncertain or
	// fails, finishing this run makes its proxy capability unusable; no launch
	// grant escapes even if a conservative orphan assignment remains.
	var authorities CaptureAuthoritySet
	capture, captureErr := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	if captureErr != nil {
		err = captureErr
	} else {
		authorities, err = issuer.authorities.AssignAndResolve(
			ctx, capture, selectedEnvironment, assignmentSource, profile,
		)
	}
	if err != nil {
		selectionErr := classifyEnvironmentSelectionError(err)
		cleanupContext, cancelCleanup := context.WithTimeout(
			context.WithoutCancel(ctx),
			2*time.Second,
		)
		cleanupErr := issuer.runs.Finish(
			cleanupContext,
			grant.Run.ID,
			grant.ControlCapability,
		)
		cancelCleanup()
		if cleanupErr != nil {
			return CaptureRunGrant{}, errors.Join(
				selectionErr,
				fmt.Errorf("finish unused CaptureRun: %w", cleanupErr),
			)
		}
		return CaptureRunGrant{}, selectionErr
	}
	protectedAuthorities := authorities.ProtectedAuthorities()
	managedAuthorities := authorities.ManagedCredentialAuthorities()
	recipe := clientadapter.LaunchGeneric
	rootPath := ""
	rootPEM := ""
	var launchAdapter *clientadapter.Evidence
	var signer *clientadapter.SignerEvidence
	// Root delivery exists only to decrypt an exact protected authority. A
	// transparent Environment keeps the verified client identity on the durable
	// run, but launches with an ordinary authenticated
	// proxy and never ask for or deliver the Root.
	if len(protectedAuthorities) != 0 {
		if runAdapter != nil {
			evidence := *runAdapter
			launchAdapter = &evidence
			recipe = evidence.LaunchRecipe
			if recipe.RequiresRoot() {
				rootPath, rootPEM = issuer.rootDelivery()
			}
		}
		if detection.Recognition == clientadapter.RecognitionRecognized &&
			detection.Signer != nil {
			evidence := *detection.Signer
			allowed := principal.Kind() == controlprincipal.KindEnrolledClient ||
				principal.Kind() == controlprincipal.KindRuntimeUser
			var askErr error
			if !allowed {
				allowed, askErr = issuer.askClientRoot(ctx, evidence)
			}
			if askErr == nil && allowed {
				recipe = evidence.LaunchRecipe
				rootPath, rootPEM = issuer.rootDelivery()
				signer = &evidence
			}
		}
	}
	return CaptureRunGrant{
		Run:                          grant,
		CatalogRevision:              detection.CatalogRevision,
		Recognition:                  detection.Recognition,
		Adapter:                      launchAdapter,
		Signer:                       signer,
		LaunchRecipe:                 recipe,
		ExecutablePath:               detection.CanonicalPath,
		ProxyAddress:                 issuer.proxyOrigin,
		ProxyDelivery:                issuer.proxy,
		RootPEMPath:                  rootPath,
		RootPEM:                      rootPEM,
		Capture:                      authorities.Capture(),
		AssignmentRevision:           authorities.AssignmentRevision(),
		EnvironmentID:                authorities.EnvironmentID(),
		InitialEnvironmentID:         authorities.InitialEnvironmentID(),
		InitialEnvironmentRevision:   authorities.InitialEnvironmentRevision(),
		InitialEnvironmentDigest:     authorities.InitialEnvironmentDigest(),
		LaunchAuthorityDigest:        authorities.AuthorityDigest(),
		ProtectedAuthorities:         protectedAuthorities,
		ManagedCredentialAuthorities: managedAuthorities,
		LaunchEnvironment:            authorities.LaunchEnvironment(),
	}, nil
}

func (issuer *Issuer) rootDelivery() (path string, inline string) {
	if issuer.proxy == ProxyDeliveryClientRelay {
		return "", string(issuer.root.CertificatePEM())
	}
	return issuer.root.Path(), ""
}

func (issuer *Issuer) resolveDetection(
	ctx context.Context,
	principal controlprincipal.Principal,
	request CaptureRunRequest,
) (clientadapter.Detection, error) {
	switch principal.Kind() {
	case controlprincipal.KindLocalCLI:
		if request.Companion != nil {
			return clientadapter.Detection{}, ErrAdapterVerification
		}
		return issuer.verifier.Verify(ctx, clientadapter.Request{
			Command:        append([]string(nil), request.Command...),
			CWD:            request.CWD,
			ExecutablePath: request.ExecutablePath,
		})
	case controlprincipal.KindEnrolledClient, controlprincipal.KindRuntimeUser:
		if request.Companion == nil || !issuer.companions.Valid() {
			return clientadapter.Detection{}, ErrAdapterVerification
		}
		return issuer.companions.ValidateCompanionAttestation(
			request.Companion.Detection,
		)
	default:
		return clientadapter.Detection{}, ErrAdapterVerification
	}
}

func companionWorkspace(
	attestation *CompanionAttestation,
) workspaceidentity.Scope {
	if attestation == nil {
		return workspaceidentity.Scope{}
	}
	return attestation.Workspace
}

func detectedClientID(detection clientadapter.Detection) string {
	if detection.Evidence != nil {
		return detection.Evidence.ID
	}
	if detection.Signer != nil {
		return detection.Signer.ID
	}
	return ""
}

func classifyEnvironmentSelectionError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, environment.ErrEnvironmentNotFound):
		return fmt.Errorf("%w: %w", ErrEnvironmentNotFound, err)
	case errors.Is(err, environment.ErrEnvironmentDisabled):
		return fmt.Errorf("%w: %w", ErrEnvironmentUnavailable, err)
	default:
		return fmt.Errorf("%w: %w", ErrProjectionUnavailable, err)
	}
}

func (issuer *Issuer) askClientRoot(
	ctx context.Context,
	evidence clientadapter.SignerEvidence,
) (bool, error) {
	if issuer.rootAsk == nil {
		return false, nil
	}
	outcome, err := issuer.rootAsk.AskClientRoot(
		ctx,
		toolapproval.ClientRootAskRequest{
			SignerID:       evidence.ID,
			SignerRevision: uint64(evidence.Revision),
			SignedPath:     evidence.SignedPath,
		},
	)
	if err != nil {
		return false, err
	}
	return outcome.Allowed, nil
}

func validateCaptureRunRequest(request CaptureRunRequest) error {
	if request.EnvironmentID != "" {
		if _, err := environment.NewEnvironmentID(request.EnvironmentID.String()); err != nil {
			return errors.New("CaptureRun Environment is invalid")
		}
	}
	if request.CWD == "" ||
		!filepath.IsAbs(request.CWD) ||
		filepath.Clean(request.CWD) != request.CWD ||
		request.ExecutablePath == "" ||
		!filepath.IsAbs(request.ExecutablePath) ||
		len(request.Command) == 0 ||
		len(request.Command) > maxArguments ||
		request.RuntimeMetadata.Validate() != nil {
		return errors.New("CaptureRun fields are invalid")
	}
	if err := request.ClientEnvironment.Validate(); err != nil {
		return errors.New("CaptureRun client environment is invalid")
	}
	total := 0
	for _, argument := range request.Command {
		if strings.ContainsRune(argument, '\x00') {
			return errors.New("CaptureRun argument contains NUL")
		}
		total += len(argument)
	}
	if request.Command[0] == "" || total > maxArgumentBytes {
		return errors.New("CaptureRun command is outside the limit")
	}
	return nil
}

func validateDetection(detection clientadapter.Detection) error {
	if !detection.CatalogRevision.Valid() ||
		detection.CanonicalPath == "" ||
		(detection.Status != clientadapter.StatusGeneric &&
			detection.Status != clientadapter.StatusVerified) ||
		(detection.Status == clientadapter.StatusGeneric &&
			detection.Evidence != nil) ||
		(detection.Signer != nil &&
			(detection.Recognition != clientadapter.RecognitionRecognized ||
				detection.Evidence != nil ||
				detection.Signer.Validate() != nil ||
				detection.Signer.CatalogRevision != detection.CatalogRevision)) ||
		(detection.Status == clientadapter.StatusVerified &&
			(detection.Evidence == nil ||
				detection.Evidence.Validate() != nil ||
				detection.Evidence.CatalogRevision != detection.CatalogRevision)) {
		return ErrAdapterVerification
	}
	return nil
}

func validateProxyOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil ||
		parsed.Scheme != "http" ||
		parsed.User != nil ||
		parsed.Path != "" ||
		parsed.RawPath != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return errors.New("capture grant proxy origin is invalid")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host != "127.0.0.1" {
		return errors.New("capture grant proxy origin is not literal IPv4 loopback")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("capture grant proxy port is invalid")
	}
	return nil
}

func validGeneration(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) >= 16 &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
}

func rootDeliveryMatches(
	identity localca.RootIdentity,
	certificate localca.RootCertificate,
) bool {
	block, rest := pem.Decode(certificate.CertificatePEM())
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return false
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	digest, err := certidentity.DigestRootCertificate(parsed.Raw)
	return err == nil && digest == identity.Digest()
}
