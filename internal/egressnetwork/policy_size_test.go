package egressnetwork

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPolicyRejectsNonIdempotentCanonicalDomainExpansion(t *testing.T) {
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, fmt.Sprintf("\u00e9%d.example", i))
	}
	p := DefaultPolicy()
	p.DirectDomains = DirectDomains(strings.Join(lines, "\n"))
	if len(p.DirectDomains) > 16384 {
		t.Fatal("fixture exceeds raw input allowance")
	}
	if _, err := p.Normalize(); err == nil {
		t.Fatal("accepted canonical domains exceeding their next-read allowance")
	}
}
func TestPolicyLargeCanonicalList(t *testing.T) {
	var lines []string
	for i := 0; i < 960; i++ {
		lines = append(lines, fmt.Sprintf("d%03d.example.com", i))
	}
	p := DefaultPolicy()
	p.DirectDomains = DirectDomains(strings.Join(lines, "\n"))
	p.Proxy = ProxyPolicy{Kind: ProxySOCKS5, Endpoint: "127.0.0.1:1080"}
	n, err := p.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.DirectDomains) != 16319 || len(encoded) <= 16384 {
		t.Fatalf("wrong canonical boundary fixture: domains=%d JSON=%d", len(n.DirectDomains), len(encoded))
	}
	again, err := n.Normalize()
	if err != nil || again != n {
		t.Fatal("canonical large profile is not idempotent", err)
	}
}
