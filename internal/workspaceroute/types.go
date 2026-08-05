// Package workspaceroute owns machine-scoped workspace route selection inside
// an already-authorized Access plan.
package workspaceroute

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const (
	MaxBindingIDBytes = 128
	DefaultPageLimit  = 50
	MaxPageLimit      = 200
	bindingIDDomain   = "vibermate:workspace-route-binding:v1"
)

var (
	ErrInvalidBinding            = errors.New("workspace route binding is invalid")
	ErrBindingNotFound           = errors.New("workspace route binding was not found")
	ErrRevisionConflict          = errors.New("workspace route binding revision conflict")
	ErrRouteUnavailable          = errors.New("workspace route is unavailable")
	ErrCaptureRunRestartRequired = errors.New("active CaptureRun must stop before changing credential source")
)

type BindingID string

func ParseBindingID(value string) (BindingID, error) {
	if value == "" || len(value) > MaxBindingIDBytes ||
		strings.TrimSpace(value) != value {
		return "", ErrInvalidBinding
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", ErrInvalidBinding
	}
	return BindingID(value), nil
}

func (id BindingID) String() string {
	return string(id)
}

func BindingIDFor(
	accessID access.AccessID,
	machineID workspaceidentity.MachineID,
	workspaceID workspaceidentity.WorkspaceID,
) (BindingID, error) {
	if accessID.String() == "" || machineID.String() == "" ||
		workspaceID.String() == "" {
		return "", ErrInvalidBinding
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		bindingIDDomain,
		accessID.String(),
		machineID.String(),
		workspaceID.String(),
	}, "\x00")))
	return BindingID(base64.RawURLEncoding.EncodeToString(digest[:])), nil
}

type Revision uint64

func (revision Revision) Valid() bool {
	return revision > 0 && uint64(revision) <= uint64(^uint64(0)>>1)
}

type Record struct {
	ID                          BindingID
	AccessID                    access.AccessID
	MachineID                   workspaceidentity.MachineID
	WorkspaceID                 workspaceidentity.WorkspaceID
	MachineRegistrationRevision uint64
	WorkspaceLabel              string
	WorkspaceEvidence           workspaceidentity.Evidence
	ProfileID                   access.EndpointProfileID
	Revision                    Revision
	UpdatedAt                   time.Time
}

func (record Record) Validate() error {
	if _, err := ParseBindingID(record.ID.String()); err != nil ||
		record.AccessID.String() == "" ||
		record.MachineID.String() == "" ||
		record.WorkspaceID.String() == "" ||
		record.ProfileID.String() == "" ||
		!record.Revision.Valid() ||
		record.UpdatedAt.IsZero() {
		return ErrInvalidBinding
	}
	if _, err := workspaceidentity.NewScope(
		record.MachineID,
		record.WorkspaceID,
		record.WorkspaceLabel,
		record.WorkspaceEvidence,
		record.MachineRegistrationRevision,
		1,
	); err != nil {
		return ErrInvalidBinding
	}
	return nil
}

type CreateRequest struct {
	ID                          BindingID
	AccessID                    access.AccessID
	MachineID                   workspaceidentity.MachineID
	WorkspaceID                 workspaceidentity.WorkspaceID
	MachineRegistrationRevision uint64
	WorkspaceLabel              string
	WorkspaceEvidence           workspaceidentity.Evidence
	ProfileID                   access.EndpointProfileID
	UpdatedAt                   time.Time
}

func (request CreateRequest) Record() (Record, error) {
	record := Record{
		ID:                          request.ID,
		AccessID:                    request.AccessID,
		MachineID:                   request.MachineID,
		WorkspaceID:                 request.WorkspaceID,
		MachineRegistrationRevision: request.MachineRegistrationRevision,
		WorkspaceLabel:              request.WorkspaceLabel,
		WorkspaceEvidence:           request.WorkspaceEvidence,
		ProfileID:                   request.ProfileID,
		Revision:                    1,
		UpdatedAt:                   request.UpdatedAt,
	}
	return record, record.Validate()
}

