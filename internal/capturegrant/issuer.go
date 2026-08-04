// Package capturegrant owns control-authorized creation of data-plane capture
// grants. HTTP handlers authenticate and decode; this package decides whether
// a typed principal may create a grant and performs the Core issuance work.
package capturegrant

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const (
	maxArguments     = 256
	maxArgumentBytes = 32 << 10
)

var (
	ErrPrincipalUnauthorized = errors.New("control principal is not authorized for the grant")
	ErrInvalidCaptureRun     = errors.New("CaptureRun request is invalid")
	ErrAdapterVerification   = errors.New("client adapter verification failed")
	ErrProjectionUnavailable = errors.New("Access projection is unavailable")
	ErrWorkspaceUnavailable  = errors.New("workspace identity is unavailable")
	ErrCaptureRunCreate      = errors.New("CaptureRun creation failed")
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
	Verifier            clientadapter.Verifier
	Authorities         access.IngressCatalogReader
	ProxyOrigin         string
	Root                localca.RootCertificate
	RunLifetime         time.Duration
	Workspaces          WorkspaceResolver
	ClientRootApprovals ClientRootApprovals
}

// Issuer is the single Core owner of capture grant creation. Only CaptureRun
// is implemented in the current product slice; ManualCapture will be another
// method on this same authority, not another transport handler's business
// implementation.
type Issuer struct {
	runs        capturerun.Controller
	verifier    clientadapter.Verifier
	authorities access.IngressCatalogReader
	proxyOrigin string
	root        localca.RootCertificate
	runLifetime time.Duration
	workspaces  WorkspaceResolver
	rootAsk     ClientRootApprovals
}

func New(options Options) (*Issuer, error) {
	if options.Runs == nil ||
		options.Verifier == nil ||
		options.Authorities == nil ||
		options.RunLifetime <= 0 ||
		options.Workspaces == nil {
		return nil, errors.New("capture grant issuer dependencies are incomplete")
	}
	if err := validateProxyOrigin(options.ProxyOrigin); err != nil {
		return nil, err
	}
	if !options.Root.Valid() {
		return nil, errors.New("capture grant public Root delivery is incomplete")
	}
	return &Issuer{
		runs:        options.Runs,
		verifier:    options.Verifier,
		authorities: options.Authorities,
		proxyOrigin: options.ProxyOrigin,
		root:        options.Root,
		runLifetime: options.RunLifetime,
		workspaces:  options.Workspaces,
		rootAsk:     options.ClientRootApprovals,
	}, nil
}

type CaptureRunRequest struct {
	CWD            string
	Command        []string
	ExecutablePath string
	LocalUserLabel string
}

type CaptureRunGrant struct {
	Run                  capturerun.LaunchGrant
	CatalogRevision      clientadapter.CatalogRevision
	Recognition          clientadapter.Recognition
	Adapter              *clientadapter.Evidence
	Signer               *clientadapter.SignerEvidence
	LaunchRecipe         clientadapter.LaunchRecipe
	ExecutablePath       string
	ProxyAddress         string
	RootPEMPath          string
	ProtectedAuthorities []string
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
	authorities, err := issuer.authorities.ActiveClientAuthorities()
	if err != nil {
		return CaptureRunGrant{}, ErrProjectionUnavailable
	}
	recipe := clientadapter.LaunchGeneric
	var adapter *clientadapter.Evidence
	rootPath := ""
	if detection.Status == clientadapter.StatusVerified &&
		detection.Evidence != nil {
		evidence := *detection.Evidence
		adapter = &evidence
		recipe = evidence.LaunchRecipe
		if recipe.RequiresRoot() {
			rootPath = issuer.root.Path()
		}
	}
	var signer *clientadapter.SignerEvidence
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
		ExecutablePath:  detection.CanonicalPath,
		Lifetime:        issuer.runLifetime,
		CatalogRevision: detection.CatalogRevision,
		Adapter:         adapter,
		Recognition:     detection.Recognition,
		Workspace:       workspace,
		LocalUserLabel:  request.LocalUserLabel,
	})
	if err != nil {
		return CaptureRunGrant{}, ErrCaptureRunCreate
	}
	return CaptureRunGrant{
		Run:                  grant,
		CatalogRevision:      detection.CatalogRevision,
		Recognition:          detection.Recognition,
		Adapter:              adapter,
		Signer:               signer,
		LaunchRecipe:         recipe,
		ExecutablePath:       detection.CanonicalPath,
		ProxyAddress:         issuer.proxyOrigin,
		RootPEMPath:          rootPath,
		ProtectedAuthorities: append([]string{}, authorities...),
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
