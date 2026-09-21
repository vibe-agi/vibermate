package serverhost

import (
	"net"
	"net/netip"
	"reflect"
	"testing"
)

func TestRuntimeConnectTargetsPreferUsablePrivateInterfaceAddresses(t *testing.T) {
	t.Parallel()

	targets, err := runtimeConnectTargets("0.0.0.0:9666", []runtimeInterfaceAddress{
		{name: "utun4", address: netip.MustParseAddr("10.8.0.2")},
		{name: "en0", address: netip.MustParseAddr("192.168.1.44")},
		{name: "en0", address: netip.MustParseAddr("fe80::1")},
		{name: "lo0", address: netip.MustParseAddr("127.0.0.1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.44:9666", "10.8.0.2:9666"}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestRuntimeConnectTargetsKeepAnExplicitListenIPAndBracketIPv6(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		listen string
		want   []string
	}{
		{listen: "192.168.50.8:9443", want: []string{"192.168.50.8:9443"}},
		{listen: "[fd00::8]:9443", want: []string{"[fd00::8]:9443"}},
		{listen: "127.0.0.1:9443", want: []string{"127.0.0.1:9443"}},
	} {
		targets, err := runtimeConnectTargets(test.listen, nil)
		if err != nil {
			t.Fatalf("runtimeConnectTargets(%q): %v", test.listen, err)
		}
		if !reflect.DeepEqual(targets, test.want) {
			t.Fatalf("runtimeConnectTargets(%q) = %#v, want %#v", test.listen, targets, test.want)
		}
	}
}

func TestRuntimeAccessTargetsPreferAnExplicitPublicAddress(t *testing.T) {
	t.Parallel()
	targets, err := runtimeAccessTargets(
		"0.0.0.0:9666",
		"runtime.example.com:443",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"runtime.example.com:443"}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestPrivateCATransportDerivesCertificateIdentityFromAccessAddress(t *testing.T) {
	t.Parallel()
	for _, address := range []string{
		"vibermate.home.arpa:9666",
		"192.168.1.20:9666",
		"[fd00::20]:9666",
	} {
		options, err := (TransportOptions{Mode: TransportPrivateCATLS}).
			forAccessAddress(address)
		if err != nil {
			t.Fatal(err)
		}
		host, _, _ := net.SplitHostPort(address)
		if len(options.TLSHosts) != 1 || options.TLSHosts[0] != host {
			t.Fatalf("%s TLS hosts = %#v", address, options.TLSHosts)
		}
	}
}

func TestPrivateCATransportKeepsExplicitCertificateIdentities(t *testing.T) {
	t.Parallel()
	options, err := (TransportOptions{
		Mode: TransportPrivateCATLS, TLSHosts: []string{"explicit.home.arpa"},
	}).forAccessAddress("another.home.arpa:9666")
	if err != nil {
		t.Fatal(err)
	}
	if len(options.TLSHosts) != 1 || options.TLSHosts[0] != "explicit.home.arpa" {
		t.Fatalf("TLS hosts = %#v", options.TLSHosts)
	}
}