type PageRequest struct {
	Limit int
}

func (request PageRequest) Normalized() PageRequest {
	if request.Limit <= 0 {
		request.Limit = DefaultPageLimit
	}
	if request.Limit > MaxPageLimit {
		request.Limit = MaxPageLimit
	}
	return request
}

type Repository interface {
	ResolveOrCreate(context.Context, CreateRequest) (Record, error)
	Get(context.Context, BindingID) (Record, error)
	CompareAndSwap(context.Context, UpdateMutation) (Record, error)
	List(context.Context, PageRequest) ([]Record, error)
}

// UpdateMutation keeps the auth-bootstrap safety condition in the same SQLite
// transaction as the route CAS. A separate preflight read would race a newly
// created CaptureRun.
type UpdateMutation struct {
	ID                        BindingID
	Expected                  Revision
	ProfileID                 access.EndpointProfileID
	UpdatedAt                 time.Time
	RequireNoActiveCaptureRun bool
}

func (mutation UpdateMutation) Validate() error {
	if _, err := ParseBindingID(mutation.ID.String()); err != nil ||
		!mutation.Expected.Valid() || mutation.ProfileID.String() == "" ||
		mutation.UpdatedAt.IsZero() {
		return ErrInvalidBinding
	}
	return nil
}

type Resolution struct {
	BindingID       BindingID
	BindingRevision Revision
	ProfileID       access.EndpointProfileID
	ProfileRevision access.Revision
	lease           *pinLease
}

// Release ends the request-time pin. It is idempotent and must be deferred by
// the Exchange immediately after a successful resolution.
func (resolution Resolution) Release() {
	if resolution.lease != nil {
		resolution.lease.release()
	}
}

func (resolution Resolution) Validate() error {
	if _, err := ParseBindingID(resolution.BindingID.String()); err != nil ||
		!resolution.BindingRevision.Valid() ||
		resolution.ProfileID.String() == "" ||
		resolution.ProfileRevision == 0 {
		return ErrInvalidBinding
	}
	return nil
}

type Resolver interface {
	Resolve(
		context.Context,
		access.AccessPlanSnapshot,
		workspaceidentity.Scope,
	) (Resolution, error)
}

type State string

const (
	StateActive      State = "active"
	StateUnavailable State = "workspace_route_unavailable"
)

type AuthPresentation string

const (
	AuthViberMateAccount AuthPresentation = "vibermate_account"
	AuthClientOAuth      AuthPresentation = "client_oauth"
	AuthClient           AuthPresentation = "client_auth"
	AuthNone             AuthPresentation = "none"
)

type ProfileOption struct {
	ProfileID         access.EndpointProfileID
	ProfileRevision   access.Revision
	Kind              access.EndpointProfileKind
	Label             string
	ModelPresentation string
	AuthPresentation  AuthPresentation
	AuthLabel         string
	Available         bool
}

type View struct {
	Record             Record
	State              State
	Profiles           []ProfileOption
	PinnedRequestCount int
}

func (view View) Validate() error {
	if err := view.Record.Validate(); err != nil {
		return err
	}
	switch view.State {
	case StateActive:
		if len(view.Profiles) == 0 {
			return ErrInvalidBinding
		}
	case StateUnavailable:
	default:
		return ErrInvalidBinding
	}
	return nil
}

type Directory interface {
	ListBindings(context.Context, PageRequest) ([]View, error)
	GetBinding(context.Context, BindingID) (View, error)
	UpdateBinding(
		context.Context,
		BindingID,
		Revision,
		access.EndpointProfileID,
	) (View, error)
}

type Controller interface {
	Resolver
	Directory
}

func validateRecordKey(record Record, scope workspaceidentity.Scope) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if err := scope.Validate(); err != nil ||
		record.MachineID != scope.MachineID() ||
		record.WorkspaceID != scope.WorkspaceID() {
		return fmt.Errorf("%w: binding scope does not match the CaptureRun", ErrRouteUnavailable)
	}
	return nil
}
