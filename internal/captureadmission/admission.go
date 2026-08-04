// Package captureadmission owns the immutable result of authenticating one
// proxy credential. Admission records how traffic entered VibeMate; it does
// not select an Access, Profile, route, account, model, or plugin plan.
package captureadmission

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const (
	// ProxyUsername is a fixed protocol field. It carries no identity, route,
	// or other business metadata; the random password is the capability.
	ProxyUsername = "capture"

	maxOpaqueIDBytes  = 128
	maxIngressIDBytes = 256
	maxSourceLabelLen = 256
)

var (
	ErrCredentialRejected = errors.New("capture credential rejected")
	ErrInvalidAdmission   = errors.New("capture admission is invalid")
)

type Kind string

const (
	KindManagedRun Kind = "managed_run"
	KindManual     Kind = "manual"
)

func (kind Kind) valid() bool {
	return kind == KindManagedRun || kind == KindManual
}

type AttributionConfidence string

const (
	AttributionVerified   AttributionConfidence = "verified"
	AttributionConfigured AttributionConfidence = "configured"
	AttributionUnknown    AttributionConfidence = "unknown"
)

func (confidence AttributionConfidence) valid() bool {
	switch confidence {
	case AttributionVerified, AttributionConfigured, AttributionUnknown:
		return true
	default:
		return false
	}
}

// ProxyCredential is an opaque capability consumed only by an Authorizer. It
// has no raw value or JSON accessor, and both formatting methods redact it.
type ProxyCredential struct {
	value capturecredential.Credential
}

func NewProxyCredential(value string) (ProxyCredential, error) {
	credential, err := capturecredential.Parse(value)
	if err != nil {
		return ProxyCredential{}, ErrCredentialRejected
	}
	return ProxyCredential{value: credential}, nil
}

func (ProxyCredential) String() string {
	return "[REDACTED]"
}

func (ProxyCredential) GoString() string {
	return "captureadmission.ProxyCredential{[REDACTED]}"
}

// Admission is immutable attribution evidence for one authenticated proxy
// connection. Optional identities stay private and are exposed through typed
// accessors so a caller cannot confuse an absent identity with an empty one.
type Admission struct {
	kind               Kind
	ingressProfileID   string
	captureRunID       string
	manualCaptureID    string
	credentialRevision uint64
	machineID          workspaceidentity.MachineID
	workspaceID        workspaceidentity.WorkspaceID
	confidence         AttributionConfidence
	sourceLabel        string
	workspace          workspaceidentity.Scope
	adapter            *clientadapter.Evidence
}

// NewManual constructs the route-neutral admission produced by a future
// ManualCapture authority. Possessing its credential is configured evidence;
// it is never client, process, machine, or workspace verification.
func NewManual(
	manualCaptureID string,
	credentialRevision uint64,
	sourceLabel string,
) (Admission, error) {
	admission := Admission{
		kind:               KindManual,
		ingressProfileID:   "manual-capture/" + manualCaptureID,
		manualCaptureID:    manualCaptureID,
		credentialRevision: credentialRevision,
		confidence:         AttributionConfigured,
		sourceLabel:        sourceLabel,
	}
	if err := admission.Validate(); err != nil {
		return Admission{}, err
	}
	return admission, nil
}

func (admission Admission) Validate() error {
	if !admission.kind.valid() ||
		!validIngressProfileID(admission.ingressProfileID) ||
		admission.credentialRevision == 0 ||
		!admission.confidence.valid() ||
		!validLabel(admission.sourceLabel) {
		return ErrInvalidAdmission
	}

	hasWorkspace := admission.workspace != (workspaceidentity.Scope{})
	hasMachineID := admission.machineID.String() != ""
	hasWorkspaceID := admission.workspaceID.String() != ""
	if hasWorkspace != (hasMachineID && hasWorkspaceID) {
		return fmt.Errorf("%w: workspace identity is incomplete", ErrInvalidAdmission)
	}
	if hasWorkspace && (admission.workspace.Validate() != nil ||
		admission.workspace.MachineID() != admission.machineID ||
		admission.workspace.WorkspaceID() != admission.workspaceID) {
		return fmt.Errorf("%w: workspace evidence is invalid", ErrInvalidAdmission)
	}

	switch admission.kind {
	case KindManagedRun:
		if !validOpaqueID(admission.captureRunID) ||
			admission.ingressProfileID != "capture-run/"+admission.captureRunID ||
			admission.manualCaptureID != "" ||
			admission.credentialRevision != 1 {
			return fmt.Errorf("%w: managed-run evidence is invalid", ErrInvalidAdmission)
		}
		if admission.confidence != AttributionConfigured &&
			admission.confidence != AttributionVerified {
			return fmt.Errorf("%w: managed-run confidence is invalid", ErrInvalidAdmission)
		}
		if (admission.adapter != nil) !=
			(admission.confidence == AttributionVerified) {
			return fmt.Errorf("%w: adapter confidence is inconsistent", ErrInvalidAdmission)
		}
		if admission.adapter != nil && admission.adapter.Validate() != nil {
			return fmt.Errorf("%w: client adapter evidence is invalid", ErrInvalidAdmission)
		}
	case KindManual:
		if !validOpaqueID(admission.manualCaptureID) ||
			admission.ingressProfileID != "manual-capture/"+admission.manualCaptureID ||
			admission.captureRunID != "" || admission.adapter != nil ||
			hasWorkspace || admission.confidence != AttributionConfigured {
			return fmt.Errorf("%w: manual-capture evidence is invalid", ErrInvalidAdmission)
		}
	}
	return nil
}

func (admission Admission) Kind() Kind {
	return admission.kind
}

func (admission Admission) IngressProfileID() string {
	return admission.ingressProfileID
}

func (admission Admission) CaptureRunID() (string, bool) {
	return admission.captureRunID, admission.captureRunID != ""
}

func (admission Admission) ManualCaptureID() (string, bool) {
	return admission.manualCaptureID, admission.manualCaptureID != ""
}

func (admission Admission) CredentialRevision() uint64 {
	return admission.credentialRevision
}

func (admission Admission) MachineID() (workspaceidentity.MachineID, bool) {
	return admission.machineID, admission.machineID.String() != ""
}

func (admission Admission) WorkspaceID() (workspaceidentity.WorkspaceID, bool) {
	return admission.workspaceID, admission.workspaceID.String() != ""
}

func (admission Admission) AttributionConfidence() AttributionConfidence {
	return admission.confidence
}

func (admission Admission) SourceLabel() string {
	return admission.sourceLabel
}

func (admission Admission) WorkspaceScope() (workspaceidentity.Scope, bool) {
	return admission.workspace, admission.workspace != (workspaceidentity.Scope{})
}

// Supports reports only feature evidence carried by a digest-verified client
// adapter. Manual and configured admissions cannot acquire adapter capability
// by naming a client or presenting a proxy credential.
func (admission Admission) Supports(feature clientadapter.Feature) bool {
	return admission.adapter != nil && admission.adapter.Supports(feature)
}

func validOpaqueID(value string) bool {
	return validIdentity(value, maxOpaqueIDBytes, false)
}

func validIngressProfileID(value string) bool {
	return validIdentity(value, maxIngressIDBytes, true)
}

func validIdentity(value string, maxBytes int, allowSlash bool) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' ||
			(allowSlash && character == '/') {
			continue
		}
		return false
	}
	return true
}

func validLabel(value string) bool {
	if value == "" || len(value) > maxSourceLabelLen ||
		!utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
