// Package codelibrary owns published JavaScript Transform revisions. A saved
// revision is immutable; runtime authority belongs to the frozen revision
// embedded by an Environment, never to the library's current pointer.
package codelibrary

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

const (
	MaxIDBytes          = 128
	MaxDisplayNameBytes = 256
	MaxRevision         = Revision(1<<63 - 1)
)

var (
	ErrInvalidLibrary     = errors.New("Code Library value is invalid")
	ErrCollectionNotFound = errors.New("Code Library collection was not found")
	ErrTransformNotFound  = errors.New("Code Library Transform revision was not found")
	ErrRevisionConflict   = errors.New("Code Library revision conflicts with the expected revision")
	ErrWriteNotCommitted  = errors.New("Code Library write was not committed")

	canonicalID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type CollectionID string
type TransformID string
type Revision uint64

func NewCollectionID(value string) (CollectionID, error) {
	if !validID(value) {
		return "", ErrInvalidLibrary
	}
	return CollectionID(value), nil
}

func NewTransformID(value string) (TransformID, error) {
	if !validID(value) {
		return "", ErrInvalidLibrary
	}
	return TransformID(value), nil
}

func (id CollectionID) String() string { return string(id) }
func (id TransformID) String() string  { return string(id) }

type Collection struct {
	ID          CollectionID `json:"id"`
	DisplayName string       `json:"displayName"`
}

type TransformRevision struct {
	ID           TransformID             `json:"id"`
	Revision     Revision                `json:"revision"`
	CollectionID CollectionID            `json:"collectionId"`
	DisplayName  string                  `json:"displayName"`
	Policy       messagetransform.Policy `json:"policy"`
	PublishedAt  time.Time               `json:"publishedAt"`
}

type CreateCollectionCommand struct {
	ID          CollectionID
	DisplayName string
}

type PublishTransformCommand struct {
	ID               TransformID
	ExpectedRevision Revision
	CollectionID     CollectionID
	DisplayName      string
	Policy           messagetransform.Policy
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
	Revision       TransformRevision
	ActualRevision Revision
}

type Catalog struct {
	Collections []Collection
	Transforms  []TransformRevision
}

type Repository interface {
	CreateCollection(context.Context, Collection) error
	WriteTransform(context.Context, Revision, TransformRevision) (CommitResult, error)
	LoadTransformRevision(context.Context, TransformID, Revision) (TransformRevision, bool, error)
	LoadCurrent(context.Context) (Catalog, error)
}

type Controller interface {
	CreateCollection(context.Context, CreateCollectionCommand) (Collection, error)
	PublishTransform(context.Context, PublishTransformCommand) (TransformRevision, error)
	GetTransformRevision(context.Context, TransformID, Revision) (TransformRevision, error)
	List(context.Context) (Catalog, error)
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
		return nil, ErrInvalidLibrary
	}
	if clock == nil {
		clock = systemClock{}
	}
	return &Manager{repository: repository, clock: clock}, nil
}

func (manager *Manager) CreateCollection(
	ctx context.Context,
	command CreateCollectionCommand,
) (Collection, error) {
	collection := Collection{ID: command.ID, DisplayName: command.DisplayName}
	if manager == nil || ctx == nil || collection.Validate() != nil {
		return Collection{}, ErrInvalidLibrary
	}
	if err := ctx.Err(); err != nil {
		return Collection{}, err
	}
	if err := manager.repository.CreateCollection(ctx, collection); err != nil {
		return Collection{}, err
	}
	return collection, nil
}

func (manager *Manager) PublishTransform(
	ctx context.Context,
	command PublishTransformCommand,
) (TransformRevision, error) {
	if manager == nil || ctx == nil || command.ExpectedRevision >= MaxRevision {
		return TransformRevision{}, ErrInvalidLibrary
	}
	if err := ctx.Err(); err != nil {
		return TransformRevision{}, err
	}
	candidate := TransformRevision{
		ID: command.ID, Revision: command.ExpectedRevision + 1,
		CollectionID: command.CollectionID, DisplayName: command.DisplayName,
		Policy: command.Policy, PublishedAt: manager.clock.Now().UTC().Truncate(time.Millisecond),
	}
	if candidate.Validate() != nil {
		return TransformRevision{}, ErrInvalidLibrary
	}
	result, err := manager.repository.WriteTransform(
		ctx,
		command.ExpectedRevision,
		candidate,
	)
	if err != nil {
		return TransformRevision{}, err
	}
	switch result.Outcome {
	case CommitCommitted:
		if !result.Revision.Equal(candidate) {
			return TransformRevision{}, ErrWriteNotCommitted
		}
		return result.Revision, nil
	case CommitConflict:
		return TransformRevision{}, ErrRevisionConflict
	default:
		return TransformRevision{}, ErrWriteNotCommitted
	}
}

