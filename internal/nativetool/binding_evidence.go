package nativetool

import (
	"math"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
)

// BindingEvidence contains only frozen public authority and opaque evidence
// digests. Equality proves consistency, never RPC validity, authenticated source,
// current permission, approval or a committed start. MAC digests fit unchanged.
type BindingEvidence struct {
	Kind                               RequestKind
	Capture                            captureidentity.Reference
	Environment                        environment.EnvironmentID
	EnvironmentRevision                environment.Revision
	EnvironmentDigest                  environment.CandidateDigest
	Route                              environment.ClientToolRoute
	Recording                          environment.ContentRecordingPolicy
	Policy                             environment.PolicySet
	Assignment, Owner                  uint64
	Session                            ID
	SessionRevision, CatalogGeneration uint64
	Catalog                            *Digest
	Parent                             *ID
	Server                             *Digest
	Request, Params, Group             Digest
	Replay                             *Digest
	Name                               string
	RPCID                              *Digest
	CancelledRPCID                     *Digest
}

func safeNativeToolName(name string) bool { return publicName.MatchString(name) }

// The anonymous embedding preserves the original v1 field names and order.
func bindingDigestV1(e BindingEvidence) Digest {
	return hashJSON(struct {
		Domain string
		BindingEvidence
	}{"native-binding/v1", e})
}

func VerifyBindingEvidence(e BindingEvidence, expected Digest) error {
	e.Catalog = copyPointer(e.Catalog)
	e.Parent = copyPointer(e.Parent)
	e.Server = copyPointer(e.Server)
	e.Replay = copyPointer(e.Replay)
	e.RPCID = copyPointer(e.RPCID)
	e.CancelledRPCID = copyPointer(e.CancelledRPCID)
	_, envErr := environment.NewEnvironmentID(e.Environment.String())
	_, sidErr := NewID(string(e.Session))
	if expected == (Digest{}) || e.Kind.Validate() != nil || e.Kind == RequestUnclassified || e.Capture.Validate() != nil || envErr != nil || !revision(uint64(e.EnvironmentRevision)) || e.EnvironmentDigest == (environment.CandidateDigest{}) || e.Route.Validate() != nil || e.Recording.Validate() != nil || e.Policy.Validate() != nil || !revision(e.Assignment) || !revision(e.Owner) || sidErr != nil || !revision(e.SessionRevision) || (e.CatalogGeneration == 0) != (e.Catalog == nil) || e.CatalogGeneration > math.MaxInt64 || (e.Parent == nil) != (e.Server == nil) || (e.Kind == RequestServerReply) != (e.Parent != nil) || (e.Kind == RequestCancel) != (e.CancelledRPCID != nil) || (e.Kind == RequestGetStream || e.Kind == RequestDeleteSession) != (e.RPCID == nil) || len(e.Name) > 256 || e.Name != "" && (e.Kind != RequestToolsCall || !safeNativeToolName(e.Name)) || e.Kind == RequestToolsCall && e.CatalogGeneration == 0 {
		return ErrInvalid
	}
	if e.Parent != nil {
		if _, err := NewID(string(*e.Parent)); err != nil {
			return ErrInvalid
		}
	}
	if bindingDigestV1(e) != expected {
		return ErrInvalid
	}
	return nil
}
