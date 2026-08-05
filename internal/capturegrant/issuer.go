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

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
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
	ErrProjectionUnavailable    = errors.New("Access projection is unavailable")
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
) (workspaceidentity.Scope, error) {
	if !principal.Valid() || principal.Kind() != controlprincipal.KindLocalCLI {
		return workspaceidentity.Scope{}, ErrWorkspaceUnavailable
	}
	return resolver.resolver.ResolveLocal(ctx, cwd)
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
	if err := validateProxyOrigin(options.ProxyOrigin); err != nil {
		return nil, err
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
	}, nil
}

type ManualCaptureRequest struct {
	DisplayName       string
	ClientClass       manualcapture.ClientClass
	Lifetime          manualcapture.Lifetime
	ExpiresIn         time.Duration
	ConfirmationToken string
}

type ManualCaptureContext struct {
	ConfirmationToken        string
	ProxyAddress             string
	RootIdentity             localca.RootIdentity
	RootCertificate          localca.RootCertificate
	DefaultTemporaryLifetime time.Duration
	MaximumTemporaryLifetime time.Duration
}

type ManualCaptureGrant struct {
	Capture manualcapture.Grant
	Context ManualCaptureContext
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
) (ManualCaptureContext, error) {
	owner, err := issuer.manualCaptureOwner(ctx, principal)
	if err != nil {
		return ManualCaptureContext{}, err
	}
	return issuer.manualCaptureContext(owner), nil
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
	if !sameManualCaptureConfirmation(
		request.ConfirmationToken,
		issuer.manualCaptureConfirmation(owner),
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
	return ManualCaptureGrant{
		Capture: grant,
		Context: issuer.manualCaptureContext(owner),
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
	return ManualCaptureGrant{
		Capture: grant,
		Context: issuer.manualCaptureContext(owner),
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
) ManualCaptureContext {
	return ManualCaptureContext{
		ConfirmationToken:        issuer.manualCaptureConfirmation(owner),
		ProxyAddress:             issuer.proxyOrigin,
		RootIdentity:             issuer.rootID,
		RootCertificate:          issuer.root,
		DefaultTemporaryLifetime: manualcapture.DefaultTemporaryLifetime,
		MaximumTemporaryLifetime: manualcapture.MaximumTemporaryLifetime,
	}
}

// manualCaptureConfirmation is an opaque review token, not an authorization
// credential. It collapses the runtime instance, Root identity, listener, and
// owner scope into one value so a create cannot silently consume a context
// different from the one the caller reviewed.
func (issuer *Issuer) manualCaptureConfirmation(
	owner manualcapture.OwnerScope,
) string {
	hash := sha256.New()
	for _, value := range []string{
		"vibermate:manual-capture-confirmation:v1",
		issuer.generation,
		issuer.proxyOrigin,
		issuer.rootID.Digest().String(),
		issuer.root.Path(),
		string(owner.Kind()),
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
	CWD            string
	Command        []string
	ExecutablePath string
	LocalUserLabel string
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
	RootPEMPath                  string
	ProtectedAuthorities         []string
	ManagedCredentialAuthorities []string
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
	if err := ctx.Err(); err != nil {
		return CaptureRunGrant{}, err
	}
	if err := validateCaptureRunRequest(request); err != nil {
		return CaptureRunGrant{}, fmt.Errorf("%w: %v", ErrInvalidCaptureRun, err)
	}
	detection, err := issuer.verifier.Verify(ctx, clientadapter.Request{
		Command:        append([]string(nil), request.Command...),
		CWD:            request.CWD,
		ExecutablePath: request.ExecutablePath,
	})
	if err != nil || validateDetection(detection) != nil {
		return CaptureRunGrant{}, ErrAdapterVerification
	}
	var runAdapter *clientadapter.Evidence
	if detection.Status == clientadapter.StatusVerified &&
		detection.Evidence != nil {
		evidence := *detection.Evidence
		runAdapter = &evidence
	}
	workspace, err := issuer.workspaces.ResolveCaptureRun(
		ctx,
		principal,
		request.CWD,
	)
	if err != nil && !errors.Is(err, workspaceidentity.ErrInvalidIdentity) {
		return CaptureRunGrant{}, ErrWorkspaceUnavailable
	}
	grant, err := issuer.runs.Create(ctx, capturerun.CreateCommand{
		CWD:             request.CWD,
		ExecutableLabel: detection.ExecutableLabel,
		Lifetime:        issuer.runLifetime,
		CatalogRevision: detection.CatalogRevision,
		Adapter:         runAdapter,
		Recognition:     detection.Recognition,
		Workspace:       workspace,
		LocalUserLabel:  request.LocalUserLabel,
	})
	if err != nil {
		return CaptureRunGrant{}, ErrCaptureRunCreate
	}
	// Persist the active run before resolving route-dependent launcher
	// authority. Workspace-route CAS checks the same SQLite state inside its
	// write transaction, so an auth-source change is ordered either entirely
	// before this resolution or rejected until this run finishes.
	authorities, err := issuer.authorities.ResolveCaptureAuthorities(ctx, workspace)
	if err != nil {
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
				ErrProjectionUnavailable,
				fmt.Errorf("finish unused CaptureRun: %w", cleanupErr),
			)
		}
		return CaptureRunGrant{}, ErrProjectionUnavailable
	}
	protectedAuthorities := authorities.ProtectedAuthorities()
	managedAuthorities := authorities.ManagedCredentialAuthorities()
	recipe := clientadapter.LaunchGeneric
	rootPath := ""
	var launchAdapter *clientadapter.Evidence
	var signer *clientadapter.SignerEvidence
	// Root delivery exists only to decrypt an exact protected authority. An
	// empty Access projection is transparent capture: keep the verified client
	// identity on the durable run, but launch with an ordinary authenticated
	// proxy and never ask for or deliver the Root.
	if len(protectedAuthorities) != 0 {
		if runAdapter != nil {
			evidence := *runAdapter
			launchAdapter = &evidence
			recipe = evidence.LaunchRecipe
			if recipe.RequiresRoot() {
				rootPath = issuer.root.Path()
			}
		}
		if detection.Recognition == clientadapter.RecognitionRecognized &&
			detection.Signer != nil {
			evidence := *detection.Signer
			outcome, askErr := issuer.askClientRoot(ctx, evidence)
			if askErr == nil && outcome {
				recipe = evidence.LaunchRecipe
				rootPath = issuer.root.Path()
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
		RootPEMPath:                  rootPath,
		ProtectedAuthorities:         protectedAuthorities,
		ManagedCredentialAuthorities: managedAuthorities,
	}, nil
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
	if request.CWD == "" ||
		!filepath.IsAbs(request.CWD) ||
		filepath.Clean(request.CWD) != request.CWD ||
		request.ExecutablePath == "" ||
		!filepath.IsAbs(request.ExecutablePath) ||
		len(request.Command) == 0 ||
		len(request.Command) > maxArguments ||
		!capturerun.ValidLocalUserLabel(request.LocalUserLabel) {
		return errors.New("CaptureRun fields are invalid")
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
