// Package resourcedeletion carries the one vocabulary every destructive
// operation in this product answers with.
//
// Four resources can be deleted — an Environment, an upstream Endpoint, a
// Capture, and the evidence archive itself — and each can be held by something
// that would break if it went. They differ in what holds them and in nothing
// else: every one either completes, or refuses and names the exact holders. A
// refusal that only says "in use" leaves the user with no move, so the holders
// are part of the answer rather than a log line behind it.
package resourcedeletion

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// Released is the measurable receipt shared by every evidence deletion.
//
// Keeping this shape beside Result gives the control plane and persistence
// plane one contract: persistence owns how the graph is removed, while the
// caller can report exactly what disappeared without importing SQLite types.
type Released struct {
	Exchanges   uint64
	Envelopes   uint64
	Activities  uint64
	Connections uint64
	Attempts    uint64
	Approvals   uint64
	Assignments uint64
	Captures    uint64
}

// Archive is the narrow destructive boundary implemented by the runtime
// store. A Capture purge and a whole-archive clear span several repositories,
// so neither operation belongs to any one of them.
type Archive interface {
	DeleteCapture(
		ctx context.Context,
		captureKind string,
		captureID string,
	) (Released, error)
	ClearEvidence(ctx context.Context) (Released, error)
}

// Kind names why something is held. The set is closed because each member is a
// case the UI must be able to explain and the user must be able to resolve.
type Kind string

const (
	// KindRunningCapture is a Capture still admitting traffic. Its admission
	// decisions are frozen against the authority being deleted.
	KindRunningCapture Kind = "running_capture"
	// KindWorkspaceDefault is a workspace whose next run would be left pointing
	// at something that no longer exists.
	KindWorkspaceDefault Kind = "workspace_default"
	// KindEnvironmentRoute is a published route that names the resource.
	KindEnvironmentRoute Kind = "environment_route"
	// KindOwnedAccount is a ProviderAccount the resource owns. Its credential
	// lives in the host SecretStore, so removing the owner silently would
	// destroy something the user never named.
	KindOwnedAccount Kind = "owned_account"
)

func (kind Kind) Valid() bool {
	switch kind {
	case KindRunningCapture, KindWorkspaceDefault, KindEnvironmentRoute,
		KindOwnedAccount:
		return true
	default:
		return false
	}
}

var (
	// ErrInvalidHolder rejects a holder that could not be acted on.
	ErrInvalidHolder = errors.New("resource deletion holder is invalid")
	// ErrTargetNotFound keeps a destructive operation honest when its named
	// authority disappeared before the transaction committed. A zero-count
	// receipt is not proof that a delete happened.
	ErrTargetNotFound = errors.New("resource deletion target was not found")
)

// Holder is one reason a delete did not happen.
//
// ID is what the user would act on; Label is what they would recognise. Both
// are required, because an ID with no label is unreadable and a label with no
// ID is unactionable.
type Holder struct {
	Kind   Kind
	ID     string
	Label  string
	Detail string
}

func (holder Holder) Validate() error {
	if !holder.Kind.Valid() ||
		strings.TrimSpace(holder.ID) == "" || len(holder.ID) > 256 ||
		strings.TrimSpace(holder.Label) == "" || len(holder.Label) > 256 ||
		len(holder.Detail) > 512 {
		return ErrInvalidHolder
	}
	return nil
}

// Result is what a delete returns. Deleted and Holders are mutually exclusive
// by construction: a delete that happened has no holders, and a delete that was
// refused names at least one.
type Result struct {
	Deleted bool
	Holders []Holder
}

// Refused builds the answer for a delete that could not happen.
func Refused(holders []Holder) (Result, error) {
	if len(holders) == 0 {
		return Result{}, ErrInvalidHolder
	}
	ordered := make([]Holder, 0, len(holders))
	for _, holder := range holders {
		if err := holder.Validate(); err != nil {
			return Result{}, err
		}
		ordered = append(ordered, holder)
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].Kind != ordered[right].Kind {
			return ordered[left].Kind < ordered[right].Kind
		}
		return ordered[left].ID < ordered[right].ID
	})
	return Result{Deleted: false, Holders: ordered}, nil
}

// Completed builds the answer for a delete that happened.
func Completed() Result {
	return Result{Deleted: true, Holders: []Holder{}}
}
