package serverconnection

import "testing"

func TestParseAddressCanonicalizesIPAndHostAuthorities(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "192.168.1.20:9666", want: "192.168.1.20:9666"},
		{input: "VIBERMATE.LAN:9666", want: "vibermate.lan:9666"},
		{input: "[2001:db8::1]:9666", want: "[2001:db8::1]:9666"},
	} {
		address, err := ParseAddress(test.input)
		if err != nil {
			t.Fatalf("ParseAddress(%q): %v", test.input, err)
		}
		if address.String() != test.want {
			t.Fatalf("ParseAddress(%q) = %q, want %q", test.input, address, test.want)
		}
	}
}

func TestParseAddressRejectsURLPathsMissingPortsAndUnsafeHosts(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"",
		"192.168.1.20",
		"http://192.168.1.20:9666",
		"user@host:9666",
		"host:0",
		"host:65536",
		"0.0.0.0:9666",
		"[::]:9666",
		"host:9666/path",
		"-host:9666",
		"host..lan:9666",
	} {
		if _, err := ParseAddress(value); err == nil {
			t.Fatalf("ParseAddress(%q) succeeded", value)
		}
	}
}
