// Package localdiscovery publishes the short-lived, user-private rendezvous
// record used by the local CLI. The record's expiry proves discovery
// freshness; it does not define the control credential's lifetime.
package localdiscovery

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"strconv"
	"time"
)

const Schema = "vibermate-local-control-discovery-v1"

var (
	ErrExpired       = errors.New("local control discovery is expired")
	ErrOwnerConflict = errors.New("local control discovery belongs to another runtime instance")
)

// Session is the complete local control discovery wire record. The credential
// is generation-scoped and may remain stable while ExpiresAt is refreshed.
type Session struct {
	Schema            string    `json:"schema"`
	InstanceID        string    `json:"instanceId"`
	ProcessID         int       `json:"pid"`
	BaseURL           string    `json:"baseUrl"`
	ControlCredential string    `json:"controlCredential"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

func validateSession(session Session, now time.Time, requireFresh bool) error {
	if session.Schema != Schema {
		return errors.New("local control discovery schema is unsupported")
	}
	instanceBytes, err := base64.RawURLEncoding.DecodeString(session.InstanceID)
	if err != nil || len(instanceBytes) < 16 {
		return errors.New("local control discovery instance ID is invalid")
	}
	if session.ProcessID <= 0 {
		return errors.New("local control discovery process ID is invalid")
	}
	if err := validateControlOrigin(session.BaseURL); err != nil {
		return err
	}
	capability, err := base64.RawURLEncoding.DecodeString(
		session.ControlCredential,
	)
	if err != nil || len(capability) != 32 {
		return errors.New("local control discovery credential is invalid")
	}
	if session.ExpiresAt.IsZero() {
		return errors.New("local control discovery expiry is missing")
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
		return errors.New("local control origin is invalid")
	}
	host, port, err := net.SplitHostPort(origin.Host)
	if err != nil || host != "127.0.0.1" {
		return errors.New("local control origin must use literal IPv4 loopback")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("local control origin port is invalid")
	}
	return nil
}
