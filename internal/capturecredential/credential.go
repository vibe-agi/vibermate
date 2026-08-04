// Package capturecredential defines the closed wire shape of proxy
// capabilities. Its type tag selects an authentication authority only; it
// carries no Access, Profile, route, account, model, machine, or workspace
// metadata.
package capturecredential

import (
	"encoding/base64"
	"errors"
)

const EntropyBytes = 32

var ErrInvalid = errors.New("capture proxy credential is invalid")

type Kind string

const (
	KindManagedRun    Kind = "managed_run"
	KindManualCapture Kind = "manual_capture"
)

func (kind Kind) Valid() bool {
	return kind == KindManagedRun || kind == KindManualCapture
}

type Credential struct {
	kind  Kind
	value string
}

func New(kind Kind, entropy []byte) (Credential, error) {
	if !kind.Valid() || len(entropy) != EntropyBytes {
		return Credential{}, ErrInvalid
	}
	value := prefix(kind) + base64.RawURLEncoding.EncodeToString(entropy)
	return Credential{kind: kind, value: value}, nil
}

func Parse(value string) (Credential, error) {
	kind, encoded, ok := split(value)
	if !ok {
		return Credential{}, ErrInvalid
	}
	entropy, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(entropy) != EntropyBytes ||
		base64.RawURLEncoding.EncodeToString(entropy) != encoded {
		return Credential{}, ErrInvalid
	}
	return Credential{kind: kind, value: value}, nil
}

func (credential Credential) Valid() bool {
	parsed, err := Parse(credential.value)
	return err == nil && parsed.kind == credential.kind
}

func (credential Credential) Kind() Kind {
	return credential.kind
}

// Value returns the opaque wire value only to a one-time grant or an
// authentication adapter. Credential formatting remains redacted.
func (credential Credential) Value() string {
	return credential.value
}

func (Credential) String() string {
	return "[REDACTED]"
}

func (Credential) GoString() string {
	return "capturecredential.Credential{[REDACTED]}"
}

func prefix(kind Kind) string {
	switch kind {
	case KindManagedRun:
		return "run_"
	case KindManualCapture:
		return "manual_"
	default:
		return ""
	}
}

func split(value string) (Kind, string, bool) {
	for _, candidate := range []Kind{KindManagedRun, KindManualCapture} {
		candidatePrefix := prefix(candidate)
		if len(value) > len(candidatePrefix) &&
			value[:len(candidatePrefix)] == candidatePrefix {
			return candidate, value[len(candidatePrefix):], true
		}
	}
	return "", "", false
}
