package serverconnection

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	pinSchema       = "vibermate-server-pins-v1"
	pinFileName     = "server-pins.json"
	maxPinFileBytes = 1 << 20
)

var (
	ErrInvalidCertificate    = errors.New("ViberMate Server certificate is invalid")
	ErrServerIdentityChanged = errors.New("ViberMate Server identity changed")
)

type PinResult struct {
	Fingerprint string
	FirstUse    bool
}

type pinDocument struct {
	Schema string            `json:"schema"`
	Pins   map[string]string `json:"pins"`
}

// PinStore is the CLI's persistent trust-on-first-use boundary. A Server
// address is pinned to the exact leaf certificate seen on its first encrypted
// connection. A changed certificate is never accepted or silently replaced.
type PinStore struct {
	mu        sync.Mutex
	directory string
	path      string
	pins      map[string]string
}

func OpenPinStore(directory string) (*PinStore, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errors.New("Server pin directory is invalid")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("prepare Server pin directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	store := &PinStore{
		directory: directory,
		path:      filepath.Join(directory, pinFileName),
		pins:      make(map[string]string),
	}
	payload, err := os.ReadFile(store.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return store, nil
	case err != nil:
		return nil, err
	}
	if len(payload) == 0 || len(payload) > maxPinFileBytes {
		return nil, errors.New("Server pin file is invalid")
	}
	var document pinDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Schema != pinSchema || document.Pins == nil {
		return nil, errors.New("Server pin file is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("Server pin file contains trailing data")
	}
	for rawAddress, fingerprint := range document.Pins {
		address, err := ParseAddress(rawAddress)
		decoded, decodeErr := hex.DecodeString(fingerprint)
		if err != nil || address.String() != rawAddress || decodeErr != nil ||
			len(decoded) != sha256.Size || hex.EncodeToString(decoded) != fingerprint {
			return nil, errors.New("Server pin file contains an invalid entry")
		}
		store.pins[rawAddress] = fingerprint
	}
	return store, nil
}

func (store *PinStore) Verify(
	address Address,
	rawCertificates [][]byte,
	now time.Time,
) (PinResult, error) {
	if store == nil || address.String() == "" || now.IsZero() || len(rawCertificates) == 0 {
		return PinResult{}, ErrInvalidCertificate
	}
	parsed, err := x509.ParseCertificate(rawCertificates[0])
	if err != nil || now.Before(parsed.NotBefore) || !now.Before(parsed.NotAfter) {
		return PinResult{}, ErrInvalidCertificate
	}
	digest := sha256.Sum256(rawCertificates[0])
	fingerprint := hex.EncodeToString(digest[:])
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, found := store.pins[address.String()]; found {
		if existing != fingerprint {
			return PinResult{Fingerprint: fingerprint}, ErrServerIdentityChanged
		}
		return PinResult{Fingerprint: fingerprint}, nil
	}
	candidate := make(map[string]string, len(store.pins)+1)
	for key, value := range store.pins {
		candidate[key] = value
	}
	candidate[address.String()] = fingerprint
	if err := store.persist(candidate); err != nil {
		return PinResult{}, err
	}
	store.pins[address.String()] = fingerprint
	return PinResult{Fingerprint: fingerprint, FirstUse: true}, nil
}

func (store *PinStore) persist(pins map[string]string) error {
	payload, err := json.Marshal(pinDocument{Schema: pinSchema, Pins: pins})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(store.directory, ".server-pins-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return err
	}
	committed = true
	return nil
}
