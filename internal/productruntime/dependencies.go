package productruntime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const instanceIDBytes = 20

var ErrInstanceIDGeneration = errors.New("instance ID generation failed")

// Clock supplies process timestamps. Implementations must be safe for
// concurrent use.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the system wall clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type synchronizedReader struct {
	mu     sync.Mutex
	reader io.Reader
}

func newSynchronizedReader(reader io.Reader) *synchronizedReader {
	return &synchronizedReader{reader: reader}
}

func (reader *synchronizedReader) Read(destination []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.reader.Read(destination)
}

// InstanceIDSource creates opaque process-lifetime incarnation identifiers.
type InstanceIDSource interface {
	NewInstanceID(context.Context) (string, error)
}

// RandomInstanceIDSource uses an explicitly supplied cryptographic reader.
type RandomInstanceIDSource struct {
	reader io.Reader
}

func NewRandomInstanceIDSource(reader io.Reader) RandomInstanceIDSource {
	return RandomInstanceIDSource{reader: reader}
}

// NewCryptographicInstanceIDSource constructs the production random source.
func NewCryptographicInstanceIDSource() RandomInstanceIDSource {
	return NewRandomInstanceIDSource(rand.Reader)
}

func (s RandomInstanceIDSource) NewInstanceID(ctx context.Context) (string, error) {
	if s.reader == nil {
		return "", fmt.Errorf("%w: random reader is nil", ErrInstanceIDGeneration)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInstanceIDGeneration, err)
	}
	randomBytes := make([]byte, instanceIDBytes)
	if _, err := io.ReadFull(s.reader, randomBytes); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInstanceIDGeneration, err)
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}
