// Package workspaceidentity owns one installation-scoped machine identity and
// the opaque workspace identities derived from exact local working directories.
package workspaceidentity

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/filetransaction"
)

const (
	identitySchema           = "vibermate-machine-identity-v1"
	identityFileName         = "machine-identity.json"
	identityBytes            = 32
	workspaceDerivationV1    = uint64(1)
	maxWorkspaceLabelBytes   = 120
	maxOpaqueIdentityBytes   = 128
	maxIdentityFileBytes     = 4096
	workspaceDerivationLabel = "vibermate.workspace/v1"
)

var (
	ErrInvalidIdentity     = errors.New("workspace identity is invalid")
	ErrIdentityUnavailable = errors.New("workspace identity is unavailable")
	ErrResolverStopped     = errors.New("workspace identity resolver is stopped")
)

type MachineID string

func ParseMachineID(value string) (MachineID, error) {
	if !validOpaqueIdentity(value) {
		return "", ErrInvalidIdentity
	}
	return MachineID(value), nil
}

func (id MachineID) String() string {
	return string(id)
}

func (id MachineID) Short() string {
	value := id.String()
	if len(value) <= 10 {
		return value
	}
	return value[:10]
}

type WorkspaceID string

func ParseWorkspaceID(value string) (WorkspaceID, error) {
	if !validOpaqueIdentity(value) {
		return "", ErrInvalidIdentity
	}
	return WorkspaceID(value), nil
}

func (id WorkspaceID) String() string {
	return string(id)
}

type Evidence string

const (
	EvidenceLocalLauncher       Evidence = "local_launcher"
	EvidenceRegisteredCompanion Evidence = "registered_companion"
)

func (evidence Evidence) Valid() bool {
	return evidence == EvidenceLocalLauncher ||
		evidence == EvidenceRegisteredCompanion
}

// Scope is immutable identity evidence attached to one CaptureRun. It is not
// authentication and it grants no route outside the Environment that later admits a
// request.
type Scope struct {
	machineID            MachineID
	workspaceID          WorkspaceID
	workspaceLabel       string
	evidence             Evidence
	registrationRevision uint64
	derivationRevision   uint64
}

func NewScope(
	machineID MachineID,
	workspaceID WorkspaceID,
	workspaceLabel string,
	evidence Evidence,
	registrationRevision uint64,
	derivationRevision uint64,
) (Scope, error) {
	scope := Scope{
		machineID:            machineID,
		workspaceID:          workspaceID,
		workspaceLabel:       workspaceLabel,
		evidence:             evidence,
		registrationRevision: registrationRevision,
		derivationRevision:   derivationRevision,
	}
	if err := scope.Validate(); err != nil {
		return Scope{}, err
	}
	return scope, nil
}

func (scope Scope) Validate() error {
	if !validOpaqueIdentity(scope.machineID.String()) ||
		!validOpaqueIdentity(scope.workspaceID.String()) ||
		!validLabel(scope.workspaceLabel) ||
		!scope.evidence.Valid() ||
		scope.registrationRevision == 0 ||
		scope.derivationRevision == 0 {
		return ErrInvalidIdentity
	}
	return nil
}

func (scope Scope) MachineID() MachineID {
	return scope.machineID
}

func (scope Scope) WorkspaceID() WorkspaceID {
	return scope.workspaceID
}

func (scope Scope) WorkspaceLabel() string {
	return scope.workspaceLabel
}

func (scope Scope) Evidence() Evidence {
	return scope.evidence
}

func (scope Scope) RegistrationRevision() uint64 {
	return scope.registrationRevision
}

func (scope Scope) DerivationRevision() uint64 {
	return scope.derivationRevision
}

type LocalResolver interface {
	ResolveLocal(context.Context, string) (Scope, error)
	MachineID() MachineID
}

// Manager keeps the namespace key only in bounded process memory. The physical
// SecretStore is the single persistence authority for the installation record.
type Manager struct {
	mu        sync.RWMutex
	machineID MachineID
	revision  uint64
	createdAt time.Time
	key       []byte
	stopped   bool
}

var _ LocalResolver = (*Manager)(nil)

