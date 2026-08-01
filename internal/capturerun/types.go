// Package capturerun owns short-lived, persisted child-process attribution
// capabilities. Raw capabilities are returned only in a one-time LaunchGrant;
// SQLite stores domain-separated hashes.
package capturerun

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const (
	MaxRunIDBytes          = 128
	MaxPathBytes           = 4096
	MaxExecutableLabelByte = 256
)

var (
	ErrInvalidRequest     = errors.New("invalid CaptureRun request")
	ErrCapabilityRejected = errors.New("CaptureRun capability rejected")
	ErrStateConflict      = errors.New("CaptureRun state conflict")
	ErrRuntimeStopping    = errors.New("CaptureRun runtime is stopping")
)

type State string

const (
	StateCreated  State = "created"
	StateAttached State = "attached"
	StateFinished State = "finished"
	StateRevoked  State = "revoked"
	StateExpired  State = "expired"
)

// Observation answers whether traffic was actually seen through this run.
// Launching a child proves only that a child was launched: a program that
// ignores proxy variables, clears its environment, dials a socket directly, or
// uses QUIC is not captured, and saying otherwise is the difference between a
// working setup and a silently broken one.
type Observation string

const (
	ObservationWaitingForTraffic Observation = "waiting_for_traffic"
	ObservationObserved          Observation = "observed"
)

// valid requires an explicit state. Leaving it unset would let a writer that
// forgot the field look the same as one that observed nothing, and those are
// different facts.
func (observation Observation) valid() bool {
	switch observation {
	case ObservationWaitingForTraffic, ObservationObserved:
		return true
	default:
		return false
	}
}

func (state State) active() bool {
	return state == StateCreated || state == StateAttached
}

type CapabilityDigest [sha256.Size]byte

func (digest CapabilityDigest) valid() bool {
	return digest != CapabilityDigest{}
}

// DurableRecord is the complete SQLite representation. It contains hashes,
// never bearer values or child argv.
type DurableRecord struct {
	ID                    string
	ProxyCapabilityHash   CapabilityDigest
	ControlCapabilityHash CapabilityDigest
	CWD                   string
	ExecutableLabel       string
	CatalogRevision       clientadapter.CatalogRevision
	Adapter               *clientadapter.Evidence
	ProcessID             int
	State                 State
	// Observation is recorded from real authenticated proxy traffic only. It
	// is never inferred from a fingerprint, user agent, loopback source port,
	// or connection reuse, because those are shared between processes.
	Observation     Observation
	FirstObservedAt time.Time
	CreatedAt       time.Time
	ExpiresAt       time.Time
	UpdatedAt       time.Time
}

func (record DurableRecord) Validate() error {
	if err := validateID(record.ID); err != nil {
		return err
	}
	if !record.Observation.valid() {
		return fmt.Errorf("%w: observation state is invalid", ErrInvalidRequest)
	}
	// An observation time is evidence: it exists exactly when something was
	// observed, and it cannot predate the run it belongs to.
	if record.Observed() {
		if record.FirstObservedAt.IsZero() ||
			record.FirstObservedAt.Before(record.CreatedAt) {
			return fmt.Errorf(
				"%w: observation time is inconsistent",
				ErrInvalidRequest,
			)
		}
	} else if !record.FirstObservedAt.IsZero() {
		return fmt.Errorf(
			"%w: an unobserved run carries an observation time",
			ErrInvalidRequest,
		)
	}
	if !record.ProxyCapabilityHash.valid() ||
		!record.ControlCapabilityHash.valid() {
		return fmt.Errorf("%w: capability hash is empty", ErrInvalidRequest)
	}
	if err := validateAbsolutePath("working directory", record.CWD); err != nil {
		return err
	}
	if err := validateText(
		"executable label",
		record.ExecutableLabel,
		MaxExecutableLabelByte,
	); err != nil {
		return err
	}
	if !record.CatalogRevision.Valid() {
		return fmt.Errorf(
			"%w: client catalog revision is invalid",
			ErrInvalidRequest,
		)
	}
	if record.Adapter != nil {
		if err := record.Adapter.Validate(); err != nil ||
			record.Adapter.CatalogRevision != record.CatalogRevision {
			return fmt.Errorf(
				"%w: client adapter evidence is invalid",
				ErrInvalidRequest,
			)
		}
	}
	if record.ProcessID < 0 {
		return fmt.Errorf("%w: process ID is negative", ErrInvalidRequest)
	}
	switch record.State {
	case StateCreated:
		if record.ProcessID != 0 {
			return fmt.Errorf("%w: created run has a process ID", ErrInvalidRequest)
		}
	case StateAttached:
		if record.ProcessID <= 0 {
			return fmt.Errorf("%w: attached run has no process ID", ErrInvalidRequest)
		}
	case StateFinished, StateRevoked, StateExpired:
	default:
		return fmt.Errorf("%w: CaptureRun state is invalid", ErrInvalidRequest)
	}
	if record.CreatedAt.IsZero() ||
		record.ExpiresAt.IsZero() ||
		record.UpdatedAt.IsZero() ||
		record.ExpiresAt.Before(record.CreatedAt) ||
		record.UpdatedAt.Before(record.CreatedAt) {
		return fmt.Errorf("%w: CaptureRun timestamps are invalid", ErrInvalidRequest)
	}
	return nil
}

