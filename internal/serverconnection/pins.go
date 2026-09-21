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
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/filetransaction"
)

const (
	pinSchema       = "vibermate-server-pins-v1"
	trustSchema     = "vibermate-server-trust-v1"
	pinFileName     = "server-pins.json"
	maxPinFileBytes = 1 << 20
)

var (
	ErrInvalidCertificate    = errors.New("ViberMate Server certificate is invalid")
	ErrServerIdentityChanged = errors.New("ViberMate Server identity changed")
	ErrPinnedIdentityMissing = errors.New("ViberMate Server has no pinned identity to migrate")
)

type TrustMode string

const (
	TrustModePinnedLeaf  TrustMode = "pinned_leaf"
	TrustModeSystemRoots TrustMode = "system_roots"
)

type PinResult struct {
	Fingerprint string
	FirstUse    bool
	Mode        TrustMode
}

type pinDocument struct {
	Schema string            `json:"schema"`
	Pins   map[string]string `json:"pins"`
}

type trustRecord struct {
	Mode        TrustMode `json:"mode"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

type trustDocument struct {
	Schema  string                 `json:"schema"`
	Servers map[string]trustRecord `json:"servers"`
}

// PinStore is the CLI's persistent Runtime Server trust boundary. Existing
// first-use leaf pins remain exact. New certificates that validate through the
// platform roots are enrolled as system-root trust so normal leaf renewal does
// not mutate trust. The historical name and file path are retained on purpose
// so upgrades never lose or silently replace an existing pin.
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
	if _, err := decodeTrust(snapshot.Payload); err != nil {
		return nil, err
	}
	return store, nil
}

func decodeTrust(payload []byte) (map[string]trustRecord, error) {
	if len(payload) == 0 || len(payload) > maxPinFileBytes {
		return nil, errors.New("Server pin file is invalid")
	}
	var envelope struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, errors.New("Server pin file is invalid")
	}
	trust := make(map[string]trustRecord)
	switch envelope.Schema {
	case pinSchema:
		var document pinDocument
		if err := decodeTrustDocument(payload, &document); err != nil || document.Pins == nil {
			return nil, errors.New("Server pin file is invalid")
		}
		for address, fingerprint := range document.Pins {
			trust[address] = trustRecord{
				Mode: TrustModePinnedLeaf, Fingerprint: fingerprint,
			}
		}
	case trustSchema:
		var document trustDocument
		if err := decodeTrustDocument(payload, &document); err != nil || document.Servers == nil {
			return nil, errors.New("Server pin file is invalid")
		}
		trust = document.Servers
	default:
		return nil, errors.New("Server pin file is invalid")
	}
	for rawAddress, record := range trust {
		address, err := ParseAddress(rawAddress)
		if err != nil || address.String() != rawAddress || !record.valid() {
			return nil, errors.New("Server pin file contains an invalid entry")
		}
	}
	return trust, nil
}

func decodeTrustDocument(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Server pin file contains trailing data")
	}
	return nil
}

func (record trustRecord) valid() bool {
	switch record.Mode {
	case TrustModePinnedLeaf:
		decoded, err := hex.DecodeString(record.Fingerprint)
		return err == nil && len(decoded) == sha256.Size &&
			hex.EncodeToString(decoded) == record.Fingerprint
	case TrustModeSystemRoots:
		return record.Fingerprint == ""
	default:
		return false
	}
}

func (store *PinStore) Verify(
	address Address,
	rawCertificates [][]byte,
	now time.Time,
) (PinResult, error) {
	_, fingerprint, err := parsePeerCertificate(address, rawCertificates, now)
	if err != nil {
		return PinResult{}, err
	}
	return store.verify(
		address,
		fingerprint,
		errors.New("leaf pin explicitly requested"),
		true,
	)
}

// VerifyPeer selects and persists the trust mode on first connection. A chain
// that validates through roots is never converted into a leaf pin. An unknown
// private issuer retains the legacy TOFU behavior, but the certificate must
// still cover the selected Server host. Once a mode exists, verification never
// falls back to another mode.
func (store *PinStore) VerifyPeer(
	address Address,
	rawCertificates [][]byte,
	now time.Time,
	roots *x509.CertPool,
) (PinResult, error) {
	leaf, fingerprint, err := parsePeerCertificate(address, rawCertificates, now)
	if err != nil {
		return PinResult{}, err
	}
	publicErr := verifySystemRoots(leaf, rawCertificates[1:], now, roots)
	unknownIssuer := publicErr != nil &&
		verifyPresentedChain(leaf, rawCertificates[1:], now) == nil
	return store.verify(address, fingerprint, publicErr, unknownIssuer)
}

func parsePeerCertificate(
	address Address,
	rawCertificates [][]byte,
	now time.Time,
) (*x509.Certificate, string, error) {
	if address.String() == "" || now.IsZero() || len(rawCertificates) == 0 {
		return nil, "", ErrInvalidCertificate
	}
	parsed, err := x509.ParseCertificate(rawCertificates[0])
	if err != nil || now.Before(parsed.NotBefore) || !now.Before(parsed.NotAfter) {
		return nil, "", ErrInvalidCertificate
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil || parsed.VerifyHostname(host) != nil {
		return nil, "", ErrInvalidCertificate
	}
	digest := sha256.Sum256(rawCertificates[0])
	return parsed, hex.EncodeToString(digest[:]), nil
}

func verifySystemRoots(
	leaf *x509.Certificate,
	rawIntermediates [][]byte,
	now time.Time,
	roots *x509.CertPool,
) error {
	intermediates := x509.NewCertPool()
	for _, raw := range rawIntermediates {
		certificate, err := x509.ParseCertificate(raw)
		if err != nil {
			return ErrInvalidCertificate
		}
		intermediates.AddCert(certificate)
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

// verifyPresentedChain distinguishes an internally valid private chain from a
// malformed or otherwise ineligible certificate. It does not establish trust:
// its only caller may retain an existing TOFU boundary after this succeeds.
func verifyPresentedChain(
	leaf *x509.Certificate,
	rawChain [][]byte,
	now time.Time,
) error {
	presentedRoots := x509.NewCertPool()
	intermediates := rawChain
	if len(rawChain) == 0 {
		presentedRoots.AddCert(leaf)
	} else {
		root, err := x509.ParseCertificate(rawChain[len(rawChain)-1])
		if err != nil {
			return err
		}
		presentedRoots.AddCert(root)
		intermediates = rawChain[:len(rawChain)-1]
	}
	return verifySystemRoots(leaf, intermediates, now, presentedRoots)
}

func (store *PinStore) verify(
	address Address,
	fingerprint string,
	publicErr error,
	unknownIssuer bool,
) (PinResult, error) {
	if store == nil || address.String() == "" || fingerprint == "" {
		return PinResult{}, ErrInvalidCertificate
	}
	result := PinResult{Fingerprint: fingerprint}
	err := filetransaction.Update(
		store.transactionOptions(),
		func(snapshot filetransaction.Snapshot) (filetransaction.Mutation, error) {
			trust := make(map[string]trustRecord)
			if snapshot.Exists {
				stored, decodeErr := decodeTrust(snapshot.Payload)
				if decodeErr != nil {
					return filetransaction.Mutation{}, decodeErr
				}
				trust = stored
			}
			if existing, found := trust[address.String()]; found {
				result.Mode = existing.Mode
				switch existing.Mode {
				case TrustModePinnedLeaf:
					if existing.Fingerprint != fingerprint {
						return filetransaction.Mutation{}, ErrServerIdentityChanged
					}
				case TrustModeSystemRoots:
					if publicErr != nil {
						return filetransaction.Mutation{}, errors.Join(
							ErrInvalidCertificate,
							publicErr,
						)
					}
				default:
					return filetransaction.Mutation{}, ErrInvalidCertificate
				}
				return filetransaction.Mutation{}, nil
			}
			if publicErr == nil {
				result.Mode = TrustModeSystemRoots
				trust[address.String()] = trustRecord{Mode: TrustModeSystemRoots}
			} else if unknownIssuer {
				result.Mode = TrustModePinnedLeaf
				result.FirstUse = true
				trust[address.String()] = trustRecord{
					Mode: TrustModePinnedLeaf, Fingerprint: fingerprint,
				}
			} else {
				return filetransaction.Mutation{}, errors.Join(
					ErrInvalidCertificate,
					publicErr,
				)
			}
			return encodeTrust(trust)
		},
	)
	if err != nil {
		return PinResult{}, err
	}
	return result, nil
}

// UseSystemRoots changes an existing exact leaf pin into normal PKI trust. It
// is intentionally separate from verification so callers can require a fresh,
// successful system-root handshake before committing this migration.
func (store *PinStore) UseSystemRoots(address Address) error {
	if store == nil || address.String() == "" {
		return ErrPinnedIdentityMissing
	}
	return filetransaction.Update(
		store.transactionOptions(),
		func(snapshot filetransaction.Snapshot) (filetransaction.Mutation, error) {
			if !snapshot.Exists {
				return filetransaction.Mutation{}, ErrPinnedIdentityMissing
			}
			trust, err := decodeTrust(snapshot.Payload)
			if err != nil {
				return filetransaction.Mutation{}, err
			}
			existing, found := trust[address.String()]
			if !found {
				return filetransaction.Mutation{}, ErrPinnedIdentityMissing
			}
			if existing.Mode == TrustModeSystemRoots {
				return filetransaction.Mutation{}, nil
			}
			if existing.Mode != TrustModePinnedLeaf {
				return filetransaction.Mutation{}, ErrServerIdentityChanged
			}
			trust[address.String()] = trustRecord{Mode: TrustModeSystemRoots}
			return encodeTrust(trust)
		},
	)
}

func encodeTrust(trust map[string]trustRecord) (filetransaction.Mutation, error) {
	payload, err := json.Marshal(trustDocument{Schema: trustSchema, Servers: trust})
	if err != nil {
		return filetransaction.Mutation{}, err
	}
	return filetransaction.Mutation{Payload: payload, Write: true}, nil
}

func (store *PinStore) transactionOptions() filetransaction.Options {
	return filetransaction.Options{
		Path: store.path, MaximumBytes: maxPinFileBytes, Mode: 0o600,
		TemporaryPrefix: ".server-pins-*",
	}
}
