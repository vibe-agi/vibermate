package exchange

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

const attemptIDBytes = 20

// AttemptIDSource mints one independent identity per upstream attempt.
// ADR-0015 section 10 requires the attempt identity to be generated
// independently of the Exchange identity; the association travels as a typed
// parent reference instead of a delimiter-joined string.
type AttemptIDSource interface {
	NewAttemptID() (string, error)
}

type randomAttemptIDSource struct {
	random io.Reader
}

// NewCryptographicAttemptIDSource returns the production attempt identity
// source.
func NewCryptographicAttemptIDSource() AttemptIDSource {
	return randomAttemptIDSource{random: rand.Reader}
}

// NewAttemptIDSource returns a source over an explicit random reader.
func NewAttemptIDSource(random io.Reader) AttemptIDSource {
	return randomAttemptIDSource{random: random}
}

func (source randomAttemptIDSource) NewAttemptID() (string, error) {
	if source.random == nil {
		return "", errors.New("attempt ID random source is nil")
	}
	data := make([]byte, attemptIDBytes)
	if _, err := io.ReadFull(source.random, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
