package egressaudit_test

import (
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"testing"
	"time"
)

func TestDatagramAuditRequiresPacketTargetAndNoModelAccount(t *testing.T) {
	input := egressaudit.NewInput{ID: "packet-attempt", ConnectionID: "packet-connection", Purpose: egressaudit.PurposeDatagram, PayloadClass: egressaudit.PayloadOpaqueDatagram, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentDatagramConnection, ID: "packet-connection"}, Caller: egressaudit.CallerCore, TargetOrigin: "udp://packet.fixture:123", Decision: egressaudit.BuiltInDirectDecision(egressaudit.AuthorityNetwork), StartedAt: time.Now()}
	if _, e := egressaudit.New(input); e != nil {
		t.Fatal(e)
	}
	for _, origin := range []string{"https://packet.fixture:123", "udp://packet.fixture:0", "udp://packet.fixture:123/path", "udp://user:password@packet.fixture:123", "udp://packet.fixture:123?payload=secret", "udp://packet.fixture:123#", "udp://packet.fixture:123?"} {
		c := input
		c.TargetOrigin = origin
		if _, e := egressaudit.New(c); e == nil {
			t.Fatal("invented UDP origin accepted", origin)
		}
	}
	c := input
	c.Decision.AccountID = "model.guessed"
	c.Decision.AccountSettingsRevision = 1
	if _, e := egressaudit.New(c); e == nil {
		t.Fatal("opaque UDP invented model account")
	}
	c = input
	c.PayloadClass = egressaudit.PayloadOpaqueTunnel
	if _, e := egressaudit.New(c); e == nil {
		t.Fatal("UDP represented as TCP tunnel")
	}
	c = input
	c.Caller = egressaudit.CallerPlugin
	c.CallerID = "plugin.guessed"
	if _, e := egressaudit.New(c); e == nil {
		t.Fatal("UDP external plugin bypassed core capture authority")
	}
}
