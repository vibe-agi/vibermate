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
	"time"

	"github.com/vibe-agi/vibermate/internal/filetransaction"
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
	path string
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
		path: filepath.Join(directory, pinFileName),
	}
	snapshot, err := filetransaction.Read(store.transactionOptions())
	if err != nil {
		return nil, err
	}
	if !snapshot.Exists {
		return store, nil
	}
	if _, err := decodePins(snapshot.Payload); err != nil {
		return nil, err
	}
	return store, nil
}

func decodePins(payload []byte) (map[string]string, error) {
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
	pins := make(map[string]string, len(document.Pins))
	for rawAddress, fingerprint := range document.Pins {
		address, err := ParseAddress(rawAddress)
		decoded, decodeErr := hex.DecodeString(fingerprint)
		if err != nil || address.String() != rawAddress || decodeErr != nil ||
			len(decoded) != sha256.Size || hex.EncodeToString(decoded) != fingerprint {
			return nil, errors.New("Server pin file contains an invalid entry")
		}
		pins[rawAddress] = fingerprint
	}
	return pins, nil
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
	result := PinResult{Fingerprint: fingerprint}
	err = filetransaction.Update(
		store.transactionOptions(),
		func(snapshot filetransaction.Snapshot) (filetransaction.Mutation, error) {
			pins := make(map[string]string)
			if snapshot.Exists {
				stored, decodeErr := decodePins(snapshot.Payload)
				if decodeErr != nil {
					return filetransaction.Mutation{}, decodeErr
				}
				pins = stored
			}
			if existing, found := pins[address.String()]; found {
				if existing != fingerprint {
					return filetransaction.Mutation{}, ErrServerIdentityChanged
				}
				return filetransaction.Mutation{}, nil
			}
			pins[address.String()] = fingerprint
			payload, encodeErr := json.Marshal(pinDocument{Schema: pinSchema, Pins: pins})
			if encodeErr != nil {
				return filetransaction.Mutation{}, encodeErr
			}
			result.FirstUse = true
			return filetransaction.Mutation{Payload: payload, Write: true}, nil
		},
	)
	if err != nil {
		return PinResult{}, err
	}
	return result, nil
}

func (store *PinStore) transactionOptions() filetransaction.Options {
	return filetransaction.Options{
		Path: store.path, MaximumBytes: maxPinFileBytes, Mode: 0o600,
		TemporaryPrefix: ".server-pins-*",
	}
}
