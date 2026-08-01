// Package connectionpolicy decides whether a proxied connection may be
// attempted, before any dial, DNS resolution, or certificate issuance. It owns
// no transport and performs no I/O: it answers a question and names the rule
// that answered it.
package connectionpolicy

import (
	"errors"
	"fmt"
	"strings"
)

const MaxRules = 512

var (
	ErrInvalidRuleSet = errors.New("connection rule set is invalid")
	// ErrAskUnsupported keeps a blocking decision from shipping half-built. An
	// ask that cannot actually block a dial on a person is an allow wearing a
	// different name.
	ErrAskUnsupported = errors.New(
		"connection rule set asks, but the ask path does not exist yet",
	)
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
	DecisionAsk   Decision = "ask"
)

func (decision Decision) valid() bool {
	switch decision {
	case DecisionAllow, DecisionDeny, DecisionAsk:
		return true
	default:
		return false
	}
}

// MatchKind is closed. A rule cannot carry a pattern language, because a
// wildcard or regular expression is how an allow list quietly becomes an allow
// everything.
type MatchKind string

const (
	MatchKindAny           MatchKind = "any"
	MatchKindExactHost     MatchKind = "exact_host"
	MatchKindExactHostPort MatchKind = "exact_host_port"
)

type Match struct {
	Kind MatchKind
	Host string
	Port uint16
}

func MatchAny() Match {
	return Match{Kind: MatchKindAny}
}

func MatchExactHost(host string) Match {
	return Match{Kind: MatchKindExactHost, Host: host}
}

func MatchExactHostPort(host string, port uint16) Match {
	return Match{Kind: MatchKindExactHostPort, Host: host, Port: port}
}

func (match Match) validate() error {
	switch match.Kind {
	case MatchKindAny:
		if match.Host != "" || match.Port != 0 {
			return fmt.Errorf("%w: an any match carries a target", ErrInvalidRuleSet)
		}
		return nil
	case MatchKindExactHost:
		if match.Port != 0 {
			return fmt.Errorf("%w: a host match carries a port", ErrInvalidRuleSet)
		}
		return validateHost(match.Host)
	case MatchKindExactHostPort:
		if match.Port == 0 {
			return fmt.Errorf("%w: a host and port match has no port", ErrInvalidRuleSet)
		}
		return validateHost(match.Host)
	default:
		return fmt.Errorf("%w: match kind is invalid", ErrInvalidRuleSet)
	}
}

func (match Match) matches(request Request) bool {
	switch match.Kind {
	case MatchKindAny:
		return true
	case MatchKindExactHost:
		return match.Host == request.Host
	case MatchKindExactHostPort:
		return match.Host == request.Host && match.Port == request.Port
	default:
		return false
	}
}

type Rule struct {
	ID       string
	Decision Decision
	Match    Match
}

func (rule Rule) validate() error {
	if err := validateIdentity("connection rule ID", rule.ID); err != nil {
		return err
	}
	if !rule.Decision.valid() {
		return fmt.Errorf("%w: rule decision is invalid", ErrInvalidRuleSet)
	}
	return rule.Match.validate()
}

// Request is what a connection is, before anything has been attempted.
type Request struct {
	Host string
	Port uint16
}

// Outcome names both the answer and the rule that produced it, so a record can
// explain itself rather than carry a literal.
type Outcome struct {
	Decision Decision
	RuleID   string
	Revision uint64
}

type RuleSetOptions struct {
	Revision uint64
	Rules    []Rule
	// Default answers a connection no rule matched. It is required, because
	// leaving it out would make that answer implicit.
	Default Rule
}

// RuleSet is immutable and ordered. The first matching rule wins, which makes
// evaluation deterministic and explainable.
type RuleSet struct {
	revision    uint64
	rules       []Rule
	defaultRule Rule
}

