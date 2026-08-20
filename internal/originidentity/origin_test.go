package originidentity

import (
	"encoding/json"
	"testing"
)

func TestClientOriginCanonicalizesDNSAndRejectsIP(t *testing.T) {
	t.Parallel()
	origin, err := ParseClientOrigin("https://API.Example.com:8443")
	if err != nil || origin.String() != "https://api.example.com:8443" ||
		origin.HTTPAuthority() != "api.example.com:8443" || origin.EndpointAuthority() != "api.example.com:8443" {
		t.Fatalf("origin = %+v, %v", origin, err)
	}
	if _, err := ParseClientOrigin("https://127.0.0.1"); err == nil {
		t.Fatal("IP ClientOrigin was accepted")
	}
}

func TestProviderOriginClassifiesConstrainedCleartext(t *testing.T) {
	t.Parallel()
	loopback, err := ParseProviderOrigin("http://127.0.0.1:8080/v1")
	if err != nil || loopback.Transport() != ProviderTransportLoopbackCleartext ||
		loopback.BasePath() != "/v1" || loopback.HTTPAuthority() != "127.0.0.1:8080" {
		t.Fatalf("loopback = %+v, %v", loopback, err)
	}
	privateDNS, err := ParseProviderOrigin("http://spark-2a59:8888")
	if err != nil || privateDNS.Transport() != ProviderTransportPrivateCleartext ||
		privateDNS.String() != "http://spark-2a59:8888" {
		t.Fatalf("private DNS = %+v, %v", privateDNS, err)
	}
	privateIP, err := ParseProviderOrigin("http://192.168.50.12:8888/v1")
	if err != nil || privateIP.Transport() != ProviderTransportPrivateCleartext {
		t.Fatalf("private IP = %+v, %v", privateIP, err)
	}
	cgnat, err := ParseProviderOrigin("http://100.100.20.30:8888")
	if err != nil || cgnat.Transport() != ProviderTransportPrivateCleartext {
		t.Fatalf("CGNAT = %+v, %v", cgnat, err)
	}
	if _, err := ParseProviderOrigin("http://203.0.113.7:8888/v1"); err == nil {
		t.Fatal("public literal cleartext ProviderOrigin was accepted")
	}
}

func TestProviderOriginKeepsIPv6AuthorityBrackets(t *testing.T) {
	t.Parallel()
	origin, err := ParseProviderOrigin("https://[2001:db8::1]/v1")
	if err != nil || origin.HTTPAuthority() != "[2001:db8::1]" ||
		origin.EndpointAuthority() != "[2001:db8::1]:443" {
		t.Fatalf("origin = %+v, %v", origin, err)
	}
}

func TestOriginJSONRequiresCanonicalMachineValue(t *testing.T) {
	t.Parallel()
	var client ClientOrigin
	if err := json.Unmarshal([]byte(`"https://API.Example.com"`), &client); err == nil {
		t.Fatal("non-canonical ClientOrigin JSON was accepted")
	}
	provider, err := ParseProviderOrigin("https://relay.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(provider)
	if err != nil || string(encoded) != `"https://relay.example/v1"` {
		t.Fatalf("encoded = %s, %v", encoded, err)
	}
}
