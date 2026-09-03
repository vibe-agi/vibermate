package serverconnection

import (
	"errors"
	"net/url"
	"strings"
)

// Transport is the exact wire transport selected for one Runtime Server.
// Clients never infer a downgrade: a Target remains HTTP or HTTPS for its
// entire lifetime and is persisted with that choice after login.
type Transport string

const (
	TransportHTTP  Transport = "http"
	TransportHTTPS Transport = "https"
)

func (transport Transport) Valid() bool {
	return transport == TransportHTTP || transport == TransportHTTPS
}

// Target identifies one Runtime Server connection without carrying any
// credential, Runtime User, or Environment decision. A bare host:port is
// intentionally HTTP so a local or private-network Server works without a
// certificate; HTTPS must be selected explicitly and never falls back.
type Target struct {
	address   Address
	transport Transport
}

func ParseTarget(value string) (Target, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return Target{}, ErrInvalidAddress
	}
	if !strings.Contains(value, "://") {
		address, err := ParseAddress(value)
		if err != nil {
			return Target{}, err
		}
		return Target{address: address, transport: TransportHTTP}, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return Target{}, ErrInvalidAddress
	}
	transport := Transport(parsed.Scheme)
	if !transport.Valid() {
		return Target{}, ErrInvalidAddress
	}
	address, err := ParseAddress(parsed.Host)
	if err != nil {
		return Target{}, errors.Join(ErrInvalidAddress, err)
	}
	return Target{address: address, transport: transport}, nil
}

func NewTarget(address Address, transport Transport) (Target, error) {
	parsed, err := ParseAddress(address.String())
	if err != nil || !transport.Valid() {
		return Target{}, ErrInvalidAddress
	}
	return Target{address: parsed, transport: transport}, nil
}

func (target Target) Valid() bool {
	_, err := NewTarget(target.address, target.transport)
	return err == nil
}

func (target Target) Address() Address     { return target.address }
func (target Target) Transport() Transport { return target.transport }
func (target Target) Origin() string {
	return string(target.transport) + "://" + target.address.String()
}
func (target Target) String() string { return target.Origin() }
