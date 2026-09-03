// Package egressprofile owns reusable, published network-exit revisions.
// Environments embed one exact revision so later profile edits cannot alter a
// published Environment or a running Capture.
package egressprofile

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

const (
	MaxRevision Revision = 1<<63 - 1
	DirectID    ID       = "profile.direct"
)

var (
	ErrInvalidProfile    = errors.New("egress profile is invalid")
	ErrProfileNotFound   = errors.New("egress profile revision was not found")
	ErrRevisionConflict  = errors.New("egress profile revision conflicts with the expected revision")
	ErrWriteNotCommitted = errors.New("egress profile write was not committed")

	canonicalID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type ID string
type Revision uint64

func NewID(value string) (ID, error) {
	if !canonicalID.MatchString(value) {
		return "", ErrInvalidProfile
	}
	return ID(value), nil
}

func (id ID) String() string { return string(id) }

type ProfileRevision struct {
	ID          ID                   `json:"id"`
	Revision    Revision             `json:"revision"`
	DisplayName string               `json:"displayName"`
	Policy      egressnetwork.Policy `json:"policy"`
	PublishedAt time.Time            `json:"publishedAt"`
}

func Direct() ProfileRevision {
	return ProfileRevision{
		ID: DirectID, Revision: 1, DisplayName: "Direct · System DNS",
		Policy: egressnetwork.DefaultPolicy(), PublishedAt: time.Unix(0, 0).UTC(),
	}
}

func (profile ProfileRevision) Validate() error {
	normalized, err := profile.Policy.Normalize()
	if !canonicalID.MatchString(profile.ID.String()) || profile.Revision == 0 ||
		profile.Revision > MaxRevision || !validDisplayName(profile.DisplayName) ||
		profile.PublishedAt.IsZero() || err != nil || normalized != profile.Policy {
		return ErrInvalidProfile
	}
	return nil
}

func (profile ProfileRevision) Equal(other ProfileRevision) bool {
	return profile.ID == other.ID && profile.Revision == other.Revision &&
		profile.DisplayName == other.DisplayName && profile.Policy == other.Policy &&
		profile.PublishedAt.Equal(other.PublishedAt)
}

type PublishCommand struct {
	ID               ID
	ExpectedRevision Revision
	DisplayName      string
	Policy           egressnetwork.Policy
}

type CommitOutcome string

const (
	CommitCommitted     CommitOutcome = "committed"
	CommitConflict      CommitOutcome = "conflict"
	CommitNotCommitted  CommitOutcome = "not_committed"
	CommitIndeterminate CommitOutcome = "indeterminate"
)

type CommitResult struct {
	Outcome        CommitOutcome
	Revision       ProfileRevision
	ActualRevision Revision
}

type Repository interface {
	Write(context.Context, Revision, ProfileRevision) (CommitResult, error)
	LoadRevision(context.Context, ID, Revision) (ProfileRevision, bool, error)
	LoadCurrent(context.Context) ([]ProfileRevision, error)
}

type Controller interface {
	Publish(context.Context, PublishCommand) (ProfileRevision, error)
	GetRevision(context.Context, ID, Revision) (ProfileRevision, error)
	List(context.Context) ([]ProfileRevision, error)
}

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type Manager struct {
	repository Repository
	clock      Clock
}

func NewManager(repository Repository, clock Clock) (*Manager, error) {
	if repository == nil {
		return nil, ErrInvalidProfile
	}
	if clock == nil {
		clock = systemClock{}
	}
	return &Manager{repository: repository, clock: clock}, nil
}

func (manager *Manager) Publish(
	ctx context.Context,
	command PublishCommand,
) (ProfileRevision, error) {
	if manager == nil || ctx == nil || command.ID == DirectID ||
		command.ExpectedRevision >= MaxRevision {
		return ProfileRevision{}, ErrInvalidProfile
	}
	if err := ctx.Err(); err != nil {
		return ProfileRevision{}, err
	}
	policy, err := command.Policy.Normalize()
	if err != nil {
		return ProfileRevision{}, ErrInvalidProfile
	}
	candidate := ProfileRevision{
		ID: command.ID, Revision: command.ExpectedRevision + 1,
		DisplayName: command.DisplayName, Policy: policy,
		PublishedAt: manager.clock.Now().UTC().Truncate(time.Millisecond),
	}
	if candidate.Validate() != nil {
		return ProfileRevision{}, ErrInvalidProfile
	}
	result, err := manager.repository.Write(ctx, command.ExpectedRevision, candidate)
	if err != nil {
		return ProfileRevision{}, err
	}
	switch result.Outcome {
	case CommitCommitted:
		if !result.Revision.Equal(candidate) {
			return ProfileRevision{}, ErrWriteNotCommitted
		}
		return result.Revision, nil
	case CommitConflict:
		return ProfileRevision{}, ErrRevisionConflict
	default:
		return ProfileRevision{}, ErrWriteNotCommitted
	}
}

func (manager *Manager) GetRevision(
	ctx context.Context,
	id ID,
	revision Revision,
) (ProfileRevision, error) {
	if manager == nil || ctx == nil || !canonicalID.MatchString(id.String()) ||
		revision == 0 || revision > MaxRevision {
		return ProfileRevision{}, ErrInvalidProfile
	}
	if err := ctx.Err(); err != nil {
		return ProfileRevision{}, err
	}
	if id == DirectID {
		direct := Direct()
		if revision != direct.Revision {
			return ProfileRevision{}, ErrProfileNotFound
		}
		return direct, nil
	}
	profile, exists, err := manager.repository.LoadRevision(ctx, id, revision)
	if err != nil {
		return ProfileRevision{}, err
	}
	if !exists {
		return ProfileRevision{}, ErrProfileNotFound
	}
	if profile.Validate() != nil || profile.ID != id || profile.Revision != revision {
		return ProfileRevision{}, ErrWriteNotCommitted
	}
	return profile, nil
}

func (manager *Manager) List(ctx context.Context) ([]ProfileRevision, error) {
	if manager == nil || ctx == nil {
		return nil, ErrInvalidProfile
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := manager.repository.LoadCurrent(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[ID]struct{}{DirectID: {}}
	for _, profile := range current {
		if profile.Validate() != nil || profile.ID == DirectID {
			return nil, ErrInvalidProfile
		}
		if _, duplicate := seen[profile.ID]; duplicate {
			return nil, ErrInvalidProfile
		}
		seen[profile.ID] = struct{}{}
	}
	sort.Slice(current, func(left, right int) bool { return current[left].ID < current[right].ID })
	return append([]ProfileRevision{Direct()}, current...), nil
}

func validDisplayName(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