func (manager *Manager) GetTransformRevision(
	ctx context.Context,
	id TransformID,
	revision Revision,
) (TransformRevision, error) {
	if manager == nil || ctx == nil ||
		!validID(string(id)) || revision == 0 || revision > MaxRevision {
		return TransformRevision{}, ErrInvalidLibrary
	}
	if err := ctx.Err(); err != nil {
		return TransformRevision{}, err
	}
	value, exists, err := manager.repository.LoadTransformRevision(ctx, id, revision)
	if err != nil {
		return TransformRevision{}, err
	}
	if !exists {
		return TransformRevision{}, ErrTransformNotFound
	}
	if value.Validate() != nil || value.ID != id || value.Revision != revision {
		return TransformRevision{}, ErrWriteNotCommitted
	}
	return value, nil
}

func (manager *Manager) List(ctx context.Context) (Catalog, error) {
	if manager == nil || ctx == nil {
		return Catalog{}, ErrInvalidLibrary
	}
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	catalog, err := manager.repository.LoadCurrent(ctx)
	if err != nil {
		return Catalog{}, err
	}
	collections := make(map[CollectionID]struct{}, len(catalog.Collections))
	for _, collection := range catalog.Collections {
		if collection.Validate() != nil {
			return Catalog{}, ErrInvalidLibrary
		}
		if _, duplicate := collections[collection.ID]; duplicate {
			return Catalog{}, ErrInvalidLibrary
		}
		collections[collection.ID] = struct{}{}
	}
	transforms := make(map[TransformID]struct{}, len(catalog.Transforms))
	for _, transform := range catalog.Transforms {
		if transform.Validate() != nil {
			return Catalog{}, ErrInvalidLibrary
		}
		if _, exists := collections[transform.CollectionID]; !exists {
			return Catalog{}, ErrInvalidLibrary
		}
		if _, duplicate := transforms[transform.ID]; duplicate {
			return Catalog{}, ErrInvalidLibrary
		}
		transforms[transform.ID] = struct{}{}
	}
	sort.Slice(catalog.Collections, func(left, right int) bool {
		return catalog.Collections[left].ID < catalog.Collections[right].ID
	})
	sort.Slice(catalog.Transforms, func(left, right int) bool {
		return catalog.Transforms[left].ID < catalog.Transforms[right].ID
	})
	return catalog, nil
}

func (collection Collection) Validate() error {
	if !validID(string(collection.ID)) || !validDisplayName(collection.DisplayName) {
		return ErrInvalidLibrary
	}
	return nil
}

func (revision TransformRevision) Validate() error {
	if !validID(string(revision.ID)) || !validID(string(revision.CollectionID)) ||
		revision.Revision == 0 || revision.Revision > MaxRevision ||
		!validDisplayName(revision.DisplayName) || revision.PublishedAt.IsZero() ||
		revision.Policy.Validate() != nil {
		return ErrInvalidLibrary
	}
	return nil
}

func (revision TransformRevision) Equal(other TransformRevision) bool {
	return revision.ID == other.ID && revision.Revision == other.Revision &&
		revision.CollectionID == other.CollectionID && revision.DisplayName == other.DisplayName &&
		revision.Policy == other.Policy && revision.PublishedAt.Equal(other.PublishedAt)
}

func validID(value string) bool {
	return len(value) <= MaxIDBytes && canonicalID.MatchString(value)
}

func validDisplayName(value string) bool {
	if value == "" || len(value) > MaxDisplayNameBytes || !utf8.ValidString(value) ||
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
