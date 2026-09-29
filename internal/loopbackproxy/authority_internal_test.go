package loopbackproxy

import (
	"testing"

	"golang.org/x/net/idna"
)

// Connection policy, audit and dialing must see one host. An authority is
// accepted only when it is already the ASCII form the dialer would resolve;
// anything a later IDNA mapping could turn into another name is refused.
func TestProxyAuthorityHostIsAlreadyTheDialedASCIIName(t *testing.T) {
	for _, authority := range []string{
		"ⓛocalhost:443",    // ⓛocalhost maps to localhost
		"loc­alhost:443",   // soft hyphen is removed by IDNA
		"ｐastebin.com:443", // fullwidth p maps to p
		"%E2%93%9Bocalhost:443",
		"exa mple.com:443",
		"example..com:443",
		"-example.com:443",
		"example.com..:443",
	} {
		if host, port, err := splitAuthority(authority); err == nil {
			t.Errorf("accepted %q as %q:%d", authority, host, port)
		}
		if host, port, err := cleartextAuthority(authority); err == nil {
			t.Errorf("cleartext accepted %q as %q:%d", authority, host, port)
		}
	}
	for authority, want := range map[string]string{
		"Example.COM.:443":          "example.com",
		"xn--bcher-kva.example:443": "xn--bcher-kva.example",
		"127.0.0.1:8080":            "127.0.0.1",
		"[::1]:443":                 "::1",
		"api.anthropic.com:443":     "api.anthropic.com",
	} {
		host, _, err := splitAuthority(authority)
		if err != nil || host != want {
			t.Errorf("splitAuthority(%q) = %q, %v; want %q", authority, host, err, want)
			continue
		}
		if ascii, err := idna.Lookup.ToASCII(host); err == nil && ascii != host {
			t.Errorf("accepted host %q is not its own IDNA form %q", host, ascii)
		}
	}
}
