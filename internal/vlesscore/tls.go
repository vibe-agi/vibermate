package vlesscore

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

type Security string

const (
	SecurityREALITY Security = "reality"
	SecurityTLS     Security = "tls"
)

type TrustMode string

const (
	TrustSystem   TrustMode = "system"
	TrustCustomCA TrustMode = "custom_ca"
)

type TLSOptions struct {
	Mode  TrustMode `json:"-"`
	CAPEM string    `json:"-"`
}

var ErrInvalidTLS = errors.New("vlesscore: invalid TLS trust configuration")

func (TLSOptions) String() string   { return "vlesscore.TLSOptions{redacted}" }
func (TLSOptions) GoString() string { return "vlesscore.TLSOptions{redacted}" }

func (o TLSOptions) Validate() error {
	switch o.Mode {
	case TrustSystem:
		if o.CAPEM != "" {
			return ErrInvalidTLS
		}
		return nil
	case TrustCustomCA:
		return validateCAPEM(o.CAPEM)
	default:
		return ErrInvalidTLS
	}
}

// Consume every byte except whitespace; partial PEM acceptance can fall back to system roots.
func validateCAPEM(value string) error {
	if len(value) > 64<<10 {
		return ErrInvalidTLS
	}
	rest := bytes.TrimSpace([]byte(value))
	if len(rest) == 0 {
		return ErrInvalidTLS
	}
	for len(rest) != 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return ErrInvalidTLS
		}
		// pem.Decode scans past malformed blocks. Limit it to the first end marker
		// so a later valid certificate cannot hide a malformed first certificate.
		endMarker := []byte("-----END CERTIFICATE-----")
		end := bytes.Index(rest, endMarker)
		if end < 0 {
			return ErrInvalidTLS
		}
		end += len(endMarker)
		if bytes.Count(rest[:end], []byte("-----BEGIN")) != 1 {
			return ErrInvalidTLS
		}
		block, trailing := pem.Decode(rest[:end])
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(trailing)) != 0 {
			return ErrInvalidTLS
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return ErrInvalidTLS
		}
		rest = bytes.TrimSpace(rest[end:])
	}
	return nil
}
