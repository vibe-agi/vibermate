// Package originidentity owns canonical inbound and provider origin values.
// It deliberately owns no Environment, route, credential, or dial authority.
package originidentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

const MaxOriginBytes = 2048

var ErrInvalidOrigin = errors.New("origin identity is invalid")

// ClientOrigin is one exact canonical HTTPS origin eligible for semantic
// interception. IP literals and wildcard names are deliberately excluded.
type ClientOrigin struct {
	canonical string
	host      string
	port      uint16
}

func ParseClientOrigin(raw string) (ClientOrigin, error) {
	if raw == "" || len(raw) > MaxOriginBytes || !utf8.ValidString(raw) ||
		strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\?#") {
		return ClientOrigin{}, invalid("ClientOrigin is empty or contains non-URL whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Opaque != "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" {
		return ClientOrigin{}, invalid("ClientOrigin must contain only an HTTPS scheme and authority")
	}
	if parsed.Host == "" || strings.Contains(parsed.Host, "*") || strings.HasSuffix(parsed.Host, ":") {
		return ClientOrigin{}, invalid("ClientOrigin host is missing, wildcarded, or has an empty port")
	}
	host := parsed.Hostname()
	if host == "" || strings.HasSuffix(host, ".") || strings.Contains(host, "%") {
		return ClientOrigin{}, invalid("ClientOrigin host is not exact and canonical")
	}
	port := uint16(443)
	if portText := parsed.Port(); portText != "" {
		value, parseErr := strconv.ParseUint(portText, 10, 16)
		if parseErr != nil || value == 0 {
			return ClientOrigin{}, invalid("ClientOrigin port is outside 1..65535")
		}
		port = uint16(value)
	}
	if _, parseErr := netip.ParseAddr(host); parseErr == nil {
		return ClientOrigin{}, invalid("IP literals are not eligible for semantic interception")
	}
	host, err = idna.Lookup.ToASCII(strings.ToLower(host))
	if err != nil {
		return ClientOrigin{}, invalid("ClientOrigin host cannot be converted to IDNA ASCII")
	}
	host = strings.ToLower(host)
	if err := validateDNSName(host); err != nil {
		return ClientOrigin{}, err
	}
	authority := host
	if port != 443 {
		authority = net.JoinHostPort(host, strconv.Itoa(int(port)))
	}
	return ClientOrigin{canonical: "https://" + authority, host: host, port: port}, nil
}

func (origin ClientOrigin) String() string { return origin.canonical }
func (origin ClientOrigin) Host() string   { return origin.host }
func (origin ClientOrigin) Port() uint16   { return origin.port }
func (origin ClientOrigin) HTTPAuthority() string {
	if origin.port == 443 {
		return origin.host
	}
	return net.JoinHostPort(origin.host, strconv.Itoa(int(origin.port)))
}
func (origin ClientOrigin) EndpointAuthority() string {
	return net.JoinHostPort(origin.host, strconv.Itoa(int(origin.port)))
}

func (origin ClientOrigin) Validate() error {
	parsed, err := ParseClientOrigin(origin.canonical)
	if err != nil || parsed != origin {
		return invalid("ClientOrigin is not canonical")
	}
	return nil
}

func (origin ClientOrigin) MarshalJSON() ([]byte, error) {
	if err := origin.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(origin.canonical)
}

func (origin *ClientOrigin) UnmarshalJSON(encoded []byte) error {
	if origin == nil {
		return invalid("ClientOrigin destination is nil")
	}
	var raw string
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return invalid("ClientOrigin JSON value is not a string")
	}
	parsed, err := ParseClientOrigin(raw)
	if err != nil || parsed.String() != raw {
		return invalid("ClientOrigin JSON value is not canonical")
	}
	*origin = parsed
	return nil
}

type ProviderTransport string

const (
	ProviderTransportStrictTLS         ProviderTransport = "strict_tls"
	ProviderTransportLoopbackCleartext ProviderTransport = "loopback_cleartext"
)

// ProviderOrigin is an outbound authority plus one canonical base path.
// Cleartext is representable only for a literal loopback address.
type ProviderOrigin struct {
	canonical string
	scheme    string
	host      string
	port      uint16
	basePath  string
	transport ProviderTransport
}

