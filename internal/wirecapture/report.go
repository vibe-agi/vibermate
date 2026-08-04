// Package wirecapture records value-only TLS and HTTP presentation evidence
// from a loopback development endpoint. It never stores request bodies, raw
// TLS records, header values other than User-Agent, or remote identifiers.
package wirecapture

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

const (
	ReportSchema      = "vibermate.wire-capture/v1"
	MaxHeaderFields   = 128
	MaxUserAgentBytes = 512
	MaxReportBytes    = 1 << 20
	privateDirectory  = 0o700
	privateReport     = 0o600
)

type H2Setting struct {
	ID    uint16 `json:"id"`
	Value uint32 `json:"value"`
}

type H2Observation struct {
	Settings           []H2Setting `json:"settings"`
	ConnectionWindow   uint32      `json:"connection_window_update"`
	PriorityFrameCount uint32      `json:"priority_frame_count"`
	AkamaiFingerprint  string      `json:"akamai_fingerprint"`
	AkamaiHash         string      `json:"akamai_hash"`
}

type Sample struct {
	CapturedAt        time.Time                    `json:"captured_at"`
	NegotiatedALPN    string                       `json:"negotiated_alpn"`
	TLS               transportprofile.Fingerprint `json:"tls"`
	UserAgent         string                       `json:"user_agent"`
	PseudoHeaderOrder []string                     `json:"pseudo_header_order"`
	HeaderOrder       []string                     `json:"header_order"`
	BodyBytes         uint64                       `json:"body_bytes"`
	HTTP2             *H2Observation               `json:"http2,omitempty"`
}

type Report struct {
	SchemaVersion string    `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
	Samples       []Sample  `json:"samples"`
}

func (report Report) Validate() error {
	if report.SchemaVersion != ReportSchema || report.CreatedAt.IsZero() ||
		len(report.Samples) == 0 || len(report.Samples) > 1024 {
		return errors.New("wire capture report is incomplete")
	}
	for _, sample := range report.Samples {
		if err := sample.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (sample Sample) validate() error {
	if sample.CapturedAt.IsZero() ||
		(sample.NegotiatedALPN != "http/1.1" && sample.NegotiatedALPN != "h2") {
		return errors.New("wire capture sample protocol is invalid")
	}
	if err := validateText("User-Agent", sample.UserAgent, MaxUserAgentBytes, true); err != nil {
		return err
	}
	if err := validateHeaderOrder(sample.PseudoHeaderOrder, true); err != nil {
		return err
	}
	if err := validateHeaderOrder(sample.HeaderOrder, false); err != nil {
		return err
	}
	if sample.TLS.JA3 == "" || sample.TLS.JA3Hash == "" ||
		sample.TLS.JA4 == "" || sample.TLS.JA4R == "" ||
		sample.TLS.Peetprint == "" || sample.TLS.PeetprintHash == "" {
		return errors.New("wire capture TLS fingerprint is incomplete")
	}
	if sample.NegotiatedALPN == "http/1.1" {
		if sample.HTTP2 != nil || len(sample.PseudoHeaderOrder) != 0 {
			return errors.New("HTTP/1.1 capture contains HTTP/2 state")
		}
		return nil
	}
	if sample.HTTP2 == nil || len(sample.HTTP2.Settings) == 0 ||
		sample.HTTP2.AkamaiFingerprint == "" || sample.HTTP2.AkamaiHash == "" {
		return errors.New("HTTP/2 capture is incomplete")
	}
	return nil
}

func validateText(label, value string, limit int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || len(value) > limit || !utf8.ValidString(value) {
		return fmt.Errorf("wire capture %s is invalid", label)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("wire capture %s contains a control character", label)
		}
	}
	return nil
}

func validateHeaderOrder(order []string, pseudo bool) error {
	if len(order) > MaxHeaderFields {
		return errors.New("wire capture header order exceeds its bound")
	}
	for _, name := range order {
		if name == "" || name != strings.ToLower(name) ||
			pseudo != strings.HasPrefix(name, ":") {
			return errors.New("wire capture header name is invalid")
		}
		for _, character := range strings.TrimPrefix(name, ":") {
			if !(character >= 'a' && character <= 'z') &&
				!(character >= '0' && character <= '9') && character != '-' {
				return errors.New("wire capture header name contains an invalid character")
			}
		}
	}
	return nil
}

func WriteReport(path string, report Report) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("wire capture output path must be absolute")
	}
	if err := report.Validate(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode wire capture report: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > MaxReportBytes {
		return errors.New("wire capture report exceeds its byte bound")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateReport)
	if err != nil {
		return fmt.Errorf("create wire capture report: %w", err)
	}
	writeErr := error(nil)
	if _, err := file.Write(encoded); err != nil {
		writeErr = err
	}
	if err := file.Sync(); writeErr == nil && err != nil {
		writeErr = err
	}
	if err := file.Close(); writeErr == nil && err != nil {
		writeErr = err
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write wire capture report: %w", writeErr)
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, privateDirectory); err != nil {
		return fmt.Errorf("create wire capture output directory: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect wire capture output directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("wire capture output directory is not owner-private")
	}
	return nil
}

func md5Fingerprint(value string) string {
	digest := md5.Sum([]byte(value)) // #nosec G401 -- protocol fingerprint, not security.
	return hex.EncodeToString(digest[:])
}
