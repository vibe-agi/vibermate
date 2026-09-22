package operationcatalog_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestCodexQuotaIsAnExplicitReadOnlyAccountOperation(t *testing.T) {
	catalog, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	op, err := protocolspec.SelectOperation(catalog.Definitions(), protocolspec.RequestTarget{
		Method: "GET", Path: "/backend-api/wham/usage", Transport: protocolspec.ClientOperationTransportHTTP,
	})
	if err != nil {
		t.Fatalf("quota query is not supported: %v", err)
	}
	if string(op.Kind()) != "account_read" || op.BodyKind() != protocolspec.ClientOperationBodyNone ||
		op.ReplayClass() != protocolspec.ClientReplaySafe || !op.EgressBearing() {
		t.Fatal("quota query must declare read-only account authority, not opaque original authentication")
	}
	for _, target := range []protocolspec.RequestTarget{
		{Method: "POST", Path: "/backend-api/wham/usage", Transport: protocolspec.ClientOperationTransportHTTP},
		{Method: "POST", Path: "/backend-api/wham/rate-limit-reset-credits/consume", Transport: protocolspec.ClientOperationTransportHTTP},
		{Method: "GET", Path: "/backend-api/wham/usage/other", Transport: protocolspec.ClientOperationTransportHTTP},
		{Method: "GET", Path: "/backend-api/wham/usage", RawQuery: "account_id=other", Transport: protocolspec.ClientOperationTransportHTTP},
	} {
		if _, err := protocolspec.SelectOperation(catalog.Definitions(), target); err == nil {
			t.Fatalf("account operation admitted an undeclared request: %s %s", target.Method, target.Path)
		}
	}
}
