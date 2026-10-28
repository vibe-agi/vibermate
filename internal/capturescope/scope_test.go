package capturescope

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestPolicyBoundsRefuseCaptureNothing(t *testing.T) {
	valid := Policy{Rules: []Rule{
		{ID: "git", Kind: KindHost, Value: "git.corp.example"},
		{ID: "intranet", Kind: KindSuffix, Value: "corp.example"},
		{ID: "lan", Kind: KindPrefix, Value: "192.168.0.0/16"},
		{ID: "tailnet", Kind: KindPrefix, Value: "100.64.0.0/10", Port: 443},
		{ID: "ula", Kind: KindPrefix, Value: "fd00::/8"},
		{ID: "nas", Kind: KindHost, Value: "nas"},
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy refused: %v", err)
	}
	for name, rule := range map[string]Rule{
		"bare wildcard":            {ID: "r", Kind: KindSuffix, Value: "*"},
		"single-label suffix":      {ID: "r", Kind: KindSuffix, Value: "com"},
		"public prefix":            {ID: "r", Kind: KindPrefix, Value: "8.8.8.0/24"},
		"everything":               {ID: "r", Kind: KindPrefix, Value: "0.0.0.0/0"},
		"wider than reserved":      {ID: "r", Kind: KindPrefix, Value: "172.0.0.0/8"},
		"non-canonical prefix":     {ID: "r", Kind: KindPrefix, Value: "10.1.2.3/8"},
		"IP as host":               {ID: "r", Kind: KindHost, Value: "10.1.2.3"},
		"upper case":               {ID: "r", Kind: KindHost, Value: "Git.corp.example"},
		"trailing dot":             {ID: "r", Kind: KindSuffix, Value: "corp.example."},
		"unknown kind":             {ID: "r", Kind: "regex", Value: ".*"},
		"built-in identifier":      {ID: "builtin.loopback", Kind: KindHost, Value: "a.example"},
		"user identifier":          {ID: "user.1", Kind: KindHost, Value: "a.example"},
		"public IPv6":              {ID: "r", Kind: KindPrefix, Value: "2001:db8::/32"},
		"IPv4-mapped reserved ish": {ID: "r", Kind: KindPrefix, Value: "::ffff:10.0.0.0/104"},
	} {
		if err := (Policy{Rules: []Rule{rule}}).Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	duplicate := Policy{Rules: []Rule{{ID: "a", Kind: KindHost, Value: "x.example"}, {ID: "a", Kind: KindHost, Value: "y.example"}}}
	if err := duplicate.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Errorf("duplicate IDs accepted: %v", err)
	}
	many := Policy{}
	for i := 0; i <= MaxRules; i++ {
		many.Rules = append(many.Rules, Rule{ID: "r" + strings.Repeat("x", i%40) + string(rune('a'+i%26)) + string(rune('a'+i/26)), Kind: KindHost, Value: "h.example"})
	}
	if err := many.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Errorf("%d rules accepted: %v", len(many.Rules), err)
	}
}

func destination(name, addr string, port uint16) Destination {
	var parsed netip.Addr
	if addr != "" {
		parsed = netip.MustParseAddr(addr)
	}
	return Destination{Name: name, Addr: parsed, Port: port}
}

func TestScopeMatchesRulesAndBuiltins(t *testing.T) {
	policy := &Policy{Rules: []Rule{
		{ID: "git", Kind: KindHost, Value: "git.corp.example"},
		{ID: "intranet", Kind: KindSuffix, Value: "corp.example"},
		{ID: "lan", Kind: KindPrefix, Value: "192.168.0.0/16"},
		{ID: "tailnet-https", Kind: KindPrefix, Value: "100.64.0.0/10", Port: 443},
	}}
	scope := Compile(policy, nil, Options{})
	for _, test := range []struct {
		destination Destination
		rule        string
		bypass      bool
	}{
		{destination("git.corp.example", "", 443), "git", true},
		{destination("wiki.corp.example", "", 443), "intranet", true},
		{destination("corp.example", "", 80), "intranet", true},
		{destination("notcorp.example", "", 443), "", false},
		{destination("", "192.168.3.15", 22), "lan", true},
		{destination("", "::ffff:192.168.3.15", 22), "lan", true},
		{destination("", "100.100.1.1", 443), "tailnet-https", true},
		{destination("", "100.100.1.1", 80), "", false},
		{destination("localhost", "", 3000), BuiltinLoopback, true},
		{destination("", "127.0.0.1", 8080), BuiltinLoopback, true},
		{destination("", "::1", 8080), BuiltinLoopback, true},
		{destination("", "169.254.169.254", 80), BuiltinLinkLocal, true},
		{destination("api.anthropic.com", "160.79.104.10", 443), "", false},
		{destination("", "10.0.0.53", 53), "", false},
	} {
		rule, bypass := scope.Match(test.destination)
		if rule != test.rule || bypass != test.bypass {
			t.Errorf("Match(%+v) = %q %v, want %q %v", test.destination, rule, bypass, test.rule, test.bypass)
		}
	}
	macOS := Compile(nil, nil, Options{DNSOnSystemPath: true})
	if rule, bypass := macOS.Match(destination("", "100.100.100.100", 53)); !bypass || rule != BuiltinDNS {
		t.Errorf("macOS DNS = %q %v, want the built-in DNS rule", rule, bypass)
	}
}

