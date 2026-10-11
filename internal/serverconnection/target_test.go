package serverconnection

import "testing"

func TestParseTargetNormalizesDefaultPorts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input, address, origin string
		transport              Transport
	}{
		{"https://runtime.example.test", "runtime.example.test:443", "https://runtime.example.test:443", TransportHTTPS},
		{"https://RUNTIME.Example.TEST", "runtime.example.test:443", "https://runtime.example.test:443", TransportHTTPS},
		{"https://192.168.1.20", "192.168.1.20:443", "https://192.168.1.20:443", TransportHTTPS},
		{"https://[2001:db8::1]", "[2001:db8::1]:443", "https://[2001:db8::1]:443", TransportHTTPS},
		{"http://runtime.example.test", "runtime.example.test:80", "http://runtime.example.test:80", TransportHTTP},
		{"https://runtime.example.test:443", "runtime.example.test:443", "https://runtime.example.test:443", TransportHTTPS},
		{"http://runtime.example.test:80", "runtime.example.test:80", "http://runtime.example.test:80", TransportHTTP},
		{"https://runtime.example.test:9443", "runtime.example.test:9443", "https://runtime.example.test:9443", TransportHTTPS},
	} {
		t.Run(test.input, func(t *testing.T) {
			target, err := ParseTarget(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if target.Address().String() != test.address || target.Origin() != test.origin || target.Transport() != test.transport {
				t.Fatalf("target = %+v; want address %q, origin %q, transport %q", target, test.address, test.origin, test.transport)
			}
		})
	}
}

func TestParseTargetDefaultPortsRejectMalformedAuthorities(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"runtime.example.test", "https://", "https://host:", "http://host:",
		"https://host:0", "https://host:invalid", "https://host:65536", "https://host:-1",
		"https://::1", "https://2001:db8::1", "https://[::1", "https://[host]", "https://[::1]:",
		"https://user@host", "https://user:password@host", "https://host/path",
		"https://host?query=1", "https://host?", "https://host#fragment", "https://host#",
		"ftp://host", " https://host", "https://host ", "https://ho st", "https://host\t",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseTarget(input); err == nil {
				t.Fatalf("ParseTarget(%q) succeeded", input)
			}
		})
	}
}

func TestParseTargetDefaultsBareServerAddressToHTTP(t *testing.T) {
	t.Parallel()

	target, err := ParseTarget("VIBERMATE.LAN:9666")
	if err != nil {
		t.Fatal(err)
	}
	if target.Transport() != TransportHTTP ||
		target.Address().String() != "vibermate.lan:9666" ||
		target.Origin() != "http://vibermate.lan:9666" {
		t.Fatalf("target = %+v", target)
	}
}

func TestParseTargetAcceptsExplicitHTTPAndHTTPSWithoutDowngradeAmbiguity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input     string
		transport Transport
		origin    string
	}{
		{input: "http://192.168.1.20:9666", transport: TransportHTTP, origin: "http://192.168.1.20:9666"},
		{input: "https://VIBERMATE.LAN:9666", transport: TransportHTTPS, origin: "https://vibermate.lan:9666"},
	} {
		target, err := ParseTarget(test.input)
		if err != nil {
			t.Fatalf("ParseTarget(%q): %v", test.input, err)
		}
		if target.Transport() != test.transport || target.Origin() != test.origin {
			t.Fatalf("ParseTarget(%q) = %+v", test.input, target)
		}
	}
}

func TestParseTargetRejectsPathsCredentialsAndUnsupportedSchemes(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"", "192.168.1.20", "ftp://host:9666", "http://user@host:9666",
		"http://host:9666/path", "http://host:9666?query=1", "http://host:9666#fragment",
	} {
		if _, err := ParseTarget(value); err == nil {
			t.Fatalf("ParseTarget(%q) succeeded", value)
		}
	}
}
