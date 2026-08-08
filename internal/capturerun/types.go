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

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const (
	MaxRunIDBytes          = 128
	MaxPathBytes           = 4096
	MaxExecutableLabelByte = 256
	MaxLocalUserLabelBytes = 128
)

var (
	ErrInvalidRequest     = errors.New("invalid CaptureRun request")
	ErrNotFound           = errors.New("CaptureRun not found")
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
	ID                          string
	ProxyCapabilityHash         CapabilityDigest
	ControlCapabilityHash       CapabilityDigest
	CWD                         string
	CanonicalExecutablePath     string
	LocalUserLabel              string
	ExecutableLabel             string
	CatalogRevision             clientadapter.CatalogRevision
	Adapter                     *clientadapter.Evidence
	MachineID                   workspaceidentity.MachineID
	MachineRegistrationRevision uint64
	WorkspaceID                 workspaceidentity.WorkspaceID
	WorkspaceLabel              string
	WorkspaceEvidence           workspaceidentity.Evidence
	WorkspaceDerivationRevision uint64
	// Recognition says whether the catalog knows this client at all, which is
	// not recoverable from Adapter alone: a run without evidence may be a
	// program nobody catalogued or a known client at an uncatalogued version.
	Recognition clientadapter.Recognition
	ProcessID   int
	State       State
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
	if err := validateAbsolutePath(
		"canonical executable",
		record.CanonicalExecutablePath,
	); err != nil {
		return err
	}
	if !ValidLocalUserLabel(record.LocalUserLabel) {
		return fmt.Errorf("%w: local user label is invalid", ErrInvalidRequest)
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
	if !record.workspaceScopeEmpty() {
		if _, err := workspaceidentity.NewScope(
			record.MachineID,
			record.WorkspaceID,
			record.WorkspaceLabel,
			record.WorkspaceEvidence,
			record.MachineRegistrationRevision,
			record.WorkspaceDerivationRevision,
		); err != nil {
			return fmt.Errorf("%w: workspace scope is invalid", ErrInvalidRequest)
		}
	}
	// Evidence and recognition are one fact seen twice: a run that verified
	// carries evidence, and a run that carries evidence verified. An unstated
	// recognition reads as unknown, which is the conservative one: it is what
	// a program nobody catalogued gets, and it grants nothing.
	if !NormalizedRecognition(record.Recognition).Valid() ||
		(NormalizedRecognition(record.Recognition) == clientadapter.RecognitionVerified) !=
			(record.Adapter != nil) {
		return fmt.Errorf(
			"%w: client recognition and adapter evidence disagree",
			ErrInvalidRequest,
		)
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
	ID              string `json:"id"`
	ExecutableLabel string `json:"executableLabel"`
	CWD             string `json:"cwd"`
	// CanonicalExecutablePath is Desktop-local read-only diagnostic evidence.
	// Launcher and Server projections deliberately do not serialize it.
	CanonicalExecutablePath     string                     `json:"-"`
	LocalUserLabel              string                     `json:"localUserLabel,omitempty"`
	MachineID                   string                     `json:"machineId,omitempty"`
	MachineRegistrationRevision uint64                     `json:"machineRegistrationRevision,omitempty"`
	WorkspaceID                 string                     `json:"workspaceId,omitempty"`
	WorkspaceLabel              string                     `json:"workspaceLabel,omitempty"`
	WorkspaceEvidence           workspaceidentity.Evidence `json:"workspaceEvidence,omitempty"`
	WorkspaceDerivationRevision uint64                     `json:"workspaceDerivationRevision,omitempty"`
	ProcessID                   int                        `json:"processId,omitempty"`
	State                       State                      `json:"state"`
	// Observation says whether traffic was actually seen through this run.
	Observation     Observation `json:"observation"`
	FirstObservedAt time.Time   `json:"-"`
	// Recognition says whether this build has release evidence for the client.
	Recognition clientadapter.Recognition `json:"recognition"`
	// CatalogRevision and Adapter remain available to trusted projections but
	// are never serialized directly. The contracted launcher wire deliberately
	// omits release hashes and feature flags carried by internal evidence.
	CatalogRevision clientadapter.CatalogRevision `json:"-"`
	Adapter         *clientadapter.Evidence       `json:"-"`
	CreatedAt       time.Time                     `json:"createdAt"`
	ExpiresAt       time.Time                     `json:"expiresAt"`
	UpdatedAt       time.Time                     `json:"-"`
}

// ViewOf renders a run for a reader. It carries no capability.
// NormalizedRecognition reads an unstated recognition as unknown.
func NormalizedRecognition(
	recognition clientadapter.Recognition,
) clientadapter.Recognition {
	if recognition == "" {
		return clientadapter.RecognitionUnknown
	}
	return recognition
}

func ViewOf(record DurableRecord) View {
	return View{
		ID:                          record.ID,
		ExecutableLabel:             record.ExecutableLabel,
		CWD:                         record.CWD,
		CanonicalExecutablePath:     record.CanonicalExecutablePath,
		LocalUserLabel:              record.LocalUserLabel,
		MachineID:                   record.MachineID.String(),
		MachineRegistrationRevision: record.MachineRegistrationRevision,
		WorkspaceID:                 record.WorkspaceID.String(),
		WorkspaceLabel:              record.WorkspaceLabel,
		WorkspaceEvidence:           record.WorkspaceEvidence,
		WorkspaceDerivationRevision: record.WorkspaceDerivationRevision,
		ProcessID:                   record.ProcessID,
		State:                       record.State,
		Observation:                 record.Observation,
		FirstObservedAt:             record.FirstObservedAt,
		Recognition:                 NormalizedRecognition(record.Recognition),
		CatalogRevision:             record.CatalogRevision,
		Adapter:                     cloneAdapter(record.Adapter),
		CreatedAt:                   record.CreatedAt,
		ExpiresAt:                   record.ExpiresAt,
		UpdatedAt:                   record.UpdatedAt,
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
	credential, err := capturecredential.Parse(value)
	if err != nil || credential.Kind() != capturecredential.KindManagedRun {
		return ProxyCapability{}, fmt.Errorf(
			"%w: proxy capability encoding is invalid",
			ErrCapabilityRejected,
		)
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
	CWD                     string
	CanonicalExecutablePath string
	// ExecutableLabel is the verifier-confirmed invocation name shown to a
	// person. CanonicalExecutablePath is persisted only as Desktop-local,
	// read-only audit evidence; reading it back never restores launch authority.
	ExecutableLabel string
	Lifetime        time.Duration
	CatalogRevision clientadapter.CatalogRevision
	Adapter         *clientadapter.Evidence
	Recognition     clientadapter.Recognition
	Workspace       workspaceidentity.Scope
	LocalUserLabel  string
}

func (command CreateCommand) validate(maxLifetime time.Duration) error {
	if err := validateAbsolutePath("working directory", command.CWD); err != nil {
		return err
	}
	if err := validateAbsolutePath(
		"canonical executable",
		command.CanonicalExecutablePath,
	); err != nil {
		return err
	}
	if err := validateText(
		"executable label",
		command.ExecutableLabel,
		MaxExecutableLabelByte,
	); err != nil {
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
	if command.Workspace != (workspaceidentity.Scope{}) &&
		command.Workspace.Validate() != nil {
		return fmt.Errorf("%w: workspace scope is invalid", ErrInvalidRequest)
	}
	if !ValidLocalUserLabel(command.LocalUserLabel) {
		return fmt.Errorf("%w: local user label is invalid", ErrInvalidRequest)
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
	// Evidence and recognition are one fact seen twice: a run that verified
	// carries evidence, and a run that carries evidence verified.
	if !NormalizedRecognition(command.Recognition).Valid() ||
		(NormalizedRecognition(command.Recognition) == clientadapter.RecognitionVerified) !=
			(command.Adapter != nil) {
		return fmt.Errorf(
			"%w: client recognition and adapter evidence disagree",
			ErrInvalidRequest,
		)
	}
	return nil
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
	Workspace       workspaceidentity.Scope
	LocalUserLabel  string
}

// AdmissionRef returns the exact short-lived ingress identity owned by a
// run. It is safe to expose as an audit join key and never carries a bearer.
func AdmissionRef(runID string) (string, error) {
	if err := validateID(runID); err != nil {
		return "", err
	}
	return "capture-run/" + runID, nil
}

func evidenceOf(record DurableRecord) Evidence {
	workspace, _ := workspaceidentity.NewScope(
		record.MachineID,
		record.WorkspaceID,
		record.WorkspaceLabel,
		record.WorkspaceEvidence,
		record.MachineRegistrationRevision,
		record.WorkspaceDerivationRevision,
	)
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
		Workspace:       workspace,
		LocalUserLabel:  record.LocalUserLabel,
	}
}

// ValidLocalUserLabel validates display-only launcher metadata. The value is
// never authentication evidence and an empty value means unavailable.
func ValidLocalUserLabel(value string) bool {
	if value == "" {
		return true
	}
	return len(value) <= MaxLocalUserLabelBytes && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func (record DurableRecord) workspaceScopeEmpty() bool {
	return record.MachineID.String() == "" &&
		record.MachineRegistrationRevision == 0 &&
		record.WorkspaceID.String() == "" &&
		record.WorkspaceLabel == "" &&
		record.WorkspaceEvidence == "" &&
		record.WorkspaceDerivationRevision == 0
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
	// List is a read for a person, not a control path: it carries no
	// capability and returns no capability, only what a run is and whether
	// anything was seen through it.
	List(context.Context, PageRequest) (Page, error)
	Get(context.Context, string) (View, error)
	Recover(context.Context, time.Time) (Recovery, error)
	RevokeActive(context.Context, time.Time) (int, error)
}

// PageRequest bounds a read of the run list.
type PageRequest struct {
	Limit int
}

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

func (request PageRequest) Normalized() PageRequest {
	if request.Limit <= 0 {
		request.Limit = DefaultPageLimit
	}
	if request.Limit > MaxPageLimit {
		request.Limit = MaxPageLimit
	}
	return request
}

type Page struct {
	Items []View `json:"items"`
}

// Reader is the read side a control API exposes.
type Reader interface {
	ListRuns(context.Context, PageRequest) (Page, error)
	GetRun(context.Context, string) (View, error)
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
