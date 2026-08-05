package access

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	ErrAccessDeletionBlocked      = errors.New("Access deletion is blocked")
	ErrAccessDeletionChanged      = errors.New("Access deletion impact changed")
	ErrAccessRetired              = errors.New("Access identity is retired")
	ErrAccessDeletionNotCommitted = errors.New("Access deletion was not committed")
)

const deletionTokenDomain = "vibermate:access-deletion-impact:v1"

// DeletionBlocker is a stable, non-localized reason a deletion cannot proceed.
type DeletionBlocker string

const (
	DeletionBlockerDisableFirst               DeletionBlocker = "disable_access_first"
	DeletionBlockerActiveCaptureRuns          DeletionBlocker = "active_capture_runs"
	DeletionBlockerConfirmWorkspaceRetirement DeletionBlocker = "confirm_workspace_retirement"
	DeletionBlockerProxyClientBindings        DeletionBlocker = "proxy_client_bindings"
)

// DeletionWorkspaceReference is one durable workspace route owned by an
// Access. ActiveCaptureRunIDs are evidence used only for the deletion fence;
// they are never inferred from a UI count.
type DeletionWorkspaceReference struct {
	BindingID           string
	Revision            uint64
	WorkspaceLabel      string
	ActiveCaptureRunIDs []string
}

// DeletionProxyClientReference is one durable remote-client policy that still
// names a Profile owned by the Access. Deletion never rewrites or sweeps this
// independent authority implicitly.
type DeletionProxyClientReference struct {
	BindingID string
	Revision  uint64
}

func (reference DeletionProxyClientReference) validate() error {
	if reference.BindingID == "" || reference.Revision == 0 {
		return ErrInvalidRepositoryState
	}
	return nil
}

func (reference DeletionWorkspaceReference) validate() error {
	if reference.BindingID == "" || reference.Revision == 0 ||
		reference.WorkspaceLabel == "" {
		return ErrInvalidRepositoryState
	}
	for _, runID := range reference.ActiveCaptureRunIDs {
		if runID == "" {
			return ErrInvalidRepositoryState
		}
	}
	return nil
}

// DeletionInspection is the repository-owned durable impact at one instant.
// Aggregate revision protects Access-owned objects; WorkspaceReferences cover
// the independently revised records that must not be silently swept in later.
type DeletionInspection struct {
	Aggregate             Aggregate
	WorkspaceReferences   []DeletionWorkspaceReference
	ProxyClientReferences []DeletionProxyClientReference
}

func (inspection DeletionInspection) Validate() error {
	if err := inspection.Aggregate.Validate(); err != nil {
		return err
	}
	previous := ""
	for _, reference := range inspection.WorkspaceReferences {
		if err := reference.validate(); err != nil ||
			(previous != "" && reference.BindingID <= previous) {
			return ErrInvalidRepositoryState
		}
		previous = reference.BindingID
	}
	previous = ""
	for _, reference := range inspection.ProxyClientReferences {
		if err := reference.validate(); err != nil ||
			(previous != "" && reference.BindingID <= previous) {
			return ErrInvalidRepositoryState
		}
		previous = reference.BindingID
	}
	return nil
}

func (inspection DeletionInspection) activeCaptureRunCount() int {
	count := 0
	for _, reference := range inspection.WorkspaceReferences {
		count += len(reference.ActiveCaptureRunIDs)
	}
	return count
}

// DeletionImpactToken binds confirmation to the exact Access revision,
// workspace references, active runs, and secret ownership classification the
// person reviewed. It contains no secret reference or payload in clear text.
type DeletionImpactToken [sha256.Size]byte

func ParseDeletionImpactToken(value string) (DeletionImpactToken, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(decoded) != value {
		return DeletionImpactToken{}, ErrInvalidAccess
	}
	var token DeletionImpactToken
	copy(token[:], decoded)
	return token, nil
}

