// Package certidentity defines public certificate identity values shared by
// Environment authorization and the local certificate authority. It owns no key
// material and performs no signing or trust-store mutation.
package certidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
)

var (
	ErrInvalidRootRevision = errors.New("Root revision is invalid")
	ErrInvalidRootDigest   = errors.New("Root certificate digest is invalid")
	ErrInvalidSAN          = errors.New("certificate SAN is invalid")
	ErrInvalidAlgorithm    = errors.New("certificate algorithm is invalid")
)

// RootRevision is the persistent revision of one installation Root.
type RootRevision uint64

const InitialRootRevision RootRevision = 1

func (revision RootRevision) Valid() bool {
	return revision > 0
}

// RootDigest is the SHA-256 identity of the exact Root certificate DER.
type RootDigest struct {
	value [sha256.Size]byte
}

func DigestRootCertificate(der []byte) (RootDigest, error) {
	if len(der) == 0 {
		return RootDigest{}, ErrInvalidRootDigest
	}
	return RootDigest{value: sha256.Sum256(der)}, nil
}

func ParseRootDigest(value string) (RootDigest, error) {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return RootDigest{}, ErrInvalidRootDigest
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return RootDigest{}, ErrInvalidRootDigest
	}
	var digest [sha256.Size]byte
	copy(digest[:], decoded)
	return RootDigest{value: digest}, nil
}

func (digest RootDigest) Valid() bool {
	return digest != RootDigest{}
}

func (digest RootDigest) Bytes() []byte {
	return append([]byte(nil), digest.value[:]...)
}

func (digest RootDigest) String() string {
	return hex.EncodeToString(digest.value[:])
}

// Fingerprint is a display representation derived from the machine digest.
// It is deliberately not a separately persisted identity.
func (digest RootDigest) Fingerprint() string {
	return digest.String()
}

type RootAlgorithm string

const RootAlgorithmECDSAP256 RootAlgorithm = "ecdsa-p256"

func (algorithm RootAlgorithm) Valid() bool {
	return algorithm == RootAlgorithmECDSAP256
}

type LeafKeyAlgorithm string

const LeafKeyAlgorithmECDSAP256 LeafKeyAlgorithm = "ecdsa-p256"

func (algorithm LeafKeyAlgorithm) Valid() bool {
	return algorithm == LeafKeyAlgorithmECDSAP256
}

type SANKind string

const (
	SANKindDNS SANKind = "dns"
	SANKindIP  SANKind = "ip"
)

// SubjectAlternativeName is a canonical DNS or IP leaf identity.
type SubjectAlternativeName struct {
	kind  SANKind
	value string
}

func NewDNSName(value string) (SubjectAlternativeName, error) {
	if !validDNSName(value) {
		return SubjectAlternativeName{}, ErrInvalidSAN
	}
	return SubjectAlternativeName{kind: SANKindDNS, value: value}, nil
}

func NewIPAddress(value string) (SubjectAlternativeName, error) {
	address, err := netip.ParseAddr(value)
	if err != nil || address.Is4In6() || address.String() != value {
		return SubjectAlternativeName{}, ErrInvalidSAN
	}
	return SubjectAlternativeName{kind: SANKindIP, value: value}, nil
}

func (name SubjectAlternativeName) Kind() SANKind {
	return name.kind
}

func (name SubjectAlternativeName) Value() string {
	return name.value
}

func (name SubjectAlternativeName) Valid() bool {
	switch name.kind {
	case SANKindDNS:
		return validDNSName(name.value)
	case SANKindIP:
		address, err := netip.ParseAddr(name.value)
		return err == nil && !address.Is4In6() && address.String() == name.value
	default:
		return false
	}
}

func validDNSName(value string) bool {
	if value == "" || len(value) > 253 || strings.ToLower(value) != value ||
		strings.HasSuffix(value, ".") || strings.Contains(value, "*") {
		return false
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' ||
			label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return false
			}
		}
	}
	return true
}
