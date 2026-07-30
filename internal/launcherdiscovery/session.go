// Package launcherdiscovery publishes the short-lived, user-private control
// capability used by the local CLI. The record is a discovery boundary, not a
// general runtime configuration file.
package launcherdiscovery

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"strconv"
	"time"
)

const SchemaV1 = "vibermate-launcher-discovery-v1"

var (
	ErrExpired       = errors.New("launcher discovery is expired")
	ErrOwnerConflict = errors.New("launcher discovery belongs to another runtime instance")
)

// Session is the complete launcher discovery wire record. LauncherToken
// authorizes only CaptureRun creation and expires with this record.
type Session struct {
	Schema        string    `json:"schema"`
	InstanceID    string    `json:"instanceId"`
	ProcessID     int       `json:"pid"`
	BaseURL       string    `json:"baseUrl"`
	LauncherToken string    `json:"launcherToken"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

func validateSession(session Session, now time.Time, requireFresh bool) error {
	if session.Schema != SchemaV1 {
		return errors.New("launcher discovery schema is unsupported")
	}
	instanceBytes, err := base64.RawURLEncoding.DecodeString(session.InstanceID)
	if err != nil || len(instanceBytes) < 16 {
		return errors.New("launcher discovery instance ID is invalid")
	}
	if session.ProcessID <= 0 {
		return errors.New("launcher discovery process ID is invalid")
	}
	if err := validateControlOrigin(session.BaseURL); err != nil {
		return err
	}
	capability, err := base64.RawURLEncoding.DecodeString(
		session.LauncherToken,
	)
	if err != nil || len(capability) != 32 {
		return errors.New("launcher discovery capability is invalid")
	}
	if session.ExpiresAt.IsZero() {
		return errors.New("launcher discovery expiry is missing")
	}
	if requireFresh && !now.UTC().Before(session.ExpiresAt.UTC()) {
		return ErrExpired
	}
	return nil
}

func validateControlOrigin(raw string) error {
	origin, err := url.Parse(raw)
	if err != nil ||
		origin.Scheme != "http" ||
		origin.User != nil ||
		origin.Path != "" ||
		origin.RawPath != "" ||
		origin.RawQuery != "" ||
		origin.Fragment != "" {
		return errors.New("launcher control origin is invalid")
	}
	host, port, err := net.SplitHostPort(origin.Host)
	if err != nil || host != "127.0.0.1" {
		return errors.New("launcher control origin must use literal IPv4 loopback")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("launcher control origin port is invalid")
	}
	return nil
}