func ParseProviderOrigin(raw string) (ProviderOrigin, error) {
	if raw == "" || len(raw) > MaxOriginBytes || !utf8.ValidString(raw) ||
		strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\?#") {
		return ProviderOrigin{}, invalid("ProviderOrigin is empty, oversized, or non-canonical")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Host == "" ||
		strings.Contains(parsed.Host, "*") || strings.HasSuffix(parsed.Host, ":") {
		return ProviderOrigin{}, invalid("ProviderOrigin authority or base path is invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := parsed.Hostname()
	if host == "" || strings.HasSuffix(host, ".") || strings.Contains(host, "%") {
		return ProviderOrigin{}, invalid("ProviderOrigin host is not exact and canonical")
	}
	transport := ProviderTransportStrictTLS
	defaultPort := uint16(443)
	switch scheme {
	case "https":
		if address, parseErr := netip.ParseAddr(host); parseErr == nil {
			host = address.Unmap().String()
		} else {
			host, err = idna.Lookup.ToASCII(strings.ToLower(host))
			if err != nil {
				return ProviderOrigin{}, invalid("ProviderOrigin host cannot be converted to IDNA ASCII")
			}
			host = strings.ToLower(host)
			if err := validateDNSName(host); err != nil {
				return ProviderOrigin{}, invalid("ProviderOrigin host is not a DNS name")
			}
		}
	case "http":
		address, parseErr := netip.ParseAddr(host)
		if parseErr != nil || !address.IsLoopback() || address.Is4In6() {
			return ProviderOrigin{}, invalid("cleartext ProviderOrigin requires a literal unmapped loopback address")
		}
		host = address.String()
		defaultPort = 80
		transport = ProviderTransportLoopbackCleartext
	default:
		return ProviderOrigin{}, invalid("ProviderOrigin scheme must be HTTPS or literal-loopback HTTP")
	}
	port := defaultPort
	if portText := parsed.Port(); portText != "" {
		value, parseErr := strconv.ParseUint(portText, 10, 16)
		if parseErr != nil || value == 0 {
			return ProviderOrigin{}, invalid("ProviderOrigin port is outside 1..65535")
		}
		port = uint16(value)
	}
	basePath := parsed.Path
	if basePath == "/" {
		basePath = ""
	}
	if basePath != "" && (!strings.HasPrefix(basePath, "/") || pathpkg.Clean(basePath) != basePath ||
		strings.HasSuffix(basePath, "/")) {
		return ProviderOrigin{}, invalid("ProviderOrigin base path is not canonical")
	}
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if port != defaultPort {
		authority = net.JoinHostPort(host, strconv.Itoa(int(port)))
	}
	return ProviderOrigin{
		canonical: scheme + "://" + authority + basePath,
		scheme:    scheme, host: host, port: port, basePath: basePath, transport: transport,
	}, nil
}

func (origin ProviderOrigin) String() string               { return origin.canonical }
func (origin ProviderOrigin) Scheme() string               { return origin.scheme }
func (origin ProviderOrigin) Host() string                 { return origin.host }
func (origin ProviderOrigin) Port() uint16                 { return origin.port }
func (origin ProviderOrigin) BasePath() string             { return origin.basePath }
func (origin ProviderOrigin) Transport() ProviderTransport { return origin.transport }
func (origin ProviderOrigin) HTTPAuthority() string {
	defaultPort := uint16(443)
	if origin.scheme == "http" {
		defaultPort = 80
	}
	if origin.port == defaultPort {
		if strings.Contains(origin.host, ":") {
			return "[" + origin.host + "]"
		}
		return origin.host
	}
	return net.JoinHostPort(origin.host, strconv.Itoa(int(origin.port)))
}
func (origin ProviderOrigin) EndpointAuthority() string {
	return net.JoinHostPort(origin.host, strconv.Itoa(int(origin.port)))
}

func (origin ProviderOrigin) Validate() error {
	parsed, err := ParseProviderOrigin(origin.canonical)
	if err != nil || parsed != origin {
		return invalid("ProviderOrigin is not canonical")
	}
	return nil
}

func (origin ProviderOrigin) MarshalJSON() ([]byte, error) {
	if err := origin.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(origin.canonical)
}

func (origin *ProviderOrigin) UnmarshalJSON(encoded []byte) error {
	if origin == nil {
		return invalid("ProviderOrigin destination is nil")
	}
	var raw string
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return invalid("ProviderOrigin JSON value is not a string")
	}
	parsed, err := ParseProviderOrigin(raw)
	if err != nil || parsed.String() != raw {
		return invalid("ProviderOrigin JSON value is not canonical")
	}
	*origin = parsed
	return nil
}

func validateDNSName(host string) error {
	if host == "" || len(host) > 253 {
		return invalid("DNS host length is invalid")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return invalid("DNS host contains an invalid label")
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
				return invalid("DNS host contains a non-DNS character")
			}
		}
	}
	return nil
}

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidOrigin, reason) }