func NewRuleSet(options RuleSetOptions) (RuleSet, error) {
	if options.Revision == 0 {
		return RuleSet{}, fmt.Errorf("%w: revision is required", ErrInvalidRuleSet)
	}
	if len(options.Rules) > MaxRules {
		return RuleSet{}, fmt.Errorf("%w: too many rules", ErrInvalidRuleSet)
	}
	if options.Default.ID == "" {
		return RuleSet{}, fmt.Errorf(
			"%w: a rule set must declare a default",
			ErrInvalidRuleSet,
		)
	}
	if err := options.Default.validate(); err != nil {
		return RuleSet{}, err
	}
	if options.Default.Match.Kind != MatchKindAny {
		return RuleSet{}, fmt.Errorf(
			"%w: the default must answer every connection",
			ErrInvalidRuleSet,
		)
	}
	// Design 06 makes this an invariant. A default that allows everything makes
	// the firewall the one control that never fires, so an operator who wants
	// that must write it as an explicit rule and see it in the list.
	if options.Default.Decision == DecisionAllow {
		return RuleSet{}, fmt.Errorf(
			"%w: the shipped default cannot allow every connection",
			ErrInvalidRuleSet,
		)
	}
	identifiers := make(map[string]struct{}, len(options.Rules)+1)
	identifiers[options.Default.ID] = struct{}{}
	rules := make([]Rule, 0, len(options.Rules))
	for _, rule := range options.Rules {
		if err := rule.validate(); err != nil {
			return RuleSet{}, err
		}
		if rule.Decision == DecisionAsk {
			return RuleSet{}, ErrAskUnsupported
		}
		if _, duplicate := identifiers[rule.ID]; duplicate {
			return RuleSet{}, fmt.Errorf(
				"%w: rule ID %q is duplicated",
				ErrInvalidRuleSet,
				rule.ID,
			)
		}
		identifiers[rule.ID] = struct{}{}
		rules = append(rules, rule)
	}
	if options.Default.Decision == DecisionAsk {
		return RuleSet{}, ErrAskUnsupported
	}
	return RuleSet{
		revision:    options.Revision,
		rules:       rules,
		defaultRule: options.Default,
	}, nil
}

// Evaluate answers one connection. It performs no I/O and consults nothing
// outside the frozen set, so the same set and the same connection always
// produce the same answer.
func (set RuleSet) Evaluate(request Request) Outcome {
	for _, rule := range set.rules {
		if rule.Match.matches(request) {
			return Outcome{
				Decision: rule.Decision,
				RuleID:   rule.ID,
				Revision: set.revision,
			}
		}
	}
	return Outcome{
		Decision: set.defaultRule.Decision,
		RuleID:   set.defaultRule.ID,
		Revision: set.revision,
	}
}

func (set RuleSet) Revision() uint64 { return set.revision }

func validateHost(host string) error {
	if host == "" ||
		len(host) > 253 ||
		strings.ToLower(host) != host ||
		strings.ContainsAny(host, " \t\r\n/*?") ||
		strings.HasPrefix(host, ".") ||
		strings.HasSuffix(host, ".") {
		return fmt.Errorf(
			"%w: rule host must be an exact canonical name",
			ErrInvalidRuleSet,
		)
	}
	return nil
}

func validateIdentity(label, value string) error {
	if value == "" ||
		len(value) > 256 ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidRuleSet, label)
	}
	return nil
}

// InterimAllowUnmatchedRuleID names the rule that stands in for `ask`.
//
// Design 06 makes a wildcard allow default an invariant violation, and the
// shipped default there is `ask` for an unknown host. The ask path does not
// exist yet, and neither available alternative is honest as a product default:
// denying by default makes the proxy unusable while no rule editing exists,
// and allowing by default is the invariant violation.
//
// So this slice ships an explicit, named rule rather than a permissive
// default. Every connection it admits carries this identifier in its
// connection record, which makes the gap visible in the data rather than
// hidden in a constant, and deleting the rule is what turns `ask` on.
const InterimAllowUnmatchedRuleID = "interim.allow-unmatched-pending-ask"

// InterimRuleSet is the policy this slice ships. It is not the product
// default; it is the placeholder the ask slice removes.
func InterimRuleSet(revision uint64) (RuleSet, error) {
	return NewRuleSet(RuleSetOptions{
		Revision: revision,
		Rules: []Rule{{
			ID:       InterimAllowUnmatchedRuleID,
			Decision: DecisionAllow,
			Match:    MatchAny(),
		}},
		Default: Rule{
			ID:       "default.deny",
			Decision: DecisionDeny,
			Match:    MatchAny(),
		},
	})
}
