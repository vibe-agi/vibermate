package egressprofile

import (
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"strings"
	"testing"
	"time"
)

func TestProfileRejectsUnserializableTimestamp(t *testing.T) {
	p := Direct()
	p.ID = "profile.timestamp"
	p.PublishedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if p.Validate() == nil {
		t.Fatal("accepted profile whose JSON timestamp cannot be encoded")
	}
}

func TestProfileMaximumEscapedFieldsFitSharedJSONContract(t *testing.T) {
	p := Direct()
	p.ID = ID(strings.Repeat("a", 128))
	p.Revision = MaxRevision
	p.DisplayName = strings.Repeat("<&", 128)
	p.PublishedAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	p.Policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/" + strings.Repeat("a", 1002), CredentialRevision: uint64(secretstore.MaxRevision)}
	p.Policy.Resolver = egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverDoH, DoHURL: "https://dns.example/" + strings.Repeat("&", 2000), Transport: egressnetwork.ResolverTransportProxy}
	var lines []string
	for i := 0; i < 960; i++ {
		lines = append(lines, fmt.Sprintf("d%03d.example.com", i))
	}
	p.Policy.DirectDomains = egressnetwork.DirectDomains(strings.Join(lines, "\n"))
	var err error
	p.Policy, err = p.Policy.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= 16384 || len(encoded) > MaxProfileJSONBytes {
		t.Fatalf("full-profile bytes=%d", len(encoded))
	}
	var choice Choice
	if err := json.Unmarshal(encoded, &choice); err != nil {
		t.Fatal(err)
	}
	t.Logf("maximum credential revision and escaped metadata: %d profile bytes", len(encoded))
}
