// Package workspacedefault owns the optional Environment selected for future
// managed runs in one installation-scoped workspace. It is a convenience
// choice, never a routing or decryption authority: every Capture still freezes
// an ordinary captureassignment at launch.
package workspacedefault

import (
	"context"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const MaxRevision = uint64(1<<63 - 1)

var (
	ErrInvalidDefault       = errors.New("Workspace Environment default is invalid")
	ErrDefaultNotFound      = errors.New("Workspace Environment default was not found")
	ErrRevisionConflict     = errors.New("Workspace Environment default revision conflicts with the expected revision")
	ErrEnvironmentNotActive = errors.New("Workspace Environment default must reference an active user Environment")
	ErrWriteNotCommitted    = errors.New("Workspace Environment default write was not committed")
	ErrCommitUnknown        = errors.New("Workspace Environment default commit outcome is unknown")
)

type Key struct {
	MachineID   workspaceidentity.MachineID
	WorkspaceID workspaceidentity.WorkspaceID
}

func NewKey(machineID, workspaceID string) (Key, error) {
	machine, machineErr := workspaceidentity.ParseMachineID(machineID)
	workspace, workspaceErr := workspaceidentity.ParseWorkspaceID(workspaceID)
	if machineErr != nil || workspaceErr != nil {
		return Key{}, ErrInvalidDefault
	}
	return Key{MachineID: machine, WorkspaceID: workspace}, nil
}

func (key Key) Validate() error {
	parsed, err := NewKey(key.MachineID.String(), key.WorkspaceID.String())
	if err != nil || parsed != key {
		return ErrInvalidDefault
	}
	return nil
}

type Record struct {
	Key           Key
	EnvironmentID environment.EnvironmentID
	Revision      uint64
	UpdatedAt     time.Time
}

func (record Record) Validate() error {
	if record.Key.Validate() != nil || record.EnvironmentID == "" ||
		record.EnvironmentID == environment.SystemTransparentID ||
		record.Revision == 0 || record.Revision > MaxRevision ||
		record.UpdatedAt.IsZero() ||
		!record.UpdatedAt.Equal(record.UpdatedAt.UTC().Truncate(time.Millisecond)) {
		return ErrInvalidDefault
	}
	if _, err := environment.NewEnvironmentID(record.EnvironmentID.String()); err != nil {
		return ErrInvalidDefault
	}
	return nil
}

type CommitOutcome string

const (
	CommitCommitted     CommitOutcome = "committed"
	CommitConflict      CommitOutcome = "conflict"
	CommitNotCommitted  CommitOutcome = "not_committed"
	CommitIndeterminate CommitOutcome = "indeterminate"
)

type CommitResult struct {
	Outcome CommitOutcome
	Record  Record
	Actual  uint64
	Deleted bool
}

type Repository interface {
	Load(context.Context, Key) (Record, bool, error)
	Write(context.Context, uint64, Record) (CommitResult, error)
	Delete(context.Context, Key, uint64) (CommitResult, error)
}

type Clock interface {
	Now() time.Time
}

type EnvironmentReader interface {
	Get(context.Context, environment.EnvironmentID) (environment.EnvironmentSnapshot, error)
}

type SetCommand struct {
	Key              Key
	ExpectedRevision uint64
	EnvironmentID    environment.EnvironmentID
}

type ClearCommand struct {
	Key              Key
	ExpectedRevision uint64
}

type Controller interface {
	Get(context.Context, Key) (Record, bool, error)
	Resolve(context.Context, workspaceidentity.Scope) (Record, bool, error)
	Set(context.Context, SetCommand) (Record, error)
	Clear(context.Context, ClearCommand) error
}
