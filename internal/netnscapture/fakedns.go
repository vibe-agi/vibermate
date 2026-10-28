package netnscapture

import (
	"container/list"
	"net/netip"
	"strings"
	"sync"

	"golang.org/x/net/dns/dnsmessage"
)

// The namespace's resolver and fake addresses come from 198.18.0.0/15
// (RFC 2544 benchmarking), which is never routed on the Internet.
var (
	tunAddress      = netip.MustParseAddr("198.18.0.1")
	resolverAddress = netip.MustParseAddr("198.18.0.2")
	fakePrefix      = netip.MustParsePrefix("198.18.0.0/15")
	firstFake       = netip.MustParseAddr("198.18.1.0")
)

const (
	fakeTTL = 60
	// fakeCapacity bounds remembered names; the oldest is reused when full.
	fakeCapacity = 1 << 16
)

// fakeDNS answers A queries with addresses that map back to the name, so a
// connection to one reaches the Runtime as a name it resolves through its
// own DoH. Other query types get an empty answer, so clients use IPv4.
type fakeDNS struct {
	mu     sync.Mutex
	next   netip.Addr
	byName map[string]*list.Element
	byAddr map[netip.Addr]*list.Element
	order  *list.List // front is the most recently used
}

type fakeEntry struct {
	name    string
	address netip.Addr
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{
		next:   firstFake,
		byName: map[string]*list.Element{},
		byAddr: map[netip.Addr]*list.Element{},
		order:  list.New(),
	}
}

// Lookup returns the name a fake address stands for.
func (dns *fakeDNS) Lookup(address netip.Addr) (string, bool) {
	dns.mu.Lock()
	defer dns.mu.Unlock()
	element, ok := dns.byAddr[address]
	if !ok {
		return "", false
	}
	dns.order.MoveToFront(element)
	return element.Value.(*fakeEntry).name, true
}

func (dns *fakeDNS) addressFor(name string) netip.Addr {
	dns.mu.Lock()
	defer dns.mu.Unlock()
	if element, ok := dns.byName[name]; ok {
		dns.order.MoveToFront(element)
		return element.Value.(*fakeEntry).address
	}
	var entry *fakeEntry
	if dns.order.Len() >= fakeCapacity {
		oldest := dns.order.Back()
		entry = oldest.Value.(*fakeEntry)
		delete(dns.byName, entry.name)
		dns.order.Remove(oldest)
		delete(dns.byAddr, entry.address)
		entry.name = name
	} else {
		entry = &fakeEntry{name: name, address: dns.next}
		dns.next = dns.next.Next()
		if !fakePrefix.Contains(dns.next) {
			dns.next = firstFake
		}
	}
	element := dns.order.PushFront(entry)
	dns.byName[name] = element
	dns.byAddr[entry.address] = element
	return entry.address
}

// Answer builds the reply to one DNS query message, or nil to drop it.
func (dns *fakeDNS) Answer(query []byte) []byte { return dns.answer(query, nil) }

// realResolver answers a name with real addresses instead of a fake one.
// handled is false for names it leaves to the fake resolver.
type realResolver func(name string) (addresses []netip.Addr, handled bool, err error)

// answer builds the reply; real, when set, may claim the name (ADR 0025: a
// name the capture scope keeps off ViberMate resolves to its real addresses,
// because the flow will leave from this machine).
func (dns *fakeDNS) answer(query []byte, real realResolver) []byte {
	var parser dnsmessage.Parser
	header, err := parser.Start(query)
	if err != nil || header.Response {
		return nil
	}
	question, err := parser.Question()
	if err != nil {
		return nil
	}
	reply := dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true,
		RecursionDesired: header.RecursionDesired, RecursionAvailable: true}
	name := strings.ToLower(strings.TrimSuffix(question.Name.String(), "."))
	if !validName(name) {
		reply.RCode = dnsmessage.RCodeNameError
	}
	var realAddresses []netip.Addr
	handled := false
	isA := question.Type == dnsmessage.TypeA && question.Class == dnsmessage.ClassINET
	if reply.RCode == dnsmessage.RCodeSuccess && real != nil && isA {
		var err error
		if realAddresses, handled, err = real(name); handled && err != nil {
			reply.RCode = dnsmessage.RCodeServerFailure
		}
	}
	builder := dnsmessage.NewBuilder(nil, reply)
	builder.EnableCompression()
	if builder.StartQuestions() != nil || builder.Question(question) != nil || builder.StartAnswers() != nil {
		return nil
	}
	if handled && reply.RCode == dnsmessage.RCodeSuccess && question.Type == dnsmessage.TypeA && question.Class == dnsmessage.ClassINET {
		for _, address := range realAddresses {
			if address = address.Unmap(); !address.Is4() {
				continue // The namespace routes IPv4 only.
			}
			if builder.AResource(dnsmessage.ResourceHeader{
				Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: fakeTTL,
			}, dnsmessage.AResource{A: address.As4()}) != nil {
				return nil
			}
		}
	} else if !handled && reply.RCode == dnsmessage.RCodeSuccess && question.Type == dnsmessage.TypeA && question.Class == dnsmessage.ClassINET {
		address := dns.addressFor(name).As4()
		if builder.AResource(dnsmessage.ResourceHeader{
			Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: fakeTTL,
		}, dnsmessage.AResource{A: address}) != nil {
			return nil
		}
	}
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

// validName accepts DNS host names the Runtime can resolve.
func validName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

// realNames remembers which real address a bypassed name resolved to, so a
// flow to that address is named and decided like the name. Bounded; the
// oldest entry goes first.
type realNames struct {
	mu     sync.Mutex
	names  map[netip.Addr]string
	order  []netip.Addr
	bounds int
}

func newRealNames(bounds int) *realNames {
	return &realNames{names: make(map[netip.Addr]string), bounds: bounds}
}

func (names *realNames) remember(address netip.Addr, name string) {
	names.mu.Lock()
	defer names.mu.Unlock()
	if _, known := names.names[address]; !known {
		if len(names.order) >= names.bounds {
			delete(names.names, names.order[0])
			names.order = names.order[1:]
		}
		names.order = append(names.order, address)
	}
	names.names[address] = name
}

func (names *realNames) lookup(address netip.Addr) (string, bool) {
	names.mu.Lock()
	defer names.mu.Unlock()
	name, ok := names.names[address]
	return name, ok
}
