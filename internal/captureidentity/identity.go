// Package captureidentity owns the one stable typed identity shared by
// managed CaptureRuns and ManualCaptures.
package captureidentity

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxIDBytes = 128

var ErrInvalidReference = errors.New("Capture reference is invalid")

type Kind string

const (
	KindManagedRun    Kind = "managed_run"
	KindManualCapture Kind = "manual_capture"
)

type Reference struct {
	Kind Kind   `json:"kind"`
	ID   string `json:"id"`
}

func New(kind Kind, id string) (Reference, error) {
	reference := Reference{Kind: kind, ID: id}
	if err := reference.Validate(); err != nil {
		return Reference{}, err
	}
	return reference, nil
}

func (reference Reference) Validate() error {
	if (reference.Kind != KindManagedRun && reference.Kind != KindManualCapture) ||
		reference.ID == "" || len(reference.ID) > MaxIDBytes ||
		!utf8.ValidString(reference.ID) || strings.TrimSpace(reference.ID) != reference.ID {
		return ErrInvalidReference
	}
	for _, character := range reference.ID {
		if unicode.IsControl(character) || character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' && character != ':' {
			return ErrInvalidReference
		}
	}
	return nil
}

func (reference Reference) Key() string {
	if reference.Validate() != nil {
		return ""
	}
	return string(reference.Kind) + ":" + reference.ID
}

func ParseKey(value string) (Reference, error) {
	kind, id, found := strings.Cut(value, ":")
	if !found {
		return Reference{}, ErrInvalidReference
	}
	return New(Kind(kind), id)
}