func Open(
	ctx context.Context,
	dataDirectory string,
	random io.Reader,
	now time.Time,
) (*Manager, error) {
	if ctx == nil || dataDirectory == "" || !filepath.IsAbs(dataDirectory) ||
		filepath.Clean(dataDirectory) != dataDirectory || random == nil || now.IsZero() {
		return nil, fmt.Errorf("%w: dependencies are incomplete", ErrIdentityUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ensurePrivateDirectory(dataDirectory); err != nil {
		return nil, err
	}
	state, key, err := loadOrCreate(
		filepath.Join(dataDirectory, identityFileName),
		random,
		now.UTC(),
	)
	if err != nil {
		return nil, err
	}
	return &Manager{
		machineID: state.ID,
		revision:  state.Revision,
		createdAt: state.CreatedAt,
		key:       key,
	}, nil
}

func (manager *Manager) MachineID() MachineID {
	if manager == nil {
		return ""
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.stopped {
		return ""
	}
	return manager.machineID
}

func (manager *Manager) ResolveLocal(
	ctx context.Context,
	cwd string,
) (Scope, error) {
	if manager == nil || ctx == nil {
		return Scope{}, ErrIdentityUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Scope{}, err
	}
	canonical, err := canonicalDirectory(cwd)
	if err != nil {
		return Scope{}, err
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.stopped || len(manager.key) != identityBytes {
		return Scope{}, ErrResolverStopped
	}
	mac := hmac.New(sha256.New, manager.key)
	_, _ = mac.Write([]byte(workspaceDerivationLabel))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(runtime.GOOS))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(canonical))
	workspaceID := WorkspaceID(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	label := filepath.Base(canonical)
	if label == "." || label == string(filepath.Separator) || label == "" {
		label = "workspace"
	}
	return NewScope(
		manager.machineID,
		workspaceID,
		label,
		EvidenceLocalLauncher,
		manager.revision,
		workspaceDerivationV1,
	)
}

func (manager *Manager) Shutdown(context.Context) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.stopped {
		return nil
	}
	clear(manager.key)
	manager.key = nil
	manager.stopped = true
	return nil
}

type fileState struct {
	Schema                string    `json:"schema"`
	ID                    MachineID `json:"machineId"`
	Revision              uint64    `json:"revision"`
	WorkspaceNamespaceKey string    `json:"workspaceNamespaceKey"`
	CreatedAt             time.Time `json:"createdAt"`
}

func (state fileState) validate() ([]byte, error) {
	if state.Schema != identitySchema || state.Revision == 0 ||
		state.CreatedAt.IsZero() {
		return nil, ErrInvalidIdentity
	}
	if _, err := ParseMachineID(state.ID.String()); err != nil {
		return nil, err
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(
		state.WorkspaceNamespaceKey,
	)
	if err != nil || len(key) != identityBytes ||
		base64.RawURLEncoding.EncodeToString(key) != state.WorkspaceNamespaceKey {
		clear(key)
		return nil, ErrInvalidIdentity
	}
	return key, nil
}

func loadOrCreate(
	path string,
	random io.Reader,
	now time.Time,
) (fileState, []byte, error) {
	var state fileState
	var key []byte
	err := filetransaction.Update(
		filetransaction.Options{
			Path: path, MaximumBytes: maxIdentityFileBytes, Mode: 0o600,
			TemporaryPrefix: ".machine-identity-*",
		},
		func(snapshot filetransaction.Snapshot) (filetransaction.Mutation, error) {
			if snapshot.Exists {
				stored, storedKey, readErr := decodeIdentity(snapshot)
				if readErr == nil {
					state = stored
					key = storedKey
					return filetransaction.Mutation{}, nil
				}
				if !errors.Is(readErr, ErrInvalidIdentity) {
					return filetransaction.Mutation{}, readErr
				}
			}

			machineBytes := make([]byte, identityBytes)
			namespaceKey := make([]byte, identityBytes)
			defer clear(machineBytes)
			if _, readErr := io.ReadFull(random, machineBytes); readErr != nil {
				clear(namespaceKey)
				return filetransaction.Mutation{}, fmt.Errorf(
					"generate MachineID: %w", readErr,
				)
			}
			if _, readErr := io.ReadFull(random, namespaceKey); readErr != nil {
				clear(namespaceKey)
				return filetransaction.Mutation{}, fmt.Errorf(
					"generate workspace namespace: %w", readErr,
				)
			}
			state = fileState{
				Schema: identitySchema,
				ID: MachineID(
					base64.RawURLEncoding.EncodeToString(machineBytes),
				),
				Revision: 1,
				WorkspaceNamespaceKey: base64.RawURLEncoding.EncodeToString(
					namespaceKey,
				),
				CreatedAt: now,
			}
			payload, encodeErr := json.Marshal(state)
			if encodeErr != nil {
				clear(namespaceKey)
				return filetransaction.Mutation{}, encodeErr
			}
			key = namespaceKey
			return filetransaction.Mutation{Payload: payload, Write: true}, nil
		},
	)
	if err != nil {
		clear(key)
		return fileState{}, nil, fmt.Errorf(
			"%w: persist installation identity: %v",
			ErrIdentityUnavailable,
			err,
		)
	}
	return state, key, nil
}

func decodeIdentity(
	snapshot filetransaction.Snapshot,
) (fileState, []byte, error) {
	if !snapshot.Exists || len(snapshot.Payload) == 0 ||
		len(snapshot.Payload) > maxIdentityFileBytes ||
		(runtime.GOOS != "windows" && snapshot.Mode.Perm()&0o077 != 0) {
		return fileState{}, nil, ErrInvalidIdentity
	}
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Payload))
	decoder.DisallowUnknownFields()
	var state fileState
	if err := decoder.Decode(&state); err != nil {
		return fileState{}, nil, ErrInvalidIdentity
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fileState{}, nil, ErrInvalidIdentity
	}
	key, err := state.validate()
	return state, key, err
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("%w: create identity directory", ErrIdentityUnavailable)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: identity directory is invalid", ErrIdentityUnavailable)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("%w: protect identity directory", ErrIdentityUnavailable)
		}
	}
	return nil
}

func canonicalDirectory(cwd string) (string, error) {
	if cwd == "" || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
		return "", fmt.Errorf("%w: working directory is not absolute and clean", ErrInvalidIdentity)
	}
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("%w: resolve working directory", ErrInvalidIdentity)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: resolve working directory", ErrInvalidIdentity)
	}
	return filepath.Clean(canonical), nil
}

func validOpaqueIdentity(value string) bool {
	if value == "" || len(value) > maxOpaqueIdentityBytes || !utf8.ValidString(value) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == identityBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validLabel(value string) bool {
	return value != "" && len(value) <= maxWorkspaceLabelBytes &&
		utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}
