// Package captureadmission owns the immutable result of authenticating one
// proxy credential. Admission records how traffic entered ViberMate; it does
// not select an Environment, route, account, model, or plugin plan.
package captureadmission

import (
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const (
	// ProxyUsername is a fixed protocol field. It carries no identity, route,
	// or other business metadata; the random password is the capability.
	ProxyUsername = "capture"

	maxOpaqueIDBytes     = 128
	maxAdmissionRefBytes = 256
	maxSourceLabelLen    = 256
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
	recordingScope     recording.Scope
	receipt            *retainedReceipt
	kind               Kind
	admissionRef       string
	captureRunID       string
	manualCaptureID    string
	credentialRevision uint64
	machineID          workspaceidentity.MachineID
	workspaceID        workspaceidentity.WorkspaceID
	confidence         AttributionConfidence
	sourceLabel        string
	workspace          workspaceidentity.Scope
	workspaceRoot      string
	adapter            *clientadapter.Evidence
	runtime            capturerun.RuntimeMetadata
	runtimeUsername    string
	runtimeUserID      runtimeuser.UserID
	recognizedClient   clientadapter.RecognizedClient
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
		admissionRef:       "manual-capture/" + manualCaptureID,
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
		!validAdmissionRef(admission.admissionRef) ||
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
	if admission.workspaceRoot != "" &&
		(!filepath.IsAbs(admission.workspaceRoot) ||
			filepath.Clean(admission.workspaceRoot) != admission.workspaceRoot) {
		return fmt.Errorf("%w: workspace root is invalid", ErrInvalidAdmission)
	}

	switch admission.kind {
	case KindManagedRun:
		if !validOpaqueID(admission.captureRunID) ||
			admission.admissionRef != "capture-run/"+admission.captureRunID ||
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
		if err := admission.validateRecognizedClient(); err != nil {
			return err
		}
		if admission.runtime.Validate() != nil {
			return fmt.Errorf("%w: managed runtime metadata is invalid", ErrInvalidAdmission)
		}
		if admission.runtimeUserID != "" && !admission.runtimeUserID.Valid() {
			return ErrInvalidAdmission
		}
		if admission.runtimeUsername != "" &&
			!runtimeuser.ValidUsername(admission.runtimeUsername) {
			return fmt.Errorf("%w: Runtime username is invalid", ErrInvalidAdmission)
		}
	case KindManual:
		if !validOpaqueID(admission.manualCaptureID) ||
			admission.admissionRef != "manual-capture/"+admission.manualCaptureID ||
			admission.captureRunID != "" || admission.adapter != nil ||
			hasWorkspace || admission.workspaceRoot != "" ||
			admission.runtime != (capturerun.RuntimeMetadata{}) ||
			admission.runtimeUsername != "" || admission.runtimeUserID != "" ||
			admission.confidence != AttributionConfigured ||
			admission.recognizedClient != (clientadapter.RecognizedClient{}) {
			return fmt.Errorf("%w: manual-capture evidence is invalid", ErrInvalidAdmission)
		}
	}
	return nil
}

func (admission Admission) Kind() Kind {
	return admission.kind
}

func (admission Admission) AdmissionRef() string {
	return admission.admissionRef
}

func (admission Admission) CaptureRunID() (string, bool) {
	return admission.captureRunID, admission.captureRunID != ""
}

func (admission Admission) ManualCaptureID() (string, bool) {
	return admission.manualCaptureID, admission.manualCaptureID != ""
}

func (admission Admission) CaptureReference() (captureidentity.Reference, error) {
	switch admission.kind {
	case KindManagedRun:
		return captureidentity.New(captureidentity.KindManagedRun, admission.captureRunID)
	case KindManual:
		return captureidentity.New(captureidentity.KindManualCapture, admission.manualCaptureID)
	default:
		return captureidentity.Reference{}, ErrInvalidAdmission
	}
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

// WorkspaceRoot is an observed path claim for display, transforms, and
// redaction. It does not grant authority to inspect the local filesystem.
func (admission Admission) WorkspaceRoot() (string, bool) {
	return admission.workspaceRoot, admission.workspaceRoot != ""
}

// LocalWorkspaceRoot supplies filesystem authority only for a scope proved by
// the local launcher. Remote and unscoped path claims cannot grant it.
func (admission Admission) LocalWorkspaceRoot() (string, bool) {
	scope, available := admission.WorkspaceScope()
	if !available || scope.Evidence() != workspaceidentity.EvidenceLocalLauncher {
		return "", false
	}
	return admission.WorkspaceRoot()
}

func (admission Admission) RuntimeMetadata() (capturerun.RuntimeMetadata, bool) {
	return admission.runtime, admission.kind == KindManagedRun
}

func (admission Admission) RuntimeUsername() (string, bool) {
	return admission.runtimeUsername, admission.runtimeUsername != ""
}

// Supports reports only feature evidence carried by a digest-verified client
// adapter. Manual and configured admissions cannot acquire adapter capability
// by naming a client or presenting a proxy credential. Readers that also serve
// publisher-recognized clients ask ProtectionBasis, which never lets a
// recognized run take the exact-release path.
func (admission Admission) Supports(feature clientadapter.Feature) bool {
	return admission.adapter != nil && admission.adapter.Supports(feature)
}

// ProtectionBasis says why this connection's client may use one protection.
// Exact release evidence decides alone when present. A run the Runtime
// recognized by its publisher's signature gets what the recognized-client
// policy grants its client. Each reader still applies the protection's own
// proof at its point of use.
func (admission Admission) ProtectionBasis(
	feature clientadapter.Feature,
) clientadapter.ProtectionBasis {
	if admission.kind != KindManagedRun || !feature.Single() {
		return clientadapter.ProtectionNone
	}
	if admission.adapter != nil {
		if admission.adapter.Supports(feature) {
			return clientadapter.ProtectionExactRelease
		}
		return clientadapter.ProtectionNone // a verified run never falls through
	}
	if admission.recognizedClient.ID != "" &&
		admission.recognizedClient.Protections.Includes(feature) {
		return clientadapter.ProtectionRecognizedClient
	}
	return clientadapter.ProtectionNone
}

// validateRecognizedClient keeps the recognized basis apart from the exact
// one: a recognized client never accompanies release evidence, stays at
// configured confidence, and receives only known bits its policy grants.
func (admission Admission) validateRecognizedClient() error {
	client := admission.recognizedClient
	if client == (clientadapter.RecognizedClient{}) {
		return nil
	}
	knownBits := func(features clientadapter.Feature) bool {
		return features == 0 || features.Includes(features)
	}
	if client.ID == "" ||
		admission.adapter != nil ||
		admission.confidence != AttributionConfigured ||
		!knownBits(client.Policy) ||
		!knownBits(client.Protections) ||
		client.Protections&^client.Policy != 0 {
		return fmt.Errorf("%w: recognized client evidence is inconsistent", ErrInvalidAdmission)
	}
	return nil
}

func validOpaqueID(value string) bool {
	return validIdentity(value, maxOpaqueIDBytes, false)
}

func validAdmissionRef(value string) bool {
	return validIdentity(value, maxAdmissionRefBytes, true)
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

func (admission Admission) RuntimeUserID() (runtimeuser.UserID, bool) {
	return admission.runtimeUserID, admission.runtimeUserID != ""
}

// RecordingScope is optional provenance selected by the authenticating control read.
func (a Admission) RecordingScope() recording.Scope { return a.recordingScope }
