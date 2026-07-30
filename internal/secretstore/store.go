// Package secretstore defines host-neutral secret references and bounded
// process-memory secret values. It does not persist secret bytes.
package secretstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	referencePrefix   = "secret://"
	maxReferenceBytes = 1024
	maxSecretBytes    = 64 << 10
)

var (
	ErrInvalidReference  = errors.New("secret reference is invalid")
	ErrNotFound          = errors.New("secret was not found")
	ErrLocked            = errors.New("secret store is locked")
	ErrDenied            = errors.New("secret read was denied")
	ErrUnavailable       = errors.New("secret store is unavailable")
	ErrReadOnly          = errors.New("secret store is read-only")
	ErrRevisionConflict  = errors.New("secret revision conflicts with the expected revision")
	ErrRevisionExhausted = errors.New("secret revision is exhausted")
	ErrDestroyed         = errors.New("secret value was destroyed")
)

const MaxRevision Revision = 1<<63 - 1

// Revision is monotonic for one logical secret reference.
type Revision uint64

// State is safe control-plane metadata. It never implies that secret bytes
// have crossed the SecretStore boundary.
type State string

const (
	StateConfigured  State = "configured"
	StateMissing     State = "missing"
	StateUnavailable State = "unavailable"
)

type Metadata struct {
	State    State
	Revision Revision
}

func (metadata Metadata) Validate() error {
	switch metadata.State {
	case StateConfigured:
		if metadata.Revision == 0 || metadata.Revision > MaxRevision {
			return errors.New("configured secret metadata has an invalid revision")
		}
	case StateMissing:
		if metadata.Revision != 0 {
			return errors.New("missing secret metadata has a revision")
		}
	case StateUnavailable:
		// A nonzero revision means the host can observe the item and preserve
		// its CAS identity, but the current process cannot read its value.
		if metadata.Revision > MaxRevision {
			return errors.New("unavailable secret metadata has an invalid revision")
		}
	default:
		return errors.New("secret metadata state is invalid")
	}
	return nil
}

type Reference struct {
	namespace string
	id        string
}

func ParseReference(value string) (Reference, error) {
	if len(value) == 0 ||
		len(value) > maxReferenceBytes ||
		!utf8.ValidString(value) ||
		!strings.HasPrefix(value, referencePrefix) ||
		strings.ContainsAny(value, "?# \t\r\n") {
		return Reference{}, ErrInvalidReference
	}
	remainder := strings.TrimPrefix(value, referencePrefix)
	namespace, id, found := strings.Cut(remainder, "/")
	if !found || !validSegment(namespace) {
		return Reference{}, ErrInvalidReference
	}
	segments := strings.Split(id, "/")
	if len(segments) == 0 {
		return Reference{}, ErrInvalidReference
	}
	for _, segment := range segments {
		if !validSegment(segment) {
			return Reference{}, ErrInvalidReference
		}
	}
	return Reference{namespace: namespace, id: id}, nil
}

func (reference Reference) String() string {
	if !validSegment(reference.namespace) {
		return ""
	}
	for _, segment := range strings.Split(reference.id, "/") {
		if !validSegment(segment) {
			return ""
		}
	}
	return referencePrefix + reference.namespace + "/" + reference.id
}

func (reference Reference) Namespace() string {
	return reference.namespace
}

func (reference Reference) ID() string {
	return reference.id
}

func validSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' ||
			character == '_' ||
			character == '.' {
			continue
		}
		return false
	}
	return true
}

// Value owns one bounded secret byte slice. CopyBytes transfers a temporary
// copy to the caller; both copies must be destroyed at their respective
// lifetime boundaries.
type Value struct {
	mu        sync.Mutex
	bytes     []byte
	destroyed bool
}

func NewValue(value []byte) (*Value, error) {
	if len(value) == 0 || len(value) > maxSecretBytes {
		return nil, errors.New("secret value has an invalid size")
	}
	if bytes.IndexByte(value, 0) >= 0 ||
		bytes.IndexByte(value, '\r') >= 0 ||
		bytes.IndexByte(value, '\n') >= 0 {
		return nil, errors.New("secret value contains a header control byte")
	}
	return &Value{bytes: bytes.Clone(value)}, nil
}

func (value *Value) CopyBytes() ([]byte, error) {
	if value == nil {
		return nil, ErrDestroyed
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.destroyed {
		return nil, ErrDestroyed
	}
	return bytes.Clone(value.bytes), nil
}

func (value *Value) Destroy() {
	if value == nil {
		return
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.destroyed {
		return
	}
	clear(value.bytes)
	value.bytes = nil
	value.destroyed = true
}

// Reader returns a fresh process-memory value for each read. Implementations
// must never include secret bytes in errors.
type Reader interface {
	Read(context.Context, Reference) (*Value, error)
}

// ReplaceCommand performs CAS against the revision stored by the physical
// SecretStore. Value remains owned by the caller and must be destroyed there.
type ReplaceCommand struct {
	Reference        Reference
	ExpectedRevision Revision
	Value            *Value
}

// Store is the host-neutral secret authority used by ProductRuntime. A
// read-only implementation reports ErrReadOnly from Replace.
type Store interface {
	Reader
	Inspect(context.Context, Reference) (Metadata, error)
	Replace(context.Context, ReplaceCommand) (Metadata, error)
}

// Factory opens exactly one host-selected physical Store. ProductRuntime
// consumes the resulting Store and never selects a backend by name.
type Factory interface {
	Open(context.Context) (Store, error)
}

func ValidateReaderResult(value *Value, err error) (*Value, error) {
	if err != nil {
		if value != nil {
			value.Destroy()
		}
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("%w: reader returned nil", ErrUnavailable)
	}
	copied, copyErr := value.CopyBytes()
	if copyErr != nil {
		value.Destroy()
		return nil, fmt.Errorf("%w: reader returned an unusable value", ErrUnavailable)
	}
	clear(copied)
	return value, nil
}