// Protected authorities never leave ViberMate: a rule that would cover one is
// dropped whole, and a destination named as one is never bypassed.
func TestScopeNeverBypassesProtectedAuthorities(t *testing.T) {
	policy := &Policy{Rules: []Rule{
		{ID: "corp", Kind: KindSuffix, Value: "corp.example"},
		{ID: "models", Kind: KindSuffix, Value: "anthropic.com"},
		{ID: "gateway", Kind: KindPrefix, Value: "10.9.0.0/16"},
	}}
	protected := []string{"llm.corp.example:443", "api.anthropic.com:443", "10.9.0.5:8443"}
	scope := Compile(policy, protected, Options{})
	for _, test := range []Destination{
		destination("llm.corp.example", "", 443),
		destination("api.anthropic.com", "", 443),
		destination("", "10.9.0.5", 8443),
		destination("wiki.corp.example", "", 443),
	} {
		if rule, bypass := scope.Match(test); bypass {
			t.Errorf("%+v bypassed by %q despite a protected authority", test, rule)
		}
	}
	if rule, covers := Covers(*policy, protected); !covers || rule != "corp" {
		t.Errorf("Covers = %q %v, want the first covering rule", rule, covers)
	}
	if _, covers := Covers(Policy{Rules: []Rule{{ID: "lan", Kind: KindPrefix, Value: "192.168.0.0/16"}}}, protected); covers {
		t.Error("an unrelated rule was reported as covering a protected authority")
	}
}

func TestUserAdditionsOnlyWhenAllowedAndBounded(t *testing.T) {
	noProxy := []string{"*.dev.example, .lab.example,10.20.0.0/16, 192.168.1.10,nas:8080, *, com, 8.8.8.8, api.anthropic.com"}
	closed := Compile(&Policy{}, nil, Options{UserNoProxy: noProxy})
	if _, bypass := closed.Match(destination("a.dev.example", "", 443)); bypass {
		t.Fatal("user NO_PROXY honored without the Environment allowing it")
	}
	open := Compile(&Policy{AllowUserAdditions: true}, []string{"api.anthropic.com:443"}, Options{UserNoProxy: noProxy})
	for _, test := range []struct {
		destination Destination
		bypass      bool
	}{
		{destination("a.dev.example", "", 443), true},
		{destination("x.lab.example", "", 443), true},
		{destination("", "10.20.3.4", 5432), true},
		{destination("", "192.168.1.10", 22), true},
		{destination("", "192.168.1.11", 22), false},
		{destination("nas", "", 8080), true},
		{destination("nas", "", 80), false},
		{destination("anything.com", "", 443), false},
		{destination("", "8.8.8.8", 53), false},
		{destination("api.anthropic.com", "", 443), false},
	} {
		rule, bypass := open.Match(test.destination)
		if bypass != test.bypass {
			t.Errorf("Match(%+v) = %q %v, want %v", test.destination, rule, bypass, test.bypass)
		}
		if bypass && !strings.HasPrefix(rule, "user.") {
			t.Errorf("Match(%+v) reported rule %q, want a user addition", test.destination, rule)
		}
	}
}

func TestNoProxyRendersPortlessRules(t *testing.T) {
	scope := Compile(&Policy{Rules: []Rule{
		{ID: "intranet", Kind: KindSuffix, Value: "corp.example"},
		{ID: "lan", Kind: KindPrefix, Value: "192.168.0.0/16"},
		{ID: "only-https", Kind: KindHost, Value: "dash.corp.example", Port: 443},
	}}, nil, Options{})
	want := "localhost,127.0.0.1,::1,169.254.0.0/16,fe80::/10,corp.example,192.168.0.0/16"
	if got := scope.NoProxy(); got != want {
		t.Fatalf("NoProxy = %q, want %q", got, want)
	}
	if got := Compile(nil, nil, Options{}).NoProxy(); got != "localhost,127.0.0.1,::1,169.254.0.0/16,fe80::/10" {
		t.Fatalf("default NoProxy = %q", got)
	}
}

func TestInvalidPolicyContributesNothing(t *testing.T) {
	scope := Compile(&Policy{Rules: []Rule{{ID: "everything", Kind: KindPrefix, Value: "0.0.0.0/0"}}}, nil, Options{})
	if _, bypass := scope.Match(destination("", "8.8.8.8", 443)); bypass {
		t.Fatal("an invalid policy let traffic out")
	}
	if len(scope.Prefixes()) != 0 {
		t.Fatal("an invalid policy contributed prefixes")
	}
}
