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
	TrustModePinnedCA    TrustMode = "pinned_ca"
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
// platform roots use system-root trust; private chains pin their CA to this
// Server address so leaf renewal does not mutate trust. The historical name and
// file path are retained on purpose
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
	case TrustModePinnedLeaf, TrustModePinnedCA:
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
		"",
	)
}

// VerifyPeer selects and persists the trust mode on first connection. A chain
// that validates through roots is never converted into a leaf pin. An unknown
// private issuer is pinned on first use, scoped to the selected Server host and
// port. A standalone leaf remains an exact leaf pin. Once a mode exists,
// verification never falls back to another mode.
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
	issuer, chainErr := verifyPresentedChain(leaf, rawCertificates[1:], now)
	issuerFingerprint := ""
	if chainErr == nil && issuer != nil {
		digest := sha256.Sum256(issuer.Raw)
		issuerFingerprint = hex.EncodeToString(digest[:])
	}
	return store.verify(address, fingerprint, publicErr, chainErr == nil, issuerFingerprint)
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
// its caller must still match or explicitly enroll the returned CA fingerprint.
//
// The anchor is the topmost presented certificate. Private PKI often serves
// leaf + intermediate and keeps its root offline, so the anchor may be an
// intermediate; it must still be a CA and sign the rest of the chain. A root
// that is presented must also carry a valid self-signature.
func verifyPresentedChain(
	leaf *x509.Certificate,
	rawChain [][]byte,
	now time.Time,
) (*x509.Certificate, error) {
	presentedRoots := x509.NewCertPool()
	intermediates := rawChain
	var issuer *x509.Certificate
	if len(rawChain) == 0 {
		presentedRoots.AddCert(leaf)
	} else {
		anchor, err := x509.ParseCertificate(rawChain[len(rawChain)-1])
		if err != nil || !anchor.IsCA || !anchor.BasicConstraintsValid ||
			anchor.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, ErrInvalidCertificate
		}
		if bytes.Equal(anchor.RawIssuer, anchor.RawSubject) && anchor.CheckSignatureFrom(anchor) != nil {
			return nil, ErrInvalidCertificate
		}
		issuer = anchor
		presentedRoots.AddCert(anchor)
		intermediates = rawChain[:len(rawChain)-1]
	}
	return issuer, verifySystemRoots(leaf, intermediates, now, presentedRoots)
}

func (store *PinStore) verify(
	address Address,
	fingerprint string,
	publicErr error,
	unknownIssuer bool,
	issuerFingerprint string,
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
				case TrustModePinnedCA:
					if existing.Fingerprint != issuerFingerprint {
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
				if issuerFingerprint != "" {
					result.Mode = TrustModePinnedCA
					fingerprint = issuerFingerprint
				}
				trust[address.String()] = trustRecord{
					Mode: result.Mode, Fingerprint: fingerprint,
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

// UsePrivateCA explicitly enrolls a private CA for one Server address. The
// expected fingerprint must come from an independent trusted channel. Callers
// supply the chain from a completed TLS handshake, before any application data.
func (store *PinStore) UsePrivateCA(address Address, rawCertificates [][]byte, now time.Time, expectedFingerprint string) (string, error) {
	record := trustRecord{Mode: TrustModePinnedCA, Fingerprint: expectedFingerprint}
	if store == nil || !record.valid() {
		return "", ErrInvalidCertificate
	}
	leaf, fingerprint, err := parsePeerCertificate(address, rawCertificates, now)
	if err != nil {
		return "", err
	}
	issuer, err := verifyPresentedChain(leaf, rawCertificates[1:], now)
	if err != nil || issuer == nil {
		return "", ErrInvalidCertificate
	}
	digest := sha256.Sum256(issuer.Raw)
	if hex.EncodeToString(digest[:]) != expectedFingerprint {
		return "", ErrServerIdentityChanged
	}
	err = filetransaction.Update(store.transactionOptions(), func(snapshot filetransaction.Snapshot) (filetransaction.Mutation, error) {
		trust := make(map[string]trustRecord)
		if snapshot.Exists {
			stored, err := decodeTrust(snapshot.Payload)
			if err != nil {
				return filetransaction.Mutation{}, err
			}
			trust = stored
		}
		if trust[address.String()] == record {
			return filetransaction.Mutation{}, nil
		}
		trust[address.String()] = record
		return encodeTrust(trust)
	})
	return fingerprint, err
}

// UseSystemRoots changes an existing exact leaf or CA pin into normal PKI trust. It
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
			if existing.Mode != TrustModePinnedLeaf && existing.Mode != TrustModePinnedCA {
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