func (token DeletionImpactToken) String() string {
	if token == (DeletionImpactToken{}) {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(token[:])
}

// DeletionSecretImpact separates physical values this Access owns alone from
// references another Access still uses. Shared references are deliberately not
// exposed to the control plane.
type DeletionSecretImpact struct {
	Exclusive []SecretRef
	Shared    []SecretRef
}

// DeletionPreview is the non-secret confirmation model consumed by control
// surfaces. CanDelete means no active work remains; workspace route retirement
// may still require the explicit DeleteCommand flag represented by Blockers.
type DeletionPreview struct {
	AccessID                AccessID
	Name                    string
	Revision                Revision
	Status                  AccessStatus
	WorkspaceBindingCount   int
	ActiveCaptureRunCount   int
	ProxyClientBindingCount int
	ExclusiveSecretCount    int
	SharedSecretCount       int
	ImpactToken             DeletionImpactToken
	Blockers                []DeletionBlocker
}

func (preview DeletionPreview) CanDelete(retireWorkspaceBindings bool) bool {
	for _, blocker := range preview.Blockers {
		if blocker == DeletionBlockerConfirmWorkspaceRetirement &&
			retireWorkspaceBindings {
			continue
		}
		return false
	}
	return true
}

type PreviewDeletionCommand struct {
	AccessID         AccessID
	ExpectedRevision Revision
	ObservedAt       time.Time
}

func (command PreviewDeletionCommand) validate() error {
	if err := command.AccessID.validate(); err != nil ||
		command.ExpectedRevision == 0 || command.ObservedAt.IsZero() {
		return ErrInvalidAccess
	}
	return nil
}

type DeleteCommand struct {
	AccessID                AccessID
	ExpectedRevision        Revision
	ExpectedImpactToken     DeletionImpactToken
	RetireWorkspaceBindings bool
	DeletedAt               time.Time
}

func (command DeleteCommand) validate() error {
	if err := command.AccessID.validate(); err != nil ||
		command.ExpectedRevision == 0 ||
		command.ExpectedImpactToken == (DeletionImpactToken{}) ||
		command.DeletedAt.IsZero() {
		return ErrInvalidAccess
	}
	return nil
}

type DeleteOutcome string

const (
	DeleteOutcomeCommitted     DeleteOutcome = "deleted"
	DeleteOutcomeNotCommitted  DeleteOutcome = "not_deleted"
	DeleteOutcomeIndeterminate DeleteOutcome = "indeterminate"
	DeleteOutcomeConflict      DeleteOutcome = "revision_conflict"
	DeleteOutcomeNotConfigured DeleteOutcome = "access_not_configured"
	DeleteOutcomeRetired       DeleteOutcome = "access_retired"
	DeleteOutcomeBlocked       DeleteOutcome = "deletion_blocked"
	DeleteOutcomeImpactChanged DeleteOutcome = "deletion_impact_changed"
)

type DeleteResult struct {
	Outcome  DeleteOutcome
	Revision Revision
}

// DeleteMutation is the final repository command after Manager has closed
// request admission, drained active requests, and retired exclusive secrets.
type DeleteMutation struct {
	AccessID                 AccessID
	ExpectedRevision         Revision
	ExpectedRepositoryImpact DeletionImpactToken
	RetireWorkspaceBindings  bool
	DeletedAt                time.Time
}

func (mutation DeleteMutation) Validate() error {
	if err := mutation.AccessID.validate(); err != nil ||
		mutation.ExpectedRevision == 0 ||
		mutation.ExpectedRepositoryImpact == (DeletionImpactToken{}) ||
		mutation.DeletedAt.IsZero() {
		return ErrInvalidAccess
	}
	return nil
}

// DeletionRepository is the SQLite boundary used by Manager. InspectDeletion
// and Delete must use the same canonical impact algorithm.
type DeletionRepository interface {
	InspectDeletion(context.Context, AccessID, time.Time) (DeletionInspection, bool, error)
	Delete(context.Context, DeleteMutation) (DeleteResult, error)
}

// SecretRetirer removes one physical value. Missing values are idempotent at
// this port; implementations must never place a reference in an error.
type SecretRetirer interface {
	RetireSecret(context.Context, SecretRef) error
}

// Deleter exposes the two explicit user operations. Preview carries no
// authority: DeleteAccess always recomputes and compares its impact.
type Deleter interface {
	PreviewDeleteAccess(context.Context, PreviewDeletionCommand) (DeletionPreview, error)
	DeleteAccess(context.Context, DeleteCommand) (DeleteResult, error)
}

func deletionImpactToken(
	inspection DeletionInspection,
	secretImpact DeletionSecretImpact,
) (DeletionImpactToken, error) {
	if err := inspection.Validate(); err != nil {
		return DeletionImpactToken{}, err
	}
	hash := sha256.New()
	writeDeletionTokenPart(hash, deletionTokenDomain)
	writeDeletionTokenPart(hash, inspection.Aggregate.Binding.ID.String())
	writeDeletionTokenPart(hash, fmt.Sprintf("%d", inspection.Aggregate.Binding.Revision))
	for _, reference := range inspection.WorkspaceReferences {
		writeDeletionTokenPart(hash, reference.BindingID)
		writeDeletionTokenPart(hash, fmt.Sprintf("%d", reference.Revision))
		writeDeletionTokenPart(hash, reference.WorkspaceLabel)
		for _, runID := range reference.ActiveCaptureRunIDs {
			writeDeletionTokenPart(hash, runID)
		}
	}
	for _, reference := range inspection.ProxyClientReferences {
		writeDeletionTokenPart(hash, "proxy_client")
		writeDeletionTokenPart(hash, reference.BindingID)
		writeDeletionTokenPart(hash, fmt.Sprintf("%d", reference.Revision))
	}
	exclusive := secretRefStrings(secretImpact.Exclusive)
	shared := secretRefStrings(secretImpact.Shared)
	for _, reference := range exclusive {
		writeDeletionTokenPart(hash, "exclusive")
		writeDeletionTokenPart(hash, reference)
	}
	for _, reference := range shared {
		writeDeletionTokenPart(hash, "shared")
		writeDeletionTokenPart(hash, reference)
	}
	var token DeletionImpactToken
	copy(token[:], hash.Sum(nil))
	return token, nil
}

// RepositoryImpactToken excludes secret ownership because the SQLite
// repository cannot observe the host SecretStore. Manager binds the complete
// user confirmation separately and passes this narrower CAS token to Delete.
func (inspection DeletionInspection) RepositoryImpactToken() (DeletionImpactToken, error) {
	return deletionImpactToken(inspection, DeletionSecretImpact{})
}

func secretRefStrings(references []SecretRef) []string {
	result := make([]string, 0, len(references))
	for _, reference := range references {
		result = append(result, reference.String())
	}
	sort.Strings(result)
	return result
}

type deletionTokenWriter interface {
	Write([]byte) (int, error)
}

func writeDeletionTokenPart(writer deletionTokenWriter, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write([]byte(value))
}