// View is a redacted immutable representation safe for control responses.
type View struct {
	ID              string    `json:"id"`
	ExecutableLabel string    `json:"executableLabel"`
	CWD             string    `json:"cwd"`
	ProcessID       int       `json:"processId,omitempty"`
	State           State     `json:"state"`
	CreatedAt       time.Time `json:"createdAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

func viewOf(record DurableRecord) View {
	return View{
		ID:              record.ID,
		ExecutableLabel: record.ExecutableLabel,
		CWD:             record.CWD,
		ProcessID:       record.ProcessID,
		State:           record.State,
		CreatedAt:       record.CreatedAt,
		ExpiresAt:       record.ExpiresAt,
	}
}

type ProxyCapability struct {
	value string
}

type ControlCapability struct {
	value string
}

// Value returns the bearer value for the narrow launcher/control boundary.
// Neither capability type implements fmt.Stringer or JSON marshaling.
func (capability ProxyCapability) Value() string {
	return capability.value
}

func NewProxyCapability(value string) (ProxyCapability, error) {
	if err := validateCapability(value); err != nil {
		return ProxyCapability{}, err
	}
	return ProxyCapability{value: value}, nil
}

func NewControlCapability(value string) (ControlCapability, error) {
	if err := validateCapability(value); err != nil {
		return ControlCapability{}, err
	}
	return ControlCapability{value: value}, nil
}

func (capability ControlCapability) Value() string {
	return capability.value
}

// LaunchGrant returns each raw capability exactly once to the caller that will
// supervise the child. The Manager does not retain either value.
type LaunchGrant struct {
	Run               View
	ProxyCapability   ProxyCapability
	ControlCapability ControlCapability
}

type CreateCommand struct {
	CWD             string
	ExecutablePath  string
	Lifetime        time.Duration
	CatalogRevision clientadapter.CatalogRevision
	Adapter         *clientadapter.Evidence
}

func (command CreateCommand) validate(maxLifetime time.Duration) error {
	if err := validateAbsolutePath("working directory", command.CWD); err != nil {
		return err
	}
	if err := validateAbsolutePath("executable path", command.ExecutablePath); err != nil {
		return err
	}
	if command.Lifetime <= 0 || command.Lifetime > maxLifetime {
		return fmt.Errorf("%w: CaptureRun lifetime is invalid", ErrInvalidRequest)
	}
	if !command.CatalogRevision.Valid() {
		return fmt.Errorf(
			"%w: client catalog revision is invalid",
			ErrInvalidRequest,
		)
	}
	if command.Adapter != nil {
		if err := command.Adapter.Validate(); err != nil ||
			command.Adapter.CatalogRevision != command.CatalogRevision {
			return fmt.Errorf(
				"%w: client adapter evidence is invalid",
				ErrInvalidRequest,
			)
		}
	}
	label := filepath.Base(command.ExecutablePath)
	return validateText("executable label", label, MaxExecutableLabelByte)
}

// Evidence is frozen after proxy capability authorization. It intentionally
// excludes the capability hash and raw value.
type Evidence struct {
	RunID           string
	Observed        bool
	FirstObservedAt time.Time
	CWD             string
	ExecutableLabel string
	CatalogRevision clientadapter.CatalogRevision
	Adapter         *clientadapter.Evidence
	ProcessID       int
	ExpiresAt       time.Time
}

func evidenceOf(record DurableRecord) Evidence {
	return Evidence{
		RunID:           record.ID,
		Observed:        record.Observed(),
		FirstObservedAt: record.FirstObservedAt,
		CWD:             record.CWD,
		ExecutableLabel: record.ExecutableLabel,
		CatalogRevision: record.CatalogRevision,
		Adapter:         cloneAdapter(record.Adapter),
		ProcessID:       record.ProcessID,
		ExpiresAt:       record.ExpiresAt,
	}
}

func cloneAdapter(
	evidence *clientadapter.Evidence,
) *clientadapter.Evidence {
	if evidence == nil {
		return nil
	}
	cloned := *evidence
	return &cloned
}

type Recovery struct {
	ExpiredCount int
	ActiveCount  int
}

type Repository interface {
	Create(context.Context, DurableRecord) error
	AuthorizeProxy(
		ctx context.Context,
		digest CapabilityDigest,
		now time.Time,
	) (DurableRecord, error)
	Attach(
		ctx context.Context,
		runID string,
		digest CapabilityDigest,
		processID int,
		now time.Time,
	) (DurableRecord, error)
	Heartbeat(
		ctx context.Context,
		runID string,
		digest CapabilityDigest,
		now time.Time,
		expiresAt time.Time,
	) (DurableRecord, error)
	Finish(
		ctx context.Context,
		runID string,
		digest CapabilityDigest,
		now time.Time,
	) error
	Recover(context.Context, time.Time) (Recovery, error)
	RevokeActive(context.Context, time.Time) (int, error)
}

// Controller is the trusted control-plane lifecycle boundary. A launcher
// capability authorizes creation outside this package; per-run control
// capabilities authorize only the returned run.
type Controller interface {
	Create(context.Context, CreateCommand) (LaunchGrant, error)
	Attach(context.Context, string, ControlCapability, int) (View, error)
	Heartbeat(
		context.Context,
		string,
		ControlCapability,
		time.Duration,
	) (View, error)
	Finish(context.Context, string, ControlCapability) error
}

type ProxyAuthorizer interface {
	AuthorizeProxy(context.Context, ProxyCapability) (Evidence, error)
}

func validateID(value string) error {
	return validateText("CaptureRun ID", value, MaxRunIDBytes)
}

func validateCapability(value string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("%w: capability encoding is invalid", ErrCapabilityRejected)
	}
	return nil
}

func validateAbsolutePath(label, value string) error {
	if value == "" ||
		len(value) > MaxPathBytes ||
		!filepath.IsAbs(value) ||
		filepath.Clean(value) != value {
		return fmt.Errorf("%w: %s is not a clean absolute path", ErrInvalidRequest, label)
	}
	return validateText(label, value, MaxPathBytes)
}

func validateText(label, value string, limit int) error {
	if value == "" ||
		len(value) > limit ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidRequest, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidRequest, label)
		}
	}
	return nil
}

// Observed reports whether authenticated traffic actually arrived through this
// run.
func (record DurableRecord) Observed() bool {
	return record.Observation == ObservationObserved
}

// WithObservedTraffic marks the first authenticated connection. It is
// monotonic and idempotent, and a run that is no longer active cannot become
// observed afterwards. It reports whether anything changed so a caller can
// avoid a write it does not need.
func (record DurableRecord) WithObservedTraffic(
	at time.Time,
) (DurableRecord, bool) {
	if !record.State.active() || record.Observed() || at.IsZero() {
		return record, false
	}
	record.Observation = ObservationObserved
	record.FirstObservedAt = at.UTC()
	return record, true
}
