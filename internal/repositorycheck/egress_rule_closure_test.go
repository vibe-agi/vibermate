package repositorycheck

import (
	"strings"
	"testing"
)

func TestCompositionSDDEvidenceExclusionIsExact(t *testing.T) {
	for _, test := range []struct {
		path     string
		archived bool
	}{
		{".superpowers/sdd/run/cmd/copied/main.go", true},
		{".superpowers/live/main.go", false},
		{".superpowers/sdd-copy/main.go", false},
		{"other/.superpowers/sdd/main.go", false},
		{"cmd/secondentry/main.go", false},
		{"internal/sneakyhost/host.go", false},
	} {
		t.Run(test.path, func(t *testing.T) {
			root := t.TempDir()
			writeAcceptanceRuleFixture(t, root, "internal/serverhost/host.go", "package serverhost; func Start() {}")
			writeAcceptanceRuleFixture(t, root, "internal/serverdaemon/daemon.go", "package serverdaemon; import \"github.com/vibe-agi/vibermate/internal/serverhost\"; func Run(){ serverhost.Start() }")
			writeAcceptanceRuleFixture(t, root, test.path, "package copied; import \"github.com/vibe-agi/vibermate/internal/serverhost\"; func f(){ serverhost.Start() }")
			violations := CheckProductionCompositionBoundary(root)
			if test.archived {
				if len(violations) != 0 {
					t.Fatalf("retained evidence counted as production: %+v", violations)
				}
				return
			}
			if len(violations) != 1 || violations[0].Rule != "unreviewed-server-host-composition" || violations[0].Path != test.path {
				t.Fatalf("live composition boundary not enforced at %s: %+v", test.path, violations)
			}
		})
	}
}

func TestPrivateCoreDialAllowanceIsExact(t *testing.T) {
	for _, dial := range []struct {
		name, path, method, network, field string
	}{
		{"tcp", "internal/vlesscore/socks.go", "authenticate", "tcp4", "endpoint"},
		{"udp", "internal/vlesscore/datagram.go", "DialUDP", "unix", "udpPath"},
	} {
		t.Run(dial.name, func(t *testing.T) {
			source := "package vlesscore; import (\"context\"; \"net\"); type Client struct{ " + dial.field + " string }; func (c *Client) " + dial.method + "(ctx context.Context)(net.Conn,error){ return (&net.Dialer{}).DialContext(ctx,\"" + dial.network + "\",c." + dial.field + ") }"
			for _, test := range []struct {
				name, path, source, symbol string
				allowed                    bool
			}{
				{"exact", dial.path, source, "", true},
				{"named-net-alias", dial.path, strings.ReplaceAll(strings.Replace(source, "\"net\"", "socket \"net\"", 1), "net.", "socket."), "", true},
				{"network", dial.path, strings.Replace(source, "\""+dial.network+"\"", "\"tcp\"", 1), "net.Dialer", false},
				{"caller-address", dial.path, strings.Replace(source, "c."+dial.field+")", "address)", 1), "net.Dialer", false},
				{"wrong-field", dial.path, strings.Replace(source, "c."+dial.field+")", "c.other)", 1), "net.Dialer", false},
				{"other-receiver", dial.path, strings.Replace(source, "(c *Client)", "(c *Other)", 1), "net.Dialer", false},
				{"value-receiver", dial.path, strings.Replace(source, "(c *Client)", "(c Client)", 1), "net.Dialer", false},
				{"receiverless", dial.path, strings.Replace(source, "(c *Client) ", "", 1), "net.Dialer", false},
				{"wrong-method", dial.path, strings.Replace(source, dial.method+"(ctx", "other(ctx", 1), "net.Dialer", false},
				{"shadowed-receiver", dial.path, strings.Replace(source, "{ return", "{ { c := &Client{}; return", 1) + " }", "net.Dialer", false},
				{"nested-closure", dial.path, strings.Replace(source, "{ return", "{ return func()(net.Conn,error){ return", 1) + "() }", "net.Dialer", false},
				{"sibling-path", "internal/vlesscore/copied.go", source, "net.Dialer", false},
				{"wrong-package", dial.path, strings.Replace(source, "package vlesscore", "package other", 1), "net.Dialer", false},
				{"nonempty-dialer", dial.path, strings.Replace(source, "net.Dialer{}", "net.Dialer{Timeout: 1}", 1), "net.Dialer", false},
				{"extra-dialer", dial.path, strings.Replace(source, "{ return", "{ _ = net.Dialer{}; return", 1), "net.Dialer", false},
				{"extra-http-client", dial.path, strings.Replace(source, "\"net\")", "\"net\"; \"net/http\")", 1) + "; var _ = http.Client{}", "http.Client", false},
				{"dot-import", dial.path, strings.ReplaceAll(strings.Replace(source, "\"net\"", ". \"net\"", 1), "net.", ""), "", false},
				{"wrong-dial-method", dial.path, strings.Replace(source, ".DialContext(", ".Dial(", 1), "net.Dialer", false},
				{"extra-call-argument", dial.path, strings.Replace(source, "c."+dial.field+")", "c."+dial.field+", address)", 1), "net.Dialer", false},
				{"unparenthesized-dialer", dial.path, strings.Replace(source, "(&net.Dialer{}).DialContext", "net.Dialer{}.DialContext", 1), "net.Dialer", false},
			} {
				t.Run(test.name, func(t *testing.T) {
					root := t.TempDir()
					writeAcceptanceRuleFixture(t, root, test.path, test.source)
					violations := CheckExternalEgressGate(root)
					if test.allowed {
						if len(violations) != 0 {
							t.Fatalf("private core dial rejected: %+v", violations)
						}
						return
					}
					message := "raw outbound symbol " + test.symbol + " is outside the gated transport"
					if test.name == "dot-import" {
						message = "protected network packages must use a named import"
					}
					if len(violations) != 1 || violations[0].Rule != "external-egress-gate" || violations[0].Path != test.path || violations[0].Message != message {
						t.Fatalf("egress boundary not enforced at %s: %+v", test.path, violations)
					}
				})
			}
		})
	}
}
