package hostcontract

import (
	"errors"
	"testing"
)

func TestHostContractConstructorsPreserveAuthenticationBoundaries(t *testing.T) {
	t.Parallel()

	desktop := Desktop()
	if err := desktop.Validate(); err != nil {
		t.Fatalf("validate Desktop contract: %v", err)
	}
	if desktop.Kind() != KindDesktop {
		t.Fatalf("unexpected Desktop kind: %q", desktop.Kind())
	}
	if desktop.ManagementAuthentication() == desktop.ProxyAuthentication() {
		t.Fatal("Desktop management and proxy authentication must be distinct")
	}
	if !desktop.SupportsCaptureRuns() || !desktop.SupportsCustomReports() {
		t.Fatal("Desktop contract is missing Desktop-only capabilities")
	}
	if desktop.SupportsWebSessions() || desktop.SupportsProxyClients() {
		t.Fatal("Desktop contract contains Server-only capabilities")
	}

	server := Server()
	if err := server.Validate(); err != nil {
		t.Fatalf("validate Server contract: %v", err)
	}
	if server.Kind() != KindServer {
		t.Fatalf("unexpected Server kind: %q", server.Kind())
	}
	if server.ManagementAuthentication() == server.ProxyAuthentication() {
		t.Fatal("Server management and proxy authentication must be distinct")
	}
	if !server.SupportsWebSessions() || !server.SupportsProxyClients() {
		t.Fatal("Server contract is missing Server-only capabilities")
	}
	if server.SupportsCaptureRuns() || server.SupportsCustomReports() {
		t.Fatal("Server contract contains Desktop-only capabilities")
	}
}

func TestHostContractRejectsZeroValue(t *testing.T) {
	t.Parallel()

	if err := (Contract{}).Validate(); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("expected ErrInvalidContract, got %v", err)
	}
}
