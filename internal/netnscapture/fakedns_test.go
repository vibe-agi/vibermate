package netnscapture

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func query(t *testing.T, name string, kind dnsmessage.Type) []byte {
	t.Helper()
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	_ = builder.StartQuestions()
	_ = builder.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: kind, Class: dnsmessage.ClassINET})
	message, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func answer(t *testing.T, dns *fakeDNS, name string, kind dnsmessage.Type) (dnsmessage.Header, []dnsmessage.Resource) {
	t.Helper()
	var message dnsmessage.Message
	if err := message.Unpack(dns.Answer(query(t, name, kind))); err != nil {
		t.Fatal(err)
	}
	return message.Header, message.Answers
}

func TestFakeDNSMapsNamesToStableAddresses(t *testing.T) {
	dns := newFakeDNS()
	header, answers := answer(t, dns, "API.Anthropic.com.", dnsmessage.TypeA)
	if header.ID != 7 || !header.Response || header.RCode != dnsmessage.RCodeSuccess || len(answers) != 1 {
		t.Fatalf("reply = %+v %+v", header, answers)
	}
	address := netip.AddrFrom4(answers[0].Body.(*dnsmessage.AResource).A)
	if !fakePrefix.Contains(address) || address == tunAddress || address == resolverAddress {
		t.Fatalf("fake address %s is outside the reserved pool", address)
	}
	if name, ok := dns.Lookup(address); !ok || name != "api.anthropic.com" {
		t.Fatalf("Lookup(%s) = %q, %t", address, name, ok)
	}
	_, again := answer(t, dns, "api.anthropic.com.", dnsmessage.TypeA)
	if netip.AddrFrom4(again[0].Body.(*dnsmessage.AResource).A) != address {
		t.Fatal("a repeated query got a different address")
	}
	_, other := answer(t, dns, "chatgpt.com.", dnsmessage.TypeA)
	if netip.AddrFrom4(other[0].Body.(*dnsmessage.AResource).A) == address {
		t.Fatal("two names share an address")
	}
}

func TestFakeDNSAnswersOtherTypesEmpty(t *testing.T) {
	dns := newFakeDNS()
	for _, kind := range []dnsmessage.Type{dnsmessage.TypeAAAA, dnsmessage.TypeHTTPS, dnsmessage.TypeTXT} {
		header, answers := answer(t, dns, "example.com.", kind)
		if header.RCode != dnsmessage.RCodeSuccess || len(answers) != 0 {
			t.Fatalf("type %v reply = %+v %d answers", kind, header, len(answers))
		}
	}
	if header, _ := answer(t, dns, "bad!name.example.", dnsmessage.TypeA); header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("invalid name rcode = %v", header.RCode)
	}
	if dns.Answer([]byte{1, 2, 3}) != nil {
		t.Fatal("a malformed query got a reply")
	}
}

func TestFakeDNSReusesTheOldestAddressWhenFull(t *testing.T) {
	dns := newFakeDNS()
	first := dns.addressFor("first.example")
	for i := 1; i < fakeCapacity; i++ {
		dns.addressFor("name-" + netip.AddrFrom4([4]byte{byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i)}).String() + ".example")
	}
	if _, ok := dns.Lookup(first); !ok {
		t.Fatal("first name evicted before the pool was full")
	}
	// first was just used by Lookup, so the next eviction takes another name.
	newest := dns.addressFor("newest.example")
	if name, _ := dns.Lookup(first); name != "first.example" {
		t.Fatal("a recently used name was evicted")
	}
	if name, ok := dns.Lookup(newest); !ok || name != "newest.example" {
		t.Fatalf("Lookup(newest) = %q, %t", name, ok)
	}
}
