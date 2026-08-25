// Package egressnetwork owns the configured network path from ViberMate to one
// AI endpoint. ViberMate always owns target resolution so configured system
// DNS or DoH cannot be silently bypassed by a proxy.
package egressnetwork

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

var ErrInvalidPolicy = errors.New("traffic egress policy is invalid")

const maximumEndpointBytes = 2048

type ProxyKind string

const (
	ProxyDirect ProxyKind = "direct"
	ProxySOCKS5 ProxyKind = "socks5"
)

type ResolverKind string

const (
	ResolverSystem ResolverKind = "system"
	ResolverDoH    ResolverKind = "doh"
)

type ResolverTransport string

const (
	ResolverTransportDirect ResolverTransport = "direct"
	ResolverTransportProxy  ResolverTransport = "proxy"
)

type ProxyPolicy struct {
	Kind     ProxyKind `json:"kind"`
	Endpoint string    `json:"endpoint,omitempty"`
}

type ResolverPolicy struct {
	Kind ResolverKind `json:"kind"`
	// DoHURL is public configuration, never a credential carrier. Userinfo,
	// query strings, fragments, and plaintext HTTP are rejected.
	DoHURL    string            `json:"dohUrl,omitempty"`
	Transport ResolverTransport `json:"transport"`
}

// Policy is frozen with one ClientProtocolPlan. Proxy controls how the target
// TCP connection is made. Resolver controls who resolves the AI endpoint; its
// Transport controls only the DoH HTTP request itself.
type Policy struct {
	Proxy    ProxyPolicy    `json:"proxy"`
	Resolver ResolverPolicy `json:"resolver"`
}

func DefaultPolicy() Policy {
	return Policy{
		Proxy: ProxyPolicy{Kind: ProxyDirect},
		Resolver: ResolverPolicy{
			Kind:      ResolverSystem,
			Transport: ResolverTransportDirect,
		},
	}
}

func (policy Policy) Validate() error {
	_, err := policy.Normalize()
	return err
}

// Normalize makes defaults and network identities explicit. The all-zero
// value intentionally means direct egress with the system resolver so callers
// can safely construct a default policy without a sentinel or pointer.
func (policy Policy) Normalize() (Policy, error) {
	if policy == (Policy{}) {
		return DefaultPolicy(), nil
	}
	if policy.Proxy.Kind == "" {
		policy.Proxy.Kind = ProxyDirect
	}
	switch policy.Proxy.Kind {
	case ProxyDirect:
		if policy.Proxy.Endpoint != "" {
			return Policy{}, fmt.Errorf("%w: direct proxy has an endpoint", ErrInvalidPolicy)
		}
	case ProxySOCKS5:
		endpoint, err := normalizeEndpoint(policy.Proxy.Endpoint)
		if err != nil {
			return Policy{}, fmt.Errorf("%w: proxy endpoint: %v", ErrInvalidPolicy, err)
		}
		policy.Proxy.Endpoint = endpoint
	default:
		return Policy{}, fmt.Errorf("%w: proxy kind is unsupported", ErrInvalidPolicy)
	}

	if policy.Resolver.Kind == "" {
		policy.Resolver.Kind = ResolverSystem
	}
	switch policy.Resolver.Kind {
	case ResolverSystem:
		if policy.Resolver.DoHURL != "" {
			return Policy{}, fmt.Errorf("%w: system resolver has a DoH endpoint", ErrInvalidPolicy)
		}
		if policy.Resolver.Transport == "" {
			policy.Resolver.Transport = ResolverTransportDirect
		}
		if policy.Resolver.Transport != ResolverTransportDirect {
			return Policy{}, fmt.Errorf("%w: system resolver transport is unsupported", ErrInvalidPolicy)
		}
	case ResolverDoH:
		dohURL, err := normalizeDoHURL(policy.Resolver.DoHURL)
		if err != nil {
			return Policy{}, fmt.Errorf("%w: DoH endpoint: %v", ErrInvalidPolicy, err)
		}
		policy.Resolver.DoHURL = dohURL
		if policy.Resolver.Transport == "" {
			policy.Resolver.Transport = ResolverTransportDirect
		}
		if policy.Resolver.Transport != ResolverTransportDirect &&
			policy.Resolver.Transport != ResolverTransportProxy {
			return Policy{}, fmt.Errorf("%w: DoH transport is unsupported", ErrInvalidPolicy)
		}
	default:
		return Policy{}, fmt.Errorf("%w: resolver kind is unsupported", ErrInvalidPolicy)
	}

	switch policy.Proxy.Kind {
	case ProxyDirect:
		if policy.Resolver.Transport == ResolverTransportProxy {
			return Policy{}, fmt.Errorf("%w: resolver requires an absent proxy", ErrInvalidPolicy)
		}
	case ProxySOCKS5:
	}
	return policy, nil
}

func normalizeEndpoint(value string) (string, error) {
	if !validText(value) || len(value) > maximumEndpointBytes {
		return "", errors.New("value is malformed")
	}
	host, rawPort, err := net.SplitHostPort(value)
	if err != nil || host == "" || rawPort == "" {
		return "", errors.New("host and port are required")
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 || strconv.FormatUint(port, 10) != rawPort {
		return "", errors.New("port is invalid")
	}
	host, err = normalizeHost(host)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, rawPort), nil
}

func normalizeDoHURL(value string) (string, error) {
	if !validText(value) || len(value) > maximumEndpointBytes {
		return "", errors.New("value is malformed")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.RawFragment != "" ||
		parsed.RawQuery != "" || parsed.Host == "" || parsed.Path == "" ||
		parsed.Path[0] != '/' || parsed.RawPath != "" {
		return "", errors.New("must be an absolute credential-free HTTPS URL")
	}
	host, err := normalizeHost(parsed.Hostname())
	if err != nil {
		return "", err
	}
	port := parsed.Port()
	if port != "" {
		parsedPort, portErr := strconv.ParseUint(port, 10, 16)
		if portErr != nil || parsedPort == 0 || strconv.FormatUint(parsedPort, 10) != port {
			return "", errors.New("port is invalid")
		}
	}
	if port == "443" {
		port = ""
	}
	if port == "" {
		if strings.Contains(host, ":") {
			parsed.Host = "[" + host + "]"
		} else {
			parsed.Host = host
		}
	} else {
		parsed.Host = net.JoinHostPort(host, port)
	}
	return parsed.String(), nil
}

func normalizeHost(value string) (string, error) {
	if value == "" || strings.Contains(value, "%") {
		return "", errors.New("host is invalid")
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap().String(), nil
	}
	value = strings.TrimSuffix(value, ".")
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", errors.New("host is invalid")
	}
	ascii = strings.ToLower(ascii)
	if !validDNSName(ascii) {
		return "", errors.New("host is invalid")
	}
	return ascii, nil
}

func validDNSName(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func validText(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
