package serverconnection

import "testing"

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
