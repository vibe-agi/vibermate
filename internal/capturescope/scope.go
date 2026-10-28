// Package capturescope decides which of an Agent's destinations never enter
// ViberMate and leave directly from the user's machine (ADR 0025). The policy
// belongs to an Environment and travels in the launch grant; every client
// capture path asks the same compiled Scope.
//
// The rule language is bounded on purpose. Capture scope is an exclusion from
// policy and recording, so it must not become "capture nothing": no bare
// wildcard, no single-label suffix, address prefixes inside reserved space
// only, and never a protected or managed authority.
package capturescope

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// MaxRules bounds one policy.
const MaxRules = 256

// ErrInvalidPolicy reports a policy outside the rule language's bounds.
var ErrInvalidPolicy = errors.New("capture scope policy is invalid")

// Kind is what a rule's value names.
type Kind string

const (
	// KindHost matches one exact host name.
	KindHost Kind = "host"
	// KindSuffix matches a domain and every name below it.
	KindSuffix Kind = "suffix"
	// KindPrefix matches an address prefix inside reserved space.
	KindPrefix Kind = "prefix"
)

// Rule is one destination that stays on the user's machine. Port 0 means any
// port.
type Rule struct {
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	Value string `json:"value"`
	Port  uint16 `json:"port,omitempty"`
}

// Policy is an Environment's capture scope.
type Policy struct {
	Rules []Rule `json:"rules,omitempty"`
	// AllowUserAdditions lets entries of the user's own NO_PROXY that obey the
	// same bounds join the scope. Off unless the Environment says so.
	AllowUserAdditions bool `json:"allowUserAdditions,omitempty"`
}

// Clone returns a deep copy.
func (policy Policy) Clone() Policy {
	policy.Rules = append([]Rule(nil), policy.Rules...)
	return policy
}

// reserved is the only address space a prefix rule may name: loopback,
// RFC 1918, carrier-grade NAT (Tailscale), link-local, and IPv6 unique-local
// and link-local. Public ranges are a later, explicit decision.
var reserved = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

// Validate checks every bound. It does not know the Environment's protected
// authorities; Covers lets the publisher refuse those, and Scope refuses them
// again at match time.
func (policy Policy) Validate() error {
	if len(policy.Rules) > MaxRules {
		return fmt.Errorf("%w: more than %d rules", ErrInvalidPolicy, MaxRules)
	}
	seen := make(map[string]struct{}, len(policy.Rules))
	for _, rule := range policy.Rules {
		if err := rule.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[rule.ID]; duplicate {
			return fmt.Errorf("%w: rule ID %q is duplicated", ErrInvalidPolicy, rule.ID)
		}
		seen[rule.ID] = struct{}{}
	}
	return nil
}

