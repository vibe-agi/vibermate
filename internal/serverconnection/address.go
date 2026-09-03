// Package serverconnection owns the explicit network address used by a CLI to
// select a remote ViberMate Server.
package serverconnection

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxAddressBytes = 512

var ErrInvalidAddress = errors.New("ViberMate Server address is invalid")

// Address is a canonical host:port authority. It carries no Environment,
// credential, admission decision, URL path, or transport downgrade flag.
type Address string

func ParseAddress(value string) (Address, error) {
	if value == "" || len(value) > MaxAddressBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value || strings.Contains(value, "://") {
		return "", ErrInvalidAddress
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil || !validHost(host) {
		return "", ErrInvalidAddress
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", ErrInvalidAddress
	}
	return Address(net.JoinHostPort(canonicalHost(host), strconv.Itoa(int(port)))), nil
}

func (address Address) String() string { return string(address) }

func validHost(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	if parsed, err := netip.ParseAddr(value); err == nil {
		return parsed.IsValid() && !parsed.IsUnspecified()
	}
	if len(value) > 253 || strings.HasPrefix(value, ".") ||
		strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' ||
			label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if unicode.IsControl(character) || character > unicode.MaxASCII ||
				!(character >= 'a' && character <= 'z') &&
					!(character >= 'A' && character <= 'Z') &&
					!(character >= '0' && character <= '9') &&
					character != '-' {
				return false
			}
		}
	}
	return true
}

func canonicalHost(value string) string {
	if parsed, err := netip.ParseAddr(value); err == nil {
		return parsed.String()
	}
	return strings.ToLower(value)
}
