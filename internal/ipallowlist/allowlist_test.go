package ipallowlist

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestParseKeepsCanonicalNetworksInOrder(t *testing.T) {
	t.Parallel()

	list, err := Parse([]string{
		"203.0.113.0/24",
		" 198.51.100.7 ",
		"198.51.100.7/32",
		"2001:DB8::/32",
		"2001:db8:1::1",
		"203.0.113.0/24",
		"0.0.0.0/0",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"203.0.113.0/24", "198.51.100.7", "2001:db8::/32", "2001:db8:1::1", "0.0.0.0/0",
	}
	if got := list.Entries(); !slices.Equal(got, want) {
		t.Fatalf("Entries() = %q, want %q", got, want)
	}
	if list.Len() != len(want) {
		t.Fatalf("Len() = %d, want %d", list.Len(), len(want))
	}
}

func TestParseNamesTheRejectedEntry(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		entry      string
		reason     string
		suggestion string
	}{
		{entry: "", reason: ReasonEmptyEntry},
		{entry: "   ", reason: ReasonEmptyEntry},
		{entry: "office", reason: ReasonSyntax},
		{entry: "203.0.113.0/33", reason: ReasonSyntax},
		{entry: "203.0.113.7/24", reason: ReasonHostBits, suggestion: "203.0.113.0/24"},
		{entry: "2001:db8::1/32", reason: ReasonHostBits, suggestion: "2001:db8::/32"},
		{entry: "fe80::1%eth0", reason: ReasonZone},
		{entry: "::ffff:203.0.113.7", reason: ReasonIPv4Mapped},
		{entry: "::ffff:203.0.113.0/120", reason: ReasonIPv4Mapped},
		{entry: strings.Repeat("1", maxEntryBytes+1), reason: ReasonEntryLength},
	} {
		_, err := Parse([]string{"198.51.100.0/24", test.entry})
		var entryErr *EntryError
		if !errors.As(err, &entryErr) || !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("Parse(%q) error = %v, want EntryError", test.entry, err)
		}
		if entryErr.Index != 1 || entryErr.Reason != test.reason || entryErr.Suggestion != test.suggestion {
			t.Fatalf("Parse(%q) = %+v, want index 1 reason %q suggestion %q",
				test.entry, entryErr, test.reason, test.suggestion)
		}
	}
}

func TestParseBoundsTheList(t *testing.T) {
	t.Parallel()

	entries := make([]string, MaxRanges+1)
	for index := range entries {
		entries[index] = fmt.Sprintf("10.%d.%d.0/24", index/256, index%256)
	}
	if _, err := Parse(entries[:MaxRanges]); err != nil {
		t.Fatalf("Parse(%d entries) = %v", MaxRanges, err)
	}
	if _, err := Parse(entries); !errors.Is(err, ErrTooManyRanges) {
		t.Fatalf("Parse(%d entries) = %v, want ErrTooManyRanges", len(entries), err)
	}
}

func TestAllows(t *testing.T) {
	t.Parallel()

	office, err := Parse([]string{"203.0.113.0/24", "2001:db8::/32", "fe80::/10"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		list    List
		address string
		want    bool
	}{
		{name: "empty list allows anyone", list: List{}, address: "198.51.100.1", want: true},
		{name: "inside a network", list: office, address: "203.0.113.200", want: true},
		{name: "IPv4-mapped client", list: office, address: "::ffff:203.0.113.200", want: true},
		{name: "IPv6 network", list: office, address: "2001:db8:5::7", want: true},
		{name: "zone is ignored", list: office, address: "fe80::1%eth0", want: true},
		{name: "outside every network", list: office, address: "198.51.100.1", want: false},
		{name: "IPv4 loopback", list: office, address: "127.0.0.1", want: true},
		{name: "other IPv4 loopback", list: office, address: "127.8.9.10", want: true},
		{name: "IPv6 loopback", list: office, address: "::1", want: true},
		{name: "mapped loopback", list: office, address: "::ffff:127.0.0.1", want: true},
	} {
		address := netip.MustParseAddr(test.address)
		if got := test.list.Allows(address); got != test.want {
			t.Errorf("%s: Allows(%s) = %v, want %v", test.name, address, got, test.want)
		}
	}
	if office.Allows(netip.Addr{}) || (List{}).Allows(netip.Addr{}) {
		t.Fatal("an unknown address was allowed")
	}
}

func TestContainsHasNoExceptions(t *testing.T) {
	t.Parallel()

	balancers, err := Parse([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	for address, want := range map[string]bool{
		"10.20.30.40":        true,
		"::ffff:10.20.30.40": true,
		"127.0.0.1":          false,
		"203.0.113.9":        false,
	} {
		if got := balancers.Contains(netip.MustParseAddr(address)); got != want {
			t.Errorf("Contains(%s) = %v, want %v", address, got, want)
		}
	}
	if (List{}).Contains(netip.MustParseAddr("203.0.113.9")) {
		t.Fatal("an empty list contained an address")
	}
}