// Validate checks one rule, including that its value is in canonical form, so
// a stored policy has exactly one spelling.
func (rule Rule) Validate() error {
	if !validID(rule.ID) {
		return fmt.Errorf("%w: rule ID %q is invalid", ErrInvalidPolicy, rule.ID)
	}
	switch rule.Kind {
	case KindHost:
		if !validHostName(rule.Value, 1) {
			return fmt.Errorf("%w: rule %q host is not a lower-case DNS name", ErrInvalidPolicy, rule.ID)
		}
	case KindSuffix:
		if !validHostName(rule.Value, 2) {
			return fmt.Errorf("%w: rule %q suffix needs at least two lower-case labels", ErrInvalidPolicy, rule.ID)
		}
	case KindPrefix:
		prefix, err := netip.ParsePrefix(rule.Value)
		if err != nil || prefix != prefix.Masked() || prefix.String() != rule.Value {
			return fmt.Errorf("%w: rule %q prefix is not a canonical address prefix", ErrInvalidPolicy, rule.ID)
		}
		if !insideReserved(prefix) {
			return fmt.Errorf("%w: rule %q prefix is outside reserved address space", ErrInvalidPolicy, rule.ID)
		}
	default:
		return fmt.Errorf("%w: rule %q kind is unsupported", ErrInvalidPolicy, rule.ID)
	}
	return nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 || strings.HasPrefix(id, "builtin.") || strings.HasPrefix(id, "user.") {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// validHostName accepts a lower-case ASCII DNS name with at least minLabels
// labels. IP literals are prefix rules instead.
func validHostName(host string, minLabels int) bool {
	if len(host) == 0 || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < minLabels {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

func insideReserved(prefix netip.Prefix) bool {
	for _, space := range reserved {
		if space.Addr().Is4() == prefix.Addr().Is4() && space.Bits() <= prefix.Bits() && space.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

// Destination is what a client knows about a flow when it decides. Name is
// empty when only the address is known.
type Destination struct {
	Name string
	Addr netip.Addr
	Port uint16
}

// Options are the per-platform built-ins.
type Options struct {
	// DNSOnSystemPath keeps port 53 off ViberMate (macOS, where the extension
	// sees the system resolver's queries as the Agent's).
	DNSOnSystemPath bool
	// UserNoProxy are the raw NO_PROXY values from the user's environment,
	// honored only when the policy allows user additions.
	UserNoProxy []string
}

// Scope is a compiled capture scope for one run.
type Scope struct {
	rules     []Rule
	prefixes  []netip.Prefix // parallel to rules; valid only for KindPrefix
	protected []authority
	dns       bool
}

type authority struct {
	host string
	port uint16
}

// Built-in rule identifiers, reported when they match.
const (
	BuiltinLoopback  = "builtin.loopback"
	BuiltinLinkLocal = "builtin.link-local"
	BuiltinDNS       = "builtin.dns"
)

// Compile builds the run's scope from its Environment's policy (nil means the
// default: built-ins only), the run's protected authorities and the options.
// It never fails: an invalid policy or addition contributes nothing, so a
// mistake keeps traffic in ViberMate rather than letting it out.
func Compile(policy *Policy, protectedAuthorities []string, options Options) Scope {
	scope := Scope{dns: options.DNSOnSystemPath}
	for _, value := range protectedAuthorities {
		host, port := splitAuthority(value)
		if host != "" {
			scope.protected = append(scope.protected, authority{host: host, port: port})
		}
	}
	if policy == nil || policy.Validate() != nil {
		return scope
	}
	for _, rule := range policy.Rules {
		scope.add(rule)
	}
	if policy.AllowUserAdditions {
		for i, rule := range parseNoProxy(options.UserNoProxy) {
			rule.ID = "user." + strconv.Itoa(i+1)
			scope.add(rule)
		}
	}
	return scope
}

func (scope *Scope) add(rule Rule) {
	var prefix netip.Prefix
	if rule.Kind == KindPrefix {
		prefix = netip.MustParsePrefix(rule.Value)
	}
	if scope.coversProtected(rule) {
		return
	}
	scope.rules = append(scope.rules, rule)
	scope.prefixes = append(scope.prefixes, prefix)
}

// coversProtected reports whether a name rule could match a protected
// authority. Such a rule is dropped whole rather than trimmed.
func (scope Scope) coversProtected(rule Rule) bool {
	for _, protected := range scope.protected {
		if rule.Port != 0 && protected.port != 0 && rule.Port != protected.port {
			continue
		}
		switch rule.Kind {
		case KindHost:
			if rule.Value == protected.host {
				return true
			}
		case KindSuffix:
			if nameUnder(protected.host, rule.Value) {
				return true
			}
		case KindPrefix:
			if addr, err := netip.ParseAddr(protected.host); err == nil &&
				netip.MustParsePrefix(rule.Value).Contains(addr.Unmap()) {
				return true
			}
		}
	}
	return false
}

// Covers reports whether policy has a rule that would match one of the
// authorities, for refusing it at publish time.
func Covers(policy Policy, authorities []string) (string, bool) {
	scope := Compile(nil, authorities, Options{})
	for _, rule := range policy.Rules {
		if rule.Validate() == nil && scope.coversProtected(rule) {
			return rule.ID, true
		}
	}
	return "", false
}

// Match reports whether destination stays on the user's machine, and which
// rule decided it.
func (scope Scope) Match(destination Destination) (string, bool) {
	name := strings.ToLower(strings.TrimSuffix(destination.Name, "."))
	addr := destination.Addr.Unmap()
	if name != "" {
		for _, protected := range scope.protected {
			if name == protected.host && (protected.port == 0 || protected.port == destination.Port) {
				return "", false
			}
		}
	}
	switch {
	case name == "localhost" || (addr.IsValid() && addr.IsLoopback()):
		return BuiltinLoopback, true
	case addr.IsValid() && (addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()):
		return BuiltinLinkLocal, true
	case scope.dns && destination.Port == 53:
		return BuiltinDNS, true
	}
	for i, rule := range scope.rules {
		if rule.Port != 0 && rule.Port != destination.Port {
			continue
		}
		switch rule.Kind {
		case KindHost:
			if name != "" && name == rule.Value {
				return rule.ID, true
			}
		case KindSuffix:
			if name != "" && nameUnder(name, rule.Value) {
				return rule.ID, true
			}
		case KindPrefix:
			if addr.IsValid() && scope.prefixes[i].Contains(addr) {
				return rule.ID, true
			}
		}
	}
	return "", false
}

// MatchesName reports whether a host or suffix rule names host, so a resolver
// can answer it with real addresses.
func (scope Scope) MatchesName(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return "", false
	}
	for _, protected := range scope.protected {
		if host == protected.host {
			return "", false
		}
	}
	for _, rule := range scope.rules {
		if (rule.Kind == KindHost && host == rule.Value) || (rule.Kind == KindSuffix && nameUnder(host, rule.Value)) {
			return rule.ID, true
		}
	}
	return "", false
}

// Prefixes returns the address rules, for a component that decides by address
// alone (the macOS broker). Name rules are left to whoever knows names.
func (scope Scope) Prefixes() []Rule {
	var rules []Rule
	for _, rule := range scope.rules {
		if rule.Kind == KindPrefix {
			rules = append(rules, rule)
		}
	}
	return rules
}

// NoProxy renders the scope for proxy-variable clients. NO_PROXY cannot say
// "this port only" or "this exact host", and clients disagree on prefixes, so
// port-specific rules are left out (they stay in ViberMate) and the rest is
// best effort; transparent capture is exact.
func (scope Scope) NoProxy() string {
	entries := []string{"localhost", "127.0.0.1", "::1", "169.254.0.0/16", "fe80::/10"}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		seen[entry] = struct{}{}
	}
	for _, rule := range scope.rules {
		if rule.Port != 0 {
			continue
		}
		if _, duplicate := seen[rule.Value]; duplicate {
			continue
		}
		seen[rule.Value] = struct{}{}
		entries = append(entries, rule.Value)
	}
	return strings.Join(entries, ",")
}

func nameUnder(name, domain string) bool {
	return name == domain || strings.HasSuffix(name, "."+domain)
}

func splitAuthority(value string) (string, uint16) {
	value = strings.ToLower(strings.TrimSpace(value))
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return strings.Trim(value, "[]"), 0
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return host, 0
	}
	return host, uint16(port)
}

// parseNoProxy turns NO_PROXY entries into rules that obey the bounds and
// drops the rest. A domain entry covers its subdomains, as clients read it.
func parseNoProxy(values []string) []Rule {
	var rules []Rule
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			entry := strings.ToLower(strings.TrimSpace(raw))
			entry = strings.TrimPrefix(strings.TrimPrefix(entry, "*"), ".")
			if entry == "" {
				continue
			}
			var port uint16
			if host, portText, err := net.SplitHostPort(entry); err == nil {
				parsed, err := strconv.ParseUint(portText, 10, 16)
				if err != nil || parsed == 0 {
					continue
				}
				entry, port = host, uint16(parsed)
			}
			var rule Rule
			switch {
			case strings.Contains(entry, "/"):
				prefix, err := netip.ParsePrefix(entry)
				if err != nil {
					continue
				}
				rule = Rule{Kind: KindPrefix, Value: prefix.Masked().String()}
			default:
				if addr, err := netip.ParseAddr(entry); err == nil {
					rule = Rule{Kind: KindPrefix, Value: netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()).String()}
				} else if strings.Contains(entry, ".") {
					rule = Rule{Kind: KindSuffix, Value: entry}
				} else {
					rule = Rule{Kind: KindHost, Value: entry}
				}
			}
			rule.Port = port
			rule.ID = "candidate"
			if rule.Validate() == nil {
				rules = append(rules, rule)
			}
		}
	}
	return rules
}
